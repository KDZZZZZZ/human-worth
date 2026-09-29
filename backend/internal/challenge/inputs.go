package challenge

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	asset "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	content "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	voting "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const maxMaterialBytes int64 = 16 << 20
const maxInputBytes = 1 << 20

// InputPolicyVersion 随角色投影、系统指令或工具语义变更递增，旧拟合记录不能跨策略复用。
const InputPolicyVersion = "materials-v1"

func InputPolicyHash(version string, modelCalls, toolCalls int32) string {
	return hash([]byte(fmt.Sprintf("%s/%d/%d/%d", version, modelCalls, toolCalls, maxInputBytes)))
}

func validAsset(a *asset.Asset) bool {
	return a != nil && validID(a.Id) && bounded(a.Filename, 255) && !strings.ContainsAny(a.Filename, "/\\") && a.Bytes >= 0 && a.Bytes <= maxMaterialBytes && validDigest(a.Sha256) && bounded(a.MediaType, 128)
}
func validDigest(v string) bool {
	b, err := hex.DecodeString(v)
	return err == nil && len(b) == 32 && v == strings.ToLower(v)
}
func validWork(w *content.Work) bool {
	if w == nil || !validID(w.Id) || len(w.Artifacts) == 0 || len(w.Artifacts) > 32 {
		return false
	}
	actual := false
	for _, a := range w.Artifacts {
		if a == nil {
			return false
		}
		switch v := a.Value.(type) {
		case *content.Artifact_Text:
			if !bounded(v.Text, maxInputBytes) {
				return false
			}
			actual = true
		case *content.Artifact_File:
			if !validAsset(v.File) {
				return false
			}
			actual = true
		case *content.Artifact_Link:
			if !bounded(v.Link, 4096) {
				return false
			}
		default:
			return false
		}
	}
	return actual
}

// WorkSetHash 与投票快照约定同一候选 ID 集合；内容版本由 SnapshotRef 单独绑定。
func WorkSetHash(works []*content.Work) string {
	ids := make([]string, 0, len(works))
	for _, w := range works {
		ids = append(ids, w.Id)
	}
	sort.Strings(ids)
	return hash([]byte(strings.Join(ids, "\n")))
}

func (s *Server) readSample(ctx context.Context, ref *content.SnapshotRef) (*pb.RankingSample, *content.SnapshotRef, error) {
	if ref == nil || !validID(ref.TaskId) {
		return nil, nil, invalid("invalid_snapshot")
	}
	task, err := s.Content.GetChallengeTaskDescription(ctx, &content.GetChallengeTaskDescriptionRequest{Snapshot: ref})
	if err != nil {
		return nil, nil, err
	}
	if !task.Visible || task.Task == nil || task.Task.TaskId != ref.TaskId || task.Task.Revision < 1 || ref.TaskRevision != 0 && task.Task.Revision != ref.TaskRevision || !bounded(task.Task.Description, 65536) {
		return nil, nil, conflict("task_unavailable")
	}
	attachments := map[string]bool{}
	for _, a := range task.Task.Attachments {
		if a == nil || !a.CloudUseAllowed || !validAsset(a.Asset) || attachments[a.Asset.Id] {
			return nil, nil, conflict("material_unavailable")
		}
		attachments[a.Asset.Id] = true
	}
	works, err := s.Content.ListChallengeWorks(ctx, &content.ListChallengeWorksRequest{Snapshot: ref})
	if err != nil {
		return nil, nil, err
	}
	if works.CatalogRevision < 1 || ref.CatalogRevision != 0 && works.CatalogRevision != ref.CatalogRevision || len(works.Works) < 2 || len(works.Works) > 100 {
		return nil, nil, conflict("work_set_unavailable")
	}
	seen := map[string]bool{}
	for _, w := range works.Works {
		if !validWork(w) || seen[w.Id] {
			return nil, nil, conflict("invalid_work_set")
		}
		seen[w.Id] = true
	}
	comments, err := s.Content.ListChallengeComments(ctx, &content.ListChallengeCommentsRequest{Snapshot: ref})
	if err != nil {
		return nil, nil, err
	}
	if comments.CutoffAt == nil || comments.CutoffAt.CheckValid() != nil || ref.CommentCutoff != nil && comments.CutoffAt.AsTime().After(ref.CommentCutoff.AsTime()) || len(comments.Comments) > 500 {
		return nil, nil, conflict("invalid_comment_snapshot")
	}
	commentIDs := map[string]bool{}
	for _, c := range comments.Comments {
		if c == nil || !validID(c.Id) || !bounded(c.Body, 8192) || commentIDs[c.Id] {
			return nil, nil, conflict("invalid_comment_snapshot")
		}
		commentIDs[c.Id] = true
	}
	sample := &pb.RankingSample{Task: task.Task, Works: works.Works, Comments: comments.Comments}
	if proto.Size(sample) > maxInputBytes {
		return nil, nil, status.Error(codes.ResourceExhausted, "materials_too_large")
	}
	fixed := &content.SnapshotRef{TaskId: ref.TaskId, TaskRevision: task.Task.Revision, CatalogRevision: works.CatalogRevision, CommentCutoff: comments.CutoffAt}
	return sample, fixed, nil
}

