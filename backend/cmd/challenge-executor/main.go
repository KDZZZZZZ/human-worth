// challenge-executor 是隔离 Job 的固定入口，不在宿主机运行模型生成的代码。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/execution"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := execution.RunExecutor(ctx); err != nil {
		slog.Error("execution failed", "reason", err.Error())
		os.Exit(1)
	}
}
