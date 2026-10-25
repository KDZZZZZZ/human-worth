// Package contract contains immutable, cross-boundary operation metadata.
// It does not register handlers or make authorization decisions for an actor.
package contract

import (
	"errors"
	"fmt"
	"time"
)

type OperationID string

const (
	CreateReport             OperationID = "create_report"
	ListReports              OperationID = "list_reports"
	ResolveReport            OperationID = "resolve_report"
	TakeDownContent          OperationID = "take_down_content"
	AdminDashboard           OperationID = "admin_dashboard"
	ListAuditEvents          OperationID = "list_audit_events"
	ListChallengeAuditEvents OperationID = "list_challenge_audit_events"
	ListComments             OperationID = "list_comments"
	CreateComment            OperationID = "create_comment"
	ReadBoard                OperationID = "read_board"
	VotingEligibility        OperationID = "voting_eligibility"
	ReadBallot               OperationID = "read_ballot"
	RequestTaskReview        OperationID = "request_task_review"
	ListReviews              OperationID = "list_reviews"
	GetReview                OperationID = "get_review"
	DecideReview             OperationID = "decide_review"
	DownloadReviewAsset      OperationID = "download_review_asset"
	CheckReviewAssetAccess   OperationID = "check_review_asset_access"
	CreateDraft              OperationID = "create_draft"
	ReadOwnSubmission        OperationID = "read_own_submission"
	ListOwnSubmissions       OperationID = "list_own_submissions"
	ReplaceDraft             OperationID = "replace_draft"
	SubmitAdditionalEntry    OperationID = "submit_additional_entry"
	ReadOwnEntrySubmission   OperationID = "read_own_entry_submission"
	ResubmitRejectedEntry    OperationID = "resubmit_rejected_entry"
	ListOwnEntrySubmissions  OperationID = "list_own_entry_submissions"
	GetEntryReviewSnapshot   OperationID = "get_entry_review_snapshot"
	ApplyEntryReviewDecision OperationID = "apply_entry_review_decision"
	CreateEntryReviewCase    OperationID = "create_entry_review_case"
	CurrentSession           OperationID = "current_session"
	Logout                   OperationID = "logout"
	CreateMCPToken           OperationID = "create_mcp_token"
	ListMCPTokens            OperationID = "list_mcp_tokens"
	RevokeMCPToken           OperationID = "revoke_mcp_token"
	ReadTask                 OperationID = "read_task"
	ListApprovedEntries      OperationID = "list_approved_entries"
	ListTasks                OperationID = "list_tasks"
	ViewStatistics           OperationID = "view_statistics"
	CastVote                 OperationID = "cast_vote"
	GetBallot                OperationID = "get_ballot"
	GetVotingEligibility     OperationID = "get_voting_eligibility"
	GetVotingCatalog         OperationID = "get_voting_catalog"
	PrepareCatalog           OperationID = "prepare_catalog"
	ActivateCatalog          OperationID = "activate_catalog"
	GetCatalogTransition     OperationID = "get_catalog_transition"
	StartRun                 OperationID = "start_run"
	GetRun                   OperationID = "get_run"
	ListRuns                 OperationID = "list_runs"
	GetRunSummary            OperationID = "get_run_summary"
	CancelRun                OperationID = "cancel_run"
	RestartRun               OperationID = "restart_run"
	RegisterCandidate        OperationID = "register_candidate"
	CreateAssetUploadSession OperationID = "create_asset_upload_session"
	UploadAsset              OperationID = "upload_asset"
	CompleteAssetUpload      OperationID = "complete_asset_upload"
	AbortAssetUpload         OperationID = "abort_asset_upload"
	GetAssetUploadSession    OperationID = "get_asset_upload_session"
	GetAssetMetadata         OperationID = "get_asset_metadata"
	ListMyAssets             OperationID = "list_my_assets"
	CheckAssetOwnership      OperationID = "check_asset_ownership"
	DownloadAsset            OperationID = "download_asset"
	StartGoogleLogin         OperationID = "start_google_login"
	CompleteGoogleLogin      OperationID = "complete_google_login"
	ApplyReviewDecision      OperationID = "apply_review_decision"
	CreateReviewCase         OperationID = "create_review_case"
	DownloadPublicAsset      OperationID = "download_public_asset"
)

