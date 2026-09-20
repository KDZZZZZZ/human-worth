// challenge-executor 是隔离 Job 的固定入口，不在宿主机运行模型生成的代码。
package main

import (
	"context"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challengeworker"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := challengeworker.RunExecutor(ctx); err != nil {
		slog.Error("execution failed", "reason", err.Error())
		os.Exit(1)
	}
}
