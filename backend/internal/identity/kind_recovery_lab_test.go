//go:build lab

package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const recoveryPublicFile = "/api/tasks/tsk_00000000000000000000000000000000/entries/ent_00000000000000000000000000000000/assets/ast_00000000000000000000000000000000/content"
const recoveryReviewFile = "/api/admin/reviews/rev_00000000000000000000000000000000/assets/ast_00000000000000000000000000000000/content"

// Test-only checkpoint after an intentional dependency outage/replacement. It
// is NOT a retry wrapper for business assertions or commands. Each real caller
// must independently converge; a rollout or another caller's success is not a
// substitute. Share one deadline across callers, including HTTP body reads.
func waitReadRecovery(ctx context.Context, clients []*http.Client, session, path string, want int) error {
	for i, client := range clients {
		for {
			req, err := http.NewRequestWithContext(ctx, "GET", "https://worth.oopsbox.cn"+path, nil)
			if err != nil {
				return err
			}
			if session != "" {
				req.AddCookie(&http.Cookie{Name: "__Host-human-worth-session", Value: session})
			}
			response, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("recovery caller %d: %w", i, err)
			}
			raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			response.Body.Close()
			if err != nil {
				return fmt.Errorf("recovery caller %d: %w", i, err)
			}
			if response.StatusCode == want {
				break
			}
			if err = dependencyFailure(response.StatusCode, raw); err != nil {
				return fmt.Errorf("recovery caller %d: %w", i, err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	return nil
}

func dependencyFailure(code int, raw []byte) error {
	var failure struct{ Code string }
	if json.Unmarshal(raw, &failure) != nil {
		return fmt.Errorf("invalid dependency failure response")
	}
	if code == 504 && failure.Code == "deadline_exceeded" {
		return nil
	}
	if code == 503 {
		switch failure.Code {
		case "identity_unavailable", "asset_unavailable", "content_unavailable", "moderation_unavailable", "voting_unavailable", "discovery_unavailable", "storage_unavailable", "review_access_unavailable":
			return nil
		}
	}
	return fmt.Errorf("unexpected recovery response: HTTP %d", code)
}

func (l *kindLab) waitReadRecovery(clients []*http.Client, user kindUser, path string, want int) {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(l.t.Context(), 105*time.Second)
	defer cancel()
	if err := waitReadRecovery(ctx, clients, user.session, path, want); err != nil {
		l.t.Fatalf("dependency recovery %s: %v", path, err)
	}
}

func (l *kindLab) waitFileAuthorization(clients []*http.Client, admin kindUser) {
	l.t.Helper()
	// Independent nonexistent-context probes, not the real draft/terminal-case
	// assertions. Neither probe can reach S3 or grant access to a file.
	l.waitReadRecovery(clients, kindUser{}, recoveryPublicFile, 404)
	if admin.session != "" {
		l.waitReadRecovery(clients, admin, recoveryReviewFile, 404)
	}
}

func (l *kindLab) assetRecoveryClients(prefix string) []*http.Client {
	l.t.Helper()
	var pods struct {
		Items []struct{ Status struct{ PodIP string } }
	}
	kindDecode(l.t, l.kube("", "get", "pods", "-l", "app=asset", "-o", "json"), &pods)
	if len(pods.Items) != 2 {
		l.t.Fatal("two actual Asset callers required for recovery")
	}
	clients := make([]*http.Client, 0, 2)
	for i, pod := range pods.Items {
		clients = append(clients, l.pinnedGateway(fmt.Sprintf("%s-%d", prefix, i), pod.Status.PodIP))
	}
	return clients
}

func (l *kindLab) waitModerationRecovery(admin kindUser) {
	l.t.Helper()
	var pods struct {
		Items []struct{ Metadata struct{ Name string } }
	}
	kindDecode(l.t, l.kube("", "get", "pods", "-l", "app=gateway", "-o", "json"), &pods)
	var clients []*http.Client
	for _, pod := range pods.Items {
		// Only normal Deployment Pods; isolated probe Pods have their own clients.
		if strings.HasPrefix(pod.Metadata.Name, "gateway-") {
			clients = append(clients, l.httpClient(l.forward("pod/"+pod.Metadata.Name, 8080)))
		}
	}
	if len(clients) != 2 {
		l.t.Fatal("two normal Gateway callers required for Moderation recovery")
	}
	l.waitReadRecovery(clients, admin, "/api/admin/reviews?limit=1", 200)
}
