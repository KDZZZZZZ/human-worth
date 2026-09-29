package application

import (
	"context"
	"time"

	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
)

func (s *Service) readSample(ctx context.Context, ref *domain.SnapshotRef) (*domain.RankingSample, *domain.SnapshotRef, error) {
	if ref == nil || !domain.ValidID(ref.TaskId) {
		return nil, nil, domain.Invalid("invalid_snapshot")
	}
	task, err := s.content.GetChallengeTaskDescription(ctx, &dto.GetChallengeTaskDescriptionRequest{Snapshot: ref})
	if err != nil {
		return nil, nil, err
	}
	if !task.Visible || task.Task == nil || task.Task.TaskId != ref.TaskId || task.Task.Revision < 1 || ref.TaskRevision != 0 && task.Task.Revision != ref.TaskRevision || !domain.Bounded(task.Task.Description, 65536) {
		return nil, nil, domain.Conflict("task_unavailable")
	}
	attachments := map[string]bool{}
	for _, a := range task.Task.Attachments {
		if a == nil || !a.CloudUseAllowed || !domain.ValidAsset(a.Asset) || attachments[a.Asset.Id] {
			return nil, nil, domain.Conflict("material_unavailable")
		}
		attachments[a.Asset.Id] = true
	}
	works, err := s.content.ListChallengeWorks(ctx, &dto.ListChallengeWorksRequest{Snapshot: ref})
	if err != nil {
		return nil, nil, err
	}
	if works.CatalogRevision < 1 || ref.CatalogRevision != 0 && works.CatalogRevision != ref.CatalogRevision || len(works.Works) < 2 || len(works.Works) > 100 {
		return nil, nil, domain.Conflict("work_set_unavailable")
	}
	seen := map[string]bool{}
	for _, w := range works.Works {
		if !domain.ValidWork(w) || seen[w.Id] {
			return nil, nil, domain.Conflict("invalid_work_set")
		}
		seen[w.Id] = true
	}
	comments, err := s.content.ListChallengeComments(ctx, &dto.ListChallengeCommentsRequest{Snapshot: ref})
	if err != nil {
		return nil, nil, err
	}
	if comments.CutoffAt == nil || !domain.ValidTime(comments.CutoffAt) || ref.CommentCutoff != nil && comments.CutoffAt.UTC().After(ref.CommentCutoff.UTC()) || len(comments.Comments) > 500 {
		return nil, nil, domain.Conflict("invalid_comment_snapshot")
	}
	commentIDs := map[string]bool{}
	for _, c := range comments.Comments {
		if c == nil || !domain.ValidID(c.Id) || !domain.Bounded(c.Body, 8192) || commentIDs[c.Id] {
			return nil, nil, domain.Conflict("invalid_comment_snapshot")
		}
		commentIDs[c.Id] = true
	}
	sample := &domain.RankingSample{Task: task.Task, Works: works.Works, Comments: comments.Comments}
	if s.fingerprints.SampleSize(sample) > domain.MaxInputBytes {
		return nil, nil, domain.Reject(domain.ResourceExhausted, "materials_too_large")
	}
	fixed := &domain.SnapshotRef{TaskId: ref.TaskId, TaskRevision: task.Task.Revision, CatalogRevision: works.CatalogRevision, CommentCutoff: comments.CutoffAt}
	return sample, fixed, nil
}

func (s *Service) prepare(ctx context.Context, st *domain.State) error {
	sample, ref, err := s.readSample(ctx, &domain.SnapshotRef{TaskId: st.Run.TaskId, TaskRevision: st.Run.Configuration.InitialTaskPackage.TaskRevision, CommentCutoff: domain.TimePtr(time.Now())})
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
			return domain.Invalid("attachment_not_in_task")
		}
	}
	// 标准生成已能读取目标任务，独立验证不能再使用它或任何已暴露的训练任务。
	st.SeenTaskIds = append(st.SeenTaskIds, st.Run.TaskId)
	batch, inputs, err := s.batch(ctx, st, "training")
	if err != nil {
		return err
	}
	s.engine.SetTraining(st, batch, inputs)
	return s.checkInputs(ctx, st, st.Assignment)
}

