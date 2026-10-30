//go:build integration

package challenge_test

import (
	"testing"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"google.golang.org/grpc/codes"
)

func TestChallengeRunCursorRequiresExistingAnchor(t *testing.T) {
	l := newLab(t)
	l.start(config())
	l.start(config())
	first, err := l.admins[0].ListRuns(t.Context(), &pb.ListRunsRequest{ActorAssertion: "admin", Limit: 1})
	must(t, err)
	if len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatal("fixture did not produce a continuation")
	}
	second, err := l.admins[1].ListRuns(t.Context(), &pb.ListRunsRequest{ActorAssertion: "admin", Limit: 1, Cursor: first.NextCursor})
	must(t, err)
	if len(second.Items) != 1 || second.Items[0].Id == first.Items[0].Id || second.NextCursor != "" {
		t.Fatal("real cursor lost stable pagination")
	}
	last, err := l.admins[0].ListRuns(t.Context(), &pb.ListRunsRequest{ActorAssertion: "admin", Limit: 1, Cursor: second.Items[0].Id})
	must(t, err)
	if len(last.Items) != 0 || last.NextCursor != "" {
		t.Fatal("existing terminal anchor must return an empty page")
	}
	for _, cursor := range []string{"not-a-real-cursor", "run_round3_synthetic_nonexistent", "bad!cursor"} {
		_, err = l.admins[0].ListRuns(t.Context(), &pb.ListRunsRequest{ActorAssertion: "admin", Limit: 1, Cursor: cursor})
		code(t, err, codes.InvalidArgument)
	}
	_, err = l.admins[0].ListRuns(t.Context(), &pb.ListRunsRequest{ActorAssertion: "user", Limit: 1, Cursor: "run_round3_synthetic_nonexistent"})
	code(t, err, codes.PermissionDenied)
}
