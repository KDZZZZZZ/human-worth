package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"time"
)

type policy struct {
	Audience                     string
	Write, Anonymous, MCP, Admin bool
}

var policies = map[string]policy{
	pb.IdentityService_GetCurrentSession_FullMethodName:      {Audience: "identity"},
	pb.IdentityService_LogoutCurrentSession_FullMethodName:   {Audience: "identity", Write: true},
	pb.IdentityService_CreateMcpToken_FullMethodName:         {Audience: "identity", Write: true},
	pb.IdentityService_ListMyMcpTokens_FullMethodName:        {Audience: "identity"},
	pb.IdentityService_RevokeMcpToken_FullMethodName:         {Audience: "identity", Write: true},
	"/humanworth.content.v1.ContentService/GetTask":          {Audience: "content", Anonymous: true, MCP: true},
	"/humanworth.discovery.v1.DiscoveryService/ListTasks":    {Audience: "discovery", Anonymous: true, MCP: true},
	"/humanworth.voting.v1.VotingService/ViewTaskStatistics": {Audience: "voting", Write: true, MCP: true},
	"/humanworth.voting.v1.VotingService/CastVote":           {Audience: "voting", Write: true},
	"/humanworth.challenge.v1.ChallengeService/StartRun":     {Audience: "challenge", Write: true, Admin: true},
	// Challenge 管理接口逐个授权，MCP 和普通账号不能通过通用前缀获得权限。
	"/humanworth.challenge.v1.ChallengeService/GetRun":            {Audience: "challenge", Admin: true},
	"/humanworth.challenge.v1.ChallengeService/ListRuns":          {Audience: "challenge", Admin: true},
	"/humanworth.challenge.v1.ChallengeService/GetRunSummary":     {Audience: "challenge", Admin: true},
	"/humanworth.challenge.v1.ChallengeService/CancelRun":         {Audience: "challenge", Write: true, Admin: true},
	"/humanworth.challenge.v1.ChallengeService/RestartRun":        {Audience: "challenge", Write: true, Admin: true},
	"/humanworth.challenge.v1.ChallengeService/RegisterCandidate": {Audience: "challenge", Write: true, Admin: true},
}

type actorClaims struct {
	jwt.RegisteredClaims
	Method       string `json:"method"`
	CredentialID string `json:"credential_id"`
	Kind         int32  `json:"kind"`
	Role         int32  `json:"role"`
	Version      int64  `json:"auth_version"`
}
type credentialRow struct {
	Principal *pb.Principal
	CSRF      string
	Name      string
}