// batch 向 Voting 取标签，只有无标签的 RankingSample 会进入 R 的验证输入。
func (s *Service) batch(ctx context.Context, st *domain.State, purpose string) (*domain.PreferenceBatch, []*domain.RankingSample, error) {
	b, err := s.voting.GetHumanPreferenceSnapshot(ctx, &dto.GetHumanPreferenceSnapshotRequest{RunId: st.Run.Id, Purpose: purpose, ExcludedTaskIds: st.SeenTaskIds, BatchIndex: st.Run.RankerRound})
	if err != nil {
		return nil, nil, err
	}
	if b.SnapshotId == "" || b.PolicyVersion == "" || b.ValidUntil == nil || !domain.ValidTime(b.ValidUntil) || !b.ValidUntil.UTC().After(time.Now()) || len(b.Samples) == 0 || len(b.Samples) > 20 {
		return nil, nil, domain.Conflict("insufficient_preference_data")
	}
	seen := map[string]bool{}
	for _, task := range st.SeenTaskIds {
		seen[task] = true
	}
	inputs := []*domain.RankingSample{}
	pairs := 0
	inputBytes := 0
	for _, p := range b.Samples {
		if p == nil || p.Snapshot == nil || seen[p.Snapshot.TaskId] || p.WindowStart == nil || p.WindowEnd == nil || !domain.ValidTime(p.WindowStart) || !domain.ValidTime(p.WindowEnd) || p.WindowEnd.UTC().After(time.Now()) || !p.WindowEnd.UTC().After(p.WindowStart.UTC()) || p.Snapshot.CommentCutoff == nil || !domain.ValidTime(p.Snapshot.CommentCutoff) || !p.Snapshot.CommentCutoff.UTC().Before(p.WindowStart.UTC()) {
			return nil, nil, domain.Conflict("invalid_preference_snapshot")
		}
		input, fixed, err := s.readSample(ctx, p.Snapshot)
		if err != nil {
			return nil, nil, err
		}
		if fixed.TaskRevision != p.Snapshot.TaskRevision || fixed.CatalogRevision != p.Snapshot.CatalogRevision || domain.WorkSetHash(input.Works) != p.WorkSetHash {
			return nil, nil, domain.Conflict("preference_snapshot_mismatch")
		}
		n, err := domain.ComparablePairs(input, p)
		if err != nil {
			return nil, nil, err
		}
		pairs += n
		inputs = append(inputs, input)
		inputBytes += s.fingerprints.SampleSize(input) + s.fingerprints.PreferenceSize(p) + 1024
		if inputBytes > domain.MaxInputBytes {
			return nil, nil, domain.Reject(domain.ResourceExhausted, "materials_too_large")
		}
		seen[p.Snapshot.TaskId] = true
	}
	if pairs < int(st.Run.Configuration.RankerMinComparablePairs) {
		return nil, nil, domain.Conflict("insufficient_preference_data")
	}
	for _, p := range b.Samples {
		st.SeenTaskIds = append(st.SeenTaskIds, p.Snapshot.TaskId)
	}
	return b, inputs, nil
}
func (s *Service) validationBatch(ctx context.Context, st *domain.State) error {
	b, inputs, err := s.batch(ctx, st, "validation")
	if err != nil {
		return err
	}
	s.engine.SetValidation(st, b, inputs)
	return s.checkInputs(ctx, st, st.Assignment)
}
func (s *Service) fitBinding(st *domain.State) string {
	return domain.Hash([]byte(st.RankerPrompt + "\n" + s.fingerprints.Model(st.Run.Configuration.Ranker) + "\n" + s.engine.PolicyHash() + "\n" + st.Validation.GetSnapshotId() + "\n" + st.Validation.GetPolicyVersion()))
}

