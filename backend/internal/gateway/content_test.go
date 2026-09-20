package gateway

import (
	"encoding/json"
	"testing"
)

func TestDraftHTTPShape(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"title":"t","summary":"","description":"","entries":[]}`, true},
		{`{"title":"t","summary":"","description":"","entries":[],"category":null}`, true},
		{`{"title":"t","summary":"","description":"","entries":[],"authorId":"forged"}`, false},
		{`{"title":"t","summary":"","description":"","entries":[],"actorAssertion":"forged"}`, false},
		{`{"title":"t","summary":"","description":""}`, false},
		{`{"title":"t","summary":"","description":"","entries":null}`, false},
		{`{"title":"t","title":"duplicate","summary":"","description":"","entries":[]}`, false},
		{`{"title":"t","summary":"","description":"","entries":[{"permissions":{"cloud_use":true}}]}`, false},
		{`{"title":"t","summary":"","description":"","entries":[{"description":null}]}`, false},
		{`null`, false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			_, ok := draftInput(json.RawMessage(tc.body))
			if ok != tc.valid {
				t.Fatalf("got %v want %v", ok, tc.valid)
			}
		})
	}
}
