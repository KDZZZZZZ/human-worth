package rpc

import (
	"context"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/protobuf"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/agent"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"google.golang.org/grpc"
)

type WorkerClient struct{ Client pb.ChallengeServiceClient }

func (c WorkerClient) ClaimWork(ctx context.Context, q *dto.ClaimWorkRequest) (*dto.ClaimWorkResponse, error) {
	v, err := c.Client.ClaimWork(ctx, protobuf.ToClaimWorkRequest(q))
	return protobuf.FromClaimWorkResponse(v), protobuf.FromError(err)
}
func (c WorkerClient) ActivateAttempt(ctx context.Context, q *dto.ActivateAttemptRequest) (*dto.ActivateAttemptResponse, error) {
	v, err := c.Client.ActivateAttempt(ctx, protobuf.ToActivateAttemptRequest(q))
	return protobuf.FromActivateAttemptResponse(v), protobuf.FromError(err)
}
func (c WorkerClient) RenewLease(ctx context.Context, q *dto.RenewLeaseRequest) (*dto.RenewLeaseResponse, error) {
	v, err := c.Client.RenewLease(ctx, protobuf.ToRenewLeaseRequest(q))
	return protobuf.FromRenewLeaseResponse(v), protobuf.FromError(err)
}
func (c WorkerClient) CompleteWork(ctx context.Context, q *dto.CompleteWorkRequest) (*dto.CompleteWorkResponse, error) {
	v, err := c.Client.CompleteWork(ctx, protobuf.ToCompleteWorkRequest(q))
	return protobuf.FromCompleteWorkResponse(v), protobuf.FromError(err)
}
func (c WorkerClient) FailWork(ctx context.Context, q *dto.FailWorkRequest) (*dto.FailWorkResponse, error) {
	v, err := c.Client.FailWork(ctx, protobuf.ToFailWorkRequest(q))
	return protobuf.FromFailWorkResponse(v), protobuf.FromError(err)
}
func (c WorkerClient) ReserveModelCall(ctx context.Context, q *dto.ReserveModelCallRequest) (*dto.ReserveModelCallResponse, error) {
	v, err := c.Client.ReserveModelCall(ctx, protobuf.ToReserveModelCallRequest(q))
	return protobuf.FromReserveModelCallResponse(v), protobuf.FromError(err)
}
func (c WorkerClient) SettleModelCall(ctx context.Context, q *dto.SettleModelCallRequest) (*dto.SettleModelCallResponse, error) {
	v, err := c.Client.SettleModelCall(ctx, protobuf.ToSettleModelCallRequest(q))
	return protobuf.FromSettleModelCallResponse(v), protobuf.FromError(err)
}

type materialReader struct {
	grpc.ServerStreamingClient[pb.ReadMaterialResponse]
}

func (r materialReader) Recv() (*dto.ReadMaterialResponse, error) {
	v, err := r.ServerStreamingClient.Recv()
	return protobuf.FromReadMaterialResponse(v), protobuf.FromError(err)
}
func (c WorkerClient) ReadMaterial(ctx context.Context, q *dto.ReadMaterialRequest) (agent.MaterialReader, error) {
	stream, err := c.Client.ReadMaterial(ctx, protobuf.ToReadMaterialRequest(q))
	if err != nil {
		return nil, protobuf.FromError(err)
	}
	return materialReader{stream}, nil
}
