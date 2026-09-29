package domain

import (
	"strings"
	"time"
)

type Result struct {
	Prompt    *PromptResult
	Rankings  *Rankings
	Generated *GeneratedWork
	Judgment  *Judgment
}

func (e Engine) PolicyHash() string {
	return InputPolicyHash(e.Config.InputPolicy, e.Config.ModelCallLimit, e.Config.ToolCallLimit)
}

func (e Engine) WorkPolicyHash(kind WorkKind) string {
	if kind != WorkKind_WORK_KIND_EXECUTE_PACKAGE {
		return InputPolicyHash(e.Config.InputPolicy, 1, 0)
	}
	return e.PolicyHash()
}

// assignment 每个工作项从白名单字段重新投影，绝不把整个运行状态交给模型。
func (e Engine) assignment(st *State, kind WorkKind) *Assignment {
	a := &Assignment{WorkItemId: NewID("work_"), Kind: kind, Role: "R", Model: CloneModelConfiguration(st.Run.Configuration.Ranker), ModelCallLimit: 1, InputPolicyHash: e.WorkPolicyHash(kind)}
	a.Model.Prompt = st.RankerPrompt
	switch kind {
	case WorkKind_WORK_KIND_PACK_TASK:
		a.Role = "P"
		a.Model = CloneModelConfiguration(st.Run.Configuration.Packer)
	case WorkKind_WORK_KIND_EXECUTE_PACKAGE:
		a.Role = "E"
		a.ModelCallLimit = e.Config.ModelCallLimit
		a.ToolCallLimit = e.Config.ToolCallLimit
		a.Model = nil
		a.Executor = CloneExecutorConfiguration(st.Run.Configuration.Executor)
	}
	return a
}
func (e Engine) SetValidation(st *State, batch *PreferenceBatch, inputs []*RankingSample) {
	st.Validation = batch
	st.Assignment = e.rankingAssignment(st, WorkKind_WORK_KIND_VALIDATE_RANKER, inputs)
}

// SetTraining 把可用业务内容一次投影给标准生成调用，不复用之后判断时的会话。
func (e Engine) SetTraining(st *State, batch *PreferenceBatch, inputs []*RankingSample) {
	st.Training = batch
	st.TrainingInputs = inputs
	input := &RefineRankerInput{
		PreviousFit: st.Run.RankerFit.GetScore(),
		Target:      &RankingSample{Task: st.Task, Works: st.Works, Comments: st.Comments},
		Initial:     st.Run.Configuration.InitialTaskPackage,
	}
	byTask := map[string]*Ranking{}
	if st.TrainingRankings != nil {
		for _, ranking := range st.TrainingRankings.Items {
			byTask[ranking.TaskId] = ranking
		}
	}
	for i, sample := range inputs {
		input.Samples = append(input.Samples, &TrainingSample{Sample: sample, Ranking: byTask[sample.Task.TaskId], HumanCounts: batch.Samples[i].CountsByWork})
	}
	a := e.assignment(st, WorkKind_WORK_KIND_REFINE_RANKER)
	a.Input = &Assignment_Refine{Refine: input}
	a.Materials = append(RankMaterials(inputs), RankMaterials([]*RankingSample{input.Target})...)
	st.Assignment = a
}

func (e Engine) rankingAssignment(st *State, kind WorkKind, inputs []*RankingSample) *Assignment {
	a := e.assignment(st, kind)
	a.Input = &Assignment_Rank{Rank: &RankInput{Samples: inputs}}
	a.Materials = RankMaterials(inputs)
	return a
}

func (e Engine) RankGeneratedWork(st *State, input *RankingSample, ref *SnapshotRef) error {
	st.Target = ref
	st.Works = input.Works
	st.Comments = input.Comments
	for _, work := range input.Works {
		if work.Id == st.Feedback.Work.Id {
			return Conflict("generated_work_id_conflict")
		}
	}
	input.Works = append(input.Works, st.Feedback.Work)
	a := e.rankingAssignment(st, WorkKind_WORK_KIND_RANK_WORKS, []*RankingSample{input})
	for _, material := range a.Materials {
		if material.SourceId == st.Feedback.Work.Id {
			material.SourceKind = "own_work"
		}
	}
	st.Assignment = a
	return nil
}

// ExplainOwnWork 只投影当前作品和获准任务材料，不包含评论或其他作品。
func (e Engine) ExplainOwnWork(st *State) *Assignment {
	a := e.assignment(st, WorkKind_WORK_KIND_EXPLAIN_OWN_WORK)
	a.Input = &Assignment_Explain{Explain: &ExplainInput{TaskDescription: st.Task.Description, OwnWork: st.Feedback.Work}}
	a.Materials = append(SelectedMaterials(st), WorkMaterials(st.Run.TaskId, st.Feedback.Work, true)...)
	return a
}

func (e Engine) nextPack(st *State) {
	st.Run.Round++
	st.Run.Stage = "optimizing_packer"
	st.Pending = ""
	a := e.assignment(st, WorkKind_WORK_KIND_PACK_TASK)
	a.Input = &Assignment_Pack{Pack: &PackInput{Initial: st.Run.Configuration.InitialTaskPackage, ExecutionHints: st.ExecutionHints, Feedback: st.Feedback}}
	a.Materials = append(SelectedMaterials(st), WorkMaterials(st.Run.TaskId, st.Feedback.GetWork(), true)...)
	st.Assignment = a
}

