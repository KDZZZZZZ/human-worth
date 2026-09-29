// challenge-worker 领取 P/R 工作；配置独立执行集群后也可领取 E 工作。
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challengeworker"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker stopped", "reason", err.Error())
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	target, err := platform.Required("CHALLENGE_TARGET")
	if err != nil {
		return err
	}
	conn, err := platform.Client(target, "challenge")
	if err != nil {
		return err
	}
	defer conn.Close()
	instance, err := platform.Required("WORKER_INSTANCE_ID")
	if err != nil {
		return err
	}
	w := &challengeworker.Worker{Client: pb.NewChallengeServiceClient(conn), Instance: instance}
	if path := os.Getenv("CHALLENGE_MODEL_CONFIG_FILE"); path != "" {
		w.Provider, err = challengeworker.LoadProvider(path)
		if err != nil {
			return err
		}
	}
	var relay *http.Server
	if path := os.Getenv("CHALLENGE_EXECUTOR_PROFILE_FILE"); path != "" {
		profile, err := challengeworker.LoadExecutorProfile(path)
		if err != nil {
			return err
		}
		// E 默认复用 P/R 的 Completion 服务，只在明确配置时选择另一份模型文件。
		provider := w.Provider
		if modelPath := os.Getenv("CHALLENGE_EXECUTOR_MODEL_CONFIG_FILE"); modelPath != "" {
			provider, err = challengeworker.LoadProvider(modelPath)
			if err != nil {
				return err
			}
		}
		if provider == nil {
			return errors.New("executor completion model configuration required")
		}
		k := &challengeworker.KubernetesExecutor{Profile: profile, Client: w.Client, Provider: provider, Instance: instance}
		preflight, stop := context.WithTimeout(ctx, 30*time.Second)
		err = k.Preflight(preflight)
		if err == nil {
			err = k.Cleanup(preflight)
		}
		stop()
		if err != nil {
			return err
		}
		cert, err := tls.LoadX509KeyPair(os.Getenv("RELAY_CERT_FILE"), os.Getenv("RELAY_KEY_FILE"))
		if err != nil {
			return errors.New("relay TLS identity unavailable")
		}
		listener, err := net.Listen("tcp", platform.Value("RELAY_LISTEN", ":9443"))
		if err != nil {
			return errors.New("relay listener unavailable")
		}
		relay = &http.Server{Handler: k, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 16384, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}}
		go func() {
			if relay.ServeTLS(listener, "", "") != http.ErrServerClosed {
				cancel()
			}
		}()
		w.Executor = k
		defer func() {
			stop, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			relay.Shutdown(stop)
		}()
	}
	if w.Provider == nil && w.Executor == nil {
		return errors.New("worker model or executor required")
	}
	slog.Info("worker started", "revision", platform.BuildVersion())
	for ctx.Err() == nil {
		worked, err := w.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Warn("work failed")
		}
		if !worked || err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}
	return nil
}
