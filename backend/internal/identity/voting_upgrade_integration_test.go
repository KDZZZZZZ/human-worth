//go:build integration

package identity

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	votingpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/KDZZZZZZ/human-worth/backend/internal/voting"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// Start from the actual released migration with persisted votes and closure,
// rather than exercising the upgrade only against an empty new database.
func TestVotingCompletionUpgradePreservesPermanentFacts(t *testing.T) {
	l := newIdentityLab(t)
	legacy, err := os.ReadFile("../voting/migrations/001_voting.sql")
	must(t, err)
	must(t, platform.Migrate(t.Context(), l.db, fstest.MapFS{"migrations/001_voting.sql": {Data: legacy}}, "voting", 7185429011))
	task := "tsk_" + strings.Repeat("a", 32)
	catalog := &contentpb.VotingCatalogSnapshot{TaskId: task, Generation: 1, TaskRevision: 1, DisplayRevision: 1, CatalogRevision: 1, State: "open"}
	for i, side := range []string{"human", "agent"} {
		catalog.Entries = append(catalog.Entries, &contentpb.ApprovedEntry{Id: "ent_" + strings.Repeat(string(rune('b'+i)), 32), TaskId: task, Side: side, Title: side, Source: "original", Author: &contentpb.PublicAuthor{Id: "legacy-author"}, Artifacts: []*contentpb.ArtifactInput{{Kind: "link", Url: proto.String("https://example.test/" + side)}}})
	}
	catalog.Sha256 = contract.CatalogHash(catalog)
	raw, err := proto.Marshal(catalog)
	must(t, err)
	_, err = l.db.Exec(t.Context(), `INSERT INTO voting.catalog_gates(task_id,generation,state,change_id,sha256,snapshot) VALUES($1,1,'open','legacy-change',$2,$3)`, task, catalog.Sha256, raw)
	must(t, err)
	_, err = l.db.Exec(t.Context(), `INSERT INTO voting.catalog_changes(change_id,task_id,generation,request_hash,phase,sha256) VALUES('legacy-change',$1,1,decode(repeat('ab',32),'hex'),'activated',$2)`, task, catalog.Sha256)
	must(t, err)
	_, err = l.db.Exec(t.Context(), `INSERT INTO voting.current_votes(account_id,task_id,id,entry_id,revision,request_hash) SELECT 'legacy-account-'||n,$1,'legacy-vote-'||n,$2,3,decode(repeat('cd',32),'hex') FROM generate_series(1,20)n`, task, catalog.Entries[0].Id)
	must(t, err)
	_, err = l.db.Exec(t.Context(), `INSERT INTO voting.account_task_guards(account_id,task_id) VALUES('legacy-account-1',$1); INSERT INTO voting.statistics_access(account_id,task_id) VALUES('legacy-account-1',$1); INSERT INTO voting.ballots(id,account_id,task_id,generation,sha256) VALUES('legacy-ballot','legacy-account-1',$1,1,'legacy-catalog-hash')`, pgx.QueryExecModeSimpleProtocol, task)
	must(t, err)
	facts := func() string {
		var v string
		must(t, l.db.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'votes',(SELECT jsonb_agg(to_jsonb(v)-'generation' ORDER BY id) FROM voting.current_votes v),
 'closures',(SELECT jsonb_agg(to_jsonb(v)) FROM voting.statistics_access v),
 'guards',(SELECT jsonb_agg(to_jsonb(v)) FROM voting.account_task_guards v),
 'ballots',(SELECT jsonb_agg(to_jsonb(v)) FROM voting.ballots v),
 'catalog',(SELECT jsonb_agg(to_jsonb(v)-'activated_at') FROM voting.catalog_gates v),
 'receipts',(SELECT jsonb_agg(to_jsonb(v)) FROM voting.catalog_changes v))::text`).Scan(&v))
		return v
	}
	before := facts()
	must(t, voting.Migrate(t.Context(), l.db))
	must(t, voting.Migrate(t.Context(), l.db))
	server := &voting.Server{DB: l.db}
	must(t, server.BackfillConclusions(t.Context()))
	must(t, server.BackfillConclusions(t.Context()))
	if facts() != before {
		t.Fatal("upgrade changed a persisted vote, closure, ballot or catalog receipt")
	}
	var conclusion string
	var flips, events, legacyVotes int
	must(t, l.db.QueryRow(t.Context(), `SELECT conclusion,flip_count,(SELECT count(*) FROM voting.conclusion_events),(SELECT count(*) FROM voting.current_votes WHERE generation=0) FROM voting.conclusions WHERE task_id=$1`, task).Scan(&conclusion, &flips, &events, &legacyVotes))
	if conclusion != "human_leading" || flips != 0 || events != 1 || legacyVotes != 20 {
		t.Fatal("historical conclusion backfill or generation marking changed")
	}
	client := votingpb.NewVotingServiceClient(serviceTestGRPC(t, l, "voting", "challenge", voting.Authorization, func(g *grpc.Server) { votingpb.RegisterVotingServiceServer(g, server) }))
	_, err = client.GetHumanPreferenceSnapshot(t.Context(), &votingpb.GetHumanPreferenceSnapshotRequest{RunId: "run_legacy_snapshot", Purpose: "training"})
	grpcCode(t, err, codes.FailedPrecondition)
}
