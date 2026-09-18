# Human Worth 后端设计

| 项目 | 约定 |
| --- | --- |
| 版本 | v0.2 · 2026-09-18 · 设计草案，尚未实现 |
| 人类明确要求 | 后端采用分布式 Go 架构；围绕接入与身份、内容管理、投票与统计、云端挑战、发现与看板五个模块设计核心类型、状态机和 Proto 接口 |
| 行为依据 | [PRD](prd.md) 的 S1～S6 与 [H1 人类修订](prd.md#human-revisions)：作品审核决定展示，全部作品一次展示并单选一件 |
| HTTP 兼容依据 | [OpenAPI 3.1.1 草案](../openapi.yaml)，包含 36 个 planned 操作和两个已实现的健康检查操作 |
| 方案归属 | 本文的数据字段、内部状态、RPC、存储、并发协议、超时与投递方案属于 Agent Self-Claimed，供后续实现和验证；不改写 PRD 可信场景 |
| 当前运行状态 | 线上仍为 Node.js 基础服务与 Swagger 静态文档；本文不表示 Go 服务、数据库或业务能力已经部署 |

## 1. 模块与数据归属

五个模块对应五个业务服务边界。接入模块还包含网站 HTTP 与 MCP 适配器；云端模块还包含调度器和 worker，两者可以分别扩容，不额外定义第六个业务模块。

| 模块 / 服务 | 唯一写入的数据 | 主要调用关系 | 来源 |
| --- | --- | --- | --- |
| 接入与身份 `IdentityService` | 账号、角色、会话、MCP 凭据、身份版本；各服务审计事件的查询投影 | 网关解析凭据；各业务服务验证身份断言；管理员概览聚合内容和运行摘要 | S2～S5 |
| 内容管理 `ContentService` | 任务、作品、资产、来源/授权、审核快照、举报、下架；云端作品登记记录和登记开关 | 给发现提供公开内容；给投票同步参选目录；接受云端的候选登记 | S2～S4；评论来自项目目标 |
| 投票与统计 `VotingService` | 账号×任务资格、完整候选快照、本人的当前选票、统计快照、系统结论；参选目录副本 | 核实完整参选目录；向云端与发现提供无具体票数的系统结论 | S1、S3～S6、H1 |
| 云端挑战 `ChallengeService` | 运行、配置快照、材料快照、阶段、租约、候选、额度账本、终止意图 | 读取内容授权和系统结论；调度 worker；调用内容登记与关闭登记 | S4 |
| 发现与看板 `DiscoveryService` | 公开索引、推荐/热度分值、分类结论汇总、游标快照、消费进度 | 消费公开内容/结论事件；返回内容前向内容服务核实公开性 | S1、S6 |

建议初期使用一个 PostgreSQL 实例、按服务分 schema 和数据库账号；每个服务仅写自己的 schema，跨服务通过 RPC 或事件协作。服务本地事务不跨 RPC 持锁；投票资格使用数据库持久化与行锁，不依赖进程内 mutex。该部署选择允许多个 Go 进程分布式运行，也保留以后拆分数据库的边界。[PostgreSQL 18 行锁参考](https://www.postgresql.org/docs/18/explicit-locking.html#LOCKING-ROWS)

后端仍部署在本机。前端开发经公网入口访问 API；部署后的前端使用同源 `/api`，Nginx 经现有 SSH 隧道访问本机接入服务。内部 gRPC 端口不作为浏览器或 MCP 的直接公网入口。具体 Go/gRPC 版本、服务端口、存储安装与迁移在实现时锁定，本轮不改变部署。

## 2. 共同契约

### 2.1 身份、权限与数据投影

- 人类身份一律来自已验证的会话或 MCP token；业务请求不接受 `actor_id`、`account_id`、`role` 作为授权依据。记录中的作者、管理员和历史操作者 ID 是服务端生成的数据。
- 内部连接使用经认证的服务身份，采用 TLS/mTLS；网关把身份服务签发的短期、绑定调用目的与接收服务的身份断言放入 gRPC metadata。业务服务校验服务身份、断言、账号/凭据状态与自身操作权限，不信任任意 `x-user-id` 或仅靠网关路径。协议依据见 [gRPC Authentication](https://grpc.io/docs/guides/auth/)。
- `ResolvePrincipal` 只向可信网关开放，`VerifyActor` 只向业务服务开放。网站 Cookie 写操作同时检查 CSRF 与来源；同时携带网站与 MCP 凭据时，在转成 Proto `oneof` 前返回 400。带无效凭据不能降级成匿名。
- MCP 的公开工具白名单只有 `list_tasks`、`get_task`、显式 `view_task_statistics`。MCP token 即使关联管理员账号，也不允许投稿、投票或管理运行。HTTP 草案中允许 MCP 凭据的辅助读取，不自动成为新增 MCP 工具。
- 下文 Proto 是内部接口，不能按 RPC 列表自动生成公开入口。内部 worker/事件/登记开关接口只接受指定服务身份；普通用户和管理员会话都不能直接调用这些内部方法。
- 可见任务、过审作品、运行进度与本人投稿/审核材料、管理运行分别使用不同类型。普通响应、错误、日志、缓存及审计投影不携带单题具体票数；具体统计只从 `AccessTaskStatistics` 返回。管理员也执行同一永久资格变化。

### 2.2 类型、版本和传输

- ID 是服务端分配的不透明字符串，不编码角色或投票资格。时间使用 `google.protobuf.Timestamp`，统一 UTC；版本和计数使用 `uint64`。额度使用十进制字符串和单位，Go 侧按十进制定点运算，不以浮点数累加预算。
- Proto3 枚举的 0 值均为 `UNSPECIFIED`，不能当作已批准、可投或成功；写请求拒绝缺失/未知状态。需要区分未提供与 false 的授权选项用 `optional bool`。`oneof` 仍需服务端验证恰好选择一个有效分支。
- 字段号发布后不更改、不复用；删除字段保留 `reserved` 的编号和名称，破坏兼容性的改动进入新 package。`humanworth.backend.v1` 与 `go_package` 是本文提出的命名。[Proto3 字段与演进规范](https://protobuf.dev/programming-guides/proto3/)
- Proto 消息不是 ORM 表，也不直接用 ProtoJSON 作为既有 HTTP 响应。适配器显式转换字段名、枚举、可空值、`uint64` 整数字面量和十进制额度；例如未设置 `human_vote_share` 映射为 JSON `null`，资格的永久状态映射为 `permanentlyClosed`。
- 普通列表的游标不透明，绑定调用者可见范围、排序、筛选与快照；延续 OpenAPI 的默认 20、最大 100。`next_cursor` 缺失映射为 `nextCursor: null`；空页但有游标仍可继续读取。**投票的 `GetBallot` 不使用分页或条数上限截断，必须一次返回完整过审作品集合。** 流式上传/下载保持逐块传输，文件总量与类型限制由运行配置给出。
- 大文件进入私有对象存储，数据库与 Proto 只保存元信息、受控资产 ID 和校验摘要；公开读取作品不抓取或执行外链。上传者身份、资产归属与授权均由内容服务验证。

### 2.3 幂等、事件与错误

修改内容通过 `expected_revision` 防止覆盖并发编辑；相同旧版本与相同重提内容找回原结果，不再次送审。启动/重启运行必须提供幂等键，唯一键为 `(account_id, RPC, idempotency_key)`，同时保存请求摘要与结果 ID；同键不同内容返回冲突。客户端超时不代表事务回滚，重试前先按业务键找回记录。

需要异步投影或恢复的操作，在服务本地事务中同时写业务记录和 outbox。发送者至少一次投递，接收者用 `event_id` 去重、按来源与聚合版本拒绝旧快照；不承诺网络恰好一次。初期可用数据库轮询发送，无需先引入消息中间件。写成功但投递失败必须可重发，事件消费与消费位点同事务提交。[Transactional outbox 参考](https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html)

RPC 设置明确 deadline 并沿调用链传递；普通 RPC 有界完成，云端工作通过持久化运行和租约继续。只对已定义幂等的调用做有界退避重试。gRPC 请求取消用于释放当前调用资源，不等于执行 `CancelRun` 业务命令。[Deadlines](https://grpc.io/docs/guides/deadlines/)、[Retry](https://grpc.io/docs/guides/retry/)、[Cancellation](https://grpc.io/docs/guides/cancellation/)

| gRPC 状态 | 稳定业务原因示例 | HTTP 映射 |
| --- | --- | --- |
| `INVALID_ARGUMENT` | 字段格式错误；`STATISTICS_CONFIRMATION_REQUIRED`、`INITIAL_SIDES_REQUIRED` 等语义校验失败 | 400；上述语义校验按现有契约为 422 |
| `UNAUTHENTICATED` | 无效、吊销或过期凭据 | 401 |
| `PERMISSION_DENIED` | `VOTING_PERMANENTLY_CLOSED`、`ADMIN_REQUIRED`、`MCP_WRITE_FORBIDDEN` | 403 |
| `NOT_FOUND` | 不存在，或当前主体不可见的任务/投稿 | 404 |
| `ABORTED` / `ALREADY_EXISTS` | 版本冲突、同一幂等键不同内容 | 409 |
| `FAILED_PRECONDITION` | 当前状态不允许操作、运行已终止、材料失效 | 409；缺少完整投稿材料为 422 |
| `RESOURCE_EXHAUSTED` | 限流、额度不足、文件超限 | 429；文件超限为 413 |
| `UNAVAILABLE` / `DEADLINE_EXCEEDED` | 依赖不可用或调用超时 | 503 / 504 |

通过 gRPC status details 携带 `ErrorDetail.reason`，HTTP 输出既有 `application/problem+json` 的稳定 `code`；失败细节不包含原始凭据、私有材料或统计。无过审作品时 `GetBallot` 返回正常的空 `entries`，HTTP 200；无法确认完整集合返回 503，不能伪装成空列表。[gRPC Status Codes](https://grpc.io/docs/guides/status-codes/)

## 3. 接入与身份

### 核心数据

| 类型 | 核心字段 / 唯一约束 | 说明 |
| --- | --- | --- |
| `Account` | `account_id`、显示名、`role`、状态、`auth_version` | 稳定账号 ID；角色只能由管理流程授予，具体注册与授予方式仍待定 |
| `CredentialRecord` | `credential_id`、`account_id`、用途、摘要、状态、过期时间 | 会话和 MCP token 绑定同一账号；凭据摘要不作为业务 API 输出 |
| `Principal` | 账号、角色、`client_kind`、凭据 ID、身份版本 | 已验证的内部身份；MCP 用途限制优先于账号的管理员角色 |
| `ActorAssertion` | 不透明签名值 | 绑定主体、凭据版本、接收方、请求用途与短有效期；各服务用 `VerifyActor` 检查当前有效性 |
| `AuditEvent` | 事件 ID、原操作主体、服务、动作、目标、结果、原因、时间 | 原始审计在执行服务持久化，身份模块维护统一查询投影；不带单题票数 |

### 状态机

| 对象 | 转换 | 条件与副作用 |
| --- | --- | --- |
| 账号 | `ACTIVE → DISABLED → ACTIVE` | 由授权管理流程改变；每次增加身份版本，不能因此删除或重建投票资格记录 |
| 凭据 | `ACTIVE → REVOKED` | 吊销后认证失败；不可通过换 token 绕过账号×任务禁投 |
| 凭据 | `ACTIVE → EXPIRED` | 服务端时间达到过期时间即失效，无需等待定时任务改状态 |
| 凭据轮换 | 旧凭据终止，创建新的 `ACTIVE` 凭据 | 保留相同 `account_id`，新 ID 不继承“新账号”资格 |

账号禁用、凭据生命周期和断言机制是实现提案。本文只定义认证边界，登录提供方、注册方式和 token 签发/撤销的公开 API 仍留待实现时补全，不虚构现有 OpenAPI 已提供这些入口。

`GetAdminDashboard` 聚合内容审核/举报数量与云端运行/额度摘要，不访问具体票数；任一必要服务不可用时返回错误，不把失败伪装成零。`ListAuditEvents` 支持有界游标读取，投影允许延迟，执行服务中的原始审计是判断操作是否发生的依据。

## 4. 内容管理

### 核心数据

| 类型 | 核心字段 / 约束 | 归属与公开边界 |
| --- | --- | --- |
| `Task` / `TaskSubmission` | ID、作者、版本、标题/摘要/正文/分类、发布状态、初始作品、审核 ID | 草稿及审核信息仅本人和管理员可见；发布版本和审核快照关联 |
| `Entry` / `EntrySubmission` | ID、task ID、作者、版本、审核状态、human/agent、initial/additional/cloud、交付物、来源、云端授权、配置 | 任务可见且审核通过即允许展示；没有公开/私有状态。agent 必有配置，human 不携带 agent 配置 |
| `Asset` / `Artifact` | 资产 ID、所有者、文件名、媒体类型、大小、摘要、可用性；交付物为文件 ID 或 HTTP(S) 链接 | 被可见任务中的过审作品引用即可对外下载；待审材料只供本人管理及管理员审核，云端用途单独检查授权 |
| `Review` | ID、目标、投稿快照版本、状态、决定、管理员、原因与时间 | `(目标, 投稿版本)` 唯一；新任务审核覆盖任务与双方初始作品 |
| `Report` | ID、举报人、目标、原因、状态、处理人/时间 | 目标为 task/entry/comment；处理结果不可覆盖历史操作 |
| `RunRegistrationGate` | `run_id`、task ID、`OPEN/SEALED`、版本、运行配置摘要、材料/授权引用 | 仅云端服务可操作；同一 run 一旦封闭不能重开；登记时据引用检查当前授权 |
| `CloudRegistration` | `(run_id, candidate_id)` 唯一、候选摘要、entry ID、review ID | 唯一键与作品/审核在同一事务创建；重试读当前审核状态 |

### 发布、审核与举报状态机

| 对象 / 当前状态 | 命令 | 下一状态 | 必须满足的条件 / 原子效果 |
| --- | --- | --- | --- |
| 新任务不存在 | 创建草稿 | `DRAFT` | 可缺一方或双方；不公开、不启动云端 |
| 任务 `DRAFT` / `REJECTED` | 替换内容 | `DRAFT` | 仅作者，版本匹配；原审核历史保留 |
| 任务 `DRAFT` | 请求审核 | `PENDING_REVIEW` | 至少一件 human 和一件 agent，所有来源/授权/资产有效；任务与初始作品整体送审 |
| 任务 `PENDING_REVIEW` | 通过审核 | 任务 `PUBLISHED`；初始作品 `APPROVED` | 管理员审核同一快照；同一内容事务使任务可见、全部初始作品通过审核并允许展示 |
| 任务 `PENDING_REVIEW` | 驳回 | `REJECTED` | 必填原因，任务和初始作品整体反馈，不部分公开 |
| 公开任务收到追加 / 云端候选 | 创建独立作品审核 | 任务仍 `PUBLISHED`；新作品 `PENDING_REVIEW` | 不检查任务作者同意；不检查投稿者是否永久禁投 |
| 独立作品 `PENDING_REVIEW` | 通过 / 驳回 | `APPROVED` / `REJECTED` | 任务仍可见才可通过；仅修改新作品，不重审任务，不需要单独公开授权 |
| 追加作品 `REJECTED` | 修改并重提 | `PENDING_REVIEW` | 仅投稿者、版本匹配、任务仍公开；同旧版本同内容重试找回已有结果 |
| 任务 `PUBLISHED` | 下架 | `TAKEN_DOWN` | 先关闭任务参选资格再提交下架；其所有作品对外不可见，作品自身审核历史保留 |
| 作品 `APPROVED` | 撤销审核通过（下架作品） | `APPROVAL_REVOKED` | 先关闭该作品参选资格；提交后停止作品展示与关联文件对外读取，不增加公开/私有状态 |
| 评论不存在 / `PUBLISHED` | 创建 / 下架 | `PUBLISHED` / `TAKEN_DOWN` | 纯文本平铺评论；仅在公开任务下创建，管理下架审计留存 |
| 审核 `PENDING` | approve / reject | `APPROVED` / `REJECTED` | 同决定重试返回原记录，不同决定冲突；不能审核过期投稿快照 |
| 举报 `OPEN` | dismiss / take_down | `DISMISSED` / `ACTIONED` | 下架实际完成后才标记 actioned；同结果重试可恢复 |

`TAKEN_DOWN` / `APPROVAL_REVOKED` 在本版中没有恢复转换；恢复与云端授权撤回的完整策略仍待定。初始作品随任务修改；现有 HTTP 只允许被驳回的独立追加作品重提，不把该入口自动用于云端候选。作品审核状态为 `DRAFT / PENDING_REVIEW / APPROVED / REJECTED / APPROVAL_REVOKED`；客户端不能直接设置状态。只有 `APPROVED` 允许对外展示，没有第二个发布状态或 `publish` 授权字段。

云端作品由已验证的云端服务映射到固定系统投稿账号，内容服务设置作者与 `origin=cloud`；触发管理员记录在运行和审计中。普通投稿不能自报 cloud 来源或系统作者。该署名选择属于本设计提案，署名不授予云端控制权。

资产内部状态为 `UPLOADING → AVAILABLE / REJECTED`，失效或封禁使 `AVAILABLE → BLOCKED`；完成持久化、校验总大小与摘要后才返回可引用 ID。普通读取检查“任务可见、关联作品审核通过、资产可用”，不检查独立公开授权；未过审材料仅网站上传者本人和平台管理员可在管理/审核中读取，MCP token 不获得此权限。云端使用另外检查 `cloud_use`，在启动、重启、阶段执行和登记时重新验证；不把早期授权快照当成永久许可。资产封禁若使作品不再允许展示，须同步收紧参选目录，不能只令下载报错而保留可投候选。

### 下架与投票的一致性

内容服务维护单调递增的 `catalog_revision`，不同于单个草稿的编辑版本。目录同步携带该任务完整的可参选作品集合；投票服务在本地事务中按版本更新目录，与投票提交锁定相同的任务/作品可用性记录。

新内容先提交任务可见/作品过审状态，再可靠投递完整目录。`GetBallot` 及新的选票写入同时调用内容服务 `GetVotingCatalog`，将权威目录同步到投票服务后再读取或校验，不能靠滞后的事件投影返回部分作品；无法核实时返回 503。

任务下架或作品撤销通过采用收紧顺序：内容服务持久化意图与待发送目录，冻结该任务的并发目录修改；投票服务先确认关闭相应任务或作品，再由内容服务提交 `TAKEN_DOWN` / `APPROVAL_REVOKED` 并通知发现模块。冻结期间 `GetVotingCatalog` 返回暂时不可用，不把半完成的目录当成完整候选。RPC 失败时保留意图并重试，不提前报告完成；删除/关闭快照保留版本墓碑，旧开放消息不能重新启用。

## 5. 投票与统计

### 核心数据

| 类型 | 核心字段 / 唯一约束 | 规则 |
| --- | --- | --- |
| `VotingEligibility` | `(account_id, task_id)` 唯一、永久状态、`closed_at` | 永久状态与其他临时限制分开；候选列表变化不能改变资格键 |
| `Ballot` | ID、账号、任务、目录版本、全部过审作品 ID 集合及展示内容 | 服务端生成并绑定主体；一次给出全部候选，用户单选一件，不含其他人的选择和计票 |
| `VoteRecord` | `(account_id, task_id)` 唯一、选中的 entry ID、revision、最近成功请求摘要、创建/更新时间 | 仅一份当前选择；未永久禁投前可改选，替换而不增票；修订历史留作审计 |
| `TaskStatistics` | 任务、目录版本、逐作品票数与占比、总票/human/agent、两方占比、结论、计算时间 | 单选没有 tie；仅有效真人当前选择；云端对抗结果不进入这些计数 |
| `TaskConclusion` | 任务、结论、算法版本、结果版本、计算时间 | 由系统计算供运行分流；不暴露票数，无管理员强制改结论 RPC |
| `VotingCatalog` | task ID、目录版本、任务可见性、全部过审作品及展示字段 | 内容服务是唯一来源；成功返回必须是同一版本的完整集合，不能截断、抽样或遗漏新过审作品 |

### 状态机与并发

永久资格只有 `OPEN → CLOSED_BY_STATISTICS`，无反向转换。`can_vote` 是永久状态、账号限制、内容可用性等条件的派生值，临时不能投不等于永久禁投。

| 命令 | 原子边界与返回条件 |
| --- | --- |
| `GetVotingEligibility` | 只读，不返回计数；资格行不存在等价于未永久关闭，不能因此绕过首次并发写入的唯一约束 |
| `GetBallot` | 核实权威完整目录与资格，绑定账号、任务、目录版本及全部候选；返回作品和本人当前选票，零候选返回空 entries；不提交票或修改永久资格 |
| `CastVote` | 锁定任务目录及账号×任务资格，检查永久状态、候选绑定、完整目录版本和 expected_vote_revision；首次创建或替换该账号的单个作品选择，不返回计票 |
| `AccessTaskStatistics` | 必须显式传 true；验证身份及任务可见，锁同一资格行并写永久关闭；提交后才能读统计、序列化或发送响应 |

投票、改选和统计访问都先用唯一键创建缺失资格行（冲突则复用），再 `SELECT ... FOR UPDATE`。仅对“查到的行”加锁会遗漏首次投票与首次查看统计的竞争。所有代码使用相同锁顺序：任务目录、账号×任务资格、该账号当前选票；目录更新与投票读取使用相容的共享/排他锁。服务本地事务不跨 RPC 持锁。

`GetBallot` 的候选集合以内容服务读取时的目录版本为快照；响应必须包含该版本的全部作品。`CastVote` 在本地事务前重新核实权威目录，新写入要求 ballot 的版本等于当前目录、所选作品仍在集合中。新增过审、撤销通过或并发目录更新使版本不一致时返回 409，客户端重新获取完整列表；依赖失败返回 503。展示顺序可与选票无关，但不能按隐藏票数排序或固定只取人类/agent 各一件。

当前选票与改选策略是 **Agent Self-Claimed**，参考 [Discourse 单选与改选流程](references.md#task-wide-voting)，不是 H1 新增的人类决定。首次 `expected_vote_revision=0`；成功写入 revision 加一并记录请求摘要。仅当最近成功摘要相同且当前 revision 恰为 expected+1 时返回幂等结果；其他旧版本返回 409，迟到请求不能覆盖后来的选择。已经提交的同请求重放不产生新选择，可找回当前票；新写入必须重新通过目录校验。每次都先检查永久资格，不能靠重试绕过禁投。

- 选票/改选先提交：保留该次当前选择，随后关闭资格；关闭先提交：新增、改选及旧票重放均返回 403。不能先命中幂等缓存就跳过资格检查。
- 关闭提交失败：不读取或返回统计。关闭提交成功但统计读取/响应失败：永久关闭仍生效；重试保留首次 `closed_at`，不恢复资格、不新增选票。
- 确认 false 或缺失返回 422，无副作用；确认不能由预取、自动重试一个普通 GET 或页面加载自动补上。
- 统计在关闭提交后通过 `GetVotingCatalog` 核实完整目录，再从同一目录与计票快照生成响应；目录冻结或不可用返回 503，已提交的禁投仍然生效。只计当前仍过审作品的有效真人当前选择；撤销通过作品的历史选择保留记录但不进入当前计票。`entry_votes` 含全部过审作品（含零票），`total = sum(entry votes) = human + agent`；各作品/两方占比为对应票数除以 total，零总票时占比缺失、HTTP 映射 null。这是单选得票占比，不解释成成对对抗胜率。
- `UNDETERMINED / HUMAN_LEADING / AGENT_LEADING` 是独立结论状态，可随有效真人反馈及算法版本重算；最低票数、领先/翻转规则仍待定。算法未配置时不擅自用简单多数伪造“已领先”。

后台计算可以读取内部选票库，但没有供网站/MCP/管理员直接获取计数的 `GetStatistics` RPC。内部 `GetTaskConclusion` 和发现事件只含结论；不能把内部计算权限变成人类查看统计的旁路。

## 6. 云端挑战

### 核心数据

| 类型 | 核心字段 / 约束 | 说明 |
| --- | --- | --- |
| `ChallengeRun` | ID、task ID、parent run、管理员、内部版本、status/stage/round、启动时结论、配置、消耗、候选、创建/结束时间 | 重启新建 ID；运行配置为不可变快照，状态、发布审核和胜负分开 |
| `RunConfiguration` | generator/voter 配置、轮数、十进制预算及单位 | 模型、prompt、harness、skills、非敏感参数；无默认隐性预算，密钥由执行环境注入 |
| `MaterialManifest` | 任务/作品/授权版本、校验摘要、资产引用 | worker 只获取本运行获准使用的材料；匿名对抗另投影去掉真实来源和双方标签 |
| `WorkLease` | run、stage、round、worker 身份、单调 `lease_epoch`、过期时间 | worker 续租和提交必须匹配租约所有者、阶段与轮次；租约过期再领取增加 epoch |
| `Candidate` | `(run_id,candidate_id)`、不可变产物摘要、作品内容、登记状态 | 仅已持久化的最终候选可登记；公开管理接口只接受 ID，不接受新结果正文 |
| `BudgetLedger` | run、execution ID、reservation/settlement、amount/unit | 先预留后执行、幂等结算；取消不退回已经发生的消耗，不同单位不相加 |
| `TerminationIntent` | run、目标终态、原因、关闭登记确认 | 内部恢复记录；阻止新推进与新登记请求，不作为新增 HTTP 状态枚举 |

### 运行与阶段状态机

| 转换 | 条件 / 效果 |
| --- | --- |
| 不存在 → `QUEUED` | 网站管理员启动；任务公开、配置/轮数/预算有效、材料及额度复核；保存幂等结果 |
| `QUEUED → ACTIVE` | 调度领取，重验执行条件，建立本 run 的登记开关；开关确认前不交付可执行工作 |
| `ACTIVE → COMPLETED` | 已完成允许的阶段且所有最终候选已登记，关闭登记后提交终态；经验提炼可以无候选，不代表作品审核通过或真人获胜 |
| `QUEUED/ACTIVE → FAILED` | 失败原因持久化，关闭登记后提交终态；重试当前 RPC 不自动产生新运行 |
| `QUEUED/ACTIVE → CANCELLED` | 终止意图与关闭登记完成后提交取消；随后停止外部模型调用；重复取消返回原记录 |
| 任意终态 → 新 `QUEUED` 运行 | 仅管理员 Restart；旧运行不修改，新 ID、parent ID、显式新配置、重新检查权限/材料/额度 |

| 启动时系统结论 | 正常阶段顺序 |
| --- | --- |
| `UNDETERMINED` | `QUEUED → GENERATING → REGISTERING → FINISHED`；只追加生成，不进入投票者验证或对抗 |
| `AGENT_LEADING` | `QUEUED → LEARNING → FINISHED`；本版选择可选经验提炼路径，无最终候选时不登记虚构作品 |
| `HUMAN_LEADING` | `QUEUED → PREPARING_MATERIALS → VALIDATING_VOTER → GENERATING → ANONYMOUS_BATTLE`；通过有效验证才进入对抗；按轮数/预算继续生成或 `REGISTERING → FINISHED` |

运行分支采用启动时的系统结论快照，不随中途结论变化任意跳阶段。验证门槛、对抗评判和终止条件由版本化执行策略补全，不能在未定义时默认“验证通过”。所有终态的 stage 为 `FINISHED`；失败或取消可从任一非终态阶段进入，但必须经过下述关闭协议。

### 候选登记与终止的跨服务顺序

不能采用“云端查询 active → 发一个内容创建 RPC”的检查后写入流程：取消可能发生在两步之间。本方案把**新作品创建与登记开关检查放在同一个内容事务**，并把**关闭登记作为提交运行终态的前置条件**：

1. 云端将 run 设为 active 后，通过可靠命令打开内容侧登记开关。`OpenRunRegistration` 幂等，已有 `SEALED` 墓碑时永远不能重开；取消发生在打开之前，也必须能先创建 sealed 墓碑。
2. `RegisterCandidate` 首先查 `(run_id,candidate_id)` 的已有登记。找到时返回原作品及当前审核状态，绝不重置为待审；未找到时才检查 active、无终止意图、材料有效，并发送持久化候选内容。
3. 内容侧 `RegisterCloudEntry` 锁定登记开关，先找回同键结果并校验候选摘要；无结果时要求开关 open、任务公开、资产和授权有效，原子创建 `PENDING_REVIEW` 作品、审核及登记记录。只有云端服务可以调用，worker 不能绕过运行校验直接调用。
4. 取消/失败/完成先在云端记录终止意图，冻结新阶段推进和新登记请求，再可靠调用内容侧 `SealRunRegistration`。内容侧封闭与登记串行化：封闭前完成的登记有效；封闭后不创建新作品。之前已经在途的请求也遵守这条锁边界。
5. 确认封闭后，按候选键找回内容侧的真实登记结果，修复“作品已创建但登记响应丢失”的本地缓存，再由云端本地事务提交 `CANCELLED/FAILED/COMPLETED`、`ended_at` 和审计。取消状态提交后才发出停止外部执行的动作。这样保持 HTTP 草案“先提交 cancelled，再停止外部调用”，并保证终态后无新作品登记。
6. 封闭或响应丢失时按 run ID 重试，云端未确认封闭前不宣称已取消。内部终止意图允许后台恢复；API deadline 到期返回超时/不可用，客户端查询运行状态。阶段和登记持续冻结，不回滚封闭开关。

`COMPLETED` 要求最终候选全部登记；经验提炼无候选时集合可为空。取消/失败后，只有在封闭后确认内容侧不存在登记的候选才能标记 `DISCARDED`，不能仅因本地未收到登记响应就丢弃。`REGISTERED` 保持稳定，其作品由内容审核状态机继续处理。终态重试登记只允许找回已存在记录，无记录则 409。不存在用补偿把迟到作品“先发布再删除”的窗口。

worker 提交先校验调用身份和 work 归属，再查 `(work_id, lease_epoch)` 的不可变回执；已接受过的同摘要请求只找回回执，不能再次推进，不同摘要冲突。没有回执的新结果，必须在同一云端事务验证 active、无终止意图、epoch、阶段、轮次和未过期租约；过期 worker 不得推进阶段或形成候选。续租和提交读取服务端租约时间，不信任请求中的 `expires_at`；旧 epoch 不能借续租复活。租约只能防止旧结果生效，不能保证外部模型恰好调用一次；供应方支持时使用 execution ID 幂等，结果未知且可能重复计费时先核对账本，不盲目重发。

`PREPARING_MATERIALS` 和 `REGISTERING` 由调度器调用受信内容服务完成；模型 worker 仅领取生成、学习、投票者验证或匿名对抗工作。worker 获得当前阶段的执行配置和匿名材料 ID，通过绑定租约的 `ReadWorkMaterial` 读取经过清理的材料，不获得真实 entry/asset ID、原始文件名、来源标签或完整运行配置。云端保留匿名 ID 与真实材料的映射；运行容器的网络访问限制也要防止模型另行查询平台公开接口还原身份。生成结果是候选提案，只有调度器按版本化策略选中并冻结后才成为可登记的最终候选。

## 7. 发现与看板

### 核心数据与状态

| 类型 | 字段 / 状态 | 规则 |
| --- | --- | --- |
| `TaskProjection` | task ID、公开文本、分类、热度、推荐分、内容版本、结论版本 | 内容版本与结论版本分别单调递增，不能拿不同服务的版本数直接比较 |
| `ListSnapshot` | 快照 ID、排序/筛选摘要、稳定顺序、过期时间 | 热度降序，同分 task ID 升序；推荐与热度分别维护；过期游标返回明确错误，不能静默换快照 |
| `Board` | 平台及分类的任务数、三种结论数量、更新时间 | 不含单题票数、得票占比或可还原它们的聚合；结论数量之和等于该组任务数 |
| `InboxRecord` | event ID、producer、聚合 ID、源版本、处理时间 | 事件与投影更新同事务；重复忽略，旧快照不覆盖新状态 |

投影状态为 `ABSENT → INDEXED → REMOVED`；重建过程为 `READY → REBUILDING → READY`，使用独立构建版本与持久化消费位点，回填和追赶完成后原子切换。重建失败继续使用原可用投影或返回不可用，不把半份索引当成完整结果。

投影可以延迟，权限检查不能依赖延迟：列表和详情返回前调用内容服务的公开读取/批量过滤，已经下架的任务直接剔除；缓存不能成为旁路。列表补充扫描受单次预算限制，可能返回空页及后续游标；不得倒回游标重复任务。内容服务不可用时不返回未经确认的旧内容。

`GetBoard` 仅聚合当前公开任务集合，必要时以内容服务可见性结果过滤其投影；不支持按任意账号、作品集合或单任务自定义分组。推荐、热度和领先算法仍是待定策略，Proto 只规定输入输出及版本，不固化权重。热度不得直接或可逆地编码真人票数、得票占比。

## 8. HTTP / MCP 与 RPC 对照

下表列出全部 36 个 planned HTTP operationId。两项现有健康检查继续作为运维接口，不属于新的业务 RPC；实现 Go 服务时另采用标准 gRPC health 协议。仅下列显式适配关系进入公开路由。

| OpenAPI operationId | RPC / 组合 |
| --- | --- |
| `getCurrentSession` | Identity.GetCurrentSession |
| `listTasks`、`getTask`、`getPublicBoard` | Discovery.ListTasks、GetTask、GetBoard |
| `createTaskDraft`、`replaceTaskDraft`、`requestTaskReview` | Content.CreateTaskDraft、ReplaceTaskDraft、RequestTaskReview |
| `submitAdditionalEntry`、`resubmitRejectedEntry` | Content.SubmitAdditionalEntry、ResubmitRejectedEntry |
| `listMySubmissions`、`getMyTaskSubmission`、`getMyEntrySubmission` | Content.ListMySubmissions、GetMyTaskSubmission、GetMyEntrySubmission |
| `listApprovedEntries`、`uploadAsset`、`downloadAsset` | Content.ListApprovedEntries、UploadAsset、DownloadAsset |
| `listComments`、`createComment`、`createReport` | Content.ListComments、CreateComment、CreateReport |
| `listReviews`、`decideReview`、`listReports`、`resolveReport`、`takeDownContent` | Content.ListReviews、DecideReview、ListReports、ResolveReport、TakeDownContent |
| `getVotingEligibility`、`getBallot`、`castVote`、`accessTaskStatistics` | Voting.GetVotingEligibility、GetBallot、CastVote、AccessTaskStatistics；候选读取/新投票先调用 Content.GetVotingCatalog 核实完整目录 |
| `getAdminDashboard`、`listAuditEvents` | Identity.GetAdminDashboard（聚合 Content/Challenge 摘要）、ListAuditEvents |
| `listChallengeRuns`、`startChallengeRun`、`getChallengeRun` | Challenge.ListRuns、StartRun、GetRun |
| `cancelChallengeRun`、`restartChallengeRun`、`registerCandidate`、`listPublicChallengeProgress` | Challenge.CancelRun、RestartRun、RegisterCandidate、ListPublicProgress |

MCP `list_tasks`、`get_task` 对应前两项发现读取；`view_task_statistics` 只调用 Voting.AccessTaskStatistics，先明确披露并确认永久禁投。Proto 内部方法不扩大这个白名单。

## 9. Proto 核心契约

下列单一 `proto` 代码块可整体提取为 `humanworth_backend_v1.proto` 编译，所有消息均在本文件或 Google 标准类型中定义。数据库私有字段（凭据摘要、锁、outbox 重试次数等）留在实现模型中，不为了表结构而加入网络消息。注释的内部调用限制需要运行时拦截器和业务验证落实；Proto 编译通过不等于权限已实现。

<!-- backend-proto:start -->
```proto
syntax = "proto3";
package humanworth.backend.v1;
option go_package = "github.com/KDZZZZZZ/human-worth/gen/backend/v1;backendv1";

import "google/protobuf/empty.proto";
import "google/protobuf/timestamp.proto";
import "google/protobuf/struct.proto";

// ---------- 共同类型；所有 actor 均由受信 metadata/凭据解析，不从业务 body 取 ----------
message PageRequest { uint32 limit = 1; optional string cursor = 2; }
message PageInfo { optional string next_cursor = 1; }
message TaskRef { string task_id = 1; }
message EntryRef { string entry_id = 1; }
message RunRef { string run_id = 1; }
message AssetRef { string asset_id = 1; }
message CandidateRef { string run_id = 1; string candidate_id = 2; }
message Quantity { string amount = 1; string unit = 2; } // 非负十进制；预算必须 > 0
message ErrorDetail { string reason = 1; repeated string fields = 2; }
message Author { string account_id = 1; string display_name = 2; }

enum Role { ROLE_UNSPECIFIED = 0; ROLE_USER = 1; ROLE_ADMIN = 2; }
enum ClientKind {
  CLIENT_KIND_UNSPECIFIED = 0; CLIENT_KIND_ANONYMOUS = 1;
  CLIENT_KIND_WEB = 2; CLIENT_KIND_MCP = 3; CLIENT_KIND_SERVICE = 4;
}
enum AccountState { ACCOUNT_STATE_UNSPECIFIED = 0; ACCOUNT_STATE_ACTIVE = 1; ACCOUNT_STATE_DISABLED = 2; }
enum CredentialState {
  CREDENTIAL_STATE_UNSPECIFIED = 0; CREDENTIAL_STATE_ACTIVE = 1;
  CREDENTIAL_STATE_REVOKED = 2; CREDENTIAL_STATE_EXPIRED = 3;
}
enum PublicationState {
  PUBLICATION_STATE_UNSPECIFIED = 0; PUBLICATION_STATE_DRAFT = 1;
  PUBLICATION_STATE_PENDING_REVIEW = 2; PUBLICATION_STATE_PUBLISHED = 3;
  PUBLICATION_STATE_REJECTED = 4; PUBLICATION_STATE_TAKEN_DOWN = 5;
}
enum EntryReviewState {
  ENTRY_REVIEW_STATE_UNSPECIFIED = 0; ENTRY_REVIEW_STATE_DRAFT = 1;
  ENTRY_REVIEW_STATE_PENDING_REVIEW = 2; ENTRY_REVIEW_STATE_APPROVED = 3;
  ENTRY_REVIEW_STATE_REJECTED = 4; ENTRY_REVIEW_STATE_APPROVAL_REVOKED = 5;
}
enum Side { SIDE_UNSPECIFIED = 0; SIDE_HUMAN = 1; SIDE_AGENT = 2; }
enum EntryOrigin { ENTRY_ORIGIN_UNSPECIFIED = 0; ENTRY_ORIGIN_INITIAL = 1; ENTRY_ORIGIN_ADDITIONAL = 2; ENTRY_ORIGIN_CLOUD = 3; }
enum Conclusion { CONCLUSION_UNSPECIFIED = 0; CONCLUSION_UNDETERMINED = 1; CONCLUSION_HUMAN_LEADING = 2; CONCLUSION_AGENT_LEADING = 3; }
enum TargetKind { TARGET_KIND_UNSPECIFIED = 0; TARGET_KIND_TASK = 1; TARGET_KIND_ENTRY = 2; TARGET_KIND_COMMENT = 3; }
message Target { TargetKind kind = 1; string id = 2; }
enum AuditOutcome { AUDIT_OUTCOME_UNSPECIFIED = 0; AUDIT_OUTCOME_SUCCEEDED = 1; AUDIT_OUTCOME_REJECTED = 2; AUDIT_OUTCOME_FAILED = 3; }
message AuditEvent {
  string event_id = 1;
  string actor_id = 2; // 执行服务从已验证身份记录，不用调用者自报值授权
  string producer = 3;
  string action = 4;
  string target_id = 5;
  AuditOutcome outcome = 6;
  optional string reason = 7;
  google.protobuf.Timestamp created_at = 8;
}

// ---------- 1. 接入与身份 ----------
service IdentityService {
  rpc ResolvePrincipal(ResolvePrincipalRequest) returns (Authentication); // 仅网关
  rpc VerifyActor(VerifyActorRequest) returns (Principal); // 仅业务服务；验证接收方/用途及当前凭据状态
  rpc GetCurrentSession(google.protobuf.Empty) returns (CurrentSession); // 网站本人
  rpc GetAdminDashboard(google.protobuf.Empty) returns (AdminDashboard); // 网站管理员
  rpc ListAuditEvents(PageRequest) returns (AuditPage); // 网站管理员
  rpc IngestAuditEvent(AuditEvent) returns (google.protobuf.Empty); // 仅执行服务，去重
}
message Account {
  string account_id = 1; string display_name = 2; Role role = 3;
  AccountState state = 4; uint64 auth_version = 5;
}
message Principal {
  string account_id = 1; Role role = 2; ClientKind client_kind = 3;
  string credential_id = 4; uint64 auth_version = 5;
}
message ActorAssertion { string value = 1; } // 不进入客户端响应、日志或事件
message VerifyActorRequest { ActorAssertion actor = 1; string rpc_method = 2; } // audience 从调用服务身份取得
message WebCredentialProof {
  string session_cookie = 1; optional string csrf_token = 2;
  string http_method = 3; optional string origin = 4;
}
message ResolvePrincipalRequest {
  oneof credential { WebCredentialProof web = 1; string mcp_token = 2; }
  bool allow_anonymous = 3; // 网关按路由设置，不能由客户端控制
  string audience = 4;
  string rpc_method = 5;
}
message Authentication { Principal principal = 1; ActorAssertion actor = 2; }
message CurrentSession { Author account = 1; Role role = 2; string csrf_token = 3; }
message AuditPage { repeated AuditEvent items = 1; PageInfo page = 2; }
message ModerationSummary { uint64 pending_task_reviews = 1; uint64 pending_entry_reviews = 2; uint64 open_reports = 3; }
message QuotaBalance { Quantity remaining = 1; Quantity used = 2; }
message RunSummary { uint64 active_runs = 1; uint64 failed_runs = 2; repeated QuotaBalance quota = 3; }
message AdminDashboard { ModerationSummary moderation = 1; RunSummary runs = 2; }

// ---------- 2. 内容；管理命令必须为网站管理员，投稿命令必须为网站用户 ----------
service ContentService {
  rpc CreateTaskDraft(TaskDraft) returns (TaskSubmission);
  rpc ReplaceTaskDraft(ReplaceTaskDraftRequest) returns (TaskSubmission);
  rpc RequestTaskReview(RequestTaskReviewRequest) returns (TaskSubmission);
  rpc SubmitAdditionalEntry(SubmitAdditionalEntryRequest) returns (EntrySubmission);
  rpc ResubmitRejectedEntry(ResubmitRejectedEntryRequest) returns (EntrySubmission);
  rpc ListMySubmissions(PageRequest) returns (SubmissionPage);
  rpc GetMyTaskSubmission(TaskRef) returns (TaskSubmission);
  rpc GetMyEntrySubmission(EntryRef) returns (EntrySubmission);
  rpc ListApprovedEntries(TaskPageRequest) returns (EntryPage);
  rpc UploadAsset(stream UploadAssetChunk) returns (Asset);
  rpc DownloadAsset(AssetRef) returns (stream DownloadAssetChunk);
  rpc ListComments(TaskPageRequest) returns (CommentPage);
  rpc CreateComment(CreateCommentRequest) returns (Comment);
  rpc CreateReport(ModerationInput) returns (Report);
  rpc ListReviews(ListReviewsRequest) returns (ReviewPage);
  rpc DecideReview(DecideReviewRequest) returns (Review);
  rpc ListReports(ListReportsRequest) returns (ReportPage);
  rpc ResolveReport(ResolveReportRequest) returns (Report);
  rpc TakeDownContent(ModerationInput) returns (google.protobuf.Empty);

  // 以下为内部协作接口，不自动暴露成 HTTP/MCP 工具。
  rpc GetPublicTask(TaskRef) returns (PublicTaskData);
  rpc BatchGetPublicTasks(BatchGetPublicTasksRequest) returns (PublicTaskBatch);
  rpc GetVotingCatalog(TaskRef) returns (VotingCatalog); // 仅投票服务；完整权威目录，收紧冻结期间不可用
  rpc GetMaterialManifest(TaskRef) returns (MaterialManifest); // 仅云端；检查 cloudUse
  rpc GetModerationSummary(google.protobuf.Empty) returns (ModerationSummary); // 仅接入模块管理聚合
  rpc OpenRunRegistration(OpenRunRegistrationRequest) returns (RunRegistrationGate); // 仅云端
  rpc SealRunRegistration(RunRef) returns (RunRegistrationGate); // 仅云端；不存在也写 SEALED 墓碑
  rpc GetCloudRegistration(CandidateRef) returns (CloudRegistrationResult); // 仅云端，查无结果为 NOT_FOUND
  rpc RegisterCloudEntry(RegisterCloudEntryRequest) returns (CloudRegistrationResult); // 仅云端
}
message AgentConfiguration {
  string model = 1;
  optional string prompt = 2;
  optional string harness = 3;
  repeated string skills = 4;
  google.protobuf.Struct parameters = 5; // 仅支持模型的非敏感参数；不是任意命令
}
message Permissions {
  reserved 1;
  reserved "publish"; // 展示只由审核结果决定，移除旧草案字段
  optional bool cloud_use = 2; // 必须显式提供；允许 false
  optional string statement = 3;
}
message Artifact { oneof value { string asset_id = 1; string url = 2; } }
message EntryInput {
  Side side = 1;
  string title = 2;
  optional string description = 3;
  string source = 4;
  repeated Artifact artifacts = 5;
  Permissions permissions = 6;
  AgentConfiguration agent_configuration = 7; // agent 必须有；human 禁止
}
message TaskDraft {
  string title = 1; string summary = 2; string description = 3;
  optional string category = 4; repeated EntryInput entries = 5;
}
message TaskSubmission {
  string task_id = 1; string author_id = 2; uint64 revision = 3;
  PublicationState state = 4; TaskDraft content = 5;
  optional string review_id = 6; optional string rejection_reason = 7;
}
message EntrySubmission {
  string entry_id = 1; string task_id = 2; string author_id = 3;
  uint64 revision = 4; EntryOrigin origin = 5; EntryReviewState state = 6;
  EntryInput content = 7; optional string review_id = 8; optional string rejection_reason = 9;
}
message Submission { oneof value { TaskSubmission task = 1; EntrySubmission entry = 2; } }
message SubmissionPage { repeated Submission items = 1; PageInfo page = 2; }
message ReplaceTaskDraftRequest { string task_id = 1; uint64 expected_revision = 2; TaskDraft content = 3; }
message RequestTaskReviewRequest { string task_id = 1; uint64 expected_revision = 2; }
message SubmitAdditionalEntryRequest { string task_id = 1; EntryInput content = 2; }
message ResubmitRejectedEntryRequest { string entry_id = 1; uint64 expected_revision = 2; EntryInput content = 3; }
message TaskPageRequest { string task_id = 1; PageRequest page = 2; }

message PublicTaskData {
  string task_id = 1; string title = 2; string summary = 3; string description = 4;
  optional string category = 5; Author author = 6; google.protobuf.Timestamp published_at = 7;
}
message ApprovedEntry {
  string entry_id = 1; string task_id = 2; Side side = 3;
  string title = 4; string description = 5; string source = 6;
  repeated Artifact artifacts = 7; Author author = 8; google.protobuf.Timestamp approved_at = 9;
}
message EntryPage { repeated ApprovedEntry items = 1; PageInfo page = 2; }
message BatchGetPublicTasksRequest { repeated string task_ids = 1; } // 有界，省略不可见项
message PublicTaskBatch { repeated PublicTaskData items = 1; }
message Asset {
  string asset_id = 1; string filename = 2; string media_type = 3;
  uint64 size_bytes = 4; string sha256 = 5;
}
message AssetUploadMetadata { string filename = 1; string media_type = 2; uint64 declared_bytes = 3; }
message UploadAssetChunk { oneof part { AssetUploadMetadata metadata = 1; bytes data = 2; } }
message DownloadAssetChunk { oneof part { Asset metadata = 1; bytes data = 2; } }
// 上传/下载首条为 metadata，后续仅 data；空文件是否允许由文件策略决定。
message Comment {
  string comment_id = 1; string task_id = 2; Author author = 3;
  string body = 4; google.protobuf.Timestamp created_at = 5;
}
message CreateCommentRequest { string task_id = 1; string body = 2; }
message CommentPage { repeated Comment items = 1; PageInfo page = 2; }
message ModerationInput { Target target = 1; string reason = 2; }
enum ReviewState { REVIEW_STATE_UNSPECIFIED = 0; REVIEW_STATE_PENDING = 1; REVIEW_STATE_APPROVED = 2; REVIEW_STATE_REJECTED = 3; }
enum ReviewDecision { REVIEW_DECISION_UNSPECIFIED = 0; REVIEW_DECISION_APPROVE = 1; REVIEW_DECISION_REJECT = 2; }
message Review {
  string review_id = 1; Submission snapshot = 2; ReviewState state = 3;
  optional string reason = 4; optional string reviewer_id = 5;
  google.protobuf.Timestamp created_at = 6; google.protobuf.Timestamp decided_at = 7;
}
message ListReviewsRequest { ReviewState state = 1; PageRequest page = 2; }
message DecideReviewRequest { string review_id = 1; ReviewDecision decision = 2; optional string reason = 3; }
message ReviewPage { repeated Review items = 1; PageInfo page = 2; }
enum ReportState { REPORT_STATE_UNSPECIFIED = 0; REPORT_STATE_OPEN = 1; REPORT_STATE_DISMISSED = 2; REPORT_STATE_ACTIONED = 3; }
enum ReportAction { REPORT_ACTION_UNSPECIFIED = 0; REPORT_ACTION_DISMISS = 1; REPORT_ACTION_TAKE_DOWN = 2; }
message Report {
  string report_id = 1; ModerationInput target = 2; ReportState state = 3;
  optional string resolution_reason = 4;
  google.protobuf.Timestamp created_at = 5; google.protobuf.Timestamp resolved_at = 6;
}
message ListReportsRequest { ReportState state = 1; PageRequest page = 2; }
message ResolveReportRequest { string report_id = 1; ReportAction action = 2; string reason = 3; }
message ReportPage { repeated Report items = 1; PageInfo page = 2; }
message MaterialReference {
  string entry_id = 1; uint64 entry_revision = 2; uint64 permission_revision = 3;
  repeated Artifact artifacts = 4; string digest = 5;
}
message MaterialManifest {
  string task_id = 1; uint64 catalog_revision = 2; repeated MaterialReference materials = 3;
  string digest = 4; google.protobuf.Timestamp checked_at = 5;
}
enum RegistrationGateState { REGISTRATION_GATE_STATE_UNSPECIFIED = 0; REGISTRATION_GATE_STATE_OPEN = 1; REGISTRATION_GATE_STATE_SEALED = 2; }
message RunRegistrationGate { string run_id = 1; RegistrationGateState state = 2; uint64 revision = 3; }
message OpenRunRegistrationRequest {
  string run_id = 1; string task_id = 2; string configuration_digest = 3;
  MaterialManifest materials = 4;
}
message RegisterCloudEntryRequest {
  CandidateRef candidate = 1; string payload_digest = 2; EntryInput content = 3;
  // 内容仅由云端已持久化候选提供；必须是 agent，资产必须属于该运行获准的范围。
}
message CloudRegistrationResult { EntrySubmission submission = 1; bool created = 2; }

// ---------- 3. 投票与统计 ----------
service VotingService {
  rpc GetVotingEligibility(TaskRef) returns (VotingEligibility);
  rpc GetBallot(TaskRef) returns (Ballot); // 网站用户；全部过审作品，无分页，MCP 无投票上下文
  rpc CastVote(CastVoteRequest) returns (Vote); // 网站用户
  rpc AccessTaskStatistics(AccessTaskStatisticsRequest) returns (TaskStatistics); // 网站/MCP；有永久副作用
  rpc SyncVotingCatalog(VotingCatalog) returns (google.protobuf.Empty); // 仅内容服务
  rpc GetTaskConclusion(TaskRef) returns (TaskConclusion); // 仅云端/投影恢复，无具体票数
}
enum PermanentEligibilityState {
  PERMANENT_ELIGIBILITY_STATE_UNSPECIFIED = 0;
  PERMANENT_ELIGIBILITY_STATE_OPEN = 1;
  PERMANENT_ELIGIBILITY_STATE_CLOSED_BY_STATISTICS = 2;
}
enum EligibilityReason {
  ELIGIBILITY_REASON_UNSPECIFIED = 0; ELIGIBILITY_REASON_NONE = 1;
  ELIGIBILITY_REASON_STATISTICS_VIEWED = 2; ELIGIBILITY_REASON_OTHER_RESTRICTION = 3;
}
message VotingEligibility {
  string task_id = 1; bool can_vote = 2; PermanentEligibilityState permanent_state = 3;
  google.protobuf.Timestamp closed_at = 4; EligibilityReason reason = 5;
}
message Ballot {
  string ballot_id = 1; string task_id = 2; uint64 catalog_revision = 3;
  repeated ApprovedEntry entries = 4; // 同版本完整集合；不得抽样、截断或按双方配对
  Vote current_vote = 5; // 仅本人当前选择；缺失映射 HTTP null，不含任何计票
}
message CastVoteRequest {
  string task_id = 1;
  reserved 2, 3;
  reserved "comparison_id", "choice";
  string ballot_id = 4; string entry_id = 5;
  uint64 expected_vote_revision = 6; // 首次为 0；旧修订不得覆盖新选择
}
message Vote {
  string vote_id = 1; string task_id = 2;
  reserved 3, 4;
  reserved "comparison_id", "choice";
  google.protobuf.Timestamp created_at = 5;
  string entry_id = 6; uint64 revision = 7; google.protobuf.Timestamp updated_at = 8;
}
message AccessTaskStatisticsRequest {
  string task_id = 1;
  optional bool permanently_close_voting = 2; // 必须存在且为 true；不得由预加载补入
}
message EntryVoteCount {
  string entry_id = 1; Side side = 2; uint64 votes = 3;
  optional double vote_share = 4; // 零总票时缺失，HTTP null
}
message TaskStatistics {
  string task_id = 1; VotingEligibility eligibility = 2;
  uint64 total_votes = 3; uint64 human_votes = 4; uint64 agent_votes = 5;
  reserved 6, 7, 8;
  reserved "ties", "human_win_rate", "agent_win_rate";
  Conclusion conclusion = 9; google.protobuf.Timestamp computed_at = 10;
  repeated EntryVoteCount entry_votes = 11; uint64 catalog_revision = 12;
  optional double human_vote_share = 13; optional double agent_vote_share = 14;
}
message VotingCatalog {
  string task_id = 1; uint64 catalog_revision = 2; bool task_published = 3;
  reserved 4;
  reserved "published_entries";
  repeated ApprovedEntry approved_entries = 5; // 全量过审集合；被移除的 entry 立即不可投
}
message TaskConclusion {
  string task_id = 1; Conclusion conclusion = 2; string algorithm_version = 3;
  uint64 revision = 4; google.protobuf.Timestamp computed_at = 5;
}

// ---------- 4. 云端挑战 ----------
service ChallengeService {
  rpc StartRun(StartRunRequest) returns (ChallengeRun); // 以下管理操作仅网站管理员
  rpc ListRuns(ListRunsRequest) returns (RunPage);
  rpc GetRun(RunRef) returns (ChallengeRun);
  rpc CancelRun(CancelRunRequest) returns (ChallengeRun);
  rpc RestartRun(RestartRunRequest) returns (ChallengeRun);
  rpc RegisterCandidate(CandidateRef) returns (CloudRegistrationResult);
  rpc ListPublicProgress(TaskPageRequest) returns (ProgressPage); // 公开投影；先检查任务可见
  rpc GetRunSummary(google.protobuf.Empty) returns (RunSummary); // 仅接入模块管理聚合
  rpc ClaimWork(ClaimWorkRequest) returns (ClaimWorkResponse); // 以下仅受信 worker
  rpc HeartbeatWork(WorkLease) returns (WorkLease);
  rpc ReadWorkMaterial(ReadWorkMaterialRequest) returns (stream WorkMaterialChunk);
  rpc CompleteWork(CompleteWorkRequest) returns (WorkReceipt);
}
enum RunStatus {
  RUN_STATUS_UNSPECIFIED = 0; RUN_STATUS_QUEUED = 1; RUN_STATUS_ACTIVE = 2;
  RUN_STATUS_COMPLETED = 3; RUN_STATUS_FAILED = 4; RUN_STATUS_CANCELLED = 5;
}
enum RunStage {
  RUN_STAGE_UNSPECIFIED = 0; RUN_STAGE_QUEUED = 1; RUN_STAGE_PREPARING_MATERIALS = 2;
  RUN_STAGE_VALIDATING_VOTER = 3; RUN_STAGE_GENERATING = 4; RUN_STAGE_LEARNING = 5;
  RUN_STAGE_ANONYMOUS_BATTLE = 6; RUN_STAGE_REGISTERING = 7; RUN_STAGE_FINISHED = 8;
}
enum RegistrationState {
  REGISTRATION_STATE_UNSPECIFIED = 0; REGISTRATION_STATE_UNREGISTERED = 1;
  REGISTRATION_STATE_REGISTERED = 2; REGISTRATION_STATE_DISCARDED = 3;
}
message RunConfiguration {
  AgentConfiguration generator = 1; AgentConfiguration voter = 2;
  uint32 round_limit = 3; Quantity budget = 4;
}
message CandidateStatus {
  string candidate_id = 1; RegistrationState registration_state = 2;
  optional string entry_id = 3; optional string review_id = 4;
}
message ChallengeRun {
  string run_id = 1; string task_id = 2; optional string parent_run_id = 3;
  string administrator_id = 4; RunStatus status = 5; RunStage stage = 6;
  uint32 round = 7; Conclusion task_conclusion = 8; RunConfiguration configuration = 9;
  Quantity consumed = 10; optional string failure_reason = 11;
  repeated CandidateStatus candidates = 12;
  google.protobuf.Timestamp created_at = 13;
  google.protobuf.Timestamp updated_at = 14;
  google.protobuf.Timestamp ended_at = 15;
}
message StartRunRequest { string task_id = 1; RunConfiguration configuration = 2; string idempotency_key = 3; }
message ListRunsRequest { optional string task_id = 1; PageRequest page = 2; }
message CancelRunRequest { string run_id = 1; string reason = 2; }
message RestartRunRequest { string run_id = 1; RunConfiguration configuration = 2; string idempotency_key = 3; }
message RunPage { repeated ChallengeRun items = 1; PageInfo page = 2; }
message PublicProgress {
  string run_id = 1; RunStatus status = 2; RunStage stage = 3;
  uint32 round = 4; google.protobuf.Timestamp updated_at = 5;
}
message ProgressPage { repeated PublicProgress items = 1; PageInfo page = 2; }
message WorkLease {
  string work_id = 1; string run_id = 2; RunStage stage = 3; uint32 round = 4;
  uint64 lease_epoch = 5; google.protobuf.Timestamp expires_at = 6;
}
message ClaimWorkRequest { repeated RunStage supported_stages = 1; } // worker 身份从 mTLS 取得
message WorkerMaterial { string material_id = 1; string media_type = 2; uint64 size_bytes = 3; }
message WorkAssignment {
  WorkLease lease = 1; AgentConfiguration executor = 2;
  repeated WorkerMaterial materials = 3; Quantity reserved_budget = 4;
  string material_access_token = 5; // 仅 worker，绑定租约/资产/用途；不能读取任意作品或统计
  string task_instructions = 6; // 按阶段清理来源与双方标签
}
message ClaimWorkResponse { WorkAssignment assignment = 1; } // 缺失表示暂无工作
message ReadWorkMaterialRequest { WorkLease lease = 1; string material_id = 2; } // 访问 token 放 metadata
message WorkMaterialChunk { oneof part { WorkerMaterial metadata = 1; bytes data = 2; } }
message GeneratedCandidate { string candidate_id = 1; EntryInput content = 2; string payload_digest = 3; }
message GeneratedWorks { repeated GeneratedCandidate candidates = 1; }
message VoterValidation { bool passed = 1; string policy_version = 2; string report_asset_id = 3; }
message LearningResult { string experience_asset_id = 1; }
message BattleResult { string report_asset_id = 1; } // 云端内部结果，不是真人选票
message WorkFailure { string code = 1; string safe_reason = 2; }
message CompleteWorkRequest {
  WorkLease lease = 1;
  string result_digest = 2;
  Quantity consumed = 3; // 云端按执行账本核实，不能盲信 worker 自报账单
  oneof outcome {
    VoterValidation voter_validation = 4;
    GeneratedWorks generated = 5;
    LearningResult learned = 6;
    BattleResult battle = 7;
    WorkFailure failure = 8;
  }
}
message WorkReceipt { string work_id = 1; bool replayed = 2; uint64 run_revision = 3; }

// ---------- 5. 发现与看板 ----------
service DiscoveryService {
  rpc ListTasks(ListTasksRequest) returns (TaskPage);
  rpc GetTask(TaskRef) returns (TaskView);
  rpc GetBoard(google.protobuf.Empty) returns (Board);
  rpc ApplyPublicEvent(PublicEvent) returns (google.protobuf.Empty); // 仅内容/投票服务；拒绝伪造来源
}
enum TaskOrder { TASK_ORDER_UNSPECIFIED = 0; TASK_ORDER_RECOMMENDED = 1; TASK_ORDER_HOT = 2; }
message ListTasksRequest {
  PageRequest page = 1; TaskOrder order = 2;
  optional string category = 3; optional string query = 4;
}
message TaskView { PublicTaskData content = 1; double heat = 2; } // HTTP 展平为 PublicTask
message TaskPage { repeated TaskView items = 1; PageInfo page = 2; }
message BoardGroup {
  uint64 published_tasks = 1; uint64 undetermined_tasks = 2;
  uint64 human_leading_tasks = 3; uint64 agent_leading_tasks = 4;
}
message CategoryBoard { optional string category = 1; BoardGroup summary = 2; }
message Board { BoardGroup overall = 1; repeated CategoryBoard categories = 2; google.protobuf.Timestamp computed_at = 3; }
message PublicContentChange {
  string task_id = 1;
  uint64 content_revision = 2;
  oneof change { PublicTaskData upsert = 3; google.protobuf.Empty removed = 4; }
}
message PublicEvent {
  string event_id = 1;
  google.protobuf.Timestamp occurred_at = 2;
  oneof payload { PublicContentChange content = 3; TaskConclusion conclusion = 4; }
  // 来源由调用身份绑定：内容服务只能发 content，投票服务只能发 conclusion。
  // 没有 Vote、TaskStatistics 或投稿审核材料 EntrySubmission 分支。
}
```
<!-- backend-proto:end -->

## 10. 验收与未决事项

| 验收对象 | 必须覆盖的证据 | 来源 |
| --- | --- | --- |
| 身份与入口 | 网站/MCP 同账号；无效 token 不降级匿名；MCP 管理员 token 无写权限；伪造身份 header 与业务 RPC 直连被拒绝 | S3～S5 |
| 发布与审核 | 首发缺一方不能送审；同版本重复送审只一条审核；追加只审核新作品；过期审核不能生效；过审即展示，无 publish 开关，cloudUse=false 不阻止展示 | S2～S4、H1.1 |
| 全部作品单选 | 超过普通列表一页时仍一次返回所有过审 human/agent 作品；不含未过审作品；提交一个 entryId；多选、旧 pair 请求、列表外及跨任务 ID 被拒绝；空集合不可投，完整性不可确认则失败 | H1.2、H1.3 |
| 当前选择与修订 | 每账号×任务至多一份当前选择；改选不增加总票；同请求重试幂等；旧重试不能覆盖新选择；目录新增/撤销后旧 ballot 的新写入被拒绝 | Agent Self-Claimed，兼容 H1 |
| 资格事务 | 多个 Go 实例同时投票/改选/看统计，包括资格行尚不存在；关闭先提交则不可写票，投票先提交则保留该选择 | S5、H1.3 |
| 统计副作用 | 提交失败不返回统计；响应丢失仍永久关闭；换 token/刷新列表/新增作品不恢复；统计含全部过审作品零票项；零总票占比 null；两方票数之和等于各作品票数之和 | S1、S3、S5、S6、H1 |
| 下架边界 | 延迟的目录/索引事件不能重新开放；下架与投票并发按门禁顺序完成；内容服务失败时不泄露旧内容 | S3、S4、S6 |
| 云端终止 | 登记/取消任意交错，封闭前已登记可找回、终态后无新增；封闭响应丢失、调度重启、旧 epoch 结果均安全 | S4.5～S4.6 |
| 预算与重启 | 同幂等键不重复运行或预留额度；不同内容冲突；新运行重新检查材料；外部执行不假定恰好一次 | S4 |
| 展示与管理数据 | 默认内容、候选列表、看板、进度、管理概览、错误、日志、事件均不泄露具体计票；候选列表仅带本人选择；未过审附件只有网站上传者/管理员可读，MCP 无管理权限 | S1、S4～S6、H1 |
| 分页与投影 | 快照翻页不重复；下架过滤、空页游标、游标过期、乱序/重复事件和重建失败可恢复 | S1、S6 |

这些是未来实现的验收要求，不能由当前文档检查或 Proto 编译代替。实施前仍需锁定：登录与凭据生命周期、Go/gRPC 和数据库版本、各服务 deadline/重试预算、文件限额与云端授权撤回、材料/投票者验证规则、真实选票有效性和领先算法、推荐/热度、额度单位、下架对在途运行的影响。历史选票保留而撤销通过作品不进入当前计票是本版技术提案。对应策略没有明确版本前，受影响路径不能默认成功。

文档与 Proto 的核对命令和实际结果见下方验证记录；设计参考及取舍统一记录到[成熟参考](references.md#backend-design)。

### 文档验证记录

2026-09-18：使用临时虚拟环境中的 `grpcio-tools==1.84.0`（`libprotoc 35.1`）整体编译本文 Proto，退出码 0；5 个 service、56 个 RPC、108 个 message、22 个 enum 的定义与类型引用有效。核对表覆盖全部 36 个 planned HTTP 操作；描述符检查确认只有 `AccessTaskStatistics` 的返回类型可到达统计类型。可信场景快照与本地文档链接通过。此结论只覆盖文档和接口结构，不代表运行时业务或并发验收通过。

下面命令在仓库根目录运行；工具和编译产物仅写入临时目录，不增加项目依赖或生成 Go 实现：

```sh
python3 -m venv /tmp/human-worth-proto-venv
/tmp/human-worth-proto-venv/bin/pip install grpcio-tools==1.84.0
/tmp/human-worth-proto-venv/bin/python - <<'PY'
from pathlib import Path
import re
import grpc_tools
from grpc_tools import protoc
from google.protobuf import descriptor_pb2

doc = Path('docs/backend.md').read_text()
block = doc.split('<!-- backend-proto:start -->', 1)[1].split('<!-- backend-proto:end -->', 1)[0]
lines = block.strip().splitlines()
assert lines[0] == chr(96) * 3 + 'proto' and lines[-1] == chr(96) * 3
out = Path('/tmp/human-worth-backend-proto')
out.mkdir(exist_ok=True)
source = out / 'humanworth_backend_v1.proto'
source.write_text('\n'.join(lines[1:-1]) + '\n')
args = ['protoc', '-I' + str(out), '-I' + str(Path(grpc_tools.__file__).parent / '_proto'),
        '--include_imports', '--descriptor_set_out=' + str(out / 'backend.pb'), str(source)]
assert protoc.main(args) == 0
files = descriptor_pb2.FileDescriptorSet.FromString((out / 'backend.pb').read_bytes()).file
contract = next(f for f in files if f.name == source.name)
assert len(contract.service) == 5
assert all(e.value[0].number == 0 and e.value[0].name.endswith('_UNSPECIFIED') for e in contract.enum_type)
types = {m.name: m for m in contract.message_type}
assert not {'Comparison', 'GetComparisonResponse', 'PublicEntry', 'VotingEntry'} & types.keys()
assert 'publish' not in {f.name for f in types['Permissions'].field}
ballot_fields = {f.name: f for f in types['Ballot'].field}
assert ballot_fields['entries'].label == ballot_fields['entries'].LABEL_REPEATED
assert ballot_fields['entries'].type_name.endswith('.ApprovedEntry')
vote_fields = {f.name: f for f in types['CastVoteRequest'].field}
assert vote_fields['entry_id'].type == vote_fields['entry_id'].TYPE_STRING
assert vote_fields['entry_id'].label != vote_fields['entry_id'].LABEL_REPEATED
assert not {'comparison_id', 'choice', 'entry_ids'} & vote_fields.keys()
operations = set(re.findall(r'^      operationId: (\w+)$', Path('openapi.yaml').read_text(), re.M))
planned = operations - {'getHealth', 'headHealth'}
mapping = doc.split('## 8. HTTP / MCP 与 RPC 对照', 1)[1].split('## 9.', 1)[0]
assert set(re.findall(r'`([a-z][A-Za-z]+)`', mapping)) & planned == planned
messages = {'.' + f.package + '.' + m.name: m for f in files for m in f.message_type}
def reachable(name, seen=None):
    seen = set() if seen is None else seen
    if name in seen:
        return seen
    seen.add(name)
    if name in messages:
        for field in messages[name].field:
            if field.type == field.TYPE_MESSAGE:
                reachable(field.type_name, seen)
    return seen
for service in contract.service:
    for method in service.method:
        if method.name != 'AccessTaskStatistics':
            assert not {'.humanworth.backend.v1.TaskStatistics', '.humanworth.backend.v1.EntryVoteCount'} & reachable(method.output_type)
print('Proto compilation, full ballot/single choice, HTTP coverage and statistics response boundary: PASS')
PY
python3 scripts/check_docs.py
git diff --check
```