func (s *Server) prepare(ctx context.Context, st *pb.StoredRun) error {
	sample, ref, err := s.readSample(ctx, &content.SnapshotRef{TaskId: st.Run.TaskId, TaskRevision: st.Run.Configuration.InitialTaskPackage.TaskRevision, CommentCutoff: timestamppb.Now()})
	if err != nil {
		return err
	}
	st.Task = sample.Task
	st.Target = ref
	st.Works = sample.Works
	st.Comments = sample.Comments
	allowed := map[string]bool{}
	for _, a := range st.Task.Attachments {
		allowed[a.Asset.Id] = true
	}
	for _, v := range st.Run.Configuration.InitialTaskPackage.TaskAttachmentIds {
		if !allowed[v] {
			return invalid("attachment_not_in_task")
		}
	}
	return s.validationBatch(ctx, st)
}

// batch 向 Voting 取标签，只有无标签的 RankingSample 会进入 R 的验证输入。
func (s *Server) batch(ctx context.Context, st *pb.StoredRun, purpose string) (*voting.GetHumanPreferenceSnapshotResponse, []*pb.RankingSample, error) {
	b, err := s.Voting.GetHumanPreferenceSnapshot(ctx, &voting.GetHumanPreferenceSnapshotRequest{RunId: st.Run.Id, Purpose: purpose, ExcludedTaskIds: st.SeenTaskIds, BatchIndex: st.Run.RankerRound})
	if err != nil {
		return nil, nil, err
	}
	if b.SnapshotId == "" || b.PolicyVersion == "" || b.ValidUntil == nil || b.ValidUntil.CheckValid() != nil || !b.ValidUntil.AsTime().After(time.Now()) || len(b.Samples) == 0 || len(b.Samples) > 20 {
		return nil, nil, conflict("insufficient_preference_data")
	}
	seen := map[string]bool{}
	for _, task := range st.SeenTaskIds {
		seen[task] = true
	}
	inputs := []*pb.RankingSample{}
	pairs := 0
	inputBytes := 0
	for _, p := range b.Samples {
		if p == nil || p.Snapshot == nil || seen[p.Snapshot.TaskId] || p.WindowStart == nil || p.WindowEnd == nil || p.WindowStart.CheckValid() != nil || p.WindowEnd.CheckValid() != nil || p.WindowEnd.AsTime().After(time.Now()) || !p.WindowEnd.AsTime().After(p.WindowStart.AsTime()) || p.Snapshot.CommentCutoff == nil || p.Snapshot.CommentCutoff.CheckValid() != nil || !p.Snapshot.CommentCutoff.AsTime().Before(p.WindowStart.AsTime()) {
			return nil, nil, conflict("invalid_preference_snapshot")
		}
		input, fixed, err := s.readSample(ctx, p.Snapshot)
		if err != nil {
			return nil, nil, err
		}
		if fixed.TaskRevision != p.Snapshot.TaskRevision || fixed.CatalogRevision != p.Snapshot.CatalogRevision || WorkSetHash(input.Works) != p.WorkSetHash {
			return nil, nil, conflict("preference_snapshot_mismatch")
		}
		n, err := comparablePairs(input, p)
		if err != nil {
			return nil, nil, err
		}
		pairs += n
		inputs = append(inputs, input)
		inputBytes += proto.Size(input) + proto.Size(p) + 1024
		if inputBytes > maxInputBytes {
			return nil, nil, status.Error(codes.ResourceExhausted, "materials_too_large")
		}
		seen[p.Snapshot.TaskId] = true
	}
	if pairs < int(st.Run.Configuration.RankerMinComparablePairs) {
		return nil, nil, conflict("insufficient_preference_data")
	}
	for _, p := range b.Samples {
		st.SeenTaskIds = append(st.SeenTaskIds, p.Snapshot.TaskId)
	}
	return b, inputs, nil
}
func (s *Server) validationBatch(ctx context.Context, st *pb.StoredRun) error {
	b, inputs, err := s.batch(ctx, st, "validation")
	if err != nil {
		return err
	}
	st.Validation = b
	a := s.assignment(st, pb.WorkKind_WORK_KIND_VALIDATE_RANKER)
	a.Input = &pb.Assignment_Rank{Rank: &pb.RankInput{Samples: inputs}}
	a.Materials = rankMaterials(inputs)
	st.Assignment = a
	return s.checkInputs(ctx, st, a)
}
func (s *Server) policyHash() string {
	return InputPolicyHash(s.Config.InputPolicy, s.Config.ModelCallLimit, s.Config.ToolCallLimit)
}
func (s *Server) fitBinding(st *pb.StoredRun) string {
	return hash([]byte(st.RankerPrompt + "\n" + messageHash(st.Run.Configuration.Ranker) + "\n" + s.policyHash() + "\n" + st.Validation.GetSnapshotId() + "\n" + st.Validation.GetPolicyVersion()))
}

