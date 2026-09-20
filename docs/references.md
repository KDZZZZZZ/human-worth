# 成熟参考与设计补全

用户要求：当人类设计不清晰时，agent **主动寻找合适的成熟参考并补全方案**，不把所有缺口退回给人类。先理解可信场景和约束，选相似问题、可核实实现与维护活跃的参考，再形成适合本项目的可验证决定。

2026-09-17 核实以下官方来源。本轮未发现已安装的 CI/CD 专门技能，因此依据官方资料完成；不是凭技能名称假设能力。

| 来源 / 版本 | 解决的问题 | 本项目采用与差异 |
| --- | --- | --- |
| [GitHub repository rules API](https://docs.github.com/en/rest/repos/rules)；核实于 2026-09-17 | PR-only、0 审批、状态检查、分支限定合并方式 | dev squash / main merge，均无 bypass；dev 要求最新基线 CI |
| [GitHub Actions secure use](https://docs.github.com/en/actions/reference/security/secure-use)；核实于 2026-09-17 | 公开仓库 CI 与本机权限边界 | PR 在 GitHub 托管 runner 执行；本机只读取 protected dev 及对应成功 CI，不注册公开仓库的自托管 runner |
| [OpenGitOps 四项原则](https://opengitops.dev/) v1.0 | 版本化期望状态、主动拉取、持续调和 | 使用 systemd 定时拉取 dev；这是轻量部署控制器，不宣称实现完整 GitOps 平台 |
| 本机 `man systemd.timer` / `systemd.service`；[systemd 上游](https://github.com/systemd/systemd) | 开机恢复、自动调度、独立账户、服务重启 | 60 秒定时检查，失败保留旧版；安装后的控制器为本机 root 管理文件，不执行未合并 PR |
| [Nginx proxy_pass](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_pass)；[OpenSSH ssh](https://man.openbsd.org/ssh#R) | 公网网关反向代理到本机 | 独立公网端口 18090；SSH 仅监听网关 loopback 28090，应用在本机 18090 |
| [阿里云 ECS RunCommand](https://www.alibabacloud.com/help/en/ecs/developer-reference/api-ecs-2014-05-26-runcommand)；本机 CLI 3.4.11 | 已有服务器增量配置与结果查询 | 从当前 CLI 账号识别现有 cn-beijing ECS，通过云助手安装独立配置，不新购资源 |
| [ponytail 技能](https://github.com/DietrichGebert/ponytail/blob/e3ba2aa6f1e6f0bc4d69eb09c9f0d0a93af56156/skills/ponytail/SKILL.md)；commit `e3ba2aa6f1e6f0bc4d69eb09c9f0d0a93af56156` | 减少无必要代码、抽象和依赖 | 编程任务使用 full 模式，先理解流程再选最小可行改动；保留明确需求、必要校验和安全边界 |

新增参考记录需说明：待解决缺口、来源链接及版本 / commit、适配点、不适配点、采用决定、验证证据。必要时查看参考项目的真实代码和测试。不要堆砌品牌、盲目复制，或把参考设计标成 Human Design。

与明确的人类要求冲突时遵从人类要求；既有授权内能用成熟惯例解决的选择直接推进。仅在成熟参考仍无法消除实质歧义，或会产生未授权的不可逆影响、显著费用时，提出一个具体问题，同时继续独立工作。

<a id="openapi-contract"></a>
## OpenAPI 接口契约草案

2026-09-17，用户要求提供 OpenAPI YAML。[根目录 openapi.yaml](../openapi.yaml) 覆盖 PRD S1～S6，并依据项目目标提供最小平铺评论接口。`GET /api/health`、`HEAD /api/health` 按现有服务描述；其他操作均标为 `planned`。本次只交付契约文档，不实现 API 或更改线上路由。

待补缺口是 HTTP 路径、字段、权限表达、分页、错误格式，以及统计访问和重试的具体请求形态。以下均为 **Agent Self-Claimed**，不将 PRD 的待定项改写为人类已批准。

| 参考 / 版本 | 适配点与选择 | 差异与验收判据 |
| --- | --- | --- |
| [OpenAPI Specification 3.1.1](https://spec.openapis.org/oas/v3.1.1.html) | 单文件 YAML、JSON Schema 条件约束、共享 schema、逐 operation 的 security 与实现状态 | 选定 3.1.1，不声称是最新版；使用规范校验器检查结构、引用、路径参数与 operationId |
| [RFC 9110 §9.2.1](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.2.1) | GET 不承载用户请求的永久资格变更；单题统计单独使用 POST，并要求显式确认 | 项目的永久禁投规则来自 S5；网站、MCP 和管理员必须先提交禁投记录，再得到统计；与并发投票串行化 |
| [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457.html) | planned 接口用 `application/problem+json`，扩展稳定 `code` 字段 | 不改写当前基础服务 404/405 的空响应；失败不得携带具体统计或私有内容 |
| [MCP tools 2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25/server/tools) | `x-mcp` 映射 `list_tasks`、`get_task` 与显式统计工具；统计工具标明非只读且有不可逆副作用 | OpenAPI 不替代 MCP JSON-RPC/传输规范；不自动暴露投稿、投票或管理操作，不能只依赖工具注解做授权 |
| [OWASP CSRF Prevention Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)，核实于 2026-09-17 | 网站会话写请求同时验证 CSRF token 与来源；MCP 使用独立用途的 Bearer token | 登录与 token 签发仍待定；两种凭据映射同一账号，MCP token 即使属于管理员也不得执行管理动作 |
| [GitHub REST 分页](https://docs.github.com/en/rest/using-the-rest-api/using-pagination-in-the-rest-api)，核实于 2026-09-17 | 普通浏览列表选择不透明游标、默认 20、上限 100 | 不复制 GitHub 的 Link 响应格式；发现流游标绑定筛选及快照，空页以 `nextCursor` 为准；H1 要求投票候选一次完整返回，不适用分页 |
| [Chatbot Arena 论文 v1](https://arxiv.org/abs/2403.04132v1)，历史参考，2026-09-18 停用 | 旧草案曾参考成对比较 | 已被人类修订 H1 覆盖；作品对、left/right/tie 及按作品对去重均不再是现行设计，现行选择见下节 |
| [GitHub issue comments API](https://docs.github.com/en/rest/issues/comments)，核实于 2026-09-17 | 最小评论列表和创建操作；本项目采用纯文本、平铺结构 | 不追加编辑、点赞或多级回复体系；评论来源为项目目标，不伪称 S1～S6 已定义评论细则 |

补全选择：新任务草稿整体替换并按版本送审；被驳回追加作品修改后重新送审；审核历史保留。上传文件在作品过审前限网站上传者/管理员管理读取；任务可见且作品过审后允许展示和下载，每次访问校验审核状态，不提供长期下载地址。不存在独立的公开授权开关。看板先提供平台/分类的任务结论数量，避免通过单任务分组还原具体票数；热度不得编码可还原的单题统计。

云端启动和重启显式要求预算、轮数及配置，用幂等键避免网络重试重复开销；具体 header 语义是本项目约定。候选登记按运行与候选唯一定位，先找回已有作品和审核状态；终态只能找回已有登记，不能新增作品。公开进度使用单独字段集合，管理员复盘的具体真人统计仍走资格变更入口。

实现前仍须补全：登录与 token 生命周期、云端授权撤回、文件和内容限额、推荐/热度/有效票/领先/翻转算法、云端材料与验证规则、额度单位、下架对在途运行的影响。撤销过审后的当前计票与历史记录按下节提案处理。不能以 schema 校验通过替代这些设计及真实业务验收。

本次契约验证使用 [openapi-spec-validator](https://openapi-spec-validator.readthedocs.io/en/latest/) `0.9.0`，安装在临时虚拟环境，不新增项目依赖。可在任意临时环境运行以下命令复核格式，再运行项目现有 CI；schema 校验不能证明并发、权限和跨入口持久化已实现。

```sh
python3 -m venv /tmp/human-worth-openapi-venv
/tmp/human-worth-openapi-venv/bin/pip install openapi-spec-validator==0.9.0
/tmp/human-worth-openapi-venv/bin/openapi-spec-validator openapi.yaml
npm run ci
```

<a id="task-wide-voting"></a>
## 审核展示与全部作品单选

2026-09-18，人类明确修订为“作品不分是否公开只分有没有过审允许展示”，投票“一次给出所有作品”，并确认“从全部作品中选一个最认可的”。这些是 [PRD H1](prd.md#human-revisions) 的 Human Design。参考只用于补足当前选择、改选和候选集合变化的具体机制，不决定单选需求。

| 官方参考 / 版本 | 采用与差异 | 验收判据 |
| --- | --- | --- |
| [Discourse 投票文档](https://meta.discourse.org/t/creating-and-managing-polls/77548?tl=en)，核实于 2026-09-18 | 单选从全部选项选一项；参考动态选项增删时保留未变选项的投票记录 | 本项目选项由作品审核产生，用户不能手工编辑投票选项；必须完整返回全部过审作品，包括超过普通列表一页的情况 |
| [Discourse v2026.8.0](https://releases.discourse.org/changelog/v2026.8.0/) | 参考隐藏结果时显示本人选择、Change vote / Update vote 及更新期间保留选票状态 | 本项目提案每账号×任务保留一份当前选择，改选替换它；绝不采用“投完自动显示结果”，统计仍须显式永久关闭该题投票资格 |

以下为 **Agent Self-Claimed**：作品使用独立审核枚举 `EntryReviewState`，不复用任务发布枚举；删除 `Permissions.publish`，保留用途不同的 `cloudUse`。`GetBallot` 一次返回完整审核目录快照及本人当前选择；`CastVote` 只接收一个 `entryId`。目录修订绑定 ballot，新增或撤销过审作品后，旧快照的新投票须刷新；目录冻结或无法确认完整集合时失败，不返回抽样结果。选择记录按账号×任务唯一，用修订号和最近成功请求摘要处理并发与幂等，旧请求不能覆盖新选择。

当前计票提案：统计只计仍过审作品上的有效真人当前选择，并列出所有过审作品（含零票）；改选不增加总票数，撤销过审作品的旧选择保留历史而不计入当前结果。作品与人类/agent 两方使用“得票占比”，不沿用成对对抗的平票与胜率字段。失去投票资格后不可首投或改选；目录刷新、token 更换和新增作品均不能恢复资格。HTTP 契约见 [OpenAPI](../openapi.yaml)，跨服务事务边界见[架构划分](backend-architecture.md)，并发演练见[实验清单](deployment.md#experiment-checklist)。

## 移除额度展示

2026-09-18，用户明确要求“不要再显示额度了”，见 [PRD H2](prd.md#hide-quota)。据此移除管理员概览的 `quota` 和运行展示的 `consumed`，后续 Proto 同样不提供这些展示字段，不新增替代展示接口。删除哪些字段属于 Agent Self-Claimed 的契约映射；启动/重启的预算配置、内部资源校验和结算继续沿用已有设计。原始场景仅作为历史依据保留，不再要求展示额度或消耗。

<a id="google-login"></a>
## Google 登录

2026-09-18，用户明确要求增加 Google 登录，见 [PRD H3](prd.md#google-login)。OpenAPI 已补充登录、回调和当前会话退出接口，核心对象、状态和内部接口见 [Identity 设计](backend-identity.md)；业务均为 planned，未配置 Google 凭据或发布登录功能。

| 官方参考 / 版本 | 采用与差异 | 验收判据 |
| --- | --- | --- |
| [Google OpenID Connect](https://developers.google.com/identity/openid-connect/openid-connect)、[协议参考](https://developers.google.com/identity/openid-connect/reference)，核实于 2026-09-18 | 使用服务端授权码流程；验证 state、签名、issuer/aud/exp/nonce 与适用的 azp；规范 Google issuer 后按 sub 关联账号，邮箱仅为资料 | 无效 token、重放与账号禁用被拒绝；邮箱变化或再次登录不创建替代账号、不恢复投票资格 |
| [RFC 9700 §2.1.1](https://www.rfc-editor.org/rfc/rfc9700.html#section-2.1.1)，2025 | 使用 PKCE S256，并将一次性流程绑定浏览器；Google 支持情况以协议参考为依据 | 双副本并发回调只占用一次；错误 verifier/nonce/state 不能建立会话；未知换码结果不盲目重试 |
| [Google Web Server 回调规则](https://developers.google.com/identity/protocols/oauth2/web-server#uri-validation)，核实于 2026-09-18 | 正式回调使用预注册 HTTPS 域名；不采用当前裸公网 IP。固定回到站内 /，不接受任意 returnTo/redirect_uri | Google 客户端、域名和回调完全匹配；校验开发代理浏览器入口、Cookie 与回调的一致性；不以 HTTP/IP 入口宣称登录可用 |

本项目的具体选择（Agent Self-Claimed）：十分钟流程 Cookie、scopes 为 openid/email/profile、首次登录自动创建普通账号、不按邮箱自动合并、服务端会话轮换、当前会话退出，以及三个 HTTP/RPC 的命名。Google token 不进入前端或 MCP；Secret 仅在服务端配置。开发代理继续连接公网 API，登录联调的 HTTPS 与预注册回调配置尚待实现，不修改现有运行入口。

<a id="identity-design"></a>
## Identity 详细设计与 platform

2026-09-18，用户要求先写 Identity 的核心对象、核心功能逻辑及公共基座清单，并指定 `/home/oops/services/sub2api` 为 Google 登录实现参考；随后明确本轮只学习逻辑和实现，密钥后续提供。交付见 [Identity 设计](backend-identity.md)。会话期限、内部断言、MCP 生命周期、技术选型和目录组织为 Agent Self-Claimed；未修改参考应用、引入真实凭据或部署 Identity。

### 指定的本机实现

sub2api 版本文件 `backend/cmd/server/VERSION` 为 0.1.155；本机工作副本无可解析 HEAD，因此用本次读取文件的 SHA-256 标识来源。版本文件不代替源码指纹，也不说明运行镜像与源码完全一致。

| 本机来源 | 核实内容与取舍 |
| --- | --- |
| [Google 处理器](/home/oops/services/sub2api/backend/internal/handler/auth_email_oauth.go) | 有 start/callback、十分钟 Cookie、服务端换码和 UserInfo 查询；本项目分到 gateway/Identity，并保留已定的 PKCE、ID token 验证、共享一次性流程和 HttpOnly 网站会话 |
| [账号登录关联](/home/oops/services/sub2api/backend/internal/service/auth_email_oauth_auto.go) | 先查外部身份，也有按邮箱关联和凭据签发；本项目只按 issuer/sub 关联，不移植邀请、密码注册、赠送额度和前端 token 交付 |
| [配置读取](/home/oops/services/sub2api/backend/internal/service/setting_oauth.go) | 数据库设置覆盖基础配置并检查必填项；本项目首版使用部署配置及 Secret 文件，无运行时跨应用数据库依赖 |
| [回调测试](/home/oops/services/sub2api/backend/internal/handler/auth_email_oauth_test.go) | 已有账号登录和新用户流程具有明确测试；本次阅读源码，未运行这些测试；本项目另验双副本、稳定账号与安全 Cookie |

文件指纹（按上表顺序）：

```text
f9048cdd67ec5b930baf87b93148fd7fafadc97ad9d22107337158696fa6efb6  auth_email_oauth.go
9db73c5addc2ae5bc35db9898a76ffdff5d7f8b224dad2a0b1e1ea45feecaa89  auth_email_oauth_auto.go
dfbfdb2577647883b6818002b0d5a7590e1df988b4a7f3dced440c48ead5f358  setting_oauth.go
8b51e7006604a4298b23d382676e804ca7ab0b4a6b261c56d2c241a3cd4d9527  auth_email_oauth_test.go
```

### 规范与实现补充

| 官方来源 / 核实日期 | 采用范围与验收判据 |
| --- | --- |
| 上节 [Google OIDC、PKCE 与回调规则](#google-login)，2026-09-18 | Google 流程采用项目既有安全边界；同一 sub 的邮箱变化不改变账号；错误 nonce/verifier/issuer/audience 被拒 |
| [OWASP Session Management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)、[CSRF](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)，2026-09-18 | 高熵不透明会话、服务端到期与撤销、CSRF token 及来源检查；24 小时会话等具体期限由本项目提出，不宣称是规范定值 |
| [Go OAuth2](https://pkg.go.dev/golang.org/x/oauth2)、[go-oidc v3](https://pkg.go.dev/github.com/coreos/go-oidc/v3/oidc)、[golang-jwt 校验选项](https://golang-jwt.github.io/jwt/usage/parse/)，2026-09-18 | 复用成熟库进行协议和签名验证，显式限制算法、issuer/audience、必要字段；Identity 自己验证 nonce/azp 和当前凭据状态；实际依赖版本在实现时锁定 |
| [PostgreSQL 18 事务隔离](https://www.postgresql.org/docs/18/transaction-iso.html)、[pgxpool](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool)，2026-09-18 | 用唯一键、条件更新和本地事务实现流程占用与并发建号；连接数按所有副本计算；数据库事务不跨外部换码 |
| [gRPC Authentication](https://grpc.io/docs/guides/auth/)、[Health Checking](https://grpc.io/docs/guides/health-checking/)，2026-09-18 | mTLS 服务身份、内部校验入口、就绪状态和停止接单；不因 Google 短时失败触发所有身份副本重启 |
| [Go slog](https://pkg.go.dev/log/slog)、[OpenTelemetry Go](https://opentelemetry.io/docs/languages/go/)，2026-09-18 | 复用日志与遥测能力，关联调用耗时和错误；过滤凭据与回调 query，不以账号/邮箱作为指标标签 |

首批基座以支撑 gateway 与 Identity 为界，不预先搭建通用认证框架或引入 Redis。关键身份变更与审计/outbox 同事务；跨服务投递在 Moderation 接入时补齐，身份撤销先通过权威查询生效。以上是项目适配，文档核对不能证明真实 Google 登录、多副本并发或集群可用性通过。

## Swagger 文档发布

2026-09-17，用户要求生成 Swagger 文档并部署。交付是现有契约的可浏览页面和 YAML 下载；没有增加业务接口实现。以下实现选择属于 **Agent Self-Claimed**。

| 官方参考 / 版本 | 补全选择 | 验收判据 |
| --- | --- | --- |
| [Swagger UI 安装](https://swagger.io/docs/open-source-tools/swagger-ui/usage/installation/)，`swagger-ui-dist` 5.33.0 | 使用官方独立浏览器资源，精确锁定开发依赖，保留许可证并随页面发布 | 构建可重现；浏览器渲染构建时的全部操作（首次发布 38 个），不请求外部 CDN |
| [Swagger UI 配置](https://swagger.io/docs/open-source-tools/swagger-ui/usage/configuration/) | `supportedSubmitMethods: []` 关闭执行，`validatorUrl: null` 关闭在线校验，展示扩展字段并禁用 URL 配置覆盖 | 实现状态可见，没有 Try it out 或凭据输入，不向外部校验服务发送契约 |
| [Nginx alias](https://nginx.org/en/docs/http/ngx_http_core_module.html#alias) | 在既有网关 `/docs/` 提供独立静态 release，保留应用代理 | 配置校验、资源摘要、浏览器访问和既有健康检查通过，应用 revision 不变 |

只读方式适配当前以 planned 为主的接口契约与公开 HTTP 文档入口。后续开放认证和接口调试须随真实业务实现启用 HTTPS，并重新验证相应权限与副作用；本次不预先实现这些功能。

## 前端 API 环境路由

用户明确要求前端开发走公网 API、部署走内部调用。采用相对 `/api/*` 和本地开发代理是 Agent Self-Claimed：复用现有 Node.js 服务与 [Node.js 24 HTTP request](https://nodejs.org/docs/latest-v24.x/api/http.html#httprequesturl-options-callback)，以流转发请求和响应，不增加运行时依赖；部署继续使用已有 Nginx 内部代理。验收覆盖真实 HTTP 上游、请求参数与请求体、上游故障、生产入口忽略开发配置，以及本地开发入口连接实际公网健康检查。

<a id="backend-design"></a>
## 分布式 Go 后端设计

以下参考用于补全服务间身份传递、数据写入归属、统计副作用与并发、终态与跨服务登记的顺序，以及内部接口。早期综合草稿已按用户要求删除，当前依据为[七服务架构](backend-architecture.md)及各模块设计，首个模块为 [Identity](backend-identity.md)。以下技术选择均为 Agent Self-Claimed；尚未安装业务数据库或实现业务 RPC。

| 官方参考 / 版本 | 采用与差异 | 验收判据 |
| --- | --- | --- |
| [Protocol Buffers proto3](https://protobuf.dev/programming-guides/proto3/)，语法规范 | 有编号的强类型消息、UNSPECIFIED 零值、optional 存在性、oneof、已发布字段删除时保留编号/名称；不直接用 ProtoJSON 覆盖现有 HTTP JSON | 编写独立 Proto 文件，使用编译器检查消息、枚举、引用及服务方法，并核对 HTTP 映射 |
| [gRPC Authentication](https://grpc.io/docs/guides/auth/) | 经认证的服务连接与身份断言；业务服务仍复核主体、用途和权限 | MCP token 不获得网站写权限；不能通过伪造 actor/role 字段调用内部接口 |
| [gRPC Deadlines](https://grpc.io/docs/guides/deadlines/)、[Retry](https://grpc.io/docs/guides/retry/)、[Cancellation](https://grpc.io/docs/guides/cancellation/)、[Status Codes](https://grpc.io/docs/guides/status-codes/)，核实于 2026-09-17 | deadline 传递、有界重试、稳定业务错误；请求取消不等于运行取消 | 响应丢失后用同一业务键找回；业务运行终态持久化，不能以连接关闭代替 |
| [PostgreSQL 18 行锁](https://www.postgresql.org/docs/18/explicit-locking.html#LOCKING-ROWS) | 提案使用服务本地事务、唯一约束和一致锁顺序；先创建资格行再锁定 | 并发首投/首查统计不能绕过永久资格；验证应使用真实数据库和多个进程 |
| [AWS transactional outbox](https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html) | 业务与待发送事件同事务、至少一次投递、消费去重；先用数据库轮询，不要求 AWS 或消息中间件 | 重复/乱序消息不回退状态；提交后进程崩溃仍可继续投递 |

本项目进一步推导两项协作协议：内容下架前先关闭投票目录，云端终态前先封闭内容登记；它们用于跨服务防止迟到写入，不能仅靠 outbox 自动获得。阶段租约与 epoch 阻止旧 worker 结果生效，也不等于供应方计费调用恰好一次。具体 Go/gRPC 运行版本、算法、权限生命周期与额度策略仍待实施时锁定。

<a id="microservices-lab"></a>
## 服务划分与单机多节点学习环境

2026-09-18，人类明确希望增加分布式微服务学习内容，并要求按照讨论重新编写架构划分、部署计划与可模拟场景，随后要求“先只使用一套实验配置”。交付见[七服务架构](backend-architecture.md)和[统一部署文档的集群计划](deployment.md#lab-plan)，统一使用 lab 基线。七服务 Proto 待按模块编写；尚未安装集群。以下为 Agent Self-Claimed 的设计选择，节点数、初始资源预算和实验配置不是已验证容量。

| 官方参考 / 版本 | 采用与差异 | 验收判据 |
| --- | --- | --- |
| [Microsoft domain analysis](https://learn.microsoft.com/en-us/azure/architecture/microservices/model/domain-analysis)，在线文档，核实于 2026-09-18 | 以业务能力与数据所有权划界；独立 Asset/Moderation，保留 Discovery；执行租约仍归 Challenge，worker 独立进程 | 每个权威状态有唯一所有者；明确审核决定应用和跨服务恢复，不按调用方数量机械拆服务 |
| [kind Nodes](https://kind.sigs.k8s.io/docs/user/configuration/#nodes)，在线配置说明，核实于 2026-09-18 | 只维护 lab：1 个控制平面 + 3 个工作节点；数据库保留 1 主 2 备；不把逻辑节点当成额外物理容量 | 检查真实 Pod 落点、宿主机资源、节点失联；控制平面只验中断恢复，不作 HA 结论 |
| [kind v0.33.0 节点基础镜像](https://github.com/kubernetes-sigs/kind/blob/v0.33.0/images/base/Dockerfile)、[Kubernetes Pods](https://kubernetes.io/docs/concepts/workloads/pods/)，核实于 2026-09-18 | 区分宿主机 Docker、kind 节点容器、节点内 containerd、Pod、应用容器与业务进程；首版每个业务 Pod 一个应用容器。引用版本仅用于结构依据，部署版本另行固定 | 图中九类应用各两个 Pod，共十八个业务主进程；同一服务的两个副本分散到不同工作节点；系统、数据库及监控不混入业务副本计数 |
| [Kubernetes topology spread](https://kubernetes.io/docs/concepts/scheduling-eviction/topology-spread-constraints/)、[Disruptions](https://kubernetes.io/docs/concepts/workloads/pods/disruptions/)，核实于 2026-09-18 | 按主机名分散同服务副本；滚动更新与 PDB 分别配置，PDB 不防止意外节点故障 | drain、突然宕机、滚动发布分别测试；故障后仍核验业务数据 |
| [Kubernetes NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/)、[Calico on kind](https://docs.tigera.io/calico/latest/getting-started/kubernetes/kind)，核实于 2026-09-18 | 使用实际执行策略的 CNI，默认拒绝并按依赖放行；mTLS/业务权限独立校验 | 合法链路可用；越权访问拒绝；不以只创建 NetworkPolicy YAML 作为网络隔离证据 |
| [gRPC load balancing](https://grpc.io/docs/guides/custom-load-balancing/)，核实于 2026-09-18 | Headless DNS 与内置 round_robin；复用现有客户端策略，不自研均衡器 | 从逐 Pod 请求量验证分流和端点更新；不能仅凭双副本宣称均衡 |
| [CloudNativePG 1.28 replication](https://cloudnative-pg.io/docs/1.28/replication/)，版本化设计参考 | 主备独立卷，同步复制保持所需持久性并验证 failover quorum；部署时再锁定受支持工具组合 | 切换后已确认投票、永久禁投和幂等登记不丢失；不能安全提升时保持不可用 |
| [Kubernetes probes](https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/)，核实于 2026-09-18 | 分离启动、就绪与存活；依赖短暂故障不直接触发全体业务重启 | 探针状态、流量摘除、优雅退出和依赖恢复分别有证据 |
| [tc-netem](https://man7.org/linux/man-pages/man8/tc-netem.8.html)，在线手册，核实于 2026-09-18 | 在实验网络命名空间定向注入网络异常；业务重复请求另由驱动器生成 | 记录注入、业务判据、自动清理和恢复；不把 TCP 重传当作 RPC 重复执行 |

七服务划分、Moderation 决定与 Content 应用回执的协调、实验资源预算和 D01～D14 矩阵是本项目的具体推导。参考资料说明工具与模式能力，不证明 Human Worth 已具备对应实现或通过了故障演练。

## Identity 实现与 lab 固定版本（2026-09-19）

本轮基于用户“按这个顺序完成”的实现授权，并使用用户指定域名与另行提供的 Google Web 客户端。所有依赖、具体期限、密钥格式、部署脚本和验证方法均为 Agent Self-Claimed。sub2api 仅作为逻辑参考，没有修改其运行配置。

| 来源 / 固定版本 | 采用与差异 | 本次验证范围 |
| --- | --- | --- |
| [Go 1.27.1](https://go.dev/dl/)、[Buf v1.72.0](https://github.com/bufbuild/buf/releases/tag/v1.72.0) | 一个 Go module、三个入口；Buf 自带编译器，生成插件由 go run 固定版本；程序和实验镜像记录来源摘要 | Proto lint、生成一致性、vet、构建和 race 集成测试 |
| [go-oidc v3.21.0](https://github.com/coreos/go-oidc/tree/v3.21.0)、[OAuth2 v0.37.0](https://pkg.go.dev/golang.org/x/oauth2@v0.37.0)、[JWT v5.3.1](https://github.com/golang-jwt/jwt/tree/v5.3.1) | 固定 Google issuer、端点与 RS256；自行补 nonce/azp 与一次性流程；内部断言固定 HS256，只在 Identity 持有签名密钥 | 真实 HTTP 测试供应方覆盖签名、JWKS 故障、nonce/PKCE、重放与不盲目换码重试；不能替代 Google 真人授权 |
| [Google OIDC](https://developers.google.com/identity/openid-connect/openid-connect)、[OIDC 协议参考](https://developers.google.com/identity/openid-connect/reference) | 固定 worth.oopsbox.cn 回调，服务端 token/JWKS 调用；独立 state、nonce 与 PKCE verifier | 授权 URL 和真实 Google 错误路径另在 lab 检查；成功真人登录待公开发布 |
| [kind v0.33.0](https://github.com/kubernetes-sigs/kind/releases/tag/v0.33.0)、[配置](https://kind.sigs.k8s.io/docs/user/configuration/)、[Calico 3.32 安装](https://docs.tigera.io/calico/latest/getting-started/kubernetes/kind) | K8s 1.36.4、Calico 3.32.2，选择与数据库 Operator 支持区间重合的版本；关闭 kind 默认 CNI；分别约束 Docker 节点和 kubelet 可分配资源 | 实际 Pod 分散、允许/拒绝网络链路，不能把 Docker 容器当成独立物理主机 |
| [CloudNativePG 1.30.0 源码与发布清单](https://github.com/cloudnative-pg/cloudnative-pg/tree/v1.30.0)、[PostgreSQL 18 锁](https://www.postgresql.org/docs/18/explicit-locking.html) | PG 18.4，独立 PVC、同步确认任一份备库、required durability、failoverQuorum；Identity schema owner 与运行/运维账号分离 | 实际迁移发现重复 CREATE SCHEMA 需要数据库级权限，改为先查 schema，再仅在不存在时创建；受限 owner 回归测试覆盖该缺陷 |
| [Squid ACL](https://www.squid-cache.org/Doc/config/acl/)、[PID 文件](https://www.squid-cache.org/Doc/config/pid_filename/) | 只允许指定 Google 域名的 CONNECT；非 root、只读、不缓存、不记录 URL；容器前台运行不写 PID，限制文件描述符及内存 | Google JWKS 允许，其他域名 403；gateway 与未授权 Pod 无法连接数据库或代理 |
| [Nginx HTTPS](https://nginx.org/en/docs/http/configuring_https_servers.html)、[Certbot](https://eff-certbot.readthedocs.io/en/stable/using.html)、[flarectl](https://github.com/cloudflare/cloudflare-go/tree/v0.118.0/cmd/flarectl) | 独立 server_name 和 DNS A 记录，Let’s Encrypt 证书与定时续期；CLI/密钥在仓库外；发布应用仍遵守 dev/CI 门槛 | HTTPS 健康、HTTP 跳转及静态 Swagger 可访问；公网连接原已发布基座 |

依赖精确版本以 [go.mod](../backend/go.mod) 为准；上游清单 SHA-256 和镜像 digest 以 [versions.json](../ops/lab/versions.json) 为准。网络与恢复检查入口见[部署文档](deployment.md#identity-lab)，结论只覆盖实际执行的 Identity 链路。早前小节中“尚未实现”的表述是当时设计记录，不代表本轮状态。

## Content 私有草稿（2026-09-20）

本阶段依据用户明确提出的创建/读取/替换、身份隔离、并发重试与真实链路验收目标。沿用已有 Go 标准库、grpc-go、pgx 和 PostgreSQL，不引入 ORM、通用权限引擎或 Redis。

| 官方来源 / 版本 | 采用与取舍（Agent Self-Claimed） | 可验证判据 |
| --- | --- | --- |
| [PostgreSQL 18 INSERT / ON CONFLICT](https://www.postgresql.org/docs/18/sql-insert.html) | `(author_id, create_key)` 唯一约束与 DO NOTHING，再用下一条 SELECT 查同账号记录；不依赖内存锁，不使用覆盖更新冒充创建重试 | 两个独立进程同键同内容只建一行；同键异内容409，跨账号隔离，编辑后创建重试不回滚 |
| [PostgreSQL 18 explicit locking](https://www.postgresql.org/docs/18/explicit-locking.html) | FOR UPDATE + expectedRevision，在单一 Content 事务中整体替换任务和初始作品；外部鉴权先于事务 | 同版本竞争只有一项成功，其余409；失败不部分改写 |
| [gRPC Retry](https://grpc.io/docs/guides/retry/)；grpc-go 版本见 [go.mod](../backend/go.mod) | 关闭应用层自动重试，不将网络超时当作未执行；创建靠持久幂等键，编辑靠版本及显式读取核对 | 服务重启后同键仍找回；旧版本 PUT 重试409；不盲目用新版本重放旧内容 |

以上官方文档于 2026-09-20 核对。数据聚合、文件暂拒绝、32件/12000字节限额与本地进程测试均是阶段实现选择，不修改 PRD 的可信场景。完整方案及后续边界见 [Content 草稿](backend-content.md)。本地验收 PostgreSQL 为临时编译的官方 18.0，源码 SHA-256 为 `0d5b903b1e5fe361bca7aa9507519933773eb34266b1357c4e7780fdee6d6078`；这不是生产数据库版本升级建议，仓库 CI 与 lab 镜像未改变。
