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

当前计票提案：统计只计仍过审作品上的有效真人当前选择，并列出所有过审作品（含零票）；改选不增加总票数，撤销过审作品的旧选择保留历史而不计入当前结果。作品与人类/agent 两方使用“得票占比”，不沿用成对对抗的平票与胜率字段。失去投票资格后不可首投或改选；目录刷新、token 更换和新增作品均不能恢复资格。完整协议与未来并发验收见 [backend](backend.md)，此次只有契约和设计变更。

## Swagger 文档发布

2026-09-17，用户要求生成 Swagger 文档并部署。交付是现有契约的可浏览页面和 YAML 下载；没有增加业务接口实现。以下实现选择属于 **Agent Self-Claimed**。

| 官方参考 / 版本 | 补全选择 | 验收判据 |
| --- | --- | --- |
| [Swagger UI 安装](https://swagger.io/docs/open-source-tools/swagger-ui/usage/installation/)，`swagger-ui-dist` 5.33.0 | 使用官方独立浏览器资源，精确锁定开发依赖，保留许可证并随页面发布 | 构建可重现；浏览器渲染 OpenAPI 3.1.1 的全部 38 个操作，不请求外部 CDN |
| [Swagger UI 配置](https://swagger.io/docs/open-source-tools/swagger-ui/usage/configuration/) | `supportedSubmitMethods: []` 关闭执行，`validatorUrl: null` 关闭在线校验，展示扩展字段并禁用 URL 配置覆盖 | 实现状态可见，没有 Try it out 或凭据输入，不向外部校验服务发送契约 |
| [Nginx alias](https://nginx.org/en/docs/http/ngx_http_core_module.html#alias) | 在既有网关 `/docs/` 提供独立静态 release，保留应用代理 | 配置校验、资源摘要、浏览器访问和既有健康检查通过，应用 revision 不变 |

只读方式适配当前以 planned 为主的接口契约与公开 HTTP 文档入口。后续开放认证和接口调试须随真实业务实现启用 HTTPS，并重新验证相应权限与副作用；本次不预先实现这些功能。

## 前端 API 环境路由

用户明确要求前端开发走公网 API、部署走内部调用。采用相对 `/api/*` 和本地开发代理是 Agent Self-Claimed：复用现有 Node.js 服务与 [Node.js 24 HTTP request](https://nodejs.org/docs/latest-v24.x/api/http.html#httprequesturl-options-callback)，以流转发请求和响应，不增加运行时依赖；部署继续使用已有 Nginx 内部代理。验收覆盖真实 HTTP 上游、请求参数与请求体、上游故障、生产入口忽略开发配置，以及本地开发入口连接实际公网健康检查。

<a id="backend-design"></a>
## 分布式 Go 后端设计

2026-09-17，用户要求把五个模块的核心类型、状态机和 Proto 写入 [backend 文档](backend.md)。待补缺口是服务间身份传递、数据写入归属、统计副作用与并发、终态与跨服务登记的顺序，以及可验证的内部接口。以下选择均为 Agent Self-Claimed；本次没有安装数据库、迁移后端或实现业务 RPC。

| 官方参考 / 版本 | 采用与差异 | 验收判据 |
| --- | --- | --- |
| [Protocol Buffers proto3](https://protobuf.dev/programming-guides/proto3/)，语法规范 | 有编号的强类型消息、UNSPECIFIED 零值、optional 存在性、oneof、删除字段保留编号/名称；不直接用 ProtoJSON 覆盖现有 HTTP JSON | 整体提取文档 Proto，使用编译器检查所有消息、枚举、引用及服务方法 |
| [gRPC Authentication](https://grpc.io/docs/guides/auth/) | 经认证的服务连接与身份断言；业务服务仍复核主体、用途和权限 | MCP token 不获得网站写权限；不能通过伪造 actor/role 字段调用内部接口 |
| [gRPC Deadlines](https://grpc.io/docs/guides/deadlines/)、[Retry](https://grpc.io/docs/guides/retry/)、[Cancellation](https://grpc.io/docs/guides/cancellation/)、[Status Codes](https://grpc.io/docs/guides/status-codes/)，核实于 2026-09-17 | deadline 传递、有界重试、稳定业务错误；请求取消不等于运行取消 | 响应丢失后用同一业务键找回；业务运行终态持久化，不能以连接关闭代替 |
| [PostgreSQL 18 行锁](https://www.postgresql.org/docs/18/explicit-locking.html#LOCKING-ROWS) | 提案使用服务本地事务、唯一约束和一致锁顺序；先创建资格行再锁定 | 并发首投/首查统计不能绕过永久资格；验证应使用真实数据库和多个进程 |
| [AWS transactional outbox](https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html) | 业务与待发送事件同事务、至少一次投递、消费去重；先用数据库轮询，不要求 AWS 或消息中间件 | 重复/乱序消息不回退状态；提交后进程崩溃仍可继续投递 |

本项目进一步推导两项协作协议：内容下架前先关闭投票目录，云端终态前先封闭内容登记；它们用于跨服务防止迟到写入，不能仅靠 outbox 自动获得。阶段租约与 epoch 阻止旧 worker 结果生效，也不等于供应方计费调用恰好一次。具体 Go/gRPC 运行版本、算法、权限生命周期与额度策略仍待实施时锁定。