// assignment 每个工作项从白名单字段重新投影，绝不把整个 StoredRun 交给模型。
func (s *Server) assignment(st *pb.StoredRun, kind pb.WorkKind) *pb.Assignment {
	a := &pb.Assignment{WorkItemId: id("work_"), Kind: kind, Role: "R", Model: proto.Clone(st.Run.Configuration.Ranker).(*pb.ModelConfiguration), ModelCallLimit: s.Config.ModelCallLimit, ToolCallLimit: s.Config.ToolCallLimit, InputPolicyHash: s.policyHash()}
	a.Model.Prompt = st.RankerPrompt
	switch kind {
	case pb.WorkKind_WORK_KIND_PACK_TASK:
		a.Role = "P"
		a.Model = proto.Clone(st.Run.Configuration.Packer).(*pb.ModelConfiguration)
	case pb.WorkKind_WORK_KIND_EXECUTE_PACKAGE:
		a.Role = "E"
		a.Model = nil
		a.Executor = proto.Clone(st.Run.Configuration.Executor).(*pb.ExecutorConfiguration)
	}
	return a
}
func taskMaterials(task *content.TaskDescription, selected map[string]bool) []*pb.Material {
	var result []*pb.Material
	for _, a := range task.Attachments {
		if selected == nil || selected[a.Asset.Id] {
			result = append(result, &pb.Material{Id: hash([]byte(task.TaskId + "/task/" + a.Asset.Id)), Asset: a.Asset, TaskId: task.TaskId, SourceKind: "task", SourceId: task.TaskId})
		}
	}
	return result
}
func workMaterials(taskID string, w *content.Work, own bool) []*pb.Material {
	var result []*pb.Material
	if w == nil {
		return result
	}
	kind := "work"
	if own {
		kind = "own_work"
	}
	for _, a := range w.Artifacts {
		if f := a.GetFile(); f != nil {
			result = append(result, &pb.Material{Id: hash([]byte(taskID + "/" + w.Id + "/" + f.Id)), Asset: f, TaskId: taskID, SourceKind: kind, SourceId: w.Id})
		}
	}
	return result
}
func rankMaterials(inputs []*pb.RankingSample) []*pb.Material {
	var out []*pb.Material
	for _, in := range inputs {
		out = append(out, taskMaterials(in.Task, nil)...)
		for _, w := range in.Works {
			out = append(out, workMaterials(in.Task.TaskId, w, false)...)
		}
	}
	return out
}
func selectedMaterials(st *pb.StoredRun) []*pb.Material {
	selected := map[string]bool{}
	for _, id := range st.Run.Configuration.InitialTaskPackage.TaskAttachmentIds {
		selected[id] = true
	}
	return taskMaterials(st.Task, selected)
}

