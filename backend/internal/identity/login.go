package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/jackc/pgx/v5"
	"sort"
	"time"
)

func (s *Server) StartGoogleLogin(ctx context.Context, r *pb.StartGoogleLoginRequest) (*pb.StartGoogleLoginResponse, error) {
	if s.Config.OAuth == nil {
		return nil, faultUnavailable("login_unavailable")
	}
	if len(r.PreviousFlowCookie) > 512 {
		return nil, faultInvalid("invalid_request")
	}
	flowID, cookie, state, nonce, verifier := newID("flow"), randomToken(), randomToken(), randomToken(), randomToken()
	encrypted, err := s.Config.Encryption.seal(verifier, flowID+":verifier")
	if err != nil {
		return nil, storageError(err)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback(ctx)
	family := ""
	if r.PreviousFlowCookie != "" {
		err = tx.QueryRow(ctx, `SELECT family_id FROM identity.login_flows WHERE cookie_hash=$1`, digest(r.PreviousFlowCookie)).Scan(&family)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, storageError(err)
		}
	}
	if family == "" {
		family = newID("browser")
		_, err = tx.Exec(ctx, `INSERT INTO identity.login_families(id) VALUES($1)`, family)
	} else {
		_, err = tx.Exec(ctx, `SELECT id FROM identity.login_families WHERE id=$1 FOR UPDATE`, family)
	}
	if err != nil {
		return nil, storageError(err)
	}
	_, err = tx.Exec(ctx, `UPDATE identity.login_flows SET status='cancelled',verifier_cipher=NULL WHERE family_id=$1 AND status IN ('pending','exchanging')`, family)
	if err != nil {
		return nil, storageError(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO identity.login_flows(id,family_id,cookie_hash,state_hash,nonce_hash,verifier_cipher,config_version,redirect_uri,status,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',clock_timestamp()+interval '10 minutes')`, flowID, family, digest(cookie), digest(state), digest(nonce), encrypted, s.Config.OAuth.Version, s.Config.OAuth.config.RedirectURL)
	if err != nil {
		return nil, storageError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storageError(err)
	}
	return &pb.StartGoogleLoginResponse{AuthorizationUrl: s.Config.OAuth.authorizationURL(state, nonce, verifier), FlowCookie: cookie, MaxAgeSeconds: 600}, nil
}

type claimedFlow struct {
	ID, Family, Attempt, Verifier string
	Nonce                         []byte
}

func (s *Server) claim(ctx context.Context, r *pb.CompleteGoogleLoginRequest) (claimedFlow, bool, error) {
	var flow claimedFlow
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return flow, false, storageError(err)
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT id,family_id FROM identity.login_flows WHERE cookie_hash=$1`, digest(r.FlowCookie)).Scan(&flow.ID, &flow.Family)
	if errors.Is(err, pgx.ErrNoRows) {
		return flow, false, faultInvalid("invalid_oauth_state")
	}
	if err != nil {
		return flow, false, storageError(err)
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM identity.login_families WHERE id=$1 FOR UPDATE`, flow.Family); err != nil {
		return flow, false, storageError(err)
	}
	var hash []byte
	var encrypted, version, callback, state string
	var valid bool
	err = tx.QueryRow(ctx, `SELECT state_hash,nonce_hash,COALESCE(verifier_cipher,''),config_version,redirect_uri,status,expires_at>clock_timestamp() FROM identity.login_flows WHERE id=$1 FOR UPDATE`, flow.ID).Scan(&hash, &flow.Nonce, &encrypted, &version, &callback, &state, &valid)
	if err != nil {
		return flow, false, storageError(err)
	}
	if state != "pending" || !valid || subtle.ConstantTimeCompare(hash, digest(r.State)) != 1 {
		return flow, false, faultInvalid("invalid_oauth_state")
	}
	if version != s.Config.OAuth.Version || callback != s.Config.OAuth.config.RedirectURL {
		return flow, false, faultUnavailable("login_unavailable")
	}
	providerError, denied := r.Outcome.(*pb.CompleteGoogleLoginRequest_ProviderError)
	if denied {
		state = "failed"
		if providerError.ProviderError == "access_denied" {
			state = "cancelled"
		}
		_, err = tx.Exec(ctx, `UPDATE identity.login_flows SET status=$2,verifier_cipher=NULL WHERE id=$1`, flow.ID, state)
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			return flow, false, storageError(err)
		}
		if state == "cancelled" {
			return flow, true, nil
		}
		return flow, false, faultUnauth("invalid_google_login")
	}
	flow.Verifier, err = s.Config.Encryption.open(encrypted, flow.ID+":verifier")
	if err != nil {
		return flow, false, storageError(err)
	}
	flow.Attempt = randomToken()
	_, err = tx.Exec(ctx, `UPDATE identity.login_flows SET status='exchanging',attempt_id=$2 WHERE id=$1`, flow.ID, flow.Attempt)
	if err == nil {
		err = tx.Commit(ctx)
	}
	return flow, false, storageError(err)
}
func (s *Server) CompleteGoogleLogin(ctx context.Context, r *pb.CompleteGoogleLoginRequest) (*pb.CompleteGoogleLoginResponse, error) {
	if s.Config.OAuth == nil {
		return nil, faultUnavailable("login_unavailable")
	}
	if r.FlowCookie == "" || len(r.FlowCookie) > 512 || r.State == "" || len(r.State) > 1024 || len(r.PreviousSessionCookie) > 512 {
		return nil, faultInvalid("invalid_request")
	}
	switch outcome := r.Outcome.(type) {
	case *pb.CompleteGoogleLoginRequest_Code:
		if len(outcome.Code) == 0 || len(outcome.Code) > 4096 {
			return nil, faultInvalid("invalid_request")
		}
	case *pb.CompleteGoogleLoginRequest_ProviderError:
		if len(outcome.ProviderError) == 0 || len(outcome.ProviderError) > 128 {
			return nil, faultInvalid("invalid_request")
		}
	default:
		return nil, faultInvalid("invalid_request")
	}
	flow, cancelled, err := s.claim(ctx, r)
	if err != nil {
		return nil, err
	}
	if cancelled {
		return &pb.CompleteGoogleLoginResponse{RedirectPath: "/?login=cancelled"}, nil
	}
	profile, err := s.Config.OAuth.exchange(ctx, r.GetCode(), flow.Verifier, flow.Nonce)
	if err == nil {
		var response *pb.CompleteGoogleLoginResponse
		response, err = s.finishLogin(ctx, flow, profile, r.PreviousSessionCookie)
		if err == nil {
			return response, nil
		}
	}
	// Never replay an external code. A commit with lost response may already be succeeded.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _ = s.DB.Exec(cleanup, `UPDATE identity.login_flows SET status='failed',verifier_cipher=NULL WHERE id=$1 AND status='exchanging' AND attempt_id=$2`, flow.ID, flow.Attempt)
	return nil, err
}
func (s *Server) finishLogin(ctx context.Context, flow claimedFlow, profile googleIdentity, oldCookie string) (*pb.CompleteGoogleLoginResponse, error) {
	token, credentialID, csrf := randomToken(), newID("cred"), randomToken()
	encrypted, err := s.Config.Encryption.seal(csrf, credentialID+":csrf")
	if err != nil {
		return nil, storageError(err)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM identity.login_families WHERE id=$1 FOR UPDATE`, flow.Family); err != nil {
		return nil, storageError(err)
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT status='exchanging' AND attempt_id=$2 AND expires_at>clock_timestamp() FROM identity.login_flows WHERE id=$1 FOR UPDATE`, flow.ID, flow.Attempt).Scan(&valid)
	if err != nil {
		return nil, storageError(err)
	}
	if !valid {
		return nil, faultInvalid("invalid_oauth_state")
	}
	// Serialize first-login association without retrying the external authorization code.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, s.Config.OAuth.issuer+":"+profile.Subject); err != nil {
		return nil, storageError(err)
	}
	accountID := ""
	err = tx.QueryRow(ctx, `SELECT account_id FROM identity.external_identities WHERE issuer=$1 AND subject=$2`, s.Config.OAuth.issuer, profile.Subject).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		accountID = newID("acc")
		if _, err = tx.Exec(ctx, `INSERT INTO identity.accounts(id,display_name) VALUES($1,$2)`, accountID, profile.Name); err != nil {
			return nil, storageError(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO identity.external_identities(issuer,subject,account_id) VALUES($1,$2,$3)`, s.Config.OAuth.issuer, profile.Subject, accountID)
	}
	if err != nil {
		return nil, storageError(err)
	}
	oldAccount := ""
	if oldCookie != "" {
		err = tx.QueryRow(ctx, `SELECT account_id FROM identity.credentials WHERE token_hash=$1 AND kind='web'`, digest(oldCookie)).Scan(&oldAccount)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, storageError(err)
		}
	}
	ids := []string{accountID}
	if oldAccount != "" && oldAccount != accountID {
		ids = append(ids, oldAccount)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err = tx.Exec(ctx, `SELECT id FROM identity.accounts WHERE id=$1 FOR UPDATE`, id); err != nil {
			return nil, storageError(err)
		}
	}
	var state string
	var version int64
	if err = tx.QueryRow(ctx, `SELECT state,auth_version FROM identity.accounts WHERE id=$1`, accountID).Scan(&state, &version); err != nil {
		return nil, storageError(err)
	}
	if state != "active" {
		return nil, faultForbidden("account_disabled")
	}
	if _, err = tx.Exec(ctx, `UPDATE identity.accounts SET display_name=$2,updated_at=clock_timestamp() WHERE id=$1`, accountID, profile.Name); err != nil {
		return nil, storageError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE identity.external_identities SET email=$3,email_verified=$4,updated_at=clock_timestamp() WHERE issuer=$1 AND subject=$2`, s.Config.OAuth.issuer, profile.Subject, profile.Email, profile.EmailVerified); err != nil {
		return nil, storageError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE identity.credentials SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE token_hash=$1 AND kind='web'`, digest(oldCookie)); err != nil {
		return nil, storageError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO identity.credentials(id,account_id,kind,token_hash,auth_version,csrf_cipher,expires_at) VALUES($1,$2,'web',$3,$4,$5,clock_timestamp()+$6*interval '1 second')`, credentialID, accountID, digest(token), version, encrypted, int64(s.Config.SessionTTL/time.Second)); err != nil {
		return nil, storageError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE identity.login_flows SET status='succeeded',verifier_cipher=NULL WHERE id=$1`, flow.ID); err != nil {
		return nil, storageError(err)
	}
	if err = audit(ctx, tx, accountID, "login", credentialID, "google", version); err != nil {
		return nil, storageError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storageError(err)
	}
	return &pb.CompleteGoogleLoginResponse{RedirectPath: "/", SessionCookie: token, MaxAgeSeconds: int32(s.Config.SessionTTL / time.Second)}, nil
}
