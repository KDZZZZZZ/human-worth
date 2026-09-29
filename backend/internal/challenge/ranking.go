package challenge

import (
	"math"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	content "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	voting "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
)

func comparablePairs(in *pb.RankingSample, p *voting.HumanPreference) (int, error) {
	if len(p.CountsByWork) != len(in.Works) || p.ValidVoteCount <= 0 {
		return 0, conflict("insufficient_preference_data")
	}
	var total int64
	for _, w := range in.Works {
		n, ok := p.CountsByWork[w.Id]
		if !ok || n < 0 || n > math.MaxInt64-total {
			return 0, conflict("invalid_preference_snapshot")
		}
		total += n
	}
	if total != p.ValidVoteCount {
		return 0, conflict("invalid_preference_snapshot")
	}
	n := 0
	for i, w := range in.Works {
		for _, other := range in.Works[i+1:] {
			if p.CountsByWork[w.Id] != p.CountsByWork[other.Id] {
				n++
			}
		}
	}
	return n, nil
}

// validateJudgment 依据只能引用当前作品或任务材料，不能夹带其他作品的证据引用。
func validateJudgment(j *pb.Judgment, w *content.Work, taskID string) error {
	if j == nil || w == nil || j.WorkId != w.Id || !bounded(j.Criteria, 4096) || !bounded(j.Rationale, 8192) || len(j.EvidenceRefs) == 0 || len(j.EvidenceRefs) > 32 {
		return invalid("invalid_judgment")
	}
	allowed := map[string]bool{w.Id: true, "task:" + taskID: true}
	for _, a := range w.Artifacts {
		if f := a.GetFile(); f != nil {
			allowed[f.Id] = true
		}
	}
	for _, ref := range j.EvidenceRefs {
		if !allowed[ref] {
			return invalid("invalid_evidence_reference")
		}
	}
	return nil
}
func validateRankings(inputs []*pb.RankingSample, out *pb.Rankings) error {
	if out == nil || len(out.Items) != len(inputs) {
		return invalid("incomplete_ranking")
	}
	byTask := map[string]*pb.Ranking{}
	for _, r := range out.Items {
		if r == nil || byTask[r.TaskId] != nil {
			return invalid("invalid_ranking")
		}
		byTask[r.TaskId] = r
	}
	for _, in := range inputs {
		r := byTask[in.Task.TaskId]
		if r == nil || len(r.OrderedWorkIds) != len(in.Works) || len(r.Judgments) != len(in.Works) {
			return invalid("incomplete_ranking")
		}
		works := map[string]*content.Work{}
		for _, w := range in.Works {
			works[w.Id] = w
		}
		seen := map[string]bool{}
		for _, id := range r.OrderedWorkIds {
			if works[id] == nil || seen[id] {
				return invalid("invalid_ranking")
			}
			seen[id] = true
		}
		seen = map[string]bool{}
		for _, j := range r.Judgments {
			if j == nil || seen[j.WorkId] {
				return invalid("invalid_judgment")
			}
			if err := validateJudgment(j, works[j.WorkId], in.Task.TaskId); err != nil {
				return err
			}
			seen[j.WorkId] = true
		}
	}
	return nil
}

// FitScore 由可信服务计算可比作品对的一致率；同票不计分，模型不能自报通过。
func FitScore(inputs []*pb.RankingSample, labels []*voting.HumanPreference, out *pb.Rankings, minimum int32) (float64, error) {
	if err := validateRankings(inputs, out); err != nil {
		return 0, err
	}
	if len(labels) != len(inputs) {
		return 0, conflict("preference_snapshot_mismatch")
	}
	total, correct := 0, 0
	for i, in := range inputs {
		p := labels[i]
		if p == nil || p.Snapshot.GetTaskId() != in.Task.TaskId {
			return 0, conflict("preference_snapshot_mismatch")
		}
		n, err := comparablePairs(in, p)
		if err != nil {
			return 0, err
		}
		total += n
		pos := map[string]int{}
		for _, r := range out.Items {
			if r.TaskId == in.Task.TaskId {
				for index, id := range r.OrderedWorkIds {
					pos[id] = index
				}
			}
		}
		for i, w := range in.Works {
			for _, other := range in.Works[i+1:] {
				left, right := p.CountsByWork[w.Id], p.CountsByWork[other.Id]
				if left != right && (left > right) == (pos[w.Id] < pos[other.Id]) {
					correct++
				}
			}
		}
	}
	if total < int(minimum) || total == 0 {
		return 0, conflict("insufficient_preference_data")
	}
	return float64(correct) / float64(total), nil
}