// ApplyResult 集中处理 R → P → E → R → 解释的结果与状态推进。
// 不访问数据库、模型或其他服务。
func (e Engine) ApplyResult(st *State, result Result, attemptID, fitBinding string, now time.Time) error {
	a := st.Assignment
	switch a.Kind {
	case WorkKind_WORK_KIND_VALIDATE_RANKER:
		if st.Run.Stage != "optimizing_ranker" || st.Run.RankerRound < 1 || !Bounded(st.RankerPrompt, 16384) {
			return Conflict("criteria_required")
		}
		score, err := FitScore(a.GetRank().Samples, st.Validation.Samples, result.Rankings, st.Run.Configuration.RankerMinComparablePairs)
		if err != nil {
			return err
		}
		st.Run.RankerFit = &RankerFit{Status: "not_met", Score: &score, EvaluatedAt: TimePtr(now)}
		st.Assignment = nil
		if score > st.Run.Configuration.RankerFitThreshold {
			st.Run.RankerFit.Status = "passed"
			st.FitBinding = fitBinding
			st.Validation.Samples = nil
			st.Training = nil
			st.TrainingInputs = nil
			st.TrainingRankings = nil
			e.nextPack(st)
		} else if st.Run.RankerRound >= st.Run.Configuration.RankerRoundLimit {
			Finish(st, "failed", "ranker_fit_not_met")
		} else {
			// 本次判断完成后才开放标签给下一轮标准生成；该批次不再作为独立验证集。
			st.TrainingRankings = result.Rankings
			e.SetTraining(st, st.Validation, a.GetRank().Samples)
			st.Validation = nil
		}
	case WorkKind_WORK_KIND_REFINE_RANKER:
		if st.Run.Stage != "optimizing_ranker" || st.Run.RankerRound >= st.Run.Configuration.RankerRoundLimit || !Bounded(result.Prompt.GetPrompt(), 16384) {
			return Invalid("invalid_ranker_prompt")
		}
		// 排序提示只保留通用规则，不允许把训练评论原文带入面向 P 的独立解释。
		for _, sample := range append([]*RankingSample{a.GetRefine().Target}, st.TrainingInputs...) {
			for _, c := range sample.Comments {
				if len(c.Body) >= 16 && strings.Contains(result.Prompt.Prompt, c.Body) {
					return Invalid("training_material_in_prompt")
				}
			}
		}
		st.RankerPrompt = result.Prompt.Prompt
		st.Run.RankerRound++
		st.Pending = "validation"
		st.Assignment = nil
	case WorkKind_WORK_KIND_PACK_TASK:
		if !Bounded(result.Prompt.GetPrompt(), 16384) {
			return Invalid("invalid_packer_prompt")
		}
		st.ExecutionHints = result.Prompt.Prompt
		next := e.assignment(st, WorkKind_WORK_KIND_EXECUTE_PACKAGE)
		next.Input = &Assignment_Execute{Execute: &ExecuteInput{Initial: st.Run.Configuration.InitialTaskPackage, ExecutionHints: st.ExecutionHints}}
		next.Materials = SelectedMaterials(st)
		st.Assignment = next
	case WorkKind_WORK_KIND_EXECUTE_PACKAGE:
		g := result.Generated
		if g == nil || !ValidWork(g.Work) || g.Work.Id != "work_"+attemptID || !Bounded(g.Report, 16384) {
			return Invalid("invalid_generated_work")
		}
		st.Feedback = &OwnFeedback{Work: g.Work, Report: g.Report, RankerFitScore: st.Run.RankerFit.GetScore(), EvaluationId: st.FitBinding}
		st.SourceAttemptId = attemptID
		st.Run.Candidates = append(st.Run.Candidates, &Candidate{Id: g.Work.Id, RegistrationState: "unregistered"})
		st.Pending = "rank"
		st.Assignment = nil
	case WorkKind_WORK_KIND_RANK_WORKS:
		if err := ValidateRankings(a.GetRank().Samples, result.Rankings); err != nil {
			return err
		}
		for i, id := range result.Rankings.Items[0].OrderedWorkIds {
			if id == st.Feedback.Work.Id {
				st.Feedback.Rank = int32(i + 1)
			}
		}
		st.Feedback.PopulationSize = int32(len(result.Rankings.Items[0].OrderedWorkIds))
		st.Assignment = e.ExplainOwnWork(st)
	case WorkKind_WORK_KIND_EXPLAIN_OWN_WORK:
		if err := ValidateJudgment(result.Judgment, st.Feedback.Work, st.Run.TaskId); err != nil {
			return err
		}
		st.Feedback.Judgment = result.Judgment
		st.Assignment = nil
		if st.Run.Round >= st.Run.Configuration.RoundLimit {
			st.Run.Stage = "registering"
			st.Pending = "register"
		} else {
			e.nextPack(st)
		}
	default:
		return Invalid("invalid_work_kind")
	}
	st.GrantHash = ""
	st.ExecutionInstanceId = ""
	st.ProgressSequence = 0
	st.ProgressPhase = ""
	return nil
}
