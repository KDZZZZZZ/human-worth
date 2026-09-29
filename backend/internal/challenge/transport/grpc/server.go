package grpc

import (
	"context"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/protobuf"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type Server struct {
	pb.UnimplementedChallengeServiceServer
	service *application.Service
}

func NewServer(service *application.Service) *Server { return &Server{service: service} }

// The certificate is transport-authenticated; no worker-supplied field can set its fingerprint.
func workerContext(ctx context.Context) (context.Context, error) {
	name, err := platform.ServiceName(ctx)
	if err != nil || name != "challenge-worker" {
		return nil, status.Error(codes.PermissionDenied, "worker_identity_required")
	}
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "service_identity_required")
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.PeerCertificates) == 0 {
		return nil, status.Error(codes.Unauthenticated, "service_identity_required")
	}
	return application.WithWorkerCertificate(ctx, domain.Hash(info.State.PeerCertificates[0].Raw)), nil
}

func (s *Server) StartRun(ctx context.Context, q *pb.StartRunRequest) (*pb.StartRunResponse, error) {
	v, err := s.service.StartRun(ctx, protobuf.FromStartRunRequest(q))
	return protobuf.ToStartRunResponse(v), protobuf.Error(err)
}
func (s *Server) GetRun(ctx context.Context, q *pb.GetRunRequest) (*pb.GetRunResponse, error) {
	v, err := s.service.GetRun(ctx, protobuf.FromGetRunRequest(q))
	return protobuf.ToGetRunResponse(v), protobuf.Error(err)
}
func (s *Server) ListRuns(ctx context.Context, q *pb.ListRunsRequest) (*pb.ListRunsResponse, error) {
	v, err := s.service.ListRuns(ctx, protobuf.FromListRunsRequest(q))
	return protobuf.ToListRunsResponse(v), protobuf.Error(err)
}
func (s *Server) GetRunSummary(ctx context.Context, q *pb.GetRunSummaryRequest) (*pb.GetRunSummaryResponse, error) {
	v, err := s.service.GetRunSummary(ctx, protobuf.FromGetRunSummaryRequest(q))
	return protobuf.ToGetRunSummaryResponse(v), protobuf.Error(err)
}
func (s *Server) CancelRun(ctx context.Context, q *pb.CancelRunRequest) (*pb.CancelRunResponse, error) {
	v, err := s.service.CancelRun(ctx, protobuf.FromCancelRunRequest(q))
	return protobuf.ToCancelRunResponse(v), protobuf.Error(err)
}
func (s *Server) RestartRun(ctx context.Context, q *pb.RestartRunRequest) (*pb.RestartRunResponse, error) {
	v, err := s.service.RestartRun(ctx, protobuf.FromRestartRunRequest(q))
	return protobuf.ToRestartRunResponse(v), protobuf.Error(err)
}
func (s *Server) RegisterCandidate(ctx context.Context, q *pb.RegisterCandidateRequest) (*pb.RegisterCandidateResponse, error) {
	v, err := s.service.RegisterCandidate(ctx, protobuf.FromRegisterCandidateRequest(q))
	return protobuf.ToRegisterCandidateResponse(v), protobuf.Error(err)
}
func (s *Server) ClaimWork(ctx context.Context, q *pb.ClaimWorkRequest) (*pb.ClaimWorkResponse, error) {
	ctx, err := workerContext(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.service.ClaimWork(ctx, protobuf.FromClaimWorkRequest(q))
	return protobuf.ToClaimWorkResponse(v), protobuf.Error(err)
}
func (s *Server) RenewLease(ctx context.Context, q *pb.RenewLeaseRequest) (*pb.RenewLeaseResponse, error) {
	ctx, err := workerContext(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.service.RenewLease(ctx, protobuf.FromRenewLeaseRequest(q))
	return protobuf.ToRenewLeaseResponse(v), protobuf.Error(err)
}
func (s *Server) ReportProgress(ctx context.Context, q *pb.ReportProgressRequest) (*pb.ReportProgressResponse, error) {
	ctx, err := workerContext(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.service.ReportProgress(ctx, protobuf.FromReportProgressRequest(q))
	return protobuf.ToReportProgressResponse(v), protobuf.Error(err)
}
func (s *Server) CompleteWork(ctx context.Context, q *pb.CompleteWorkRequest) (*pb.CompleteWorkResponse, error) {
	ctx, err := workerContext(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.service.CompleteWork(ctx, protobuf.FromCompleteWorkRequest(q))
	return protobuf.ToCompleteWorkResponse(v), protobuf.Error(err)
}
func (s *Server) FailWork(ctx context.Context, q *pb.FailWorkRequest) (*pb.FailWorkResponse, error) {
	ctx, err := workerContext(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.service.FailWork(ctx, protobuf.FromFailWorkRequest(q))
	return protobuf.ToFailWorkResponse(v), protobuf.Error(err)
}
func (s *Server) ActivateAttempt(ctx context.Context, q *pb.ActivateAttemptRequest) (*pb.ActivateAttemptResponse, error) {
	ctx, err := workerContext(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.service.ActivateAttempt(ctx, protobuf.FromActivateAttemptRequest(q))
	return protobuf.ToActivateAttemptResponse(v), protobuf.Error(err)
}
func (s *Server) CheckTaskAccess(ctx context.Context, q *pb.CheckTaskAccessRequest) (*pb.CheckTaskAccessResponse, error) {
	ctx, err := workerContext(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.service.CheckTaskAccess(ctx, protobuf.FromCheckTaskAccessRequest(q))
	return protobuf.ToCheckTaskAccessResponse(v), protobuf.Error(err)
}
func (s *Server) ReserveModelCall(ctx context.Context, q *pb.ReserveModelCallRequest) (*pb.ReserveModelCallResponse, error) {
	ctx, err := workerContext(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.service.ReserveModelCall(ctx, protobuf.FromReserveModelCallRequest(q))
	return protobuf.ToReserveModelCallResponse(v), protobuf.Error(err)
}
func (s *Server) SettleModelCall(ctx context.Context, q *pb.SettleModelCallRequest) (*pb.SettleModelCallResponse, error) {
	ctx, err := workerContext(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.service.SettleModelCall(ctx, protobuf.FromSettleModelCallRequest(q))
	return protobuf.ToSettleModelCallResponse(v), protobuf.Error(err)
}

type materialStream struct {
	grpc.ServerStreamingServer[pb.ReadMaterialResponse]
	ctx context.Context
}

func (s materialStream) Context() context.Context { return s.ctx }
func (s materialStream) Send(v *dto.ReadMaterialResponse) error {
	return protobuf.FromError(s.ServerStreamingServer.Send(protobuf.ToReadMaterialResponse(v)))
}
func (s *Server) ReadMaterial(q *pb.ReadMaterialRequest, stream grpc.ServerStreamingServer[pb.ReadMaterialResponse]) error {
	ctx, err := workerContext(stream.Context())
	if err != nil {
		return err
	}
	return protobuf.Error(s.service.ReadMaterial(protobuf.FromReadMaterialRequest(q), materialStream{ServerStreamingServer: stream, ctx: ctx}))
}

type uploadStream struct {
	grpc.ClientStreamingServer[pb.UploadArtifactRequest, pb.UploadArtifactResponse]
	ctx context.Context
}

func (s uploadStream) Context() context.Context { return s.ctx }
func (s uploadStream) Recv() (*dto.UploadArtifactRequest, error) {
	v, err := s.ClientStreamingServer.Recv()
	return protobuf.FromUploadArtifactRequest(v), protobuf.FromError(err)
}
func (s uploadStream) SendAndClose(v *dto.UploadArtifactResponse) error {
	return protobuf.FromError(s.ClientStreamingServer.SendAndClose(protobuf.ToUploadArtifactResponse(v)))
}
func (s *Server) UploadArtifact(stream grpc.ClientStreamingServer[pb.UploadArtifactRequest, pb.UploadArtifactResponse]) error {
	ctx, err := workerContext(stream.Context())
	if err != nil {
		return err
	}
	return protobuf.Error(s.service.UploadArtifact(uploadStream{ClientStreamingServer: stream, ctx: ctx}))
}
