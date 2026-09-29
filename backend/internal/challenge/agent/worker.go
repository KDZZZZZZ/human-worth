package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
)

// Executor 是真正执行作品的边界；正式运行必须使用隔离环境，测试可提供同契约 fake。
type Executor interface {
	Execute(context.Context, *domain.Assignment, string) (*domain.GeneratedWork, error)
}

// Worker 每个实例串行负责一个 Attempt，不共享 P/R 会话或将 E 工作放到宿主机执行。
type Worker struct {
	Client   Scheduler
	Codec    Payloads
	Provider Model
	Executor Executor
	Instance string
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// RunOnce 领取后即按租约续期；丢失续租时取消模型请求或隔离执行。
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	if w.Codec == nil || w.Client == nil || w.Instance == "" || w.Provider == nil && w.Executor == nil {
		return false, errors.New("worker configuration required")
	}
	capabilities := []domain.WorkKind{}
	if w.Provider != nil && w.Provider.Protocol() == "completion" {
		capabilities = append(capabilities, 1, 2, 3, 4, 6, 7)
	}
	if w.Executor != nil {
		capabilities = append(capabilities, domain.WorkKind_WORK_KIND_EXECUTE_PACKAGE)
	}
	claim := &dto.ClaimWorkRequest{WorkerInstanceId: w.Instance, ClaimRequestId: randomID(), Capabilities: capabilities}
	var response *dto.ClaimWorkResponse
	var err error
	for retry := 0; retry < 3; retry++ {
		response, err = w.Client.ClaimWork(ctx, claim)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
	}
	if err != nil {
		return false, err
	}
	a := response.Assignment
	if a == nil {
		return false, nil
	}
	ctx, cancel := context.WithDeadline(ctx, domain.TimeValue(a.Deadline))
	defer cancel()
	activation, err := w.Client.ActivateAttempt(ctx, &dto.ActivateAttemptRequest{Attempt: a.Attempt, ExecutionInstanceId: w.Instance + "-" + randomID()})
	if err != nil {
		return true, err
	}
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		defer close(done)
		interval := time.Until(domain.TimeValue(a.LeaseExpiresAt)) / 3
		if interval < 10*time.Millisecond {
			interval = 10 * time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				renew, e := w.Client.RenewLease(ctx, &dto.RenewLeaseRequest{Attempt: a.Attempt})
				if e != nil || renew.StopRequested {
					cancel()
					return
				}
			}
		}
	}()
	result := &dto.CompleteWorkRequest{Attempt: a.Attempt}
	if a.Kind == domain.WorkKind_WORK_KIND_EXECUTE_PACKAGE {
		var g *domain.GeneratedWork
		g, err = w.Executor.Execute(ctx, a, activation.Grant)
		if err == nil {
			result.Result = &dto.CompleteWorkRequest_Generated{Generated: g}
		}
	} else {
		result, err = w.Generate(ctx, a, activation.Grant)
	}
	if err != nil {
		reason := "invalid_model_result"
		if a.Role == "E" {
			reason = "executor_failed"
		}
		if errors.Is(err, ErrModelUnknown) {
			reason = "model_outcome_unknown"
		}
		failureCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		_, _ = w.Client.FailWork(failureCtx, &dto.FailWorkRequest{Attempt: a.Attempt, FailureCode: reason, OutcomeKnown: !errors.Is(err, ErrModelUnknown) && ctx.Err() == nil})
		return true, err
	}
	result.ResultDigest = w.Codec.ResultDigest(result)
	for retry := 0; retry < 3; retry++ {
		_, err = w.Client.CompleteWork(ctx, result)
		if err == nil {
			return true, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		_, _ = w.Client.FailWork(ctx, &dto.FailWorkRequest{Attempt: a.Attempt, FailureCode: "invalid_model_result", OutcomeKnown: true})
	}
	return true, err
}
