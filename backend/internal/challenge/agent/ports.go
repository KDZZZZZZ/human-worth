// Package agent 集中维护 P/R 结构化生成与 E 工具循环。
// 基础设施提供能力；核心不直接连接网络、启动进程、挂载工作区或读取供应商凭据。
package agent

import (
	"context"

	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
)

type Model interface {
	Model() string
	Protocol() string
	Complete(context.Context, []byte) (*CompletionResponse, string, error)
}
type MaterialReader interface {
	Recv() (*dto.ReadMaterialResponse, error)
}
type Scheduler interface {
	ClaimWork(context.Context, *dto.ClaimWorkRequest) (*dto.ClaimWorkResponse, error)
	ActivateAttempt(context.Context, *dto.ActivateAttemptRequest) (*dto.ActivateAttemptResponse, error)
	RenewLease(context.Context, *dto.RenewLeaseRequest) (*dto.RenewLeaseResponse, error)
	CompleteWork(context.Context, *dto.CompleteWorkRequest) (*dto.CompleteWorkResponse, error)
	FailWork(context.Context, *dto.FailWorkRequest) (*dto.FailWorkResponse, error)
	ReserveModelCall(context.Context, *dto.ReserveModelCallRequest) (*dto.ReserveModelCallResponse, error)
	SettleModelCall(context.Context, *dto.SettleModelCallRequest) (*dto.SettleModelCallResponse, error)
	ReadMaterial(context.Context, *dto.ReadMaterialRequest) (MaterialReader, error)
}
type Payloads interface {
	Input(*domain.Assignment) ([]byte, error)
	ExecutionInput(*domain.ExecuteInput) ([]byte, error)
	Result(domain.WorkKind, []byte) (*dto.CompleteWorkRequest, error)
	ResultDigest(*dto.CompleteWorkRequest) string
}
