# Identity 身份服务设计

| 项目 | 内容 |
| --- | --- |
| 版本 | v0.3 · 2026-09-19 · Identity 已发布，真人 Google 登录经用户确认 |
| 人类要求 | 先设计再按顺序实现 Identity；Google 登录参考本机 sub2api，入口使用 worth.oopsbox.cn |
| 依据 | [PRD](prd.md) 的 S2～S5、[H3 Google 登录](prd.md#google-login)、[七服务架构](backend-architecture.md)、[OpenAPI](../openapi.yaml) |
| 技术方案归属 | 本文的字段、期限、存储、RPC 补全与基座组织属于 Agent Self-Claimed |
| 当前状态 | Go 实现、9 个 RPC、SQL 迁移和真实 PostgreSQL 测试已完成；公网经私有 TLS 隧道连接本机 kind lab 的双副本 gateway/Identity |

**Identity 回答三个问题：你是谁、你的凭据是否仍有效、你以什么身份调用哪个接口。** 是否能修改某件作品、投某个任务，仍由对应业务服务判断。

阅读顺序：先看核心对象和功能流程；准备写代码时再看接口、[platform 清单](#platform)与验收。本文是身份模块设计依据，RPC 契约源为 [identity.proto](../backend/proto/humanworth/identity/v1/identity.proto)；其余服务设计见[架构划分](backend-architecture.md)，部署统一见[部署与实验计划](deployment.md)。

## 1. 边界：哪些归 Identity

| 归属 | 负责什么 |
| --- | --- |
| Identity | Google 登录验证、稳定账号、网站会话、MCP 凭据、账号状态和角色、内部身份断言、自己的原始审计 |
| gateway | 解析 HTTP、过滤重复或冲突凭据、设置 Cookie、浏览器跳转、传递可信来源信息、把 RPC 错误映射成 HTTP |
| 各业务服务 | 自己的数据权限和规则；例如 Content 判定作品能否访问，Voting 保存账号×任务的永久禁投资格 |
| Moderation | 跨服务审计查询投影；Identity 只保存自己的原始审计 |
| platform | 各进程复用的配置、通信、数据库连接、日志和启停代码；它没有自己的业务进程或账号表 |

Google 客户端 Secret 仅给 Identity。gateway 不向业务服务转发原始 Cookie、Google token 或 MCP token，只携带短期身份断言。账号 ID 不因重新登录、换 token、改邮箱或进程重启而改变；Identity 不读写 Voting 的资格记录。

## 2. 核心对象

### 2.1 要持久化的对象

所有表位于 Identity 自己的 PostgreSQL schema，其他服务通过 RPC 使用它们。跨对象原子操作在这个 schema 内完成。

| 对象 | 主要字段 | 关键约束 |
| --- | --- | --- |
| **Account 账号** | `account_id`、`display_name`、`role`、`state`、`auth_version`、创建/更新时间 | ID 永久稳定；角色只有 USER/ADMIN；状态 ACTIVE/DISABLED；首次 Google 登录只建普通账号 |
| **ExternalIdentity 外部身份** | `issuer`、`subject`、`account_id`、可选邮箱及验证标记、更新时间 | `UNIQUE(issuer, subject)`；Google 的 `sub` 原样、区分大小写；邮箱仅为资料，不作为关联键 |
| **GoogleLoginTransaction 登录流程** | `flow_id`、流程 Cookie 摘要、`state_hash`、`nonce_hash`、加密的 PKCE verifier、OAuth 配置版本、固定回调 URI、状态、`exchange_attempt_id`、到期时间 | 十分钟有效；同一流程最多一次换码；所有副本共享；终态不能重新开放 |
| **LoginFamily 浏览器流程族** | `family_id`、创建时间 | 同一个旧流程 Cookie 并发发起时锁定同一 family，取消旧 pending/exchanging，避免双副本各保留一份旧流程 |
| **CredentialRecord 凭据** | `credential_id`、`account_id`、`kind`、`token_hash`、签发时 `auth_version`、状态、创建/到期/撤销时间 | WEB_SESSION 与 MCP_READ 分开；摘要唯一；明文随机凭据不落库；所有校验读取权威主库 |
| **WebSession 会话附属数据** | `credential_id`、加密的 `csrf_token`、加密密钥版本 | 与 WEB_SESSION 一对一；同一会话重复获取 CSRF token 不会让其他标签页失效 |
| **McpTokenMetadata 凭据附属数据** | `credential_id`、用户填写的名称、`create_request_id` | 与 MCP_READ 一对一；名称不影响权限；`(account_id, create_request_id)` 唯一，供响应丢失后查回记录 |
| **IdentityAudit / Outbox** | 事件 ID、动作、可信操作者、目标 ID、结果、原因、时间、账号版本 | 关键成功变更与审计/待投递事件同事务；不记录凭据、Google 授权码、完整资料或投票统计 |

`WebSession`、`McpTokenMetadata` 是说明不同凭据特有字段的逻辑对象，首版可放在凭据表的受约束列中，不强制多建两张表。Account、ExternalIdentity 与 CredentialRecord 的关联可使用 schema 内外键。

### 2.2 只在调用中使用的对象

| 对象 | 用途 |
| --- | --- |
| **Principal 已验证主体** | `account_id`、角色、`client_kind`、凭据 ID、账号身份版本；普通业务代码使用它，不重新解析 Cookie |
| **ActorAssertion 身份断言** | Identity 签发的不透明字符串；绑定主体、凭据、接收服务、完整 RPC 方法名、用途、签发时间和过期时间 |
| **ServicePrincipal 服务身份** | 从 mTLS 客户端证书取得的 gateway/content 等身份；不能从请求体、任意 Header 或 Pod 名称取得 |
| **GoogleOAuthConfig** | Client ID、Secret 文件引用、预注册回调 URI、允许的网站 Origin、配置版本；来自受控部署配置 |

匿名 Principal 不包含账号或角色，服务主体不能冒充某个用户。账号角色与凭据用途同时生效：管理员的 MCP token 仍然只能用于允许的读取与显式统计访问。

### 2.3 状态与首版参数

| 对象 | 状态变化 | 规则 |
| --- | --- | --- |
| 账号 | ACTIVE ↔ DISABLED；USER ↔ ADMIN | 状态或角色变化时递增 `auth_version`；旧凭据因此失效，恢复账号后也需重新登录 |
| 凭据 | ACTIVE → REVOKED / EXPIRED | 不恢复旧凭据；到期直接按时间拒绝，不等待清理任务 |
| 登录流程 | PENDING → EXCHANGING → SUCCEEDED / FAILED / EXPIRED | 先持久占用，再向 Google 换码；换码失败或结果未知后重新发起 |
| 登录取消/替换 | PENDING → CANCELLED；EXCHANGING → CANCELLED | 合法取消，或该浏览器重新发起并使旧流程失效；旧执行者提交时须重新检查状态 |

首版建议网站会话绝对有效期 **24 小时**、MCP token **30 天**、内部断言 **30 秒**；不做滑动续期或网站 refresh token。登录流程十分钟沿用已有契约，其余期限是可调整的技术默认值。允许小幅时钟误差不延长数据库中会话的有效期；凭据与流程的到期判断使用主库时间。

## 3. 核心功能逻辑

### 3.1 发起 Google 登录

1. 浏览器顶层导航到 `GET /api/auth/google`。gateway 拒绝 Bearer 凭据，调用 `StartGoogleLogin`；已有过期网站会话不阻止重新登录。
2. Identity 检查当前 OAuth 配置，生成独立的随机流程 Cookie、state、nonce 和 PKCE verifier。随机量至少使用 `crypto/rand` 生成 32 字节，PKCE 使用 S256。
3. 在本地事务中保存流程及固定配置版本；请求带有效旧流程 Cookie 时使旧未完成流程失效。并发替换使用行锁和状态条件，旧 EXCHANGING 执行者不能继续提交成功。
4. gateway 设置 `__Host-human-worth-oauth`，使用 `Secure; HttpOnly; SameSite=Lax; Path=/`、十分钟期限且不设置 Domain，302 跳转 Google。只申请 `openid email profile`。

回调 URI 来自部署配置，不能由请求的 Host、redirect_uri 或 returnTo 任意指定。Secret、PKCE verifier 不进入浏览器；state、nonce 是独立随机值，不嵌入账号、角色或敏感资料。

### 3.2 回调、建号与建立会话

| 步骤 | 操作 | 一致性边界 |
| --- | --- | --- |
| A. 解析 | 必须恰有一个 state，以及 code/error 中恰好一个；重复参数拒绝；未知附加参数忽略 | gateway 保留参数出现次数，不能先取第一个值掩盖冲突 |
| B. 占用 | 校验流程 Cookie、state、期限和状态；code 分支原子改为 EXCHANGING，记录随机 attempt ID | 条件更新成功才允许换码；两个副本同时处理时只有一个成功 |
| C. 验证 Google | 在数据库事务外换码；验证 ID token 的签名、允许算法、issuer、aud、exp、nonce 和适用的 azp | 只用固定 Google discovery/JWKS 来源；token 中的地址不参与取钥；换码不自动重试 |
| D. 本地提交 | 再校验流程仍属本 attempt 且未取消/过期；找回或创建账号、写入外部关联、新会话和 CSRF token、撤销当前浏览器旧会话、写审计并将流程置 SUCCEEDED | 一个本地事务提交；失败不能留下已成功账号关联却未完成的半次登录状态 |
| E. 返回浏览器 | 设置新网站 Cookie，以另一条独立 Set-Cookie 清流程 Cookie，303 返回 `/`；前端读取 `/api/me` | 多条 Set-Cookie 分开发送；不把 Google token 或网站会话明文放进 URL、JSON、localStorage |

Google 两种允许的 issuer 写法在完整验证后规范为 `https://accounts.google.com`，以 `(issuer, sub)` 查账号。首次登录按 issuer/sub 获取事务级 advisory lock，再查关联与建号；唯一键作为最终约束。锁冲突或数据库失败明确报错，不自动重做 Google 换码。账号与关联在同一事务创建，不留下孤立重复账号。稳定关联及 issuer/sub 规则依据 [Google OIDC](https://developers.google.com/identity/openid-connect/openid-connect)。

已有账号需锁定并重新检查状态和版本；禁用期间不能靠新登录创建替代账号。邮箱和显示名变动只更新资料，不更新关联，不授予管理员。没有显示名时使用普通默认名称，不把邮箱作为公开显示名。

`error=access_denied` 也必须先通过流程 Cookie/state 校验，且流程仍为 PENDING，才置 CANCELLED 并返回 `/?login=cancelled`；其他供应方错误转为脱敏错误。失败或取消不主动撤销旧网站会话。若 D 已提交但响应丢失，新的会话轮换已生效；再次登录仍找回同一账号，不声称旧会话一定有效。

### 3.3 网站请求认证与内部鉴权

1. **gateway 按服务端路由表选用途。** 决定是否允许匿名、目标服务和 RPC 方法。用户不能自己提交 audience、角色或 `allow_anonymous`。同时带网站会话与 Bearer 返回 400；无效凭据返回 401，不降级匿名。
2. **Identity.ResolvePrincipal 校验凭据。** 查询主库中的凭据和账号：类型、到期、撤销、账号状态、签发版本都要通过。网站写请求再验证 CSRF token 和精确 Origin；二者缺失或不符返回 403。受信代理只转交经过明确规则处理的来源信息，客户端伪造 Forwarded Header 无效。
3. **Identity 签发 ActorAssertion。** 首版采用成熟 JWS 库、固定 HS256 和 `kid`；签名密钥只在 Identity 副本之间共享。内容含账号/凭据/版本、issuer、audience、完整 RPC 方法、client_kind、签发/到期时间；明确要求这些字段存在，拒绝未知 key ID 和算法。它不进入客户端、日志或事件。
4. **业务服务验证两种身份。** mTLS 确认调用服务获准调用该方法；再用 `VerifyActor` 验证断言。audience 从调用 VerifyActor 的证书身份取得，RPC 方法由服务端拦截器取得，随后检查签名、期限、用途及主库中的当前账号/凭据状态。首版不缓存“允许”结果。
5. **业务服务执行自己的规则。** 例如 Voting 检查永久禁投资格，Moderation 检查管理员；身份认证成功不等于业务操作自动获准。

匿名只在没有凭据且路由明确允许时成立：签发绑定公开读取方法的匿名断言，VerifyActor 验证签名、服务和方法，不查询不存在的账号/凭据；匿名断言不能用于网站写入或管理。登录主体的账号与凭据则在同一主库查询快照内核对，断言中的账号、kind 和版本必须与记录一致。

Identity 的认证 RPC 使用服务身份白名单，不能安装一个会递归调用自身 `VerifyActor` 的通用拦截器。`GetCurrentSession`、`LogoutCurrentSession` 在 Identity 内复用本地验证函数。断言不是一次性业务命令：写请求的防重复仍归业务幂等协议。

服务调用内部协作方法时使用该方法明确允许的服务身份，不把用户 account_id 参数当作授权证明。需要再次按用户授权的跨服务调用，必须设计受限断言委托契约；首版不允许直接把只面向 Content 的断言转发给 Asset，也不开放任意重新签发权限。

**撤销的保证范围**：撤销提交后开始的新认证会拒绝旧凭据；已经通过校验的在途业务请求可能完成。不能用“断言只活 30 秒”声称可以取消所有在途操作，尤其不能以此替代 Voting 自己的事务规则。

### 3.4 当前会话、CSRF 与退出

- `GET /api/me` 只接受网站会话，返回已有 Session schema 中的账号资料、角色和当前会话 CSRF token；响应 `no-store`。GET 不改变任务投票资格。
- CSRF token 每会话生成并加密保存，`/api/me` 可重复读取；网站写入要求 `X-CSRF-Token` 与允许的 Origin 同时通过。SameSite 是补充约束。依据 [OWASP CSRF](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)。
- `POST /api/auth/logout` 验证网站会话、CSRF 与来源后，在事务中撤销当前凭据并记审计；提交成功才清 Cookie。重试遇到已撤销会话仍按当前 OpenAPI 返回 401。
- 退出只影响当前网站会话。其他设备、MCP token、Google 账号和任务投票资格保持各自状态；没有前端通过 GET 或仅删除 Cookie 就宣告服务端退出的路径。

### 3.5 MCP 凭据生命周期

既有契约要求 MCP token 属于用户本人。以下生命周期已经实现，对应 HTTP 与 RPC 同步记录在 OpenAPI 和 Proto：

| 操作 | 核心逻辑 |
| --- | --- |
| 创建 | 仅已登录网站用户，验证 CSRF/Origin；账号来自 Principal；生成 32 字节随机 token，事务内存摘要和元信息；明文只在首次成功响应返回 |
| 列表 | 只查本人；返回凭据 ID、名称、创建/到期/撤销状态和创建请求 ID，不返回明文或摘要 |
| 撤销 | 只撤销本人凭据；MCP token 自己不能调用管理接口；同一撤销目标重复操作稳定返回已撤销结果 |
| 轮换 | 创建新 token，用户更新客户端后显式撤销旧 token；两者始终绑定同一个账号，资格不会重新建立 |
| 响应丢失 | 按 `create_request_id` 查回元信息；无法再次显示明文时撤销该凭据后重新创建；重试同一个 request ID 不生成第二个凭据 |

MCP 工具只开放 `list_tasks`、`get_task`、`view_task_statistics`。最后一个必须显式确认，并由 Voting 先落永久禁投记录再返回统计；“内容读取 token”不代表这个操作无副作用。HTTP 中允许 MCP 的辅助读取不自动成为 MCP 工具。Google access token、网站会话和 MCP token 不能相互替代；这里也不把个人 token 方案宣称为已经实现完整 MCP OAuth 授权服务器。

### 3.6 角色与账号状态管理

首次登录不授予管理员，Google 邮箱域名也不授予管理员。实验阶段用受限运维 Job 调用 Identity 自己的管理用例，输入已存在的账号 ID、目标状态/角色和预期 `auth_version`，审计记录目标角色与状态；操作者来自运维身份。Job 使用 Identity 的代码和专用权限，不给其他业务服务写账号表的入口。

管理用例锁定账号、检查预期版本，更新状态/角色并递增 `auth_version`，同事务记录审计。凭据保存的签发版本与新版本不符时全部失效；账号恢复后不复活旧凭据。并发签发凭据也要锁定同一账号并读取当前版本。首次管理员需要人工指定已验证账号；测试只操作合成账号，不自动授予真实用户管理员。面向网站的账号管理 UI/API 后续另行设计。

## 4. 多副本下怎样保证正确

Identity 沿用同一套 lab 配置：两个 Pod，连接同一 PostgreSQL 主端点。节点变化不会改变用户账号或会话。

| 情况 | 处理规则 |
| --- | --- |
| 发起登录在副本 A，回调落到 B | 流程、verifier、会话都在共享主库，配置/解密密钥版本对齐；无需粘滞会话 |
| 两份相同回调同时到达 | `PENDING → EXCHANGING` 条件更新仅一份成功；另一个返回流程无效，不能二次换码 |
| 换码时进程崩溃 | 记录仍为 EXCHANGING，到期后失效；重新发起登录，不跨进程接着重放授权码 |
| 已占用流程被替换或到期 | 最终提交重新核对状态、期限、attempt ID；迟到结果不得建立会话 |
| 两个有效登录同时首次建号 | 外部身份唯一键和整体事务保证一个稳定账号；两个不同设备可以各自得到独立会话 |
| 数据库提交超时 | 结果可能未知；按 flow/request ID 确认持久结果，不假定回滚；凭据只存摘要，不能重新返回旧会话明文，必要时重新登录；不重复外部换码 |
| 主库/Identity 不可用 | 返回 503；不使用过期缓存认证、不按匿名放行；已有公网健康检查不能替代此验证 |
| Google 暂时不可用 | 新登录失败，已有本地会话仍可验证；不把 Google 可达性设成整个服务的存活探针 |
| 密钥轮换或滚动发布 | 两副本先同时具备新旧 key ID，再切换新签发；旧解密/验签 key 保留到对应数据失效或重加密完成 |

涉及同一事务中的多个账号（例如切换账号并撤销旧会话），按账号 ID 排序加锁，再锁定本次相关凭据；登录 family 和流程行在账号锁之前锁定。审计与 outbox 最后追加。所有外部 HTTP/RPC 都在数据库事务之外；当前事务错误直接返回，不自动重试 OAuth 或写 RPC。

凭据摘要使用高熵随机 token 的 SHA-256；PKCE verifier 和 CSRF 值使用标准 AEAD 加密并绑定记录 ID/字段用途，密钥来自 Secret 文件。不要自写 OAuth/JWT/JWKS 验证器；已采用 `golang.org/x/oauth2`、`coreos/go-oidc/v3` 与 `golang-jwt/jwt/v5`，版本见 [go.mod](../backend/go.mod)，并覆盖 Google issuer 兼容行为。库完成签名等通用校验后，Identity 仍显式检查 nonce 和适用的 azp，不能假定库替代业务绑定。内存中的 Google token 用后释放，不存储 refresh token。

流程进入终态时清除加密 verifier；过期流程和凭据按小批次清理，多个副本的清理动作必须可重复执行。到期拒绝在请求校验中完成，清理延迟不会延长有效期。审计与账号关联按独立保留策略保存，不随会话过期删除账号。

## 5. 接口边界与 Proto 落地范围

### 已实现的九个 RPC

| RPC | 调用方 | 输入 / 输出重点 | 对外入口 |
| --- | --- | --- | --- |
| `StartGoogleLogin` | gateway 服务身份 | 可选旧流程 Cookie → 授权 URL、流程 Cookie、期限 | `GET /api/auth/google` |
| `CompleteGoogleLogin` | gateway 服务身份 | 流程 Cookie、state、code/error oneof、可选旧网站 Cookie → 固定回跳路径、可选新会话 Cookie | `GET /api/auth/google/callback` |
| `ResolvePrincipal` | gateway 服务身份 | 网站凭据或 MCP token oneof、服务端路由用途 → Principal＋ActorAssertion | 内部 |
| `VerifyActor` | 白名单业务服务 | ActorAssertion＋服务端 RPC 方法 → 当前有效 Principal | 内部 |
| `GetCurrentSession` | gateway，网站本人 | 已验证网站主体 → 账号、角色、CSRF token | `GET /api/me` |
| `LogoutCurrentSession` | gateway，网站本人 | 已验证主体及写入用途 → Empty | `POST /api/auth/logout` |

敏感登录返回值只供 gateway 设置 Cookie，不能自动用 ProtoJSON 透传。内部方法默认拒绝未列明的服务身份；健康检查独立配置访问规则。

管理概览由未来的 gateway 聚合；`ListAuditEvents`、`IngestAuditEvent` 属于 Moderation 的审计投影，不放入 IdentityService。上述六个方法和下面三个 MCP 生命周期方法均已生成 Go 接口并实现。

MCP 管理已补 `CreateMcpToken(name, create_request_id)`、`ListMyMcpTokens(cursor)`、`RevokeMcpToken(credential_id)`，都以网站 Principal 决定所有者；列表返回元信息，创建额外返回一次明文。账号运维暂用本模块 Job 用例，不新增公开 RPC。公开入口为 `POST/GET /api/me/mcp-tokens` 与 `DELETE /api/me/mcp-tokens/{credentialId}`；MCP 工具本身仍待 Content/Voting 等模块实现。

错误延续既有映射：格式/流程问题 400，凭据无效 401，来源/CSRF/用途/角色禁止 403，依赖不可用 503。未知账号与无效凭据不返回可枚举的资料；错误只包含稳定原因和 request ID，不包含 Google 原始响应体。回调所有响应使用 no-store/no-referrer。

<a id="platform"></a>
## 6. 为 Identity 实现哪些 platform 公共基座

**先实现能支撑 Identity 和 gateway 的公共能力，再随第二个业务服务提取真正重复的代码。** 已在 [platform](../backend/internal/platform) 实现下表所需公共能力，既有 Node.js/Python 基座与部署保护继续保留。

### 6.1 第一批：运行 Identity 必需

| 能力 | 最小实现 | 放在哪里 / 完成判据 |
| --- | --- | --- |
| **配置与密钥加载** | 类型化配置；环境变量与只读 Secret 文件；必填、地址、期限和配置版本校验；错误不带密钥 | platform 提供加载工具；Google 字段属于 Identity。缺配置明确失败，双副本读取一致版本 |
| **服务生命周期** | `context`、系统信号、启动/就绪/存活检查、gRPC graceful stop、关闭连接池、限时退出；内部健康入口供探针使用 | platform 管启停；schema/key 不就绪不接业务；SIGTERM 停接新请求后处理在途工作；健康入口不开放业务方法 |
| **gRPC 通信** | 服务端/客户端初始化、mTLS、证书服务身份、deadline、panic 转内部错误、消息大小限制；K8s DNS 与 round_robin | platform 提供机制，各模块登记允许调用者；错误证书、伪造身份被拒；合法调用能落到两个副本 |
| **数据库连接与迁移执行** | `pgxpool` 连接主端点、连接上限、context 超时、事务回滚；迁移独立 Job、版本表与锁 | platform 复用连接与迁移机制；Identity 拥有 SQL、表与事务。两个 Pod 不竞相修改 schema，滚动版本兼容 |
| **请求上下文与错误** | request ID、trace context、已验证 ServicePrincipal/Principal 的 context 存取；gRPC code＋稳定 reason | 不把调用者自报账号当主体；gateway 单独维护 HTTP 映射；Identity 自己执行账号/凭据策略 |
| **日志和基本遥测** | `log/slog` JSON、OpenTelemetry 的 trace/指标接入；记录方法、耗时、错误率、连接池；敏感字段白名单 | 已有 gateway→Identity→数据库查询/Google 换码 span、JSON 日志、Prometheus 指标；OTLP 接收端尚未部署，不采集 SQL 参数、Cookie、token、回调 query |
| **入口资源保护** | 有界请求大小、并发和超时；登录入口基础限流；Google HTTP 客户端复用连接并限制响应大小 | 先做进程级保护并明确双副本会扩大总限额，不能宣称全局限流；超限及时拒绝，无无限排队/重试 |

目录可采用 `backend/internal/platform/`，用小包承载已经被多个入口使用的机制；业务代码在 `backend/internal/identity/`，HTTP/Cookie 适配在 gateway。对应目录均已有真实调用方；schema 迁移执行仍留在 Identity，等第二个模块需要时再提取。

建议初始内部普通 RPC 总期限 2 秒、Google 登录回调总期限 15 秒，Google 换码最多占用其中 10 秒；子调用始终受父 deadline 限制。每个 Identity 副本连接池先设上限 4，双副本共 8，滚动增加一份时为 12，迁移连接另算；这些是实验起点，后续按观测调整。

### 6.2 同步需要的工程支撑

| 项目 | Identity 阶段做到什么 |
| --- | --- |
| Go/Proto 构建 | 建一个 Go module，锁定 Go、Buf 编译器、生成插件及依赖版本；生成代码检查、格式检查、`go test`、`go test -race` 接入 CI |
| 真实数据库测试 | 临时 PostgreSQL 与隔离 schema；验证唯一约束、事务回滚、双进程竞争、撤销和失效；不能只用内存仓库替代 |
| HTTP 与 Google 联调 | 测试 OIDC 供应方覆盖签名/JWKS、issuer/aud/nonce、PKCE、超时和重放；再用真实 Google 测试账号走浏览器回调 |
| lab 部署 | 独立镜像、两个 Pod、服务证书、Secret 挂载、主库连接、readiness、允许的网络调用；Identity 的 Google HTTPS 出口单独放行 |
| 认证入口 | 正式 HTTPS 域名、预注册 callback；开发代理仍走公网，但本地 HTTPS 浏览器入口、允许 Origin、回调与 Cookie 必须成套匹配 |

同一 Google 客户端可以有允许的多环境回调，但只能从服务器配置选择。当前 Start RPC 没有环境选择字段：第一次联调先固定一个入口；需要同时支持本地与正式浏览器入口时，先给可信 gateway→Identity 契约补充受限的配置选择机制，不让浏览器任意指定 callback，也不靠不可信 Host 切换身份环境。

### 6.3 第二批：接入其他模块时再实现

- **Outbox 投递 / Inbox 去重**：Identity 先在本地事务保存审计和 outbox；Moderation 接入时实现投递与查询投影。登录正确性不依赖事件到达或异步撤销缓存。
- **统一鉴权接入代码**：第一份业务服务接入时复用 mTLS 调用者检查、VerifyActor 客户端和上下文存取；各服务的权限规则留在本模块。
- **必要的跨服务用户委托**：具体调用链确有需要时定义原接收服务、目标服务、方法与用途限制，再增加委托 RPC；不以共享签名密钥替代边界设计。

Google 换码、账号关联、会话/CSRF、MCP 用途与角色变更都是 Identity 业务逻辑，不抽成通用 platform 登录框架。Redis、消息队列、服务网格、通用 Repository、插件系统和动态配置中心不属于第一批依赖。

<a id="sub2api-reference"></a>
## 7. sub2api 参考与后续 Google 配置

用户指定的源码位于 `/home/oops/services/sub2api`，本机版本文件为 **0.1.155**。本次检视的工作副本没有可解析 HEAD，因此按文件 SHA-256 记录来源，详见[参考记录](references.md#identity-design)。没有直接复制代码或引入该项目的业务依赖。

| 参考位置 | 学习内容 | Human Worth 的适配 |
| --- | --- | --- |
| `backend/internal/handler/auth_email_oauth.go` | Google start/callback、十分钟 state Cookie、服务端换码、配置读取与错误分流 | gateway 只处理 HTTP；换码与身份确认集中到 Identity；增加共享一次性流程、PKCE 和 ID token 验证 |
| `backend/internal/service/auth_email_oauth_auto.go` | 外部身份查询、账号状态检查、建号与凭据签发 | 仅按 issuer/sub 找回账号；不采用邮箱自动关联、密码注册、邀请或赠送额度流程 |
| `backend/internal/service/setting_oauth.go` | 配置默认值与存储设置合并、必要参数校验 | 首版用部署配置和 Secret 文件；不依赖另一套应用数据库实时读取配置 |
| `backend/internal/handler/auth_email_oauth_test.go` | 已有账号登录、首次注册及回调结果的测试组织 | 改为验证稳定账号、HttpOnly 会话、多副本回放与撤销；源码阅读不等于测试已运行 |

源码中的 Google 路径使用 access token 请求 UserInfo，并将平台 access/refresh token 放入前端跳转 fragment；Human Worth 沿用已定的 OIDC 校验与服务端会话契约。该差异也使平台无需复制 sub2api 的前端 token 持久化逻辑。

sub2api 的 Google 登录设置名是 `google_oauth_client_id`、`google_oauth_client_secret`、`google_oauth_redirect_url`；配置组 `google_oauth` 提供基础值。用户后续明确本轮只学习登录逻辑和实现，密钥另行提供；本设计不依赖取得现有密钥。

Human Worth 实现时使用 `GOOGLE_OAUTH_CLIENT_ID`、`GOOGLE_OAUTH_CLIENT_SECRET_FILE`、`GOOGLE_OAUTH_REDIRECT_URI` 等配置，Secret 只读挂载到 Identity。需要的是 Web OAuth 客户端凭据；Google/Gemini API key 或服务账号私钥不能代替网页登录 Client Secret。即使复用已有客户端，也须为 Human Worth 登记精确回调并核实授权配置；本轮未修改 sub2api 或 Google 控制台；已将用户提供的 Web 客户端凭据保存到仓库外私有文件，并通过 Secret 挂载到 lab Identity。

## 8. 实施顺序与验收

1. **定 Identity Proto 与表迁移**：按九个 RPC 编写接口，校对数据唯一键、敏感返回值和 gateway 映射。
2. **做第一批 platform 与最小入口**：能启动、健康检查、mTLS 通信、连接主库、记录脱敏日志；保留现有 Node.js 公网服务。
3. **做 Google 登录闭环**：先写状态/并发测试，再实现发起、回调、会话和退出；随后验证真实 Google 浏览器登录。
4. **补角色变更与 MCP 生命周期**：完成对应管理契约、版本失效、用途校验与本地审计。
5. **双副本验收**：请求交替落到不同副本，验证下表；再接 Content，实现第一条真实业务鉴权链路。

| 验收目标 | 必须观察到的结果 |
| --- | --- |
| 首次 / 再次登录 | 首次一份普通账号；再次及邮箱变化仍是同一 ID；并发首次登录无重复账号 |
| Google 验证边界 | 错误签名、issuer/aud/nonce/verifier、过期 token、伪造或重复 state、取消分支缺绑定全部被拒 |
| 双副本与失败恢复 | 跨副本回调成功；重复回调只一次换码；崩溃/替换/过期后的迟到结果无效；提交丢响应不造成重建账号 |
| 会话与 CSRF | 安全 Cookie、独立 Set-Cookie、旧会话轮换；缺 CSRF/错误 Origin 拒绝，GET 不退出；多标签页读取 CSRF 不互相失效 |
| 撤销、角色与账号 | 撤销/禁用/角色变更提交后，新认证拒绝旧版本；重新启用不复活旧 token；并发签发与管理变更顺序明确 |
| 服务调用 | 无证书/错误服务/错误 audience 或方法/伪造角色被拒；Identity 认证链不递归；合法调用成功 |
| MCP 用途与稳定资格 | 同账号网站与 MCP 主体一致；MCP 不能写入/管理；显式统计永久禁投，换 token 后仍不可投；此项须与 Voting 集成验证 |
| 密钥与观测 | 两副本密钥轮换兼容；日志、trace、错误、镜像及 Git 不包含真实凭据；新登录失败不拖垮已有会话认证 |
| 部署与依赖故障 | Pod 切换会话保留；主库故障不降级放行；Google 断连只影响新登录；真实 HTTPS 代理链保持 Cookie/Origin/callback 一致 |

上表是完整验收要求，实际证据分层记录在下文；模拟供应方测试和 Pod 就绪不能替代真实 Google 用户登录，也不能证明尚未实现的 Voting 资格规则。

历史记录（2026-09-18，仅文档）：`python3 scripts/check_docs.py`、`git diff --check` 退出码均为 0；六个 RPC 的职责与 HTTP/内部入口已核对，sub2api 参考文件指纹可复核。文档整理后接口设计保留在本文，独立 Proto 尚待编写；未运行 Google 登录或新增软件测试。


## 9. 实现与验证入口

- [Go 工程说明](../backend/README.md)：生成 Proto、检查与本地测试命令。
- [登录与账号代码](../backend/internal/identity)、[HTTP 适配](../backend/internal/gateway)、[迁移 SQL](../backend/internal/identity/migrations/001_identity.sql)。
- [lab 安装与故障验证](deployment.md#identity-lab)：真实双副本、数据库、网络权限和运维 Job。

实现固定网站会话 24 小时、MCP token 30 天、ActorAssertion 30 秒、登录流程 10 分钟。单 gateway 每分钟允许 60 次登录发起；两副本合计上限随副本数变化，不是全局或每用户配额。普通 RPC 总期限 2 秒，回调 15 秒，Google HTTP 10 秒；单进程最多 64 个在途业务请求。每 Identity 连接池最多 4 条连接，滚动期间 3 份合计最多 12 条。

过期 web 会话保留 7 天后清理；MCP 元信息与撤销记录保留以维持创建请求 ID 的防重语义。审计先保存在本模块的 outbox 表，尚未向 Moderation 投递。其余模块在路由策略表中的 RPC 名称是待对应模块定稿的权限用例，不表示这些业务接口已经实现。

2026-09-19：`go vet ./...`、`go test -race -tags=integration ./... -count=1 -timeout=120s` 通过。测试使用隔离 PostgreSQL、两个真实 mTLS gRPC 服务器、两个 HTTPS gateway，以及测试专用 OIDC HTTP/JWKS 供应方；覆盖并发首次登录、回调占用/替换/迟到、签名与 nonce/PKCE、CSRF、撤销、权限版本、MCP 防重、受限迁移/运行账号、密钥重叠及数据库断连拒绝。生产程序没有切换到测试供应方的配置开关。

用户提供的 Google 凭据已配置，接口已从受保护 dev 发布，用户于 2026-09-19 确认“登录成功，能看到账号”。当前入口固定为 `https://worth.oopsbox.cn`；本地 HTTP 开发代理可以读取公网 API，但不能声称支持该域名 Cookie 的本地登录。需要完整本地登录时再登记一套可信 HTTPS 回调与 Origin 配置。
