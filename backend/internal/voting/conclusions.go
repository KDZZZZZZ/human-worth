package voting

import (
	"context"
	"errors"
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"math"
	"time"
)

// Agent Self-Claimed policy: minimum 20 current votes and a 95% Wilson interval
// excluding 1/2. This describes participating accounts, not a population sample.
func conclusion(human, agent int64) string {
	n := float64(human + agent)
	if n < 20 {
		return "undetermined"
	}
	p := float64(human) / n
	const z = 1.959963984540054
	center := (p + z*z/(2*n)) / (1 + z*z/n)
	margin := z * math.Sqrt(p*(1-p)/n+z*z/(4*n*n)) / (1 + z*z/n)
	if center-margin > .5 {
		return "human_leading"
	}
	if center+margin < .5 {
		return "agent_leading"
	}
	return "undetermined"
}

// Caller already owns the exclusive catalog gate. Vote writes and catalog
// activations therefore publish one ordered, transactionally consistent history.
func updateConclusion(ctx context.Context, tx pgx.Tx, c *contentpb.VotingCatalogSnapshot) error {
	ids := []string{}
	sides := map[string]string{}
	for _, e := range c.Entries {
		ids = append(ids, e.Id)
		sides[e.Id] = e.Side
	}
	rows, err := tx.Query(ctx, `SELECT entry_id,count(*) FROM voting.current_votes WHERE task_id=$1 AND entry_id=ANY($2::text[]) GROUP BY entry_id`, c.TaskId, ids)
	if err != nil {
		return storage(err)
	}
	var human, agent int64
	for rows.Next() {
		var id string
		var n int64
		if err = rows.Scan(&id, &n); err != nil {
			rows.Close()
			return storage(err)
		}
		if sides[id] == "human" {
			human += n
		} else {
			agent += n
		}
	}
	rows.Close()
	if rows.Err() != nil {
		return storage(rows.Err())
	}
	next := "undetermined"
	if c.State == "open" {
		next = conclusion(human, agent)
	}
	previous, last := "undetermined", ""
	var flips int64
	var lastFlip *time.Time
	err = tx.QueryRow(ctx, `SELECT conclusion,last_leading,flip_count,last_flip_at FROM voting.conclusions WHERE task_id=$1`, c.TaskId).Scan(&previous, &last, &flips, &lastFlip)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return storage(err)
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return storage(err)
	}
	flip := next != "undetermined" && last != "" && next != last
	if flip {
		flips++
		lastFlip = &now
	}
	if next != "undetermined" {
		last = next
	}
	_, err = tx.Exec(ctx, `INSERT INTO voting.conclusions(task_id,conclusion,last_leading,flip_count,last_flip_at,changed_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(task_id) DO UPDATE SET conclusion=$2,last_leading=$3,flip_count=$4,last_flip_at=$5,changed_at=CASE WHEN voting.conclusions.conclusion<>$2 THEN $6 ELSE voting.conclusions.changed_at END`, c.TaskId, next, last, flips, lastFlip, now)
	if err == nil && next != previous {
		_, err = tx.Exec(ctx, `INSERT INTO voting.conclusion_events(task_id,catalog_revision,previous_conclusion,conclusion,is_flip,recorded_at) VALUES($1,$2,$3,$4,$5,$6)`, c.TaskId, c.CatalogRevision, previous, next, flip, now)
	}
	return storage(err)
}

func (s *Server) GetPublicConclusions(ctx context.Context, r *pb.GetPublicConclusionsRequest) (*pb.GetPublicConclusionsResponse, error) {
	if err := peer(ctx, "discovery"); err != nil {
		return nil, err
	}
	if len(r.GetTaskIds()) > 100 {
		return nil, invalid("too_many_tasks")
	}
	out := &pb.GetPublicConclusionsResponse{Conclusions: map[string]string{}}
	for _, id := range r.GetTaskIds() {
		if !taskID.MatchString(id) {
			return nil, invalid("invalid_task")
		}
		out.Conclusions[id] = "undetermined"
	}
	// Ordinary discovery reads never aggregate votes or expose per-task statistics.
	rows, err := s.DB.Query(ctx, `SELECT c.task_id,c.conclusion FROM voting.conclusions c JOIN voting.catalog_gates g ON g.task_id=c.task_id WHERE c.task_id=ANY($1::text[]) AND g.state='open'`, r.GetTaskIds())
	if err != nil {
		return nil, storage(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, v string
		if err = rows.Scan(&id, &v); err != nil {
			return nil, storage(err)
		}
		out.Conclusions[id] = v
	}
	return out, storage(rows.Err())
}

// Backfill only missing projections before accepting traffic after an upgrade.
func (s *Server) BackfillConclusions(ctx context.Context) error {
	for {
		rows, err := s.DB.Query(ctx, `SELECT g.task_id FROM voting.catalog_gates g LEFT JOIN voting.conclusions c USING(task_id) WHERE c.task_id IS NULL AND g.snapshot IS NOT NULL ORDER BY g.task_id LIMIT 100`)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if rows.Err() != nil {
			return rows.Err()
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			tx, err := s.DB.Begin(ctx)
			if err != nil {
				return err
			}
			err = func() error {
				defer tx.Rollback(ctx)
				var raw []byte
				if e := tx.QueryRow(ctx, `SELECT snapshot FROM voting.catalog_gates WHERE task_id=$1 FOR UPDATE`, id).Scan(&raw); e != nil {
					return e
				}
				c := &contentpb.VotingCatalogSnapshot{}
				if proto.Unmarshal(raw, c) != nil {
					return status.Error(codes.Internal, "invalid_catalog")
				}
				if e := updateConclusion(ctx, tx, c); e != nil {
					return e
				}
				return tx.Commit(ctx)
			}()
			if err != nil {
				return err
			}
		}
	}
}
