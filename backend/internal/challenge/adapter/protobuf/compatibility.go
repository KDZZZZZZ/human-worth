package protobuf

import (
	"context"
	"errors"
	"io"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var wireCodes = map[domain.Code]codes.Code{
	domain.InvalidArgument: codes.InvalidArgument, domain.PermissionDenied: codes.PermissionDenied,
	domain.FailedPrecondition: codes.FailedPrecondition, domain.Unavailable: codes.Unavailable,
	domain.NotFound: codes.NotFound, domain.Aborted: codes.Aborted, domain.ResourceExhausted: codes.ResourceExhausted,
	domain.Unauthenticated: codes.Unauthenticated, domain.DeadlineExceeded: codes.DeadlineExceeded, domain.Canceled: codes.Canceled,
	domain.AlreadyExists: codes.AlreadyExists, domain.Internal: codes.Internal, domain.OutOfRange: codes.OutOfRange,
	domain.Unimplemented: codes.Unimplemented, domain.DataLoss: codes.DataLoss,
}

func Error(err error) error {
	if err == nil {
		return nil
	}
	code, ok := wireCodes[domain.ErrorCode(err)]
	if !ok {
		return status.Error(codes.Unavailable, "challenge_unavailable")
	}
	return status.Error(code, err.Error())
}
func FromError(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	s := status.Convert(err)
	for code, wire := range wireCodes {
		if s.Code() == wire {
			return domain.Reject(code, s.Message())
		}
	}
	return domain.UnavailableError()
}

type Fingerprints struct{}

func messageHash(v proto.Message) string {
	b, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(v)
	return domain.Hash(b)
}
func (Fingerprints) Configuration(v *domain.RunConfiguration) string {
	return messageHash(ToRunConfiguration(v))
}
func (Fingerprints) Model(v *domain.ModelConfiguration) string {
	return messageHash(ToModelConfiguration(v))
}
func (Fingerprints) Executor(v *domain.ExecutorConfiguration) string {
	return messageHash(ToExecutorConfiguration(v))
}
func (Fingerprints) Claim(v *dto.ClaimWorkRequest) string { return messageHash(ToClaimWorkRequest(v)) }
func (Fingerprints) Result(v *dto.CompleteWorkRequest) string {
	return ResultDigest(ToCompleteWorkRequest(v))
}
func (Fingerprints) SampleSize(v *domain.RankingSample) int { return proto.Size(ToRankingSample(v)) }
func (Fingerprints) RefineSize(v *domain.RefineRankerInput) int {
	return proto.Size(ToRefineRankerInput(v))
}
func (Fingerprints) PreferenceSize(v *domain.HumanPreference) int {
	return proto.Size(ToHumanPreference(v))
}

// ResultDigest retains the byte-level idempotency contract with existing workers.
func ResultDigest(q *pb.CompleteWorkRequest) string {
	copy := proto.Clone(q).(*pb.CompleteWorkRequest)
	copy.Attempt = nil
	copy.ResultDigest = ""
	return messageHash(copy)
}