func (s *Server) nextPack(st *pb.StoredRun) {
	st.Run.Round++
	st.Run.Stage = "optimizing_packer"
	st.Pending = ""
	a := s.assignment(st, pb.WorkKind_WORK_KIND_PACK_TASK)
	a.Input = &pb.Assignment_Pack{Pack: &pb.PackInput{Initial: st.Run.Configuration.InitialTaskPackage, ExecutionHints: st.ExecutionHints, Feedback: st.Feedback}}
	a.Materials = append(selectedMaterials(st), workMaterials(st.Run.TaskId, st.Feedback.GetWork(), true)...)
	st.Assignment = a
}

// checkInputs 每次发放材料或接收结果都重新核实当前授权；验证标签从不进入此请求。
func (s *Server) checkInputs(ctx context.Context, st *pb.StoredRun, a *pb.Assignment) error {
	if a == nil || a.InputPolicyHash != s.policyHash() {
		return conflict("input_policy_changed")
	}
	if st.Run.Stage == "optimizing_packer" || st.Run.Stage == "registering" {
		if st.Run.RankerFit.Status != "passed" || st.Run.RankerFit.Score == nil || *st.Run.RankerFit.Score <= st.Run.Configuration.RankerFitThreshold || st.FitBinding != s.fitBinding(st) {
			return conflict("ranker_fit_invalidated")
		}
	}
	if st.Validation != nil {
		if st.Validation.ValidUntil == nil || !st.Validation.ValidUntil.AsTime().After(time.Now()) {
			return conflict("ranker_fit_invalidated")
		}
		v, err := s.Voting.CheckHumanPreferenceSnapshot(ctx, &voting.CheckHumanPreferenceSnapshotRequest{SnapshotId: st.Validation.SnapshotId, PolicyVersion: st.Validation.PolicyVersion})
		if err != nil {
			return unavailable()
		}
		if !v.Valid {
			return conflict("ranker_fit_invalidated")
		}
	}
	refs := map[string]*content.SnapshotRef{st.Run.TaskId: st.Target}
	// 优先沿用 Voting 绑定的目录版本与评论截点，不能降级为仅校验任务描述版本。
	snapshotFor := func(task string, revision int64) *content.SnapshotRef {
		if task == st.Run.TaskId {
			return st.Target
		}
		for _, batch := range []*voting.GetHumanPreferenceSnapshotResponse{st.Validation, st.Training} {
			for _, sample := range batch.GetSamples() {
				if sample.Snapshot.TaskId == task {
					return sample.Snapshot
				}
			}
		}
		return &content.SnapshotRef{TaskId: task, TaskRevision: revision}
	}
	works := map[string][]string{}
	if rank := a.GetRank(); rank != nil {
		for _, sample := range rank.Samples {
			refs[sample.Task.TaskId] = snapshotFor(sample.Task.TaskId, sample.Task.Revision)
			for _, w := range sample.Works {
				if w.Id != st.Feedback.GetWork().GetId() {
					works[sample.Task.TaskId] = append(works[sample.Task.TaskId], w.Id)
				}
			}
		}
	}
	if refine := a.GetRefine(); refine != nil {
		for _, t := range refine.Samples {
			refs[t.Sample.Task.TaskId] = snapshotFor(t.Sample.Task.TaskId, t.Sample.Task.Revision)
			for _, w := range t.Sample.Works {
				works[t.Sample.Task.TaskId] = append(works[t.Sample.Task.TaskId], w.Id)
			}
		}
	}
	for task, ref := range refs {
		req := &content.CheckChallengeMaterialsRequest{RunId: st.Run.Id, Snapshot: ref, Purpose: a.Kind.String(), WorkIds: works[task]}
		for _, m := range a.Materials {
			if m.TaskId == task && m.SourceKind != "own_work" {
				req.Materials = append(req.Materials, &content.MaterialRef{AssetId: m.Asset.Id, SourceKind: m.SourceKind, SourceId: m.SourceId})
			}
		}
		v, err := s.Content.CheckChallengeMaterials(ctx, req)
		if err != nil {
			return unavailable()
		}
		if !v.Allowed {
			return conflict("material_unavailable")
		}
	}
	return nil
}