func role(value string) pb.Role {
	if value == "admin" {
		return pb.Role_ROLE_ADMIN
	}
	return pb.Role_ROLE_USER
}
func (s *Server) credential(ctx context.Context, raw string, kind pb.ClientKind, id string) (credentialRow, error) {
	var row credentialRow
	row.Principal = &pb.Principal{}
	var dbKind, dbRole, state string
	var version, issued int64
	var active bool
	var err error
	query := `SELECT c.id,c.account_id,c.kind,c.auth_version,a.auth_version,a.role,a.state,a.display_name,COALESCE(c.csrf_cipher,''),c.revoked_at IS NULL AND c.expires_at>clock_timestamp() FROM identity.credentials c JOIN identity.accounts a ON a.id=c.account_id WHERE `
	if id != "" {
		err = s.DB.QueryRow(ctx, query+`c.id=$1`, id).Scan(&row.Principal.CredentialId, &row.Principal.AccountId, &dbKind, &issued, &version, &dbRole, &state, &row.Name, &row.CSRF, &active)
	} else {
		err = s.DB.QueryRow(ctx, query+`c.token_hash=$1`, digest(raw)).Scan(&row.Principal.CredentialId, &row.Principal.AccountId, &dbKind, &issued, &version, &dbRole, &state, &row.Name, &row.CSRF, &active)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return row, faultUnauth("unauthenticated")
	}
	if err != nil {
		return row, storageError(err)
	}
	expected := "web"
	if kind == pb.ClientKind_CLIENT_KIND_MCP_READ {
		expected = "mcp"
	}
	if !active || state != "active" || version != issued || dbKind != expected {
		return row, faultUnauth("unauthenticated")
	}
	row.Principal.Role = role(dbRole)
	row.Principal.AuthVersion = version
	row.Principal.ClientKind = kind
	return row, nil
}
func (s *Server) ResolvePrincipal(ctx context.Context, r *pb.ResolvePrincipalRequest) (*pb.ResolvePrincipalResponse, error) {
	rule, ok := policies[r.FullMethod]
	if !ok || rule.Audience != r.Audience {
		return nil, faultForbidden("purpose_forbidden")
	}
	principal := &pb.Principal{ClientKind: pb.ClientKind_CLIENT_KIND_ANONYMOUS}
	var row credentialRow
	if r.Credential != nil {
		kind := pb.ClientKind_CLIENT_KIND_WEB_SESSION
		raw := r.GetSessionCookie()
		if _, ok := r.Credential.(*pb.ResolvePrincipalRequest_McpToken); ok {
			kind = pb.ClientKind_CLIENT_KIND_MCP_READ
			raw = r.GetMcpToken()
		}
		if len(raw) == 0 || len(raw) > 512 {
			return nil, faultUnauth("unauthenticated")
		}
		var err error
		row, err = s.credential(ctx, raw, kind, "")
		if err != nil {
			return nil, err
		}
		principal = row.Principal
	} else if !rule.Anonymous {
		return nil, faultUnauth("unauthenticated")
	}
	if principal.ClientKind == pb.ClientKind_CLIENT_KIND_MCP_READ && !rule.MCP {
		return nil, faultForbidden("credential_purpose_forbidden")
	}
	if rule.Admin && principal.Role != pb.Role_ROLE_ADMIN {
		return nil, faultForbidden("administrator_required")
	}
	if rule.Write && principal.ClientKind == pb.ClientKind_CLIENT_KIND_WEB_SESSION {
		if r.Origin != s.Config.Origin || r.CsrfToken == "" || len(r.CsrfToken) > 512 {
			return nil, faultForbidden("csrf_invalid")
		}
		csrf, err := s.Config.Encryption.open(row.CSRF, principal.CredentialId+":csrf")
		if err != nil {
			return nil, storageError(err)
		}
		if subtle.ConstantTimeCompare([]byte(csrf), []byte(r.CsrfToken)) != 1 {
			return nil, faultForbidden("csrf_invalid")
		}
	}
	now := time.Now()
	claims := actorClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "human-worth.identity", Subject: principal.AccountId, Audience: jwt.ClaimStrings{r.Audience}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(30 * time.Second)), ID: randomToken()}, Method: r.FullMethod, CredentialID: principal.CredentialId, Kind: int32(principal.ClientKind), Role: int32(principal.Role), Version: principal.AuthVersion}
	key, err := s.Config.Signing.key(s.Config.Signing.Active)
	if err != nil {
		return nil, storageError(err)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["kid"] = s.Config.Signing.Active
	signed, err := token.SignedString(key)
	if err != nil {
		return nil, storageError(err)
	}
	return &pb.ResolvePrincipalResponse{Principal: principal, ActorAssertion: signed}, nil
}
func (s *Server) verify(ctx context.Context, raw, audience, method string) (credentialRow, error) {
	var result credentialRow
	if len(raw) == 0 || len(raw) > 4096 {
		return result, faultUnauth("invalid_actor")
	}
	rule, ok := policies[method]
	if !ok || rule.Audience != audience {
		return result, faultForbidden("purpose_forbidden")
	}
	var claims actorClaims
	parsed, err := jwt.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok {
			return nil, errors.New("missing key id")
		}
		return s.Config.Signing.key(kid)
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("human-worth.identity"), jwt.WithAudience(audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || !parsed.Valid || claims.IssuedAt == nil || claims.ID == "" || claims.Method != method || len(claims.Audience) != 1 || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > 30*time.Second {
		return result, faultUnauth("invalid_actor")
	}
	kind := pb.ClientKind(claims.Kind)
	if kind == pb.ClientKind_CLIENT_KIND_ANONYMOUS {
		if !rule.Anonymous || claims.Subject != "" || claims.CredentialID != "" || claims.Version != 0 || claims.Role != 0 {
			return result, faultForbidden("purpose_forbidden")
		}
		return credentialRow{Principal: &pb.Principal{ClientKind: kind}}, nil
	}
	if kind != pb.ClientKind_CLIENT_KIND_WEB_SESSION && kind != pb.ClientKind_CLIENT_KIND_MCP_READ {
		return result, faultUnauth("invalid_actor")
	}
	if claims.Subject == "" || claims.CredentialID == "" || claims.Version < 1 {
		return result, faultUnauth("invalid_actor")
	}
	result, err = s.credential(ctx, "", kind, claims.CredentialID)
	if err != nil {
		return result, err
	}
	if result.Principal.AccountId != claims.Subject || result.Principal.AuthVersion != claims.Version || int32(result.Principal.Role) != claims.Role {
		return result, faultUnauth("invalid_actor")
	}
	if kind == pb.ClientKind_CLIENT_KIND_MCP_READ && !rule.MCP {
		return result, faultForbidden("credential_purpose_forbidden")
	}
	if rule.Admin && result.Principal.Role != pb.Role_ROLE_ADMIN {
		return result, faultForbidden("administrator_required")
	}
	return result, nil
}
func (s *Server) VerifyActor(ctx context.Context, r *pb.VerifyActorRequest) (*pb.VerifyActorResponse, error) {
	caller, err := platform.ServiceName(ctx)
	if err != nil {
		return nil, faultUnauth("service_identity_required")
	}
	row, err := s.verify(ctx, r.ActorAssertion, caller, r.FullMethod)
	if err != nil {
		return nil, err
	}
	return &pb.VerifyActorResponse{Principal: row.Principal}, nil
}
func (s *Server) GetCurrentSession(ctx context.Context, r *pb.GetCurrentSessionRequest) (*pb.GetCurrentSessionResponse, error) {
	row, err := s.verify(ctx, r.ActorAssertion, "identity", pb.IdentityService_GetCurrentSession_FullMethodName)
	if err != nil {
		return nil, err
	}
	csrf, err := s.Config.Encryption.open(row.CSRF, row.Principal.CredentialId+":csrf")
	if err != nil {
		return nil, storageError(err)
	}
	return &pb.GetCurrentSessionResponse{Account: &pb.Account{Id: row.Principal.AccountId, DisplayName: row.Name}, Role: row.Principal.Role, CsrfToken: csrf}, nil
}
func lockActor(ctx context.Context, tx pgx.Tx, p *pb.Principal) error {
	var version int64
	var state string
	err := tx.QueryRow(ctx, `SELECT auth_version,state FROM identity.accounts WHERE id=$1 FOR UPDATE`, p.AccountId).Scan(&version, &state)
	if err != nil {
		return storageError(err)
	}
	if version != p.AuthVersion || state != "active" {
		return faultUnauth("unauthenticated")
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT revoked_at IS NULL AND expires_at>clock_timestamp() AND kind='web' AND auth_version=$3 FROM identity.credentials WHERE id=$1 AND account_id=$2 FOR UPDATE`, p.CredentialId, p.AccountId, p.AuthVersion).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return faultUnauth("unauthenticated")
	}
	if err != nil {
		return storageError(err)
	}
	if !active {
		return faultUnauth("unauthenticated")
	}
	return nil
}
func (s *Server) LogoutCurrentSession(ctx context.Context, r *pb.LogoutCurrentSessionRequest) (*pb.LogoutCurrentSessionResponse, error) {
	row, err := s.verify(ctx, r.ActorAssertion, "identity", pb.IdentityService_LogoutCurrentSession_FullMethodName)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback(ctx)
	if err = lockActor(ctx, tx, row.Principal); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE identity.credentials SET revoked_at=clock_timestamp() WHERE id=$1`, row.Principal.CredentialId); err != nil {
		return nil, storageError(err)
	}
	if err = audit(ctx, tx, row.Principal.AccountId, "logout", row.Principal.CredentialId, "", row.Principal.AuthVersion); err != nil {
		return nil, storageError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storageError(err)
	}
	return &pb.LogoutCurrentSessionResponse{}, nil
}

// Used only by the separate operator Job/CLI with the identity database capability.
func (s *Server) ChangeAccount(ctx context.Context, actor, account, newRole, newState string, expected int64) error {
	if actor == "" || account == "" || expected < 1 || (newRole != "user" && newRole != "admin") || (newState != "active" && newState != "disabled") {
		return faultInvalid("invalid_request")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback(ctx)
	var version int64
	if err = tx.QueryRow(ctx, `SELECT auth_version FROM identity.accounts WHERE id=$1 FOR UPDATE`, account).Scan(&version); errors.Is(err, pgx.ErrNoRows) {
		return status.Error(codes.NotFound, "account_not_found")
	} else if err != nil {
		return storageError(err)
	}
	if version != expected {
		return status.Error(codes.Aborted, "revision_conflict")
	}
	if _, err = tx.Exec(ctx, `UPDATE identity.accounts SET role=$2,state=$3,auth_version=auth_version+1,updated_at=clock_timestamp() WHERE id=$1`, account, newRole, newState); err != nil {
		return storageError(err)
	}
	if err = audit(ctx, tx, actor, "account_change", account, newRole+":"+newState, version+1); err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit(ctx))
}