const (
	DefaultHTTPTimeout    = 2 * time.Second
	DefaultRPCTimeout     = 2 * time.Second
	AssetCompleteTimeout  = 2 * time.Minute
	AssetUploadChunkBytes = 5 << 20
)

// IdentityPolicy is static policy metadata. Identity still evaluates it against
// the current principal, credential, account state, and resource state.
type IdentityPolicy struct {
	Audience                     string
	Write, Anonymous, MCP, Admin bool
}

type Operation struct {
	ID         OperationID
	FullMethod string
	Policy     IdentityPolicy
	HasPolicy  bool
	RPCTimeout time.Duration // zero selects DefaultRPCTimeout
}

type HTTPRoute struct {
	Name, OpenAPIID, Method, Path string
	Target                        OperationID   // empty for routes that do not issue an actor assertion
	Timeout                       time.Duration // zero selects DefaultHTTPTimeout
	MaxBodyBytes                  int64         // zero selects the gateway default
	MaxRequestURIBytes            int           // zero selects the gateway default
	Feature                       string        // optional deployment feature gate, e.g. "challenge"
}

func (route HTTPRoute) SpecOperationID() string {
	if route.OpenAPIID != "" {
		return route.OpenAPIID
	}
	return route.Name
}

var operations = [...]Operation{
	{ID: GetBallot, FullMethod: "/humanworth.voting.v1.VotingService/GetBallot", Policy: IdentityPolicy{Audience: "voting"}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: GetVotingEligibility, FullMethod: "/humanworth.voting.v1.VotingService/GetVotingEligibility", Policy: IdentityPolicy{Audience: "voting", MCP: true}, HasPolicy: true},
	{ID: GetVotingCatalog, FullMethod: "/humanworth.content.v1.ContentService/GetVotingCatalogSnapshot", RPCTimeout: 5 * time.Second},
	{ID: PrepareCatalog, FullMethod: "/humanworth.voting.v1.VotingService/PrepareCatalogChange", RPCTimeout: 5 * time.Second},
	{ID: ActivateCatalog, FullMethod: "/humanworth.voting.v1.VotingService/ActivateCatalogSnapshot", RPCTimeout: 5 * time.Second},
	{ID: GetCatalogTransition, FullMethod: "/humanworth.voting.v1.VotingService/GetCatalogTransition"},
	{ID: CreateReport, FullMethod: "/humanworth.moderation.v1.ModerationService/CreateReport", Policy: IdentityPolicy{Audience: "moderation", Write: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: ListReports, FullMethod: "/humanworth.moderation.v1.ModerationService/ListReports", Policy: IdentityPolicy{Audience: "moderation", Admin: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: ResolveReport, FullMethod: "/humanworth.moderation.v1.ModerationService/ResolveReport", Policy: IdentityPolicy{Audience: "moderation", Write: true, Admin: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: TakeDownContent, FullMethod: "/humanworth.moderation.v1.ModerationService/TakeDownContent", Policy: IdentityPolicy{Audience: "moderation", Write: true, Admin: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: AdminDashboard, FullMethod: "/humanworth.moderation.v1.ModerationService/GetDashboard", Policy: IdentityPolicy{Audience: "moderation", Admin: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: ListAuditEvents, FullMethod: "/humanworth.moderation.v1.ModerationService/ListAuditEvents", Policy: IdentityPolicy{Audience: "moderation", Admin: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: ListChallengeAuditEvents, FullMethod: "/humanworth.challenge.v1.ChallengeService/ListAuditEvents", Policy: IdentityPolicy{Audience: "challenge", Admin: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},

	{ID: ListComments, FullMethod: "/humanworth.content.v1.ContentService/ListComments", Policy: IdentityPolicy{Audience: "content", Anonymous: true, MCP: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: CreateComment, FullMethod: "/humanworth.content.v1.ContentService/CreateComment", Policy: IdentityPolicy{Audience: "content", Write: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: ReadBoard, FullMethod: "/humanworth.discovery.v1.DiscoveryService/GetPublicBoard", Policy: IdentityPolicy{Audience: "discovery", Anonymous: true, MCP: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},

	{ID: RequestTaskReview, FullMethod: "/humanworth.content.v1.ContentService/RequestTaskReview", Policy: IdentityPolicy{Audience: "content", Write: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: ListReviews, FullMethod: "/humanworth.moderation.v1.ModerationService/ListReviews", Policy: IdentityPolicy{Audience: "moderation", Admin: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: GetReview, FullMethod: "/humanworth.moderation.v1.ModerationService/GetReview", Policy: IdentityPolicy{Audience: "moderation", Admin: true}, HasPolicy: true},
	{ID: DecideReview, FullMethod: "/humanworth.moderation.v1.ModerationService/DecideReview", Policy: IdentityPolicy{Audience: "moderation", Write: true, Admin: true}, HasPolicy: true},
	{ID: DownloadReviewAsset, FullMethod: "/humanworth.asset.v1.AssetService/DownloadReviewAsset", Policy: IdentityPolicy{Audience: "asset", Admin: true}, HasPolicy: true, RPCTimeout: 15 * time.Minute},
	{ID: CheckReviewAssetAccess, FullMethod: "/humanworth.content.v1.ContentService/CheckReviewAssetAccess", Policy: IdentityPolicy{Audience: "content", Admin: true}, HasPolicy: true},
	{ID: CreateDraft, FullMethod: "/humanworth.content.v1.ContentService/CreateTaskDraft", Policy: IdentityPolicy{Audience: "content", Write: true}, HasPolicy: true},
	{ID: ReadOwnSubmission, FullMethod: "/humanworth.content.v1.ContentService/GetMyTaskSubmission", Policy: IdentityPolicy{Audience: "content"}, HasPolicy: true},
	{ID: ListOwnSubmissions, FullMethod: "/humanworth.content.v1.ContentService/ListMySubmissions", Policy: IdentityPolicy{Audience: "content"}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: ReplaceDraft, FullMethod: "/humanworth.content.v1.ContentService/ReplaceTaskDraft", Policy: IdentityPolicy{Audience: "content", Write: true}, HasPolicy: true},
	{ID: SubmitAdditionalEntry, FullMethod: "/humanworth.content.v1.ContentService/SubmitAdditionalEntry", Policy: IdentityPolicy{Audience: "content", Write: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: ReadOwnEntrySubmission, FullMethod: "/humanworth.content.v1.ContentService/GetMyEntrySubmission", Policy: IdentityPolicy{Audience: "content"}, HasPolicy: true},
	{ID: ResubmitRejectedEntry, FullMethod: "/humanworth.content.v1.ContentService/ResubmitRejectedEntry", Policy: IdentityPolicy{Audience: "content", Write: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: ListOwnEntrySubmissions, FullMethod: "/humanworth.content.v1.ContentService/ListMyEntrySubmissions", Policy: IdentityPolicy{Audience: "content"}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: GetEntryReviewSnapshot, FullMethod: "/humanworth.content.v1.ContentService/GetEntryReviewSnapshot"},
	{ID: ApplyEntryReviewDecision, FullMethod: "/humanworth.content.v1.ContentService/ApplyEntryReviewDecision", RPCTimeout: 5 * time.Second},
	{ID: CreateEntryReviewCase, FullMethod: "/humanworth.moderation.v1.ModerationService/CreateEntryReviewCase", RPCTimeout: 5 * time.Second},
	{ID: CurrentSession, FullMethod: "/humanworth.identity.v1.IdentityService/GetCurrentSession", Policy: IdentityPolicy{Audience: "identity"}, HasPolicy: true},
	{ID: Logout, FullMethod: "/humanworth.identity.v1.IdentityService/LogoutCurrentSession", Policy: IdentityPolicy{Audience: "identity", Write: true}, HasPolicy: true},
	{ID: CreateMCPToken, FullMethod: "/humanworth.identity.v1.IdentityService/CreateMcpToken", Policy: IdentityPolicy{Audience: "identity", Write: true}, HasPolicy: true},
	{ID: ListMCPTokens, FullMethod: "/humanworth.identity.v1.IdentityService/ListMyMcpTokens", Policy: IdentityPolicy{Audience: "identity"}, HasPolicy: true},
	{ID: RevokeMCPToken, FullMethod: "/humanworth.identity.v1.IdentityService/RevokeMcpToken", Policy: IdentityPolicy{Audience: "identity", Write: true}, HasPolicy: true},
	{ID: ReadTask, FullMethod: "/humanworth.content.v1.ContentService/GetTask", Policy: IdentityPolicy{Audience: "content", Anonymous: true, MCP: true}, HasPolicy: true},
	{ID: ListApprovedEntries, FullMethod: "/humanworth.content.v1.ContentService/ListApprovedEntries", Policy: IdentityPolicy{Audience: "content", Anonymous: true, MCP: true}, HasPolicy: true, RPCTimeout: 5 * time.Second},
	{ID: ListTasks, FullMethod: "/humanworth.discovery.v1.DiscoveryService/ListTasks", Policy: IdentityPolicy{Audience: "discovery", Anonymous: true, MCP: true}, HasPolicy: true},
	{ID: ViewStatistics, FullMethod: "/humanworth.voting.v1.VotingService/ViewTaskStatistics", Policy: IdentityPolicy{Audience: "voting", Write: true, MCP: true}, HasPolicy: true},
	{ID: CastVote, FullMethod: "/humanworth.voting.v1.VotingService/CastVote", Policy: IdentityPolicy{Audience: "voting", Write: true}, HasPolicy: true},
	{ID: StartRun, FullMethod: "/humanworth.challenge.v1.ChallengeService/StartRun", Policy: IdentityPolicy{Audience: "challenge", Write: true, Admin: true}, HasPolicy: true, RPCTimeout: 15 * time.Second},
	{ID: GetRun, FullMethod: "/humanworth.challenge.v1.ChallengeService/GetRun", Policy: IdentityPolicy{Audience: "challenge", Admin: true}, HasPolicy: true},
	{ID: ListRuns, FullMethod: "/humanworth.challenge.v1.ChallengeService/ListRuns", Policy: IdentityPolicy{Audience: "challenge", Admin: true}, HasPolicy: true},
	{ID: GetRunSummary, FullMethod: "/humanworth.challenge.v1.ChallengeService/GetRunSummary", Policy: IdentityPolicy{Audience: "challenge", Admin: true}, HasPolicy: true},
	{ID: CancelRun, FullMethod: "/humanworth.challenge.v1.ChallengeService/CancelRun", Policy: IdentityPolicy{Audience: "challenge", Write: true, Admin: true}, HasPolicy: true},
	{ID: RestartRun, FullMethod: "/humanworth.challenge.v1.ChallengeService/RestartRun", Policy: IdentityPolicy{Audience: "challenge", Write: true, Admin: true}, HasPolicy: true, RPCTimeout: 15 * time.Second},
	{ID: RegisterCandidate, FullMethod: "/humanworth.challenge.v1.ChallengeService/RegisterCandidate", Policy: IdentityPolicy{Audience: "challenge", Write: true, Admin: true}, HasPolicy: true},
	{ID: CreateAssetUploadSession, FullMethod: "/humanworth.asset.v1.AssetService/CreateUploadSession", Policy: IdentityPolicy{Audience: "asset", Write: true}, HasPolicy: true},
	{ID: UploadAsset, FullMethod: "/humanworth.asset.v1.AssetService/UploadAsset", Policy: IdentityPolicy{Audience: "asset", Write: true}, HasPolicy: true, RPCTimeout: 15 * time.Minute},
	{ID: CompleteAssetUpload, FullMethod: "/humanworth.asset.v1.AssetService/CompleteUpload", Policy: IdentityPolicy{Audience: "asset", Write: true}, HasPolicy: true, RPCTimeout: AssetCompleteTimeout},
	{ID: AbortAssetUpload, FullMethod: "/humanworth.asset.v1.AssetService/AbortUpload", Policy: IdentityPolicy{Audience: "asset", Write: true}, HasPolicy: true},
	{ID: GetAssetUploadSession, FullMethod: "/humanworth.asset.v1.AssetService/GetUploadSession", Policy: IdentityPolicy{Audience: "asset"}, HasPolicy: true},
	{ID: GetAssetMetadata, FullMethod: "/humanworth.asset.v1.AssetService/GetAssetMetadata", Policy: IdentityPolicy{Audience: "asset"}, HasPolicy: true},
	{ID: ListMyAssets, FullMethod: "/humanworth.asset.v1.AssetService/ListMyAssets", Policy: IdentityPolicy{Audience: "asset"}, HasPolicy: true},
	{ID: CheckAssetOwnership, FullMethod: "/humanworth.asset.v1.AssetService/CheckAssetOwnership", Policy: IdentityPolicy{Audience: "asset"}, HasPolicy: true},
	{ID: DownloadAsset, FullMethod: "/humanworth.asset.v1.AssetService/DownloadAsset", Policy: IdentityPolicy{Audience: "asset"}, HasPolicy: true, RPCTimeout: 15 * time.Minute},
	{ID: StartGoogleLogin, FullMethod: "/humanworth.identity.v1.IdentityService/StartGoogleLogin"},
	{ID: CompleteGoogleLogin, FullMethod: "/humanworth.identity.v1.IdentityService/CompleteGoogleLogin", RPCTimeout: 15 * time.Second},
	{ID: ApplyReviewDecision, FullMethod: "/humanworth.content.v1.ContentService/ApplyReviewDecision", RPCTimeout: 5 * time.Second},
	{ID: CreateReviewCase, FullMethod: "/humanworth.moderation.v1.ModerationService/CreateReviewCase", RPCTimeout: 5 * time.Second},
	{ID: DownloadPublicAsset, FullMethod: "/humanworth.asset.v1.AssetService/DownloadPublicAsset", RPCTimeout: 15 * time.Minute},
}

var httpRoutes = [...]HTTPRoute{
	{Name: "getVotingEligibility", Method: "GET", Path: "/api/tasks/{taskId}/voting-eligibility", Target: GetVotingEligibility, Timeout: 5 * time.Second},
	{Name: "getBallot", Method: "GET", Path: "/api/tasks/{taskId}/ballot", Target: GetBallot, Timeout: 5 * time.Second},
	{Name: "castVote", Method: "PUT", Path: "/api/tasks/{taskId}/vote", Target: CastVote, Timeout: 5 * time.Second},
	{Name: "accessTaskStatistics", Method: "POST", Path: "/api/tasks/{taskId}/statistics-access", Target: ViewStatistics, Timeout: 5 * time.Second},
	{Name: "mcpRequest", Method: "POST", Path: "/api/mcp", Timeout: 10 * time.Second, MaxBodyBytes: 64 << 10},
	{Name: "createReport", Method: "POST", Path: "/api/reports", Target: CreateReport, Timeout: 5 * time.Second},
	{Name: "listReports", Method: "GET", Path: "/api/admin/reports", Target: ListReports, Timeout: 5 * time.Second},
	{Name: "resolveReport", Method: "POST", Path: "/api/admin/reports/{reportId}/resolution", Target: ResolveReport, Timeout: 5 * time.Second},
	{Name: "takeDownContent", Method: "POST", Path: "/api/admin/takedowns", Target: TakeDownContent, Timeout: 5 * time.Second},
	{Name: "getAdminDashboard", Method: "GET", Path: "/api/admin/dashboard", Target: AdminDashboard, Timeout: 5 * time.Second},
	{Name: "listAuditEvents", Method: "GET", Path: "/api/admin/audit-events", Target: ListAuditEvents, Timeout: 5 * time.Second},

	{Name: "listTasks", Method: "GET", Path: "/api/tasks", Target: ListTasks, Timeout: 5 * time.Second},
	{Name: "getPublicBoard", Method: "GET", Path: "/api/board", Target: ReadBoard, Timeout: 5 * time.Second},
	{Name: "listComments", Method: "GET", Path: "/api/tasks/{taskId}/comments", Target: ListComments, Timeout: 5 * time.Second},
	{Name: "createComment", Method: "POST", Path: "/api/tasks/{taskId}/comments", Target: CreateComment, Timeout: 5 * time.Second},

	{Name: "downloadTaskAsset", OpenAPIID: "downloadTaskAsset", Method: "GET", Path: "/api/tasks/{taskId}/assets/{assetId}/content", Timeout: 15 * time.Minute},
	{Name: "getTask", Method: "GET", Path: "/api/tasks/{taskId}", Target: ReadTask},
	{Name: "listApprovedEntries", Method: "GET", Path: "/api/tasks/{taskId}/entries", Target: ListApprovedEntries, Timeout: 5 * time.Second},
	{Name: "submitAdditionalEntry", Method: "POST", Path: "/api/tasks/{taskId}/entries", Target: SubmitAdditionalEntry, Timeout: 5 * time.Second},
	{Name: "getMyEntrySubmission", Method: "GET", Path: "/api/me/entries/{entryId}", Target: ReadOwnEntrySubmission},
	{Name: "resubmitRejectedEntry", Method: "PUT", Path: "/api/me/entries/{entryId}", Target: ResubmitRejectedEntry, Timeout: 5 * time.Second},
	{Name: "requestTaskReview", Method: "POST", Path: "/api/me/tasks/{taskId}/review-requests", Target: RequestTaskReview, Timeout: 5 * time.Second},
	{Name: "listReviews", Method: "GET", Path: "/api/admin/reviews", Target: ListReviews, Timeout: 5 * time.Second},
	{Name: "getReview", Method: "GET", Path: "/api/admin/reviews/{reviewId}", Target: GetReview},
	{Name: "decideReview", Method: "POST", Path: "/api/admin/reviews/{reviewId}/decision", Target: DecideReview},
	{Name: "downloadReviewAsset", Method: "GET", Path: "/api/admin/reviews/{reviewId}/assets/{assetId}/content", Target: DownloadReviewAsset, Timeout: 15 * time.Minute},
	{Name: "createTaskDraft", Method: "POST", Path: "/api/tasks", Target: CreateDraft},
	{Name: "listMySubmissions", Method: "GET", Path: "/api/me/submissions", Target: ListOwnSubmissions, Timeout: 5 * time.Second, MaxRequestURIBytes: 40 << 10},
	{Name: "createAssetUploadSession", Method: "POST", Path: "/api/me/assets/upload-sessions", Target: CreateAssetUploadSession},
	{Name: "uploadAsset", OpenAPIID: "uploadAssetChunk", Method: "PUT", Path: "/api/me/assets/upload-sessions/{uploadSessionId}/content", Target: UploadAsset, Timeout: 15 * time.Minute, MaxBodyBytes: AssetUploadChunkBytes + 1},
	{Name: "completeAsset", OpenAPIID: "completeAssetUpload", Method: "POST", Path: "/api/me/assets/{assetId}/complete", Target: CompleteAssetUpload, Timeout: AssetCompleteTimeout},
	{Name: "abortAsset", OpenAPIID: "abortAssetUpload", Method: "DELETE", Path: "/api/me/assets/upload-sessions/{uploadSessionId}", Target: AbortAssetUpload},
	{Name: "getAssetUploadSession", Method: "GET", Path: "/api/me/upload-sessions/{uploadSessionId}", Target: GetAssetUploadSession},
	{Name: "listMyAssets", Method: "GET", Path: "/api/me/assets", Target: ListMyAssets},
	{Name: "getAssetMetadata", OpenAPIID: "getMyAssetMetadata", Method: "GET", Path: "/api/me/assets/{assetId}", Target: GetAssetMetadata},
	{Name: "downloadAsset", OpenAPIID: "downloadMyAsset", Method: "GET", Path: "/api/me/assets/{assetId}/content", Target: DownloadAsset, Timeout: 15 * time.Minute},
	{Name: "downloadPublicAsset", OpenAPIID: "downloadReviewedEntryAsset", Method: "GET", Path: "/api/tasks/{taskId}/entries/{entryId}/assets/{assetId}/content", Target: DownloadAsset, Timeout: 15 * time.Minute},
	{Name: "getMyTaskSubmission", Method: "GET", Path: "/api/me/tasks/{taskId}", Target: ReadOwnSubmission},
	{Name: "replaceTaskDraft", Method: "PUT", Path: "/api/me/tasks/{taskId}", Target: ReplaceDraft},
	{Name: "startGoogleLogin", Method: "GET", Path: "/api/auth/google", Target: StartGoogleLogin},
	{Name: "completeGoogleLogin", Method: "GET", Path: "/api/auth/google/callback", Target: CompleteGoogleLogin, Timeout: 15 * time.Second},
	{Name: "getCurrentSession", Method: "GET", Path: "/api/me", Target: CurrentSession},
	{Name: "logoutCurrentSession", Method: "POST", Path: "/api/auth/logout", Target: Logout},
	{Name: "createMcpToken", Method: "POST", Path: "/api/me/mcp-tokens", Target: CreateMCPToken},
	{Name: "listMyMcpTokens", Method: "GET", Path: "/api/me/mcp-tokens", Target: ListMCPTokens},
	{Name: "revokeMcpToken", Method: "DELETE", Path: "/api/me/mcp-tokens/{credentialId}", Target: RevokeMCPToken},
	{Name: "startChallengeRun", Method: "POST", Path: "/api/admin/challenge-runs", Target: StartRun, Timeout: 15 * time.Second, MaxBodyBytes: 1 << 20, Feature: "challenge"},
	{Name: "listChallengeRuns", Method: "GET", Path: "/api/admin/challenge-runs", Target: ListRuns, Feature: "challenge"},
	{Name: "getChallengeRun", Method: "GET", Path: "/api/admin/challenge-runs/{runId}", Target: GetRun, Feature: "challenge"},
	{Name: "cancelChallengeRun", Method: "POST", Path: "/api/admin/challenge-runs/{runId}/cancel", Target: CancelRun, Feature: "challenge"},
	{Name: "restartChallengeRun", Method: "POST", Path: "/api/admin/challenge-runs/{runId}/restart", Target: RestartRun, Timeout: 15 * time.Second, MaxBodyBytes: 1 << 20, Feature: "challenge"},
	{Name: "registerCandidate", Method: "PUT", Path: "/api/admin/challenge-runs/{runId}/candidates/{candidateId}/registration", Target: RegisterCandidate, Feature: "challenge"},
}

func Operations() []Operation { return append([]Operation(nil), operations[:]...) }

func HTTPRoutes() []HTTPRoute { return append([]HTTPRoute(nil), httpRoutes[:]...) }

func MaxRequestURIBytes(method, path string) int {
	for _, route := range httpRoutes {
		if route.Method == method && route.Path == path && route.MaxRequestURIBytes > 0 {
			return route.MaxRequestURIBytes
		}
	}
	return 8192
}

func LookupOperation(id OperationID) (Operation, bool) {
	for _, operation := range operations {
		if operation.ID == id {
			return operation, true
		}
	}
	return Operation{}, false
}

func LookupMethod(method string) (Operation, bool) {
	for _, operation := range operations {
		if operation.FullMethod == method {
			return operation, true
		}
	}
	return Operation{}, false
}

// ValidateHTTPHandlers ensures every catalogued route has one explicit
// gateway handler and that no handler is registered outside the catalog.
func ValidateHTTPHandlers(handlers map[string]bool) error {
	seenNames := make(map[string]struct{}, len(httpRoutes))
	seenRoutes := make(map[string]struct{}, len(httpRoutes))
	for _, route := range httpRoutes {
		if route.Name == "" || route.Method == "" || route.Path == "" {
			return errors.New("HTTP operation is missing a name, method, or path")
		}
		if _, exists := seenNames[route.Name]; exists {
			return fmt.Errorf("duplicate HTTP operation %q", route.Name)
		}
		seenNames[route.Name] = struct{}{}
		key := route.Method + " " + route.Path
		if _, exists := seenRoutes[key]; exists {
			return fmt.Errorf("duplicate HTTP route %q", key)
		}
		seenRoutes[key] = struct{}{}
		if route.Target != "" {
			if _, exists := LookupOperation(route.Target); !exists {
				return fmt.Errorf("HTTP operation %q references unknown operation %q", route.Name, route.Target)
			}
		}
		if route.Timeout < 0 || route.MaxBodyBytes < 0 || route.MaxRequestURIBytes < 0 {
			return fmt.Errorf("HTTP operation %q has a negative limit", route.Name)
		}
		if !handlers[route.Name] {
			return fmt.Errorf("HTTP operation %q has no explicit gateway handler", route.Name)
		}
	}
	for name, present := range handlers {
		if present {
			if _, exists := seenNames[name]; !exists {
				return fmt.Errorf("gateway handler %q has no HTTP operation definition", name)
			}
		}
	}
	return nil
}

func ValidateOperations() error {
	ids := make(map[OperationID]struct{}, len(operations))
	methods := make(map[string]struct{}, len(operations))
	for _, operation := range operations {
		if operation.ID == "" || operation.FullMethod == "" {
			return errors.New("operation is missing an ID or full RPC method")
		}
		if _, exists := ids[operation.ID]; exists {
			return fmt.Errorf("duplicate operation ID %q", operation.ID)
		}
		ids[operation.ID] = struct{}{}
		if _, exists := methods[operation.FullMethod]; exists {
			return fmt.Errorf("duplicate RPC method %q", operation.FullMethod)
		}
		methods[operation.FullMethod] = struct{}{}
		if operation.HasPolicy && operation.Policy.Audience == "" {
			return fmt.Errorf("operation %q has a policy without an audience", operation.ID)
		}
		if operation.RPCTimeout < 0 {
			return fmt.Errorf("operation %q has a negative RPC timeout", operation.ID)
		}
	}
	return nil
}
