package platform

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUnaryUsesMethodDeadline(t *testing.T) {
	const delay = 2500 * time.Millisecond
	type result struct {
		method string
		err    error
	}
	results := make(chan result, 3)
	for _, method := range []string{
		"/humanworth.content.v1.ContentService/ListMySubmissions",
		"/humanworth.content.v1.ContentService/ListApprovedEntries",
		"/humanworth.content.v1.ContentService/GetMyTaskSubmission",
	} {
		go func(method string) {
			runtime := NewRuntime("test")
			runtime.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
			_, err := runtime.Unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: method}, func(ctx context.Context, _ any) (any, error) {
				select {
				case <-time.After(delay):
					return struct{}{}, nil
				case <-ctx.Done():
					return nil, status.FromContextError(ctx.Err()).Err()
				}
			})
			results <- result{method: method, err: err}
		}(method)
	}

	for range 3 {
		result := <-results
		switch result.method {
		case "/humanworth.content.v1.ContentService/ListMySubmissions", "/humanworth.content.v1.ContentService/ListApprovedEntries":
			if result.err != nil {
				t.Fatalf("list deadline did not cover controlled delay: %v", result.err)
			}
		default:
			if status.Code(result.err) != codes.DeadlineExceeded {
				t.Fatalf("ordinary RPC deadline changed: %v", result.err)
			}
		}
	}
}

func TestPublicAssetTimeouts(t *testing.T) {
	if methodTimeout("/humanworth.asset.v1.AssetService/DownloadPublicAsset") != 15*time.Minute {
		t.Fatal("public stream deadline mismatch")
	}
	if methodTimeout("/humanworth.asset.v1.AssetService/CompleteUpload") != AssetCompleteTimeout {
		t.Fatal("asset completion deadline mismatch")
	}
	for _, method := range []string{"/humanworth.content.v1.ContentService/CheckAssetAccess", "/humanworth.content.v1.ContentService/ApplyAssetReview"} {
		if methodTimeout(method) != 2*time.Second {
			t.Fatal("public control-plane deadline mismatch", method)
		}
	}
}

func TestReviewMethodTimeouts(t *testing.T) {
	for _, method := range []string{"/humanworth.content.v1.ContentService/RequestTaskReview", "/humanworth.content.v1.ContentService/ApplyReviewDecision", "/humanworth.moderation.v1.ModerationService/CreateReviewCase", "/humanworth.moderation.v1.ModerationService/ListReviews"} {
		if methodTimeout(method) != 5*time.Second {
			t.Fatal("review delivery deadline mismatch", method)
		}
	}
	for _, method := range []string{"/humanworth.moderation.v1.ModerationService/DecideReview", "/humanworth.moderation.v1.ModerationService/GetReview", "/humanworth.content.v1.ContentService/GetReviewSnapshot", "/humanworth.content.v1.ContentService/CheckReviewAssetAccess"} {
		if methodTimeout(method) != 2*time.Second {
			t.Fatal("review control-plane deadline widened", method)
		}
	}
	if methodTimeout("/humanworth.asset.v1.AssetService/DownloadReviewAsset") != 15*time.Minute {
		t.Fatal("pending file stream deadline mismatch")
	}
}

func TestReadinessLogsTransitionsWithoutPrivateErrors(t *testing.T) {
	var output bytes.Buffer
	runtime := NewRuntime("test")
	runtime.Log = slog.New(slog.NewJSONHandler(&output, nil))
	current := errors.New("postgres://private-password@private-host/database")
	server := runtime.Health(":0", func(context.Context) error { return current })
	probe := func(expected int) {
		t.Helper()
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, httptest.NewRequest("GET", "/readyz", nil))
		if response.Code != expected {
			t.Fatal("unexpected readiness code", response.Code)
		}
	}
	probe(503)
	probe(503)
	current = nil
	probe(204)
	probe(204)
	current = &dependencyHealthError{"asset", "TRANSIENT_FAILURE", "Unavailable", "UNKNOWN"}
	probe(503)
	log := output.String()
	if strings.Count(log, "readiness_changed") != 3 || strings.Contains(log, "private-") || !strings.Contains(log, `"dependency":"asset"`) || !strings.Contains(log, `"rpc_code":"Unavailable"`) {
		t.Fatal("missing transition diagnostics or private error leaked")
	}
}
