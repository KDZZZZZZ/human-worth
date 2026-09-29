package protobuf

import (
	"errors"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// AgentPayloads preserves the established model JSON and typed-result digest.
// The agent decides role, context and tools; this adapter only encodes its projection.
type AgentPayloads struct{}

func (AgentPayloads) Input(a *domain.Assignment) ([]byte, error) {
	var input proto.Message
	switch a.Kind {
	case domain.WorkKind_WORK_KIND_VALIDATE_RANKER, domain.WorkKind_WORK_KIND_TRAIN_RANKER, domain.WorkKind_WORK_KIND_RANK_WORKS:
		input = ToRankInput(a.GetRank())
	case domain.WorkKind_WORK_KIND_REFINE_RANKER:
		input = ToRefineRankerInput(a.GetRefine())
	case domain.WorkKind_WORK_KIND_PACK_TASK:
		input = ToPackInput(a.GetPack())
	case domain.WorkKind_WORK_KIND_EXPLAIN_OWN_WORK:
		input = ToExplainInput(a.GetExplain())
	default:
		return nil, errors.New("invalid model work kind")
	}
	return protojson.Marshal(input)
}
func (AgentPayloads) ExecutionInput(v *domain.ExecuteInput) ([]byte, error) {
	return protojson.Marshal(ToExecuteInput(v))
}
func (AgentPayloads) Result(kind domain.WorkKind, raw []byte) (*dto.CompleteWorkRequest, error) {
	q := &pb.CompleteWorkRequest{}
	decoder := protojson.UnmarshalOptions{DiscardUnknown: false}
	var err error
	switch kind {
	case domain.WorkKind_WORK_KIND_PACK_TASK, domain.WorkKind_WORK_KIND_REFINE_RANKER:
		v := &pb.PromptResult{}
		err = decoder.Unmarshal(raw, v)
		q.Result = &pb.CompleteWorkRequest_Prompt{Prompt: v}
	case domain.WorkKind_WORK_KIND_EXPLAIN_OWN_WORK:
		v := &pb.Judgment{}
		err = decoder.Unmarshal(raw, v)
		q.Result = &pb.CompleteWorkRequest_Judgment{Judgment: v}
	default:
		v := &pb.Rankings{}
		err = decoder.Unmarshal(raw, v)
		q.Result = &pb.CompleteWorkRequest_Rankings{Rankings: v}
	}
	return FromCompleteWorkRequest(q), err
}
func (AgentPayloads) ResultDigest(q *dto.CompleteWorkRequest) string { return Fingerprints{}.Result(q) }
