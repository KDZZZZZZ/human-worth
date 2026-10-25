package gateway

import (
	"encoding/json"
	"net/http"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"google.golang.org/protobuf/encoding/protojson"
)

func (h *Handler) votingActor(w http.ResponseWriter, r *http.Request, operation contract.OperationID) (string, bool) {
	if !noQuery(w, r) {
		return "", false
	}
	if h.options.Voting == nil {
		problem(w, 503, "voting_unavailable")
		return "", false
	}
	return h.actorFor(w, r, operation)
}
func voteJSON(v *pb.Vote) any {
	if v == nil {
		return nil
	}
	return map[string]any{"id": v.Id, "taskId": v.TaskId, "entryId": v.EntryId, "revision": v.Revision, "createdAt": timestampJSON(v.CreatedAt), "updatedAt": timestampJSON(v.UpdatedAt)}
}
func eligibilityJSON(e *pb.VotingEligibility) any {
	var reason any
	if e.Reason != nil {
		reason = *e.Reason
	}
	return map[string]any{"taskId": e.TaskId, "canVote": e.CanVote, "permanentlyClosed": e.PermanentlyClosed, "closedAt": timestampJSON(e.ClosedAt), "reason": reason}
}
func votingJSON(w http.ResponseWriter, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		problem(w, 503, "voting_unavailable")
		return
	}
	if len(raw) > contract.VotingCatalogBytes {
		problem(w, 503, "catalog_too_large")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	w.Write(raw)
}
func (h *Handler) getVotingEligibility(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.votingActor(w, r, contract.GetVotingEligibility)
	if !ok {
		return
	}
	out, err := h.options.Voting.GetVotingEligibility(r.Context(), &pb.GetVotingEligibilityRequest{ActorAssertion: actor, TaskId: r.PathValue("taskId")})
	if err != nil {
		rpcErrorFor(w, err, "voting_unavailable")
		return
	}
	if out.GetEligibility() == nil {
		problem(w, 503, "voting_unavailable")
		return
	}
	votingJSON(w, eligibilityJSON(out.Eligibility))
}
func (h *Handler) getBallot(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.votingActor(w, r, contract.GetBallot)
	if !ok {
		return
	}
	out, err := h.options.Voting.GetBallot(r.Context(), &pb.GetBallotRequest{ActorAssertion: actor, TaskId: r.PathValue("taskId")})
	if err != nil {
		rpcErrorFor(w, err, "voting_unavailable")
		return
	}
	b := out.GetBallot()
	if b == nil {
		problem(w, 503, "voting_unavailable")
		return
	}
	entries := make([]any, 0, len(b.Entries))
	for _, e := range b.Entries {
		raw, err := (protojson.MarshalOptions{EmitDefaultValues: true}).Marshal(e)
		if err != nil {
			problem(w, 503, "voting_unavailable")
			return
		}
		var value map[string]any
		if json.Unmarshal(raw, &value) != nil {
			problem(w, 503, "voting_unavailable")
			return
		}
		value["author"] = publicAuthorJSON(e.Author)
		value["approvedAt"] = timestampJSON(e.ApprovedAt)
		entries = append(entries, value)
	}
	votingJSON(w, map[string]any{"id": b.Id, "taskId": b.TaskId, "catalogRevision": b.CatalogRevision, "entries": entries, "currentVote": voteJSON(b.CurrentVote), "generation": b.Generation, "expiresAt": timestampJSON(b.ExpiresAt)})
}
func (h *Handler) castVote(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.votingActor(w, r, contract.CastVote)
	if !ok {
		return
	}
	var raw json.RawMessage
	if !decode(w, r, &raw) {
		return
	}
	obj, ok := httpObject(raw, []string{"ballotId", "entryId", "expectedVoteRevision"})
	if !ok {
		problem(w, 400, "invalid_vote")
		return
	}
	var ballot, entry string
	var revision int64
	if json.Unmarshal(obj["ballotId"], &ballot) != nil || json.Unmarshal(obj["entryId"], &entry) != nil || json.Unmarshal(obj["expectedVoteRevision"], &revision) != nil || revision < 0 {
		problem(w, 400, "invalid_vote")
		return
	}
	out, err := h.options.Voting.CastVote(r.Context(), &pb.CastVoteRequest{ActorAssertion: actor, TaskId: r.PathValue("taskId"), BallotId: ballot, EntryId: entry, ExpectedVoteRevision: revision})
	if err != nil {
		rpcErrorFor(w, err, "voting_unavailable")
		return
	}
	votingJSON(w, voteJSON(out.GetVote()))
}
func (h *Handler) accessTaskStatistics(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.votingActor(w, r, contract.ViewStatistics)
	if !ok {
		return
	}
	var raw json.RawMessage
	if !decode(w, r, &raw) {
		return
	}
	obj, ok := httpObject(raw, nil, "permanentlyCloseVoting")
	if !ok {
		problem(w, 400, "invalid_request")
		return
	}
	var confirm bool
	if obj["permanentlyCloseVoting"] != nil && json.Unmarshal(obj["permanentlyCloseVoting"], &confirm) != nil {
		problem(w, 400, "invalid_request")
		return
	}
	if !confirm {
		problem(w, 422, "statistics_confirmation_required")
		return
	}
	out, err := h.options.Voting.ViewTaskStatistics(r.Context(), &pb.ViewTaskStatisticsRequest{ActorAssertion: actor, TaskId: r.PathValue("taskId"), PermanentlyCloseVoting: true})
	if err != nil {
		rpcErrorFor(w, err, "voting_unavailable")
		return
	}
	s := out.GetStatistics()
	if s == nil || s.Eligibility == nil || !s.Eligibility.PermanentlyClosed {
		problem(w, 503, "voting_unavailable")
		return
	}
	votingJSON(w, statisticsJSON(s))
}

func statisticsJSON(s *pb.TaskStatistics) any {
	entries := make([]any, 0, len(s.EntryVotes))
	for _, e := range s.EntryVotes {
		entries = append(entries, map[string]any{"entryId": e.EntryId, "side": e.Side, "votes": e.Votes, "voteShare": e.VoteShare})
	}
	return map[string]any{"taskId": s.TaskId, "eligibility": eligibilityJSON(s.Eligibility), "totalVotes": s.TotalVotes, "humanVotes": s.HumanVotes, "agentVotes": s.AgentVotes, "humanVoteShare": s.HumanVoteShare, "agentVoteShare": s.AgentVoteShare, "entryVotes": entries, "catalogRevision": s.CatalogRevision, "conclusion": s.Conclusion, "computedAt": timestampJSON(s.ComputedAt), "flipCount": s.FlipCount, "lastFlipAt": timestampJSON(s.LastFlipAt)}
}
