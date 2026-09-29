package domain

type Operation string

const (
	CreateDraft        Operation = "create_draft"
	ReadOwnSubmission  Operation = "read_own_submission"
	ListOwnSubmissions Operation = "list_own_submissions"
	ReplaceDraft       Operation = "replace_draft"
	CurrentSession     Operation = "current_session"
	Logout             Operation = "logout"
	CreateMCPToken     Operation = "create_mcp_token"
	ListMCPTokens      Operation = "list_mcp_tokens"
	RevokeMCPToken     Operation = "revoke_mcp_token"
	ReadTask           Operation = "read_task"
	ListTasks          Operation = "list_tasks"
	ViewStatistics     Operation = "view_statistics"
	CastVote           Operation = "cast_vote"
	StartRun           Operation = "start_run"
	GetRun             Operation = "get_run"
	ListRuns           Operation = "list_runs"
	GetRunSummary      Operation = "get_run_summary"
	CancelRun          Operation = "cancel_run"
	RestartRun         Operation = "restart_run"
	RegisterCandidate  Operation = "register_candidate"
)

// 【阅读 1】Policy 约束一个业务操作的受众与凭据用途；Write 表示存在副作用，web 调用需要 CSRF。
// Anonymous、MCP、Admin 分别控制匿名、MCP 只读凭据和管理员能力，不能由 HTTP 请求自行指定。
type Policy struct {
	Audience                     string
	Write, Anonymous, MCP, Admin bool
}

// 【阅读 2】PolicyFor 是 ResolvePrincipal 与 verify 共用的业务策略；transport/grpc/targets.go 绑定完整 RPC 名，未登记的方法默认拒绝。
// 表内未来模块的方法仅定义身份策略，不代表接口已经实现或公开；例如查看统计虽属读取入口，
// 仍有永久关闭投票资格的副作用，故标为 Write，实际资格事务应由 Voting 自己完成。
func PolicyFor(op Operation) (Policy, bool) {
	switch op {
	case CreateDraft, ReplaceDraft:
		return Policy{Audience: "content", Write: true}, true
	case ReadOwnSubmission, ListOwnSubmissions:
		return Policy{Audience: "content"}, true
	case CurrentSession, ListMCPTokens:
		return Policy{Audience: "identity"}, true
	case Logout, CreateMCPToken, RevokeMCPToken:
		return Policy{Audience: "identity", Write: true}, true
	case ReadTask:
		return Policy{Audience: "content", Anonymous: true, MCP: true}, true
	case ListTasks:
		return Policy{Audience: "discovery", Anonymous: true, MCP: true}, true
	case ViewStatistics:
		return Policy{Audience: "voting", Write: true, MCP: true}, true
	case CastVote:
		return Policy{Audience: "voting", Write: true}, true
	case StartRun, CancelRun, RestartRun, RegisterCandidate:
		return Policy{Audience: "challenge", Write: true, Admin: true}, true
	case GetRun, ListRuns, GetRunSummary:
		return Policy{Audience: "challenge", Admin: true}, true
	default:
		return Policy{}, false
	}
}
func (p Policy) Authorize(actor Principal) error {
	if err := actor.Validate(); err != nil {
		return err
	}
	if actor.ClientKind == Anonymous && !p.Anonymous {
		return ErrUnauthenticated
	}
	if actor.ClientKind == MCPRead && !p.MCP {
		return ErrCredentialPurpose
	}
	if p.Admin && actor.Role != Admin {
		return ErrAdministratorRequired
	}
	return nil
}
