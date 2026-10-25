package content

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func entryTestInput(t *testing.T, raw string) *pb.EntryInput {
	t.Helper()
	e := &pb.EntryInput{}
	if err := protojson.Unmarshal([]byte(raw), e); err != nil {
		t.Fatal(err)
	}
	return e
}

const entryTestHuman = `{"side":"human","title":"work","source":"original","artifacts":[{"kind":"text","text":"actual work body"},{"kind":"link","url":"https://example.com/work"}],"permissions":{"cloudUse":false}}`

func TestAdditionalEntryValidation(t *testing.T) {
	input := entryTestInput(t, entryTestHuman)
	before := proto.Clone(input)
	first, err := validateEntry(input)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &pb.EntryInput{}
	if err = protojson.Unmarshal(first, decoded); err != nil || !proto.Equal(input, decoded) || !proto.Equal(before, input) {
		t.Fatal("entry validation changed content or persisted wrapper", err)
	}
	agentA := strings.Replace(entryTestHuman, `"side":"human"`, `"side":"agent","agentConfiguration":{"model":"m","parameters":{"b":2,"a":1}}`, 1)
	agentB := strings.Replace(agentA, `"b":2,"a":1`, `"a":1,"b":2`, 1)
	a, err := validateEntry(entryTestInput(t, agentA))
	if err != nil {
		t.Fatal(err)
	}
	b, err := validateEntry(entryTestInput(t, agentB))
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("parameter order changed idempotency fingerprint", err)
	}
	for name, edit := range map[string]func(*pb.EntryInput){
		"side":                   func(e *pb.EntryInput) { e.Side = "other" },
		"title":                  func(e *pb.EntryInput) { e.Title = "  " },
		"source":                 func(e *pb.EntryInput) { e.Source = "" },
		"permissions":            func(e *pb.EntryInput) { e.Permissions = nil },
		"explicit consent":       func(e *pb.EntryInput) { e.Permissions.CloudUse = nil },
		"artifacts":              func(e *pb.EntryInput) { e.Artifacts = nil },
		"nil artifact":           func(e *pb.EntryInput) { e.Artifacts[0] = nil },
		"unsafe URL":             func(e *pb.EntryInput) { e.Artifacts[0].Url = proto.String("javascript:alert(1)") },
		"mixed artifact":         func(e *pb.EntryInput) { e.Artifacts[0].AssetId = proto.String("ast_bad") },
		"agent config required":  func(e *pb.EntryInput) { e.Side = "agent" },
		"human config forbidden": func(e *pb.EntryInput) { e.AgentConfiguration = &pb.AgentConfigurationInput{Model: "m"} },
		"NUL":                    func(e *pb.EntryInput) { e.Title = "bad\x00title" },
		"UTF8":                   func(e *pb.EntryInput) { e.Title = string([]byte{0xff}) },
		"byte limit":             func(e *pb.EntryInput) { e.Description = proto.String(strings.Repeat("绘", maxDraftBytes/3)) },
	} {
		t.Run(name, func(t *testing.T) {
			e := proto.Clone(input).(*pb.EntryInput)
			edit(e)
			if _, err := validateEntry(e); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("invalid entry accepted: %v", err)
			}
		})
	}
	if _, err := validateEntry(nil); status.Code(err) != codes.InvalidArgument {
		t.Fatal("nil entry accepted", err)
	}
}

func TestAdditionalEntryOwnCursor(t *testing.T) {
	created := time.Date(2026, 10, 23, 2, 3, 4, 123456000, time.FixedZone("offset", 8*3600))
	id := "ent_" + strings.Repeat("a", 32)
	category := "绘画"
	scope := ownEntryScope("alice", "pending_review", &category)
	raw := ownEntryNext(scope, created, id)
	got, gotID, err := decodeOwnEntryCursor(raw, scope)
	if err != nil || !got.Equal(created) || gotID != id {
		t.Fatalf("cursor roundtrip: %v %q %v", got, gotID, err)
	}
	for _, other := range []string{ownEntryScope("bob", "pending_review", &category), ownEntryScope("alice", "approved", &category), ownEntryScope("alice", "pending_review", nil), ownEntryScope("alice", "pending_review", proto.String("代码"))} {
		if _, _, err := decodeOwnEntryCursor(raw, other); status.Code(err) != codes.InvalidArgument {
			t.Fatal("cross-account/filter cursor accepted", err)
		}
	}
	data, _ := base64.RawURLEncoding.DecodeString(raw)
	var cursor ownEntryCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		t.Fatal(err)
	}
	bad := []string{"", "!", raw + "=", strings.Repeat("a", maxSubmissionCursorLength+1)}
	for _, edit := range []func(*ownEntryCursor){
		func(c *ownEntryCursor) { c.Version = 2 },
		func(c *ownEntryCursor) { c.ID = "tsk_" + strings.Repeat("a", 32) },
		func(c *ownEntryCursor) { c.Created = "yesterday" },
		func(c *ownEntryCursor) { c.Created = created.Format(time.RFC3339Nano) },
	} {
		c := cursor
		edit(&c)
		encoded, _ := json.Marshal(c)
		bad = append(bad, base64.RawURLEncoding.EncodeToString(encoded))
	}
	for _, value := range []string{string(data) + ` {}`, strings.TrimSuffix(string(data), "}") + `,"v":1}`, strings.TrimSuffix(string(data), "}") + `,"unknown":true}`} {
		bad = append(bad, base64.RawURLEncoding.EncodeToString([]byte(value)))
	}
	for _, value := range bad {
		if _, _, err := decodeOwnEntryCursor(value, scope); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("malformed/noncanonical cursor accepted: %q %v", value, err)
		}
	}
}

