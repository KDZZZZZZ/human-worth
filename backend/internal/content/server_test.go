package content

import (
	"bytes"
	"testing"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestDraftValidationAndFingerprint(t *testing.T) {
	const empty = `{"title":"t","summary":"","description":"","entries":[]}`
	const entry = `{"title":"t","summary":"","description":"","entries":[{"side":"agent","title":"work","source":"model","artifacts":[{"kind":"link","url":"https://example.com/work"}],"permissions":{"cloudUse":false},"agentConfiguration":{"model":"example","parameters":{"b":2,"a":1}}}]}`
	for _, raw := range []string{empty, entry} {
		input := &pb.TaskDraftInput{}
		if err := protojson.Unmarshal([]byte(raw), input); err != nil {
			t.Fatal(err)
		}
		first, err := validate(input)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 10; i++ {
			second, err := validate(input)
			if err != nil || !bytes.Equal(first, second) {
				t.Fatal("unstable fingerprint", err)
			}
		}
	}
	for _, tc := range []struct {
		raw  string
		code codes.Code
	}{
		{`{}`, codes.InvalidArgument},
		{`{"title":"t","summary":"","entries":[]}`, codes.InvalidArgument},
		{`{"title":"\u0000","summary":"","description":"","entries":[]}`, codes.InvalidArgument},
		{`{"title":"t","summary":"","description":"","entries":[{"side":"human","title":"w","source":"s","artifacts":[{"kind":"link","url":"javascript:alert(1)"}],"permissions":{"cloudUse":false}}]}`, codes.InvalidArgument},
		{`{"title":"t","summary":"","description":"","entries":[{"side":"human","title":"w","source":"s","artifacts":[{"kind":"file","assetId":"somebody-elses-file"}],"permissions":{"cloudUse":false}}]}`, codes.FailedPrecondition},
		{`{"title":"t","summary":"","description":"","entries":[{"side":"human","title":"w","source":"s","artifacts":[{"kind":"link","url":"https://example.com"}],"permissions":{}}]}`, codes.InvalidArgument},
		{`{"title":"t","summary":"","description":"","entries":[{"side":"agent","title":"w","source":"s","artifacts":[{"kind":"link","url":"https://example.com"}],"permissions":{"cloudUse":false}}]}`, codes.InvalidArgument},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			input := &pb.TaskDraftInput{}
			if err := protojson.Unmarshal([]byte(tc.raw), input); err != nil {
				t.Fatal(err)
			}
			_, err := validate(input)
			if status.Code(err) != tc.code {
				t.Fatalf("got %v want %v", err, tc.code)
			}
		})
	}
}
