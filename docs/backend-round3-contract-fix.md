# 第3轮契约问题修复与复验（2026-10-30）

用户授权修复第三轮发现的问题并重新验证。本轮没有新增业务功能、前端改动、迁移、提交、推送、PR、合并或部署。

## 基线与工作区

获取到的上游 `dev` 为 `b5904a58f14e95eb62072db35bf0ef37aee10691`。audit 工作区仍在 `backend/test-upstream-20261028@7c47abb123c841de0d8c37a075483c729f048bec`，原有 `docs/backend-completion.md` 和未跟踪的 `provider_fault_test.go` 保持原样；本轮不切换脏分支、不新建 worktree。

修复文件保存在 audit 工作区，同时将同一任务补丁应用于最新 dev 的仓库外源码快照，后者是本轮复验基线。这样不会夹带或覆盖上一轮尚未提交的改动。验证结果不是“audit 所有旧代码已经与 dev 同步”。持久证据：`/home/cherry/.local/state/human-worth/acceptance/20261030-round3-fix-140439/`。

## 修复决定

以下具体协议与兼容性选择属于 Agent Self-Claimed，实现用户授权的修复目标。

|问题|处理|结果|
|---|---|---|
|R3-01 重启请求结构|OpenAPI 改为必填 `configuration` 外层对象；保持已有调用方和集成测试的请求格式，拒绝多余字段|schema 与真实 HTTP 202、幂等重试和 parentRunId 一致；裸配置400|
|R3-02 现代 MCP 列表|仅为 `2026-07-28` 的 tools/list 增加 `ttlMs: 0`、`cacheScope: private`|独立 Inspector 能正常列工具并调用；保持每次鉴权与撤销检查，旧版结果结构不添加新字段|
|R3-03 MCP 错误契约|为400明确声明 JSON-RPC error 与 HTTP Problem 两种媒体类型，各自具有严格 schema|解析错误、非法 jsonrpc、params、过期/未来协议及 query 拒绝均通过独立响应检查|
|R3-04 不存在游标|仓储在键集查询前校验锚点存在性，不存在返回 invalid_cursor/400|真实游标仍可翻页；存在的末尾锚点返回200空页；权限先于游标检查；数据库故障仍传播原依赖错误|
|R3-05 附件数组必填差异|明确省略 `taskAttachmentIds` 等同空数组，表示不选附件；与 Protobuf repeated 和现有无附件调用一致|省略与显式[]均获 schema/HTTP202；重复/不属于任务的附件仍被拒绝，未取消归属校验|

其中两项修改实现，三项修正公开契约；不是将服务错误忽略或禁用 schema 检查。没有改动 Proto、生成代码或数据库格式。游标续页新增一次索引存在性读取；当前运行记录没有删除路径，不涉及跨外部I/O的数据库事务。

现代 MCP 判据来自本机固定安装的 `@modelcontextprotocol/inspector@2.13.0` / client 2.4.0 的现代工具列表 schema，原第三轮已保留其缺字段错误；本轮用同一版本独立复验，没有依赖浮动latest。

## 回归与证据

新增持久回归：`gateway.TestMCPToolListProtocolCachePolicy`（四版本）、`challenge.TestChallengeRunCursorRequiresExistingAnchor`（真实PG/mTLS、两个服务实例）、`ops/lab/test_round3_contract.py`（3项 schema 检查）。这些回归在修复前均失败；原始失败保留于证据目录 `raw/before-*.log`，修复后通过。

最新 dev 加本轮补丁的实际结果：

- **PASS**：相关 Go 单元/契约测试59项顶层、277个含子测试PASS事件；16项 Challenge PostgreSQL 集成顶层、34个含子测试PASS事件；2项真实 Identity/MCP撤销与Challenge取消重启集成。所有实际测试0 FAIL/0 SKIP；另有4个包无测试文件，Go JSON以package skip报告，不能冒充4项测试通过。
- **PASS**：Schemathesis定向40请求、17个语义断言、0失败，覆盖五问题和相邻评论/读取/HEAD。请求含有效身份与真实种子资源，鉴权拒绝单列，不作为成功业务覆盖。
- **PASS**：独立 Inspector CLI 14次进程、24项语义断言；现代与旧版列表/调用、统计确认与永久禁投、其他用户资格、撤销后401。伴随HTTP断言19请求、0失败；CLI进程数不等于HTTP请求数。
- **PASS**：Schemathesis原生状态机5条关联、6个操作、258请求、0失败，4次完成上传、4次下载字节一致；其中404、403和409不计作成功上传。
- **PASS**：Buf lint/breaking/generate（breaking仅对7c47abb的FILE兼容）；15个生成文件与最新基线一致；`go vet ./...`；`go build -buildvcs=false ./cmd/...`；根 `npm ci --ignore-scripts --no-audit --no-fund` 后 `npm run ci`（7项Node、36项Python）；3项新增schema回归。
- **PASS**：真实外部测试夹具 `-race` 正常退出；PG数据库/角色集合恢复、15个基础设施Pod UID/容器ID/restarts/readiness未变、三节点Ready、原私有配置0600；临时凭据在结束后删除。

准确命令、cwd、时间、退出码及日志哈希见证据目录 `commands.jsonl`，详细汇总见 `REPORT.md`。Git差异检查不包含用户原改动；完整最新基线补丁保存为 `round3-fix.patch`。

运行仓库内回归的命令（从根目录/指定backend目录执行）：

```sh
# 已有第三轮 Python 工具环境，或按最新 dev 的 ops/lab/requirements-api-contract.txt 准备的环境
/path/to/acceptance-python ops/lab/test_round3_contract.py
cd backend
GOTOOLCHAIN=go1.27.1 go test -race ./internal/gateway ./internal/contract ./internal/platform ./internal/challenge/... -count=1
CHALLENGE_TEST_DATABASE_URL_FILE=/path/to/private/dedicated-test-dsn \
GOTOOLCHAIN=go1.27.1 go test -race -tags=integration ./internal/challenge -count=1 -timeout=240s
IDENTITY_TEST_DATABASE_URL_FILE=/path/to/private/dedicated-test-dsn \
GOTOOLCHAIN=go1.27.1 go test -race -tags=integration ./internal/identity \
  -run '^TestBackend(MCPProtocolAndRevocation|ChallengeCancelAndRestart)$' -count=1 -timeout=180s
```

DSN必须指向专用测试实例，不打印内容。Go archive源码没有Git元数据，本轮普通build最初因VCS stamping报错，随后显式`-buildvcs=false`成功；真实Git checkout可保留默认stamping。另一次Challenge命令误从证据目录执行，因无go.mod未运行测试，已保留该日志并在正确cwd复跑通过；它不是产品失败。生成代码校验脚本的首个相对路径错误也已纠正，没有以跳过校验代替通过。

## 限制与下一步

本轮五项差异已关闭；在上述复验范围内未发现额外产品问题。历史日志缺失不再追找，本轮使用新持久证据；没有借此声称历史完整验收已经重跑。

测试环境是HTTPS Gateway + gRPC/mTLS + 专用真实PostgreSQL + FileStorage，服务在测试进程内组装，不等价九模块kind部署。真实模型调用为0。完整第三轮54接口探索未再次全量运行；本轮是修复后的定向与相邻回归。真实Google成功登录、成功候选登记、所有字段/组合穷举、真实S3/沙箱、公网、前后端、性能容量、生产HA/灾备仍未验收。

本地变更已达到建议提交的里程碑，但未经本轮明确授权不commit/push。提交前应以最新dev接收本轮补丁，并排除原有用户改动；不要把audit旧HEAD当作当前完整dev。