func TestAdditionalEntryPublicCursorVersions(t *testing.T) {
	task, id := "tsk_"+strings.Repeat("a", 32), "ent_"+strings.Repeat("b", 32)
	for _, position := range []int32{0, 31, 32, 100, 2147483646} {
		raw := encodeEntryCursor(task, 1, position, id)
		c, err := decodeEntryCursor(raw, task)
		if err != nil || c.Position != position || (position <= 31 && c.Version != 1) || (position > 31 && c.Version != 2) {
			t.Fatalf("position %d cursor incompatible: %+v %v", position, c, err)
		}
		if c.Version == 1 {
			c.Version = 2
		} else {
			c.Version = 1
		}
		data, _ := json.Marshal(c)
		if _, err := decodeEntryCursor(base64.RawURLEncoding.EncodeToString(data), task); status.Code(err) != codes.InvalidArgument {
			t.Fatal("position/version mismatch accepted", err)
		}
	}
}

func TestAdditionalEntryServiceBoundaries(t *testing.T) {
	methods := map[string]string{
		pb.ContentService_SubmitAdditionalEntry_FullMethodName:    "gateway",
		pb.ContentService_ResubmitRejectedEntry_FullMethodName:    "gateway",
		pb.ContentService_GetMyEntrySubmission_FullMethodName:     "gateway",
		pb.ContentService_ListMyEntrySubmissions_FullMethodName:   "gateway",
		pb.ContentService_GetEntryReviewSnapshot_FullMethodName:   "moderation",
		pb.ContentService_ApplyEntryReviewDecision_FullMethodName: "moderation",
	}
	for method, allowed := range methods {
		for _, caller := range []string{"", "gateway", "moderation", "asset", "content", "identity", "challenge"} {
			ctx := context.Background()
			if caller != "" {
				ctx = serviceContext(caller)
			}
			called := false
			_, err := Authorization(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method}, func(context.Context, any) (any, error) { called = true; return nil, nil })
			if called != (caller == allowed) || (err == nil) != called {
				t.Fatalf("%s caller=%q reached handler=%t err=%v", method, caller, called, err)
			}
		}
	}
	s := &Server{}
	if _, err := s.GetEntryReviewSnapshot(context.Background(), &pb.GetEntryReviewSnapshotRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	if _, err := s.ApplyEntryReviewDecision(serviceContext("gateway"), &pb.ApplyEntryReviewDecisionRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
}

func TestAdditionalEntryReviewRejectsInvalidInputBeforeDatabase(t *testing.T) {
	valid := &pb.ApplyEntryReviewDecisionRequest{DecisionId: "dec_" + strings.Repeat("a", 32), ReviewId: "rev_" + strings.Repeat("b", 32), TaskId: "tsk_" + strings.Repeat("c", 32), EntryId: "ent_" + strings.Repeat("d", 32), SubmissionRevision: 1, SnapshotSha256: strings.Repeat("e", 64), Outcome: pb.ReviewOutcome_REVIEW_OUTCOME_REJECT, Reason: proto.String("change required")}
	for _, edit := range []func(*pb.ApplyEntryReviewDecisionRequest){
		func(r *pb.ApplyEntryReviewDecisionRequest) { r.DecisionId = "short" },
		func(r *pb.ApplyEntryReviewDecisionRequest) { r.TaskId = "bad" },
		func(r *pb.ApplyEntryReviewDecisionRequest) { r.EntryId = "bad" },
		func(r *pb.ApplyEntryReviewDecisionRequest) { r.SubmissionRevision = 0 },
		func(r *pb.ApplyEntryReviewDecisionRequest) { r.SnapshotSha256 = "bad" },
		func(r *pb.ApplyEntryReviewDecisionRequest) { r.Outcome = pb.ReviewOutcome_REVIEW_OUTCOME_UNSPECIFIED },
		func(r *pb.ApplyEntryReviewDecisionRequest) { r.Reason = nil },
		func(r *pb.ApplyEntryReviewDecisionRequest) { r.Reason = proto.String(" \n") },
		func(r *pb.ApplyEntryReviewDecisionRequest) { r.Reason = proto.String("bad\x00reason") },
		func(r *pb.ApplyEntryReviewDecisionRequest) { r.Reason = proto.String(string([]byte{0xff})) },
		func(r *pb.ApplyEntryReviewDecisionRequest) { r.Reason = proto.String(strings.Repeat("绘", 1334)) },
	} {
		r := proto.Clone(valid).(*pb.ApplyEntryReviewDecisionRequest)
		edit(r)
		if _, err := (&Server{}).ApplyEntryReviewDecision(serviceContext("moderation"), r); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid decision reached persistence: %v", err)
		}
	}
}
