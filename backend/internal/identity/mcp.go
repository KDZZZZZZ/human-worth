package identity

import (
	"context"
	"encoding/base64"
	"errors"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strings"
	"time"
)

func (s *Server) CreateMcpToken(ctx context.Context, r *pb.CreateMcpTokenRequest) (*pb.CreateMcpTokenResponse, error) {
	if strings.TrimSpace(r.Name) == "" || len([]rune(r.Name)) > 80 || strings.TrimSpace(r.CreateRequestId) == "" || len(r.CreateRequestId) > 128 {
		return nil, faultInvalid("invalid_request")
	}
	actor, err := s.verify(ctx, r.ActorAssertion, "identity", pb.IdentityService_CreateMcpToken_FullMethodName)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback(ctx)
	if err = lockActor(ctx, tx, actor.Principal); err != nil {
		return nil, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.credentials WHERE account_id=$1 AND create_request_id=$2)`, actor.Principal.AccountId, r.CreateRequestId).Scan(&exists); err != nil {
		return nil, storageError(err)
	}
	if exists {
		return nil, status.Error(codes.AlreadyExists, "idempotency_conflict")
	}
	token, id := randomToken(), newID("cred")
	var created, expires time.Time
	err = tx.QueryRow(ctx, `INSERT INTO identity.credentials(id,account_id,kind,token_hash,auth_version,name,create_request_id,expires_at) VALUES($1,$2,'mcp',$3,$4,$5,$6,clock_timestamp()+$7*interval '1 second') RETURNING created_at,expires_at`, id, actor.Principal.AccountId, digest(token), actor.Principal.AuthVersion, strings.TrimSpace(r.Name), r.CreateRequestId, int64(s.Config.McpTTL/time.Second)).Scan(&created, &expires)
	if err != nil {
		return nil, storageError(err)
	}
	if err = audit(ctx, tx, actor.Principal.AccountId, "mcp_created", id, "", actor.Principal.AuthVersion); err != nil {
		return nil, storageError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storageError(err)
	}
	return &pb.CreateMcpTokenResponse{Token: token, Credential: &pb.McpToken{Id: id, Name: strings.TrimSpace(r.Name), CreateRequestId: r.CreateRequestId, State: pb.CredentialState_CREDENTIAL_STATE_ACTIVE, CreatedAt: timestamppb.New(created), ExpiresAt: timestamppb.New(expires)}}, nil
}
func (s *Server) ListMyMcpTokens(ctx context.Context, r *pb.ListMyMcpTokensRequest) (*pb.ListMyMcpTokensResponse, error) {
	actor, err := s.verify(ctx, r.ActorAssertion, "identity", pb.IdentityService_ListMyMcpTokens_FullMethodName)
	if err != nil {
		return nil, err
	}
	limit := r.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return nil, faultInvalid("invalid_request")
	}
	cursor := ""
	if r.Cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(r.Cursor)
		if err != nil || len(decoded) > 100 || !strings.HasPrefix(string(decoded), "cred_") {
			return nil, faultInvalid("invalid_cursor")
		}
		cursor = string(decoded)
	}
	rows, err := s.DB.Query(ctx, `SELECT id,name,create_request_id,created_at,expires_at,revoked_at,CASE WHEN revoked_at IS NOT NULL OR auth_version<>$4 THEN 'revoked' WHEN expires_at<=clock_timestamp() THEN 'expired' ELSE 'active' END FROM identity.credentials WHERE account_id=$1 AND kind='mcp' AND id>$2 ORDER BY id LIMIT $3`, actor.Principal.AccountId, cursor, limit+1, actor.Principal.AuthVersion)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	response := &pb.ListMyMcpTokensResponse{}
	for rows.Next() {
		token := &pb.McpToken{}
		var created, expires time.Time
		var revoked *time.Time
		var state string
		if err = rows.Scan(&token.Id, &token.Name, &token.CreateRequestId, &created, &expires, &revoked, &state); err != nil {
			return nil, storageError(err)
		}
		token.CreatedAt = timestamppb.New(created)
		token.ExpiresAt = timestamppb.New(expires)
		if revoked != nil {
			token.RevokedAt = timestamppb.New(*revoked)
		}
		token.State = pb.CredentialState_CREDENTIAL_STATE_ACTIVE
		if state == "revoked" {
			token.State = pb.CredentialState_CREDENTIAL_STATE_REVOKED
		} else if state == "expired" {
			token.State = pb.CredentialState_CREDENTIAL_STATE_EXPIRED
		}
		response.Items = append(response.Items, token)
	}
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	if len(response.Items) > int(limit) {
		response.Items = response.Items[:limit]
		response.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(response.Items[len(response.Items)-1].Id))
	}
	return response, nil
}
func (s *Server) RevokeMcpToken(ctx context.Context, r *pb.RevokeMcpTokenRequest) (*pb.RevokeMcpTokenResponse, error) {
	if !strings.HasPrefix(r.CredentialId, "cred_") || len(r.CredentialId) > 100 {
		return nil, faultInvalid("invalid_request")
	}
	actor, err := s.verify(ctx, r.ActorAssertion, "identity", pb.IdentityService_RevokeMcpToken_FullMethodName)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback(ctx)
	if err = lockActor(ctx, tx, actor.Principal); err != nil {
		return nil, err
	}
	var revoked *time.Time
	err = tx.QueryRow(ctx, `SELECT revoked_at FROM identity.credentials WHERE id=$1 AND account_id=$2 AND kind='mcp' FOR UPDATE`, r.CredentialId, actor.Principal.AccountId).Scan(&revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "credential_not_found")
	}
	if err != nil {
		return nil, storageError(err)
	}
	if revoked == nil {
		if _, err = tx.Exec(ctx, `UPDATE identity.credentials SET revoked_at=clock_timestamp() WHERE id=$1`, r.CredentialId); err != nil {
			return nil, storageError(err)
		}
		if err = audit(ctx, tx, actor.Principal.AccountId, "mcp_revoked", r.CredentialId, "", actor.Principal.AuthVersion); err != nil {
			return nil, storageError(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storageError(err)
	}
	return &pb.RevokeMcpTokenResponse{}, nil
}
