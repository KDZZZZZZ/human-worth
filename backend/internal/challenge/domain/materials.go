package domain

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

const MaxMaterialBytes int64 = 16 << 20
const MaxInputBytes = 1 << 20

// InputPolicyVersion 随角色投影、系统指令或工具语义变更递增，旧拟合记录不能跨策略复用。
const InputPolicyVersion = "structured-criteria-v2"

func InputPolicyHash(version string, modelCalls, toolCalls int32) string {
	return Hash([]byte(fmt.Sprintf("%s/%d/%d/%d", version, modelCalls, toolCalls, MaxInputBytes)))
}

func ValidAsset(a *Asset) bool {
	return a != nil && ValidID(a.Id) && Bounded(a.Filename, 255) && !strings.ContainsAny(a.Filename, "/\\") && a.Bytes >= 0 && a.Bytes <= MaxMaterialBytes && ValidDigest(a.Sha256) && Bounded(a.MediaType, 128)
}
func ValidDigest(v string) bool {
	b, err := hex.DecodeString(v)
	return err == nil && len(b) == 32 && v == strings.ToLower(v)
}
func ValidWork(w *Work) bool {
	if w == nil || !ValidID(w.Id) || len(w.Artifacts) == 0 || len(w.Artifacts) > 32 {
		return false
	}
	actual := false
	for _, a := range w.Artifacts {
		if a == nil {
			return false
		}
		switch v := a.Value.(type) {
		case *Artifact_Text:
			if !Bounded(v.Text, MaxInputBytes) {
				return false
			}
			actual = true
		case *Artifact_File:
			if !ValidAsset(v.File) {
				return false
			}
			actual = true
		case *Artifact_Link:
			if !Bounded(v.Link, 4096) {
				return false
			}
		default:
			return false
		}
	}
	return actual
}

// WorkSetHash 与投票快照约定同一候选 ID 集合；内容版本由 SnapshotRef 单独绑定。
func WorkSetHash(works []*Work) string {
	ids := make([]string, 0, len(works))
	for _, w := range works {
		ids = append(ids, w.Id)
	}
	sort.Strings(ids)
	return Hash([]byte(strings.Join(ids, "\n")))
}

func TaskMaterials(task *TaskDescription, selected map[string]bool) []*Material {
	var result []*Material
	for _, a := range task.Attachments {
		if selected == nil || selected[a.Asset.Id] {
			result = append(result, &Material{Id: Hash([]byte(task.TaskId + "/task/" + a.Asset.Id)), Asset: a.Asset, TaskId: task.TaskId, SourceKind: "task", SourceId: task.TaskId})
		}
	}
	return result
}
func WorkMaterials(taskID string, w *Work, own bool) []*Material {
	var result []*Material
	if w == nil {
		return result
	}
	kind := "work"
	if own {
		kind = "own_work"
	}
	for _, a := range w.Artifacts {
		if f := a.GetFile(); f != nil {
			result = append(result, &Material{Id: Hash([]byte(taskID + "/" + w.Id + "/" + f.Id)), Asset: f, TaskId: taskID, SourceKind: kind, SourceId: w.Id})
		}
	}
	return result
}
func RankMaterials(inputs []*RankingSample) []*Material {
	var out []*Material
	for _, in := range inputs {
		out = append(out, TaskMaterials(in.Task, nil)...)
		for _, w := range in.Works {
			out = append(out, WorkMaterials(in.Task.TaskId, w, false)...)
		}
	}
	return out
}
func SelectedMaterials(st *State) []*Material {
	selected := map[string]bool{}
	for _, id := range st.Run.Configuration.InitialTaskPackage.TaskAttachmentIds {
		selected[id] = true
	}
	return TaskMaterials(st.Task, selected)
}
