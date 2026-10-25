package voting

import "testing"

func TestConclusionRequiresEnoughEvidence(t *testing.T) {
	for _, v := range []struct {
		h, a int64
		want string
	}{{0, 0, "undetermined"}, {19, 0, "undetermined"}, {11, 9, "undetermined"}, {14, 6, "undetermined"}, {15, 5, "human_leading"}, {5, 15, "agent_leading"}, {0, 20, "agent_leading"}} {
		if got := conclusion(v.h, v.a); got != v.want {
			t.Errorf("%d/%d: %s, want %s", v.h, v.a, got, v.want)
		}
	}
}
