// identity-admin is run as a restricted operator Job, never exposed by gateway.
package main

import (
	"context"
	"flag"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"log/slog"
	"os"
	"time"
)

func main() {
	account := flag.String("account", "", "existing account id")
	role := flag.String("role", "", "user or admin")
	state := flag.String("state", "", "active or disabled")
	version := flag.Int64("expected-version", 0, "current auth_version")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	actor, err := platform.Required("OPERATOR_IDENTITY")
	if err != nil {
		slog.Error("operator identity required")
		os.Exit(1)
	}
	dsn, err := platform.Secret("IDENTITY_DATABASE_URL_FILE")
	if err != nil {
		slog.Error("database configuration unavailable")
		os.Exit(1)
	}
	db, err := platform.Database(ctx, dsn)
	if err != nil {
		slog.Error("database unavailable")
		os.Exit(1)
	}
	defer db.Close()
	service := &identity.Server{DB: db}
	if err = service.ChangeAccount(ctx, actor, *account, *role, *state, *version); err != nil {
		slog.Error("account change rejected", "reason", err.Error())
		os.Exit(1)
	}
	slog.Info("account changed", "account_id", *account, "auth_version", *version+1)
}
