package voting

import (
	"context"
	"time"

	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *Server) ViewTaskStatistics(ctx context.Context, r *pb.ViewTaskStatisticsRequest) (*pb.ViewTaskStatisticsResponse, error) {
	account, err := s.actor(ctx, r.GetActorAssertion(), pb.VotingService_ViewTaskStatistics_FullMethodName, true)
	if err != nil {
		return nil, err
	}
	if !r.GetPermanentlyCloseVoting() {
		return nil, status.Error(codes.FailedPrecondition, "statistics_confirmation_required")
	}
	c, err := s.authority(ctx, r.GetTaskId())
	if err != nil {
		return nil, err
	}
	closed, err := s.closeVoting(ctx, account, c)
	if err != nil {
		return nil, err
	}
	// Closure is already committed; a later outage cannot restore eligibility.
	c, err = s.authority(ctx, r.TaskId)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	var state, digest string
	var generation int64
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT state,generation,sha256,snapshot FROM voting.catalog_gates WHERE task_id=$1`, c.TaskId).Scan(&state, &generation, &digest, &raw); err != nil {
		return nil, storage(err)
	}
	if state != "open" || generation != c.Generation || digest != c.Sha256 {
		return nil, conflict("catalog_changed")
	}
	snapshot := &contentpb.VotingCatalogSnapshot{}
	if proto.Unmarshal(raw, snapshot) != nil || contract.ValidateCatalog(snapshot) != nil {
		return nil, status.Error(codes.Unavailable, "invalid_catalog")
	}
	ids := make([]string, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		ids = append(ids, entry.Id)
	}
	rows, err := tx.Query(ctx, `SELECT entry_id,count(*) FROM voting.current_votes WHERE task_id=$1 AND entry_id=ANY($2::text[]) GROUP BY entry_id`, c.TaskId, ids)
	if err != nil {
		return nil, storage(err)
	}
	counts := map[string]int64{}
	for rows.Next() {
		var key string
		var count int64
		if err = rows.Scan(&key, &count); err != nil {
			rows.Close()
			return nil, storage(err)
		}
		counts[key] = count
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, storage(err)
	}
	var computed time.Time
	if err = tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&computed); err != nil {
		return nil, storage(err)
	}
	out := &pb.TaskStatistics{TaskId: c.TaskId, Eligibility: eligibility(c.TaskId, &closed), CatalogRevision: c.CatalogRevision, Conclusion: "undetermined", ComputedAt: timestamppb.New(computed)}
	for _, e := range snapshot.Entries {
		n := counts[e.Id]
		out.TotalVotes += n
		if e.Side == "human" {
			out.HumanVotes += n
		} else {
			out.AgentVotes += n
		}
		out.EntryVotes = append(out.EntryVotes, &pb.EntryVoteCount{EntryId: e.Id, Side: e.Side, Votes: n})
	}
	if out.TotalVotes > 0 {
		h, a := float64(out.HumanVotes)/float64(out.TotalVotes), float64(out.AgentVotes)/float64(out.TotalVotes)
		out.HumanVoteShare = &h
		out.AgentVoteShare = &a
		for _, e := range out.EntryVotes {
			share := float64(e.Votes) / float64(out.TotalVotes)
			e.VoteShare = &share
		}
	}
	out.Conclusion = conclusion(out.HumanVotes, out.AgentVotes)
	var lastFlip *time.Time
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT flip_count FROM voting.conclusions WHERE task_id=$1),0),(SELECT last_flip_at FROM voting.conclusions WHERE task_id=$1)`, c.TaskId).Scan(&out.FlipCount, &lastFlip); err != nil {
		return nil, storage(err)
	}
	if lastFlip != nil {
		out.LastFlipAt = timestamppb.New(*lastFlip)
	}
	return &pb.ViewTaskStatisticsResponse{Statistics: out}, nil
}
func (s *Server) closeVoting(ctx context.Context, account string, c *contentpb.VotingCatalogSnapshot) (time.Time, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return time.Time{}, storage(err)
	}
	defer tx.Rollback(ctx)
	if _, err = gate(ctx, tx, c); err != nil {
		return time.Time{}, err
	}
	closed, err := guard(ctx, tx, account, c.TaskId)
	if err != nil {
		return time.Time{}, err
	}
	if closed == nil {
		var value time.Time
		err = tx.QueryRow(ctx, `INSERT INTO voting.statistics_access(account_id,task_id) VALUES($1,$2) ON CONFLICT DO NOTHING RETURNING closed_at`, account, c.TaskId).Scan(&value)
		if err != nil {
			return time.Time{}, storage(err)
		}
		closed = &value
	}
	if err = tx.Commit(ctx); err != nil {
		// A lost COMMIT response is NOT rollback evidence. Confirm on a fresh DB
		// operation; never release counts based only on an in-memory closed flag.
		check, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		var persisted time.Time
		if e := s.DB.QueryRow(check, `SELECT closed_at FROM voting.statistics_access WHERE account_id=$1 AND task_id=$2`, account, c.TaskId).Scan(&persisted); e != nil {
			return time.Time{}, storage(err)
		}
		return persisted, nil
	}
	return *closed, nil
}
func (s *Server) CleanupBallots(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM voting.ballots WHERE id IN (SELECT id FROM voting.ballots WHERE expires_at<clock_timestamp()-interval '1 day' ORDER BY expires_at,id LIMIT 256)`)
	return err
}
