package identity

import (
	"context"
	"errors"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/url"
	"time"
)

type Config struct {
	Origin     string
	Encryption KeyRing
	Signing    KeyRing
	OAuth      *GoogleOAuth
	SessionTTL time.Duration
	McpTTL     time.Duration
}
type Server struct {
	pb.UnimplementedIdentityServiceServer
	DB     *pgxpool.Pool
	Config Config
}

func NewServer(db *pgxpool.Pool, cfg Config) (*Server, error) {
	origin, err := url.Parse(cfg.Origin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" {
		return nil, errors.New("PUBLIC_ORIGIN must be an HTTPS origin without a path")
	}
	if _, err = cfg.Encryption.key(cfg.Encryption.Active); err != nil {
		return nil, err
	}
	if _, err = cfg.Signing.key(cfg.Signing.Active); err != nil {
		return nil, err
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 24 * time.Hour
	}
	if cfg.McpTTL == 0 {
		cfg.McpTTL = 30 * 24 * time.Hour
	}
	if cfg.SessionTTL <= 0 || cfg.McpTTL <= 0 {
		return nil, errors.New("invalid credential lifetime")
	}
	if cfg.OAuth != nil && (cfg.OAuth.config.RedirectURL != cfg.Origin+"/api/auth/google/callback" || cfg.OAuth.Version == "" || cfg.OAuth.config.ClientID == "" || cfg.OAuth.config.ClientSecret == "") {
		return nil, errors.New("invalid Google configuration")
	}
	return &Server{DB: db, Config: cfg}, nil
}
func faultInvalid(reason string) error     { return status.Error(codes.InvalidArgument, reason) }
func faultUnauth(reason string) error      { return status.Error(codes.Unauthenticated, reason) }
func faultForbidden(reason string) error   { return status.Error(codes.PermissionDenied, reason) }
func faultUnavailable(reason string) error { return status.Error(codes.Unavailable, reason) }
func storageError(err error) error {
	if err == nil {
		return nil
	}
	return faultUnavailable("identity_unavailable")
}

// Only gateway can present raw user credentials. Services can only verify assertions.
func Authorization(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	caller, err := platform.ServiceName(ctx)
	if err != nil {
		return nil, faultUnauth("service_identity_required")
	}
	if info.FullMethod == pb.IdentityService_VerifyActor_FullMethodName {
		switch caller {
		case "content", "asset", "voting", "moderation", "challenge", "discovery":
		default:
			return nil, faultForbidden("service_forbidden")
		}
	} else if caller != "gateway" {
		return nil, faultForbidden("service_forbidden")
	}
	return next(ctx, req)
}

// Health.Watch is streaming too; it must not bypass the unary caller policy.
func AuthorizationStream(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
	_, err := Authorization(stream.Context(), nil, &grpc.UnaryServerInfo{FullMethod: info.FullMethod}, func(context.Context, any) (any, error) {
		return nil, next(server, stream)
	})
	return err
}
func audit(ctx context.Context, tx pgx.Tx, actor, action, target, reason string, version int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO identity.audit_events(id,actor_id,action,target_id,reason,auth_version) VALUES($1,$2,$3,$4,$5,$6)`, newID("audit"), actor, action, target, reason, version)
	return err
}
func (s *Server) Cleanup(ctx context.Context) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Small bounded batches; expiry is enforced on reads regardless of cleanup timing.
	_, err = tx.Exec(ctx, `WITH batch AS (SELECT id FROM identity.login_flows WHERE expires_at<=clock_timestamp() AND status IN ('pending','exchanging') LIMIT 100 FOR UPDATE SKIP LOCKED) UPDATE identity.login_flows SET status='expired',verifier_cipher=NULL FROM batch WHERE login_flows.id=batch.id`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM identity.login_families WHERE id IN (SELECT f.id FROM identity.login_families f WHERE NOT EXISTS(SELECT 1 FROM identity.login_flows l WHERE l.family_id=f.id AND l.expires_at>clock_timestamp()-interval '1 day') LIMIT 100)`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM identity.credentials WHERE id IN (SELECT id FROM identity.credentials WHERE kind='web' AND expires_at<clock_timestamp()-interval '7 days' LIMIT 100)`)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