// checkInputs 每次发放材料或接收结果都重新核实当前授权；验证标签从不进入此请求。
func (s *Service) checkInputs(ctx context.Context, st *domain.State, a *domain.Assignment) error {
	if a == nil || a.InputPolicyHash != s.engine.WorkPolicyHash(a.Kind) {
		return domain.Conflict("input_policy_changed")
	}
	if st.Run.Stage == "optimizing_packer" || st.Run.Stage == "registering" {
		if st.Run.RankerFit.Status != "passed" || st.Run.RankerFit.Score == nil || *st.Run.RankerFit.Score <= st.Run.Configuration.RankerFitThreshold || st.FitBinding != s.fitBinding(st) {
			return domain.Conflict("ranker_fit_invalidated")
		}
	}
	batches := []*domain.PreferenceBatch{st.Validation}
	if a.Kind == domain.WorkKind_WORK_KIND_REFINE_RANKER {
		batches = append(batches, st.Training)
	}
	for _, batch := range batches {
		if batch == nil {
			continue
		}
		if batch.ValidUntil == nil || !batch.ValidUntil.UTC().After(time.Now()) {
			return domain.Conflict("ranker_fit_invalidated")
		}
		v, err := s.voting.CheckHumanPreferenceSnapshot(ctx, &dto.CheckHumanPreferenceSnapshotRequest{SnapshotId: batch.SnapshotId, PolicyVersion: batch.PolicyVersion})
		if err != nil {
			return domain.UnavailableError()
		}
		if !v.Valid {
			return domain.Conflict("ranker_fit_invalidated")
		}
	}
	refs := map[string]*domain.SnapshotRef{st.Run.TaskId: st.Target}
	// 优先沿用 Voting 绑定的目录版本与评论截点，不能降级为仅校验任务描述版本。
	snapshotFor := func(task string, revision int64) *domain.SnapshotRef {
		if task == st.Run.TaskId {
			return st.Target
		}
		for _, batch := range []*domain.PreferenceBatch{st.Validation, st.Training} {
			for _, sample := range batch.GetSamples() {
				if sample.Snapshot.TaskId == task {
					return sample.Snapshot
				}
			}
		}
		return &domain.SnapshotRef{TaskId: task, TaskRevision: revision}
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
		if refine.Target == nil {
			return domain.Invalid("criteria_context_required")
		}
		if s.fingerprints.RefineSize(refine) > domain.MaxInputBytes {
			return domain.Reject(domain.ResourceExhausted, "materials_too_large")
		}
		for _, w := range refine.Target.Works {
			works[st.Run.TaskId] = append(works[st.Run.TaskId], w.Id)
		}
		for _, t := range refine.Samples {
			refs[t.Sample.Task.TaskId] = snapshotFor(t.Sample.Task.TaskId, t.Sample.Task.Revision)
			for _, w := range t.Sample.Works {
				works[t.Sample.Task.TaskId] = append(works[t.Sample.Task.TaskId], w.Id)
			}
		}
	}
	for task, ref := range refs {
		req := &dto.CheckChallengeMaterialsRequest{RunId: st.Run.Id, Snapshot: ref, Purpose: a.Kind.String(), WorkIds: works[task]}
		for _, m := range a.Materials {
			if m.TaskId == task && m.SourceKind != "own_work" {
				req.Materials = append(req.Materials, &domain.MaterialRef{AssetId: m.Asset.Id, SourceKind: m.SourceKind, SourceId: m.SourceId})
			}
		}
		v, err := s.content.CheckChallengeMaterials(ctx, req)
		if err != nil {
			return domain.UnavailableError()
		}
		if !v.Allowed {
			return domain.Conflict("material_unavailable")
		}
	}
	return nil
}
