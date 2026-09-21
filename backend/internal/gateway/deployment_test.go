package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthSeparatesGatewayReadinessFromCompletedDeployment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	var failure error
	handler, err := New(nil, Options{Origin: "https://worth.example.test", DeploymentStatusFile: path, Ready: func(context.Context) error { return failure }})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []struct{ file, revision string }{
		{"", ""},
		{`{"revision":"incomplete"}`, ""},
		{`{"revision":"` + strings.Repeat("z", 40) + `","services":["identity"]}`, ""},
		{`{"revision":"` + strings.Repeat("a", 40) + `","services":["identity","content","gateway"]}`, strings.Repeat("a", 40)},
		{`{"revision":"` + strings.Repeat("b", 40) + `","services":["identity","content","gateway"]}`, strings.Repeat("b", 40)},
	} {
		if state.file != "" {
			if err := os.WriteFile(path, []byte(state.file), 0600); err != nil {
				t.Fatal(err)
			}
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "https://worth.example.test/api/health", nil))
		var body map[string]any
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil {
			t.Fatal("readiness should work before the first completed deployment")
		}
		revision, _ := body["deploymentRevision"].(string)
		if revision != state.revision {
			t.Fatal("completion marker was invented, cached or not updated")
		}
	}
	failure = errors.New("dependency unavailable")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "https://worth.example.test/api/health", nil))
	if response.Code != 503 || strings.Contains(response.Body.String(), "deploymentRevision") {
		t.Fatal("a previous completed deployment must not hide a current dependency failure")
	}
}
