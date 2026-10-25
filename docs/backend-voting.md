# Voting 第七阶段：规划历史与本地实现

日期：2026-10-25。**第1～12节为原始规划历史，原用户内容保留；第13节起为实施定稿和实际验收。** 基础投票已随 dev `1fe4d3f` 发布，本次增加结论及监督数据；当前范围和九模块发布流程见[后端补全记录](backend-completion.md)，不从历史本地记录推断生产承诺。

本地后续补全已实现结论／翻转、Challenge 监督快照和 Discovery 分类读取，见 [当前补全记录](backend-completion.md)。下文第七阶段验收保留原始上下文，不代表后续新增功能仍未实现。

## 1. 依据、基线与授权

**Human Design**：用户要求封存第六阶段，再规划 Voting。产品依据为 [PRD](prd.md) 的 S1/S3/S5/H1、FR-05 及既定不变量：完整过审作品单选；显式查看统计先永久关闭稳定账号在该任务的后续投票资格；网站与MCP共用记录；管理员不豁免；普通阅读和追加作品不影响资格。

**Agent Self-Claimed**：下述阶段拆分、表/方法名称、目录栅栏、锁序、幂等和验收组织均为建议；不把当前技术提案改写为新的人类产品要求。改选、撤销作品不计入当前统计等细则沿用现有 [OpenAPI提案](../openapi.yaml) 和 [成熟参考](references.md#task-wide-voting)，实现前须以并发测试验证。

第六阶段封存：`backend/feat-additional-entry-review@b434a9eeffc016f97c6342c62c888d87b2617a6f`。本轮fetch后，origin/dev与upstream/dev均仍为 `dc8e5758926b685e8bc4152190ddf82fce7abbbd`，即第六阶段尚未进入dev。本规划在封存后单独编写，不夹入第六阶段提交。

优先等第六阶段经授权PR合入，再从最新dev建Voting任务分支；如提前实施，须从包含b434a9e的基线建立依赖分支并明确依赖，不能丢掉追加作品模型。squash合入后按实际文件树识别包含关系，不重复移植。本轮不创建新分支、不实现服务、不修改正式Proto/生成代码/OpenAPI/迁移/部署清单，不推送、开PR、合并或部署。

## 2. 实际起点与不能直接复用的部分

- 已有 [Voting Proto](../backend/proto/humanworth/voting/v1/voting.proto) 只有Challenge受限监督快照的两个RPC及生成代码，没有网站投票业务实现。保留这些契约，但不把fake联调当真实实现。
- 尚无cmd/voting、internal/voting及独立schema迁移；Gateway目前没有Voting客户端或实际四个HTTP处理器。
- [OpenAPI](../openapi.yaml) 已规划资格、ballot、vote、statistics-access；contract已登记CastVote与ViewTaskStatistics策略，其descriptor例外仍属planned。新增正式方法后同步移除相应例外。
- Content已拥有初始/追加作品冻结投影、真实作者、稳定ID及catalog_revision；当前作品分页不是完整ballot，不能循环拼页冒充同一目录快照。
- 第六阶段追加批准/作品撤销会更新catalog_revision；当前任务下架路径主要改变展示revision和state，**不能只检查catalog_revision而遗漏任务下架**。
- 当前Content批准/收紧均在本地事务直接生效，尚无Voting目录栅栏。事先查询一次Content、异步同步投影或单独增加RPC都不能自动消除“检查后目录变化、随后写票”的窗口。

## 3. 首个交付范围

目标链路：

```text
网站登录用户 → 读取全部候选及本人选择 → 单选/改选
                           ↓
网站或MCP明确确认查看统计 → 永久禁投提交 → 读取统计
                           ↓
换会话、换token、重启、新增作品后仍不可首投或改选
```

不实现Discovery推荐/热度、评论、完整MCP传输、Challenge训练数据生产、自动防刷系统或领先/翻转算法。没有可信算法时conclusion保留undetermined，不按票数随意宣布领先；明确这是首切片未实现的结论能力。基于稳定认证账号计票不等于证明每个账号对应唯一真人，抗多账号作弊仍是独立工作。

## 4. 权威与调用方向

| 模块 | 唯一责任 |
| --- | --- |
| Identity | 稳定账号、凭据用途、当前会话与角色校验，不存投票资格 |
| Content | 作品/任务权威展示状态、完整冻结候选、目录版本及变更意图 |
| Voting | 账号×任务永久禁投、完整目录副本/写票栅栏、ballot、当前选择、回执及计票 |
| Gateway | 严格凭据/请求解析、方法断言、CSRF、HTTP映射，不执行计票事务 |

各服务独立schema与owner/runtime，无跨schema SQL/FK。Voting不逐作品调用Asset或Identity；不复制附件字节、不返回长期公开URL。公开文件仍走Asset→Content关系校验。

常规读取/写票采用固定次数身份校验与一次Content完整目录/状态核对；本地事务只比较已验证的版本/hash和Voting栅栏，不在事务中等待RPC。目录变更的额外通信发生在变更流程，不随ballot中的作品数量增长。精确RPC次数待断言复用和真实调用图核对，不提前虚报。

## 5. 数据草案与不变量

下列为语义记录，不要求机械按每行建表；7A给出最终DDL和实际索引。

| 记录 | 约束 |
| --- | --- |
| catalog_gate / catalog_snapshot | task唯一；权威正文版本、作品目录版本、展示版本、控制generation、open/frozen/closed、完整集合hash/数量；同版本异hash拒绝 |
| account_task_guard | `(account_id,task_id)`唯一，作为首投/改选与统计关闭的共同串行化锁 |
| statistics_access | `(account_id,task_id)`唯一，首次closedAt不可变；建议独立只追加事实，runtime无UPDATE/DELETE，不能通过普通upsert重开 |
| ballot | 服务端ID，绑定account/task/generation/目录版本/hash/完整候选集合；持久化或可验证但不可伪造，有限生命周期；过期/跨账号不得投票 |
| current_vote | `(account_id,task_id)`唯一；entryId、revision、首次及最近时间；改选替换，不增加一票 |
| vote_receipt / 最近请求摘要 | 绑定ballot/entry/expectedRevision；响应丢失找回；旧重试不能覆盖后来的选择；禁投检查先于成功重放 |
| catalog_transition/outbox/inbox | 持久变更ID、目标版本、generation、摘要、步骤、租约及确认；可跨进程恢复，不能靠内存锁 |

账号从VerifyActor取得，不接受accountId/role/票数。缺少禁投记录不代表允许绕过身份、任务状态或目录栅栏；任何服务异常都不能把“未知”解释为可投。

统计必须列出该目录下全部候选（含零票），totalVotes等于各作品票数和、也等于humanVotes+agentVotes；零票时比例NULL。历史选票保留，但已撤销作品不计入当前统计；不给普通响应夹带此统计。

## 6. 最重要的设计门槛：目录变化与写票原子边界

### 明确拒绝的捷径

- 不用浏览分页拼接ballot；不把多次SQL当同一数据库快照。
- 不以“CastVote开始时问过Content”证明提交时目录仍新鲜。
- 不把未经栅栏保护的异步outbox、TTL缓存或签名ballot当成最新目录保证。
- 不用跨服务数据库锁、数据库事务中RPC或进程mutex补洞。

### 推荐技术方案：先冻结Voting写票，再提交Content变更

借鉴现有持久意图/outbox/回执，用每任务可恢复状态机落实架构原有的收紧协议。**以下是方案草案，完成7B并发与崩溃验证前不可开放写票。**

1. Content短事务保存一个串行的目录变更意图及目标generation/outbox；权威目录尚不提交改变，避免同任务两个变更相互覆盖。
2. 事务外调用Voting.PrepareCatalogChange。Voting锁本地catalog_gate并持久置frozen，等待本地已进入提交临界区的旧写票结束，再确认prepare；prepare成功是关闭旧写票的线性化点。
3. Content收到或查回prepare回执后，在新短事务核对同一意图并提交权威增量/下架/撤销及目标完整快照/outbox。审核应用此时仍须区分“本地状态已更新”和“跨服务协议已完成”，不得伪称最终完成。
4. 事务外将确切目标快照同步给Voting；完整性、版本、generation及摘要一致才激活新目录。下架使用closed墓碑而非重新open；Voting成功后持久确认，Content完成意图并允许下一变更。
5. Prepare/Activate/查询回执均幂等；迟到旧激活不能覆盖更高generation或closed状态。Content中途失败后保持frozen并恢复既有意图，不凭超时自行重新开放旧目录。

initial批准、单件追加批准、旧受信维护入口、作品撤销、任务下架及任何恢复开放路径都要审计，不能存在绕过栅栏的旧写入口。任务state变化即便未改变catalog_revision，也必须推进控制generation并进入该协议。错误客户端不能任意构造“开放目录”命令；mTLS仅允许Content，服务端核对已准备意图。

公开展示在意图期间的语义必须在7A写清：收紧中拒绝受影响对象的新授权，不以旧缓存放行；新增批准的作品在最终接纳前不提前公开。网络故障时保持收紧/待处理，只有完整回执后才能称下架/撤销完成。已提交的旧在途投票与已发送字节不追溯撤销；Voting本地prepare锁保证冻结确认之后不会再提交旧generation写票。

这会改变Content部分审核完成语义，需同步Moderation回执和升级门槛，不能只改Voting。第六阶段既有行为应在Voting尚未启用时保持；启用前必须证明所有Content实例兼容并完成完整目录引导，没有旧实例继续无栅栏修改目录。

## 7. 投票与统计永久关闭的本地事务

推荐一致锁序：目录gate → 账号×任务guard → 当前选票/回执。目录变更使用与写票冲突的锁；普通不同账号的写票可评估共享目录锁，避免无必要地全任务串行，具体锁模式必须真实验证。

### CastVote

1. 事务外验证网站身份、CSRF、任务当前权威状态和完整目录标识。
2. 短事务锁gate和guard，复查open/generation/版本/hash及永久禁投事实。
3. 已禁投一律403，包括旧成功投票重放；不能先读回执返回200绕过关闭。
4. 确切最近成功请求且当前选票仍匹配时只找回；过时expectedVoteRevision、新旧目录不符或同幂等请求异内容409，不回退较新选择。
5. 检查entry属于完整且有效集合，首投revision=1，改选递增；选票及回执同事务提交。不返回统计。

### statistics-access

1. 网站要求CSRF；MCP使用独立Bearer用途；请求必须明确 `permanentlyCloseVoting=true`，否则422且不落禁投事实。
2. 事务外确认身份及当前任务可见性，再锁相同gate/guard，插入或找回首次禁投记录并提交。与写票按该本地串行顺序决定先后。
3. 提交成功或通过独立新连接确认禁投事实已经提交后，才进行统计查询。COMMIT结果未知且不能确认时只返回错误，绝不释放统计。
4. 用后续独立的一致数据库快照生成目录和计票；可用单SQL或明确隔离级别，不能在默认隔离下多条查询拼出不同瞬间的目录/票数。统计开始前重新核对必要的Content状态，RPC不在持锁事务中。
5. 统计生成失败允许503，已经提交的禁投保持；重试不改变closedAt，不添加选票，不要求网站/MCP各关闭一次。

若写票先提交则选择保留，随后关闭；若关闭先提交则写票拒绝。关闭后的该题仍可普通阅读及追加作品，其他题不受影响。资格GET是无副作用查询，ballot GET可创建投票上下文但绝不关闭永久资格。

## 8. HTTP / Proto / 权限计划

| HTTP既有planned入口 | 建议Voting RPC | 凭据 |
| --- | --- | --- |
| GET `/api/tasks/{taskId}/voting-eligibility` | GetVotingEligibility | 网站/MCP；非匿名；无票数 |
| GET `/api/tasks/{taskId}/ballot` | GetBallot | 网站；无票数，永久禁投403 |
| PUT `/api/tasks/{taskId}/vote` | CastVote | 网站＋CSRF；MCP拒绝 |
| POST `/api/tasks/{taskId}/statistics-access` | ViewTaskStatistics | 网站＋CSRF或MCP＋显式确认；管理员同规则 |

建议新增Content.GetVotingCatalogSnapshot，以及Voting.PrepareCatalogChange、ActivateCatalogSnapshot、GetCatalogTransition等内部能力；正式命名和消息编号在7A完成。完整快照同时包含任务状态、各版本、ordered entries、完整hash和数量，不含统计/私有配置。用单SQL/同事务只读快照生成；完整ballot设置实际Proto和HTTP编码预算，超过预算明确失败，不分页或截断。容量上限及错误reason在7A对齐既有映射，不能无限提高所有服务4MiB上限。

保持Voting现有Challenge两个RPC字段/编号及用途。首切片不实现真实训练样本时明确Unimplemented，不能返回伪造样本或把它们挂到网站/MCP。后续实现须另做Challenge mTLS白名单、运行用途与材料授权，不能把机器调用当管理员统计旁路。

同步contract目录、Identity绑定、Gateway显式路由、目标配置、方法预算、descriptor planned例外清理、正式Proto及固定插件生成、OpenAPI状态与错误示例。400格式、401身份、403用途/禁投、409版本/幂等、422缺确认，以及503/504技术失败不得泄露统计。所有业务响应no-store，日志/trace/指标禁止记录票数、选择内容或统计载荷。

## 9. 推进顺序与退出条件

| 步骤 | 工作与交付 | 完成门槛 |
| --- | --- | --- |
| 7A 设计定稿 | 状态图、目录冻结协议、数据/唯一约束、RPC/HTTP映射、错误表、锁序、消息预算、兼容/升级方案 | 消除目录竞态和统计旁路；每种故障有持久恢复路径；不把草案当已运行 |
| 7B 完整目录与栅栏 | Content快照与变更意图、Voting目录门控及回执、既有审核/收紧入口适配 | 真PG多实例证明freeze与并发写票、乱序、丢响应、崩溃恢复；此步未过不开放投票 |
| 7C 资格/ballot/选择 | cmd/voting、最小分层、独立schema和账号、本人接口、版本/幂等 | 完整初始＋追加候选，跨账号拒绝、并发改选不重复票、超限拒绝 |
| 7D 统计永久关闭 | 网站/MCP显式入口、同账号同任务锁、统计一致快照 | 投票/查看统计竞争、未知提交、跨凭据与重启验证，所有旁路拒绝 |
| 7E 部署与联合回归 | services、mTLS、网络、迁移、双副本、worker/依赖故障、旧流程回归 | 真HTTP/gRPC/PG/S3＋完整kind，临时资源清理，独立审查 |

7B～7D是实现切片而非分别暴露的半成品。首个可交付版本必须同时有安全目录、写票与永久关闭规则，不能先把“可投票”上线再补统计隔离。

## 10. 验收矩阵

- A发布、B追加过审后，ballot一次包含全部初始/追加作品及正确作者；超过普通分页一页仍完整，空集合200但不能投票，超预算明确失败。
- 本人首投/改选只一份当前选择；并发同/异请求、旧ballot、新增批准、撤销和下架都得到确定结果。
- 未确认统计422且资格不变；普通读取/ballot/投稿不关闭资格；统计写入失败不泄露任何数值。
- 使用真实数据库屏障令“先查Content、后改目录”窗口稳定出现：冻结确认后旧generation不能提交；不以测试随机时序代替证明。
- 写票和统计关闭两种提交顺序均验证；网站/MCP/管理员共用稳定账号，换token、重新登录、重启、追加作品不恢复资格。
- 投票/统计关闭/目录同步分别覆盖COMMIT前失败、数据库已提交丢响应、远端成功丢回执、重投、租约过期、旧执行者迟到、取消和锁等待超时，检查连接/锁释放。
- 公共DTO、错误、日志、管理入口及Challenge路径没有未授权具体统计泄漏；不同任务不相互禁投；已关闭用户仍可追加作品。
- runtime不能DDL、跨schema、改作者、重置禁投时间或删禁投事实；初始旧数据升级、重复/篡改迁移、混合版本和回滚安全。
- Voting停止不应因新增全局readiness依赖拖垮登录/浏览/私有上传。涉及目录变更的批准/下架可进入持久待处理，但必须明确暂不可完成，不能伪称成功或绕过栅栏。
- 全量race、专用PG/S3、Proto/生成/OpenAPI一致性、旧Content/Asset/Moderation回归、双副本正常Service/NodePort和完整kind；所有SKIP/缺环境单独列出。

## 11. 部署和Git边界

后续实现才创建cmd/voting、internal/voting、ops/voting和services配置。优先复用platform技术能力；按项目既有domain/application/repository/transport职责组织，但不引入通用CQRS、消息代理、Redis或每表CRUD抽象。最小接口按真实调用定义。

迁移序号以实施时真实基线为准；Voting独立owner/runtime，Content仅新增本schema意图/栅栏相关迁移，不改已应用迁移。网络仅开放Gateway→Voting、Voting→Identity/Content及Content→Voting必要方向，机器监督暂不开放业务旁路。服务排序不能引入全局readiness递归。

升级需先禁用新投票入口，完成两侧迁移/最小授权、部署全部兼容Content/Voting、为既有公开任务完成完整目录引导及generation校验，最后部署/开启Gateway入口。Voting尚未引导的任务fail closed，不静默返回空候选。启用后禁止回滚为无栅栏的旧Content继续写目录；先关闭写票入口并确认在途/门控，再按已验收的恢复流程操作，不把关HTTP路由当成已撤销所有内部/在途调用。

完成7A后再决定实现范围；本轮仅规划，无正式契约/运行时变更，也不声称Voting任何业务已通过测试。

## 12. 技术参考与采用边界

2026-10-25核对以下一手页面；本轮研究结果另存第六阶段封存证据目录的voting-reference-check.json。

| 来源 | 采用与限制 | 验证判据 |
| --- | --- | --- |
| [PostgreSQL 18 Explicit Locking](https://www.postgresql.org/docs/18/explicit-locking.html) | 使用数据库事务锁串行化目录/账号操作；锁可能持续到事务结束，不能持锁做RPC | 真PG锁等待、取消、超时、冻结与旧写票竞争 |
| [PostgreSQL 18 Transaction Isolation](https://www.postgresql.org/docs/18/transaction-iso.html) | 不把默认Read Committed下多条SQL误当一个固定快照；统计与候选需明确一致读边界 | 并发改选/目录变更下算术恒等式及版本一致 |
| [Protocol Buffers proto3 Updating a Message Type](https://protobuf.dev/programming-guides/proto3/#updating) | 兼容新增业务消息与方法，保留Challenge字段/编号；wire兼容不等于混合服务语义安全 | 开发前基线breaking、生成一致、旧客户端及旧服务拒绝路径 |

目录栅栏是项目既有架构约束的本地化方案，不是PostgreSQL自动提供跨服务原子性；数据库隔离不能代替两侧的协议。Discourse的当前选择/改选参考只沿用已有记录，本轮未重新核对该产品最新版，也不借其行为改变永久禁投的人类要求。

<a id="voting-stage7"></a>
## 13. 第七阶段实施定稿（2026-10-25，Agent Self-Claimed）

本次重新 fetch：origin/dev 与 upstream/dev 均为 `a155639`（第六阶段 PR #32 squash），与封存 b434a9e 文件树完全一致。从此基线建立 `backend/feat-voting-lifecycle`，保留上面的历史草案及原 references 修改。本节是技术定稿，不是已验收/已部署声明；完成情况另记。

### 状态、数据及线性化点

Content 增加独立 `voting_catalog_heads`（task唯一，generation、active意图）及 `voting_catalog_changes`（change唯一、原请求hash/bytes、method、queued/applied/completed/blocked、目标generation、完整结果与目录、租约）。同任务只有一个未完成意图。初始批准、追加批准及所有旧受信收紧/恢复入口统一经过包装器；旧精确业务回执仍只找回，不重新执行。拒绝决定不改目录，继续旧路径。未引导目录不返回空成功。历史公开任务由专用bootstrap意图完整同步，非逐页拼接。

`queued → Voting frozen → Content applied → Voting activated(open/closed/unpublished) → completed`；重试不超时自动解冻。意图期间暂拒该任务全部新公开授权（保守全任务收紧，不缓存旧允许）；新增内容在最终确认前不公开。不可恢复冲突持久 blocked、保持冻结，需受信运维核查，不无限快速重试。业务无效命令不得推进目录。首次准备关闭旧写票的线性化点为Voting独占gate锁的COMMIT；Content权威变更线性化点为业务＋applied意图同一COMMIT；最终审核成功仅在activate及Content确认完成之后返回。

Voting：`catalog_gates(task PK,generation,state,change,hash,snapshot)`；`catalog_changes(change PK,request hash,generation,phase)`；`account_task_guards(account,task PK)`；`statistics_access(account,task PK,closed_at)`只INSERT/SELECT；`ballots(id PK,account,task,generation,hash,expires)`；`current_votes(account,task PK,id UNIQUE,revision,entry,request_hash)`。ballot有效期15分钟，可跨副本找回；过期记录有界批量清理；永久关闭及当前票不可随清理删除。目录最多2MiB Proto和2MiB HTTP编码，另限4096候选作为内存上界；任一超限明确ResourceExhausted/catalog_too_large，不截断。超限必须在Content应用前检查目标，不能把过大的新增目录先公开。

锁序：Content意图串行锁→短事务业务原锁（decision advisory→parent→submission/review），不持锁RPC；Voting gate `FOR SHARE`→guard `FOR UPDATE`→票；prepare/activate `FOR UPDATE`，不同账号共享gate，避免全任务串行。统计关闭同序；统计后续REPEATABLE READ READ ONLY快照一次读取gate/当前候选和聚合计票，不能把默认隔离多条SELECT当固定快照。

### 幂等、失败及恢复矩阵

| 操作/失败 | 持久判据与结果 |
| --- | --- |
| 创建意图/COMMIT未知 | method+decision ID+确定性Proto SHA256；先查同一意图，不换ID；异内容AlreadyExists |
| Prepare已提交丢回执 | 同change/generation摘要重试或查询回执，仍frozen；旧generation不能覆盖新gate |
| Content业务COMMIT前失败 | 回滚业务和applied；技术失败重试原意图，业务冲突blocked并不伪称成功 |
| Content业务已提交丢响应 | applied和结果/完整目录同事务已保存；worker重读，不重新分配位置/时间 |
| Activate丢回执 | 查/重试确切prepare与完整摘要；迟到旧activate不覆盖新状态；closed墓碑不允许恢复open |
| worker崩溃/租约过期 | 已保存阶段恢复；确认带lease token，旧worker不得确认新领取者 |
| Cast提交未知 | request hash绑定ballot/entry/expectedRevision；同最近成功请求找回；较旧请求409；关闭检查优先403 |
| 永久关闭提交未知 | 新数据库操作确认只追加关闭事实；无法确认不返回统计；后续统计失败禁投不回滚 |
| 取消/锁等待超时 | context传入SQL，deferred rollback释放连接；不推断超时等于回滚，客户端复用原操作核对 |

### 入口、权限及通信

四个HTTP对应GetVotingEligibility/GetBallot/CastVote/ViewTaskStatistics，网站须当前会话，写操作CSRF；资格和显式统计允许MCP，ballot/投票不允许。管理员无豁免。显式统计缺true为422且不关闭。账号只取VerifyActor。普通响应只含本人选择；具体统计没有GET旁路；Challenge两个旧RPC保留Unimplemented。Content.GetVotingCatalogSnapshot仅Voting mTLS，完整白名单投影、一个本地一致快照；Voting内部prepare/activate/receipt仅Content mTLS。每次业务固定ResolvePrincipal→Voting→VerifyActor及Content完整状态校验，无逐作品RPC；变更RPC独立于用户请求恢复。

### 验收矩阵（判据独立于当前实现）

| 组 | 动作与必须观察到的结果 |
| --- | --- |
| A | 初始+追加超过20件一次完整ballot、作者和顺序正确；空集合、Proto/HTTP/内存预算拒绝、无私有/统计字段 |
| B | 网站/MCP/admin用途、跨账号ballot、无效撤销凭据、混合凭据、伪造account/method/audience/cert均正确拒绝 |
| C | 确定性屏障复现检查后变更；prepare确认后旧票无法提交；并发首投/改选、旧请求不覆盖 |
| D | 未确认无副作用；票先/关闭先两种序列；跨凭据/重启/新增作品永久关闭；其他题/投稿不受影响 |
| E | PG提交前失败/已提交丢响应、RPC丢回执、worker重启/旧租约、取消释放、旧激活不能恢复墓碑 |
| F | 初始审核/追加/分页/文件/上传回归；PG权限、迁移升级及重复；独立namespace双副本正常Service/NodePort、Voting故障隔离、迁移失败重试与重复部署 |

部署顺序：全部owner迁移/grants→Voting/全部Content支持协议（无旧实例旁路）→历史目录bootstrap完成→最后Gateway开放。Content与Gateway不得把Voting加入全局readiness；Voting故障目录变更持久待处理，既有普通阅读/上传不因全局健康失败。回滚先关闭投票入口并排空在途/冻结门控，禁止回到无栅栏旧Content。生产存储、公网、真实Challenge监督数据不在本轮。


### 实施补充边界

完整目录保守预留`4096 + 64×4096`字节，覆盖逐项HTTP null/default字段及ballot外壳，实际HTTP仍最终验证2MiB。Voting每进程同时执行两个完整目录业务，其余在方法deadline内等候；这是有界昂贵计算/对象驻留策略，不是全连接/宿主机内存硬保证或生产容量验收。统计GROUP BY只接收当前完整目录ID集，避免撤销历史ID使结果map无界增长。ballot清理每分钟、每次256条，删除过期超过一天的记录，不触碰永久禁投或历史票。

本轮重新HTTP读取PostgreSQL18锁/隔离及Proto3官方参考均200，脱敏记录`voting-20261025/reference-check.json`；采用FOR SHARE与FOR UPDATE冲突及RR同快照，不声称这些提供跨服务事务。相同参考的规划期记录继续保留，不冒充本轮测试。


精确最近投票重试仍要求原ballot记录存在且目录generation/hash未变；ballot过期拒绝新写，最近成功的精确回放可在清理前找回。目录变化409、永久关闭403优先，过期超过一天的ballot清理后不承诺旧请求找回；当前票和永久事实不清理。普通Voting业务各一次Content权威RPC，显式统计在禁投提交前、后各一次；两者均为固定次数，不逐作品查询。2MiB/4096项上限、每进程两个昂贵操作是首切片保守边界，不是全生产压测结论。

## 14. 本轮执行记录（2026-10-25，Agent Self-Claimed）

开发前基线完整SHA为 `a1556394e6b4f27101e79327c349e6c39561ca29`，分支为 `backend/feat-voting-lifecycle`；第六阶段已经经PR #32 squash进入该基线，无重复移植。保留原未提交references增量与Voting规划历史。未修改前端，未执行commit、push、PR、合并或公网发布。

脱敏原始证据位于本机 `/home/cherry/.local/state/human-worth/validation/voting-20261025/`；每次执行保存实际命令、cwd、退出码、耗时的同名 `.json` 和原始 `.log`。专用隔离环境为PostgreSQL 18.4及SeaweedFS 4.13，随机数据库/owner/runtime与loopback动态端口，不使用产品库或真实用户凭据。Go 1.27.1、Buf 1.72.0；使用默认 `/home/cherry/.cache/go-build`，没有临时GOCACHE。正式 `buf.gen.yaml` 的固定Go插件复用本机Go缓存，没有临时生成配置、手工编辑pb.go或提交插件/缓存。

### 静态、常规与真实组件验证

以下命令均已退出；静态命令按表中顺序在 `set -e` 下执行，组合退出0，证据 `final-static-v3.{json,log}`，耗时9.647秒。基线不是当前未提交Proto，也不是临时生成目录。

| 实际命令（backend目录） | 退出码 | 结果 |
| --- | --- | --- |
| `test -z "$(gofmt -l cmd internal)"` | 0 | 无待格式化文件 |
| `buf lint` | 0 | 正式Proto通过 |
| `buf generate` | 0 | 正式固定插件生成 |
| `buf breaking --against "../.git#ref=a1556394e6b4f27101e79327c349e6c39561ca29,subdir=backend"` | 0 | 相对开发前真实dev兼容 |
| `d=$(mktemp -d); buf generate --output "$d"; diff -qr gen "$d/gen"` | 0 | 独立逐文件重生成一致；临时目录已删除 |
| `go vet ./...` | 0 | 常规包 |
| `go vet -tags=integration ./...` | 0 | 包含最新真实PG/未知提交测试 |
| `go vet -tags=lab ./internal/identity` | 0 | 包含最新故障恢复夹具 |
| `go build ./cmd/...` | 0 | 包含独立Voting入口 |
| `go test -race ./... -count=1 -json` | 0 | 323测试/子测试PASS，0失败、0跳过 |
| `go test -race -tags=integration ./... -count=1 -timeout=180s -json` | 0 | 576测试/子测试PASS，0失败、0跳过；70.877秒，见 `final-integration-v3` |

全量integration已配置 `IDENTITY/CONTENT/ASSET/MODERATION/VOTING/CHALLENGE_TEST_DATABASE_URL_FILE` 和 `ASSET_S3_TEST_CONFIG_FILE`，受限凭据在仓库外，未打印其内容。576为323常规项加253标签新增项，**不是576项全部真实HTTP链路**：既有Challenge等组件fake仍在原边界内；真实Voting跨服务专项在Identity包，共19项测试/子测试（含父测试，不重复换算独立场景数）。其使用真实HTTP/HTTPS、正式gRPC/mTLS、PG受限账号和共享S3；是同测试进程的多个服务实例，不冒充独立Pod或真实Google登录。

Voting专项实际包含22候选（初始加追加）的完整顺序/实际作者、跨Gateway首投/改选/并发、网站/MCP/admin及撤销身份、追加/撤销使旧ballot失效、跨任务拒绝、关闭后换token和重启不恢复、零票项/撤销历史票、PG权限/升级/重复迁移、有界预算、确定性目录屏障、锁取消释放、提交前故障、真实COMMIT响应丢失、无法确认禁投不返回统计、远端成功丢回执及陈旧租约恢复。`voting-safety-repeat`保留较早版本连续三轮证据，不替代最终全量结果。

验收中修复了Activate读取prepare回执时把数据库技术故障误判为业务冲突的问题；现在只有缺失/异内容属于冲突，断连接返回可重试依赖错误，避免Content被错误地持久blocked。真实PG关闭连接的定向回归已包含在最终全量中。

早期非零结果也保留，不以最终通过覆盖原证据：`base-check-first`退出1，发现OpenAPI编辑产生重复的`x-deployment-status`键，现已去重并通过真实YAML/契约检查；`buf-breaking`退出1为错误使用backend相对`.git`路径，后续改用表中的`../.git`及固定基线重新执行。`integration-all-first`退出1，暴露未启用Voting的旧维护兼容fixture被新增墓碑规则误拒绝，以及丢回执夹具未覆盖实际调用的第二实例；前者将新限制绑定到已启用/引导Voting的路径，后者在两个真实实例都安装调用真实RPC后才丢响应的故障注入。`voting-recovery-second`仍因后者退出1，修正后专项连续三轮及最终全量均0；没有放宽Unavailable/frozen/queued判据。

### kind失败证据与恢复判据

- `kind-first`：退出1，496.781秒。S3故障后只证明一个固定Asset实例恢复，未等正常Service客户端/全部Gateway端点收敛，后续公开文件503。新增有界恢复checkpoint：两个正常Gateway EndpointSlice ready，并通过正常NodePort核对真实文件及Content响应；不更改业务期望码或使用跳过掩盖失败。
- `kind-second`：退出1，642.150秒。Voting、追加、Asset、Content和Moderation业务通过，但公开文件测试在删除Content Pod后仍用绑定旧Pod的测试维护 `port-forward service/content`，TLS握手EOF。恢复后仅重建测试维护通道，应用客户端仍以正常DNS/Service和NodePort独立验证；不能把port-forward自动重定向当作Service恢复证据。
- 两次失败原始日志及修复前观察均保留；其namespace分别为 `human-worth-verify-fe477202`、`human-worth-verify-50c97a4e`，已经删除。最终完整重跑另行记录，不拿其中通过的单项冒充整个validator通过。

最终 `PATH="$HOME/.local/bin:$PATH" python3 -u ops/lab/validate_modules.py` **退出0，866.524秒**，见 `kind-final.{json,log}`。独立namespace `human-worth-verify-7a63b35a`；不可变运行时代码revision `025a87a3a37ca54f5053aa8dfb1b834c9725890d`（是本地制品标识，不是Git提交）。`runtime-source-final.json`核对当前运行时hash完全匹配；最后新增integration测试不进入运行时hash，不拿旧镜像冒充新实现。

六项真实Pod业务全部通过，无失败/跳过：AdditionalEntry 53.67秒、Asset 209.18秒、PublicAsset 40.73秒、Content 3.09秒、Moderation 96.40秒、Voting 56.98秒。后续七种调用方网络允许/拒绝探针全部符合预期，失败rollout恢复、同SHA失败迁移重试、完整重复部署通过。初始rollout/HTTPS发布标记检查使用诊断port-forward；**Voting/Moderation故障隔离另用正常私有NodePort/Service、全新TCP连接及两个ready Gateway端点**，不能把前者等同后者。

Voting验证两个不同节点的真实Pod、跨副本ballot/投票回执、真实S3候选文件、显式禁投后拒绝重放；Voting缩至0时Gateway两个正常端点仍ready，登录及既有公开详情/列表可用，投票依赖返回503/504。离线追加批准持久queued并暂拒该任务新读取/文件；恢复后自动批准接纳、三个候选且首次closedAt和总票不变。相邻公开文件测试实际删除Content Pod，重建测试维护通道后验证正常Service授权与恢复，未放宽200/404或数据内容断言。

### 清理、兼容与交付边界

validator已删除本轮namespace，另行`get namespace --ignore-not-found`无输出、退出0确认不存在；未操作常驻业务namespace。两只专用PG/S3容器已按本轮所有权label核对后删除，仓库外四个受限凭据文件及private目录已删除；证据中未发现这些凭据原文，记录 `cleanup-components.json`。保留原始失败/最终日志、环境版本及源码hash，不提交临时二进制、数据库、凭据或插件缓存。

正式Proto只兼容新增；原Challenge两个RPC与字段保留，仍明确Unimplemented。Content新增007、Voting新增001，不改旧迁移/校验和。runtime跨schema/DDL及重置禁投事实均拒绝。先owner迁移与grants，再全部Content/Voting升级并bootstrap，最后开Gateway；wire兼容不代表旧无栅栏Content能混合写目录。回滚不能删永久事实或直接启用旧Content，需先禁新写、处理在途与门控。目录blocked故障保守保持冻结，当前没有自动“清空状态重开”的管理旁路。

本地必要业务验收PASS，适合提交独立审查，**不是已提交、已发布或生产验收**。未验证生产容量、恶意高负载、唯一真人/多账号防刷、生产S3/PostgreSQL HA、真实Google端到端或公网；未实现Discovery、热度/可信结论算法、Challenge监督数据、MCP工具传输及前端。`conclusion=undetermined`诚实保留；上述范围不借本地Pod测试宣称完成。已完成临界区的旧在途操作与已发送文件字节不追溯撤销。原阶段六封存分支及dev/main不变，等待人类决定是否提交/推送/PR。

文档收尾后根目录 `npm run ci`、`python3 -m py_compile ops/lab/*.py`、`git diff --check` 顺序执行并均退出0（`final-root-closeout`，1.017秒）；CI实际为5个Node测试、28个Python测试，无失败/跳过，包含check与build:docs。日志中的“Public deployment verified”来自既有Python部署夹具，**不是本轮执行了公网发布**。最终61个文件存在未暂存修改/新增（含已保留的原用户规划），暂存区为空，HEAD仍为开发前基线；逐文件hash及状态分别记录在`source-files-final.json`、`git-final.json`。本次验收所需本地环境均可用，无必要本地门槛以SKIP/INCONCLUSIVE替代；上述明确排除的生产/真人/Challenge范围继续未实现或未验收。

## 15. 第二轮安全与恢复审查（2026-10-25）

本节独立于第14节的原实施验收。开始时逐项核对原61文件摘要，全部匹配，但新增回归仍复现了原验收没有识别的问题；不能用测试数量代替信任边界和升级场景的覆盖。新证据目录为 `/home/cherry/.local/state/human-worth/validation/voting-review-20261025/`，不覆盖历史失败或原验收日志。

### 发现与修复范围

| 问题 | 原可观察行为 | 本轮处理 |
| --- | --- | --- |
| P1：临时限流误判成不可恢复目录错误 | Voting 的 `ResourceExhausted/too_many_requests` 会让 Content 持久 blocked；负载恢复后任务仍隐藏 | 将临时资源不足保留为 queued/applied 可重试；只有明确目录预算/校验错误才作为终止冲突。仍保留 active head 和必要的 frozen gate，不靠清空状态解冻 |
| P1：首次升级与失败回滚缺少协议兼容门禁 | 旧无栅栏 Content 可与新写入者混合运行；发布失败可能缩掉新增 Voting 并恢复旧 Content | 增加实际镜像的无依赖协议能力探针、持久启用标记、首次升级旧 Content 排空及回滚兼容检查。首次启用接受有界 Content 中断，禁止为恢复可用性而重新启用无栅栏写入者；兼容升级保持正常滚动部署 |
| P2：历史成功回执重放产生新目录代次 | 第六阶段已成功的任务/追加作品审核和受信维护回执，在 bootstrap 后重放会创建新意图、使旧 ballot 失效；Voting 故障时还可能隐藏原公开任务 | 先从不可变业务回执或 completed 协议回执找回精确结果；同键异内容冲突。已有 queued/applied 意图不能借本地业务回执提前确认完成 |
| P2：流式健康 RPC 绕过服务授权 | 真实 Voting 入口只装一元拦截器，受 CA 信任的非允许调用方可订阅 `Health.Watch` | 正式入口安装 Voting 流式拦截器；本阶段没有受支持流式方法，错误身份拒绝，Gateway 返回 Unimplemented。保留 Gateway/Content 的一元 Health.Check |

新增回归先在旧实现上失败，再核对修复。Health 用实际 `cmd/voting` 子进程和真实 mTLS 证书，避免共享测试夹具自带的流式拦截器掩盖正式入口遗漏。历史回执覆盖 task/entry/legacy、在线、缺客户端、真实停止的 Voting、同键异内容，以及另一个目录意图待处理期间的只读找回。临时限流经真实 mTLS 入口注入 admission 错误，不伪造目录或数据库结果。

### 追加验证的业务不变量

- 用真实 PostgreSQL COMMIT 屏障固定“投票先提交”和“统计关闭先提交”两种顺序，并从 `pg_stat_activity` 确认竞争 RPC 等待同一账号×任务锁；最终票数分别为1和0，关闭后的旧请求均拒绝。
- 远端 activate 已提交但回执尚未返回时，用 `FOR UPDATE NOWAIT` 验证 Content head、任务草稿、公开任务及 Voting gate 的锁已释放；取消原调用后，另一实例从 applied 恢复同一意图。
- ballot 到期拒绝新写，但清理前精确最近回执可找回；每轮清理最多256条，清理后不承诺旧回执，不能删除当前票或永久禁投事实。
- prepare 和 activate 分别实际提交 PostgreSQL 后丢失 COMMIT 响应，再经另一实例找回相同持久回执；Content queued/applied/completed、统计无法确认提交等既有回归继续保留。
- Proto、固定插件生成、独立重生成、真实开发前基线 breaking、OpenAPI 路由/权限契约、007/001迁移及最小授权继续联合检查。本轮修复不改变字段编号或数据模型，不为代码修复增加空迁移。

### 本轮最终验收

**第二轮当时结论：Voting 核心实现及下列专项通过，但当时第七阶段不能按完整本地验收完成收尾。当轮完整 kind 命令退出1，仍有两项真实文件拒绝路径失败；后续修复与完整补验见第16节。** 第14节旧kind通过记录不是本轮通过证据。本轮未执行 commit、push、PR、合并或公网部署。

| 命令／证据（均在本轮证据目录） | 退出码 | 实际结果 |
| --- | --- | --- |
| `review-static-final`：gofmt、`buf lint`、`buf generate`、对开发前 `a1556394e6b4f27101e79327c349e6c39561ca29` 的 `buf breaking`、独立输出逐文件比较、三种 vet、build、常规 race | 0 | 11.815秒；323测试/子测试PASS，0失败/跳过 |
| `review-integration-frozen`：`go test -race -tags=integration ./... -count=1 -timeout=240s -json` | 0 | 86.953秒；604测试/子测试PASS，0失败/跳过，含323常规项＋281标签新增项；不是604项全部真实链路 |
| `review-voting-repeat`：`go test -race -tags=integration ./internal/identity -run "^TestVoting" -count=3 -timeout=240s -json` | 0 | 115.426秒；三轮合计141测试/子测试PASS，0失败/跳过，不等于141个独立场景 |
| `review-root-updated`：`npm run ci`、Python编译及 `git diff --check` | 0 | 5个Node、34个Python测试通过，包含文档及契约检查；不是公网发布 |
| `review-kind`：`PATH="$HOME/.local/bin:$PATH" python3 -u ops/lab/validate_modules.py` | **1** | 795.981秒；升级回归及六项业务中的四项通过、两项失败；没有执行到后续网络/失败rollout/迁移重试/重复部署验收，不能声称完整部署验证通过 |

kind使用独立 namespace `human-worth-verify-178e7ae8`，本地制品标识 `53a668a9b611f3a65b7ab5eec977bc0e85373259`（不是Git提交）。运行时代码hash `b2e9cf80ae0240f9fa48f9430706c1638c80f9b801e3acc391f27a40e32aaae7` 与最终工作区一致；运行期间只有README/部署说明修改。真实第六阶段镜像与SQL历史投影fixture已验证旧HTTP读取，然后实际排空旧Content、注入Voting停机与激活故障，分别在queued/applied状态拒绝旧版本回滚并成功前向恢复；没有用手写Pod标签代替镜像能力核验。

四项业务通过：AdditionalEntry 53.62秒、Asset 128.06秒、Content 2.84秒、Voting 59.45秒。Voting正常私有NodePort/Service故障隔离、双副本持久回执、永久关闭及离线批准queued后恢复均通过。**仍失败的两项如下，保留原404判据，不接受503作为授权拒绝验收：**

| 用例 | 失败位置与可观察行为 | 当前判断 |
| --- | --- | --- |
| `TestKindPublicAssetDeployment` | `backend/internal/identity/asset_public_lab_test.go:102`：尚未批准的任务、无匹配作品的匿名文件读取，预期404，实际503 `asset_unavailable`；14.31秒 | 拒绝了文件但错误类别不符合验收；尚未确定是跨测试依赖恢复、连接状态还是业务缺陷，不能描述为已修复或单纯环境抖动 |
| `TestKindModerationDeployment` | `backend/internal/identity/moderation_lab_test.go:298`：审核已批准且公开文件已读成功，再读取待审文件入口，预期404，实际503 `asset_unavailable`；58.94秒 | 不证明越权泄露，但同样阻止当前阶段完整收尾；需追踪该请求在Gateway/Asset/Content的实际RPC错误并加入确定性回归 |

原失败日志及方法/状态诊断保留。额外分别给真实PG/mTLS集成夹具启用正式Runtime拦截器、再启用OTel统计处理器运行 `TestReviewedAssetHTTPAccess`，两次退出0（`review-runtime-diagnostic`、`review-runtime-otel-diagnostic`）；**没有复现kind错误，不能替代kind验收或据此认定根因**。诊断性夹具改动已完整恢复，未将临时试验代码混入交付。

下一步先确定上述两条503的来源；如果属于依赖恢复，必须单独证明各实际调用方已在有界时间恢复，再执行严格404/200断言，不能全局重试掩盖持续故障或放宽期望码。如果属于业务/错误传播缺陷，补能在旧实现失败的真实入口回归。修复后重跑完整kind及受影响的静态、集成和契约测试，之后才决定提交收尾。

收尾时根目录 `npm run ci && python3 -m py_compile ops/lab/*.py && git diff --check` 退出0，1.116秒（`review-root-closeout`）。两只专用PG/S3容器按本轮所有权label核验后删除，受限凭据目录删除，隔离namespace确认不存在，无残留catalog探针容器；对证据逐文件检查未发现本轮凭据原文，见 `cleanup-components.json`。不改常驻业务namespace，不安装仓库外常驻控制器。首次真实启用前仍须先按已有流程更新可信安装的控制器。生产容量/HA、多账号防刷、真实Google端到端、Challenge监督数据、Discovery/MCP传输/前端和公网验收不在本轮完成范围。

## 16. 文件503定位与逐调用方恢复回归（2026-10-25）

本轮沿用第15节的基线与未提交工作区；没有提交、推送、PR、合并或公网部署。证据另存于 `/home/cherry/.local/state/human-worth/validation/voting-file-errors-20261025/`，不覆盖旧失败。本节最终结论以本节末尾完整验收结果为准。

### 定位结论与证据边界

受控真实Pod实验固定了以下顺序：两个 Asset 调用方的匿名/管理员文件入口原先均404 → 停掉全部Content后均503 → 恢复Content并等待rollout成功 → 第一条匿名拒绝请求仍503。该请求ID为 `kl4_tNkVuxFkKVyD1NQgtg`：Gateway收到 `Unavailable/asset_unavailable`，对应Asset流也返回Unavailable，Asset的Content依赖错误为 `Unavailable/no children to pick from`；并非Content已返回 `Allowed=false` 后被错误映射成503。管理员文件方法在同一故障中同样记录了底层 `no children to pick from`。见 `diagnostic-recovery.{json,log}`，旧“rollout后立即要求404”的实验退出1，17.34秒。

因此明确识别出的验收缺口是：**Pod就绪、Moderation收到Content回执、某个Gateway/Asset恢复，并不能替代每个Asset→Content授权客户端独立恢复。** Asset就绪不依赖Content是既有防递归依赖设计，本轮不把Content硬塞进Asset/Gateway全局readiness。另一次完整诊断中，原两项文件断言通过，但下一项Voting测试的审核POST返回503；原测试只等Moderation rollout/worker回执，未确认两个正常Gateway各自的Moderation客户端恢复。

原第15节两条历史请求缺少底层流错误日志，**不能追认每条请求唯一根因**；这里给出的是已由真实调用链复现、与该症状一致的连接恢复机制及原夹具未覆盖的条件，不把历史失败抹成“环境抖动”。诊断镜像、改变运行次数的实验均不充当最终正式源码验收。

### 最小修复与确定性回归

- 新增仅供lab的有界恢复checkpoint：通过分别固定到两个实际Asset Pod的Gateway，使用独立的、不存在的合法文件上下文，检查匿名 `CheckAssetAccess` 和管理员 `CheckReviewAssetAccess` 链路。105秒预算在同组调用方之间共享，包含真实HTTP及读body；只容忍已列明的503依赖错误或504/deadline，403、错误JSON、取消及任意其他错误立即失败。
- PublicAsset在前序故障/替换后、以及自身Content停机恢复后先执行独立checkpoint；随后真实草稿404、公开文件200及字节、撤销/下架404仍是**不重试的业务断言**。删除原先会把各种响应和错误字节一直吞到超时的公开文件恢复循环。
- Moderation在Content恢复后逐个验证两个Asset调用方，再通过两者分别读取新pending案件的真实S3字节200，正式批准后两者均严格404。该组临时Asset探针在后续Content探针创建前删除，保持原namespace资源上限，不靠调高quota通过。
- 每次主动恢复Moderation后，分别通过两个正常Gateway Deployment Pod的既有客户端执行只读审核列表checkpoint。该诊断通道不代替既有正常Service/NodePort故障隔离验收；写请求没有自动重试。
- 新增 `TestKindFileAuthorizationRecoveryDeployment`：真实Pod/Identity/mTLS/PG，明确验证Content停机时两类文件入口都不能访问；恢复后检查每个Asset调用方，再通过固定调用方及正常NodePort执行严格404。真实文件的允许/终审拒绝及S3字节由相邻PublicAsset/Moderation用例验证。
- 三个checkpoint夹具回归覆盖“第一个调用方已好、第二个仍被屏障阻塞”、拒绝非技术错误、取消和持续故障超时；连续10轮共30个PASS事件。仅在仓库外把checkpoint故意退化为只检查首个调用方，`TestKindRecoveryCheckpointAllCallers`立即失败（`recovery-first-caller-mutation`退出1），证明该回归能识别原先遗漏其他调用方的条件。它是HTTP夹具测试，不冒充业务集成。

本轮没有修改产品错误码、授权规则、RPC预算、自动重试策略或公开缓存；没有新增API、Proto字段、生成代码变化、数据库迁移或schema授权。既有正式Proto/生成物/OpenAPI/007与001迁移及部署权限仍联合验证；`validate_modules.py`新增真实恢复场景和三个夹具回归，整体业务测试超时由720秒增至900秒，不放宽单个RPC deadline。HTTP失败日志附带边缘request ID，不记录body/凭据。

### 诊断失败保留

`diagnostic-kind`退出1，747.225秒：原两项文件用例通过，新增观察到下一项Voting测试的审核依赖503。`diagnostic-kind-v2`退出1：重复实验误用了Identity的base64 ID构造legacy entry hex ID，公开文件正确拒绝404；重复整套测试还碰到全局维护decision ID冲突，因此该实验被明确标记为无效夹具证据并停止，不用于产品结论。后续v3改用合法、按任务隔离的历史fixture，PublicAsset/Moderation各连续三轮通过；为运行受控恢复探针暂留独立namespace，定位完成后中止后续无关部署检查并清理，命令退出-2，不声称完整validator通过。

`recovery-harness-preflight`退出1：新PublicAsset与真实恢复场景通过，Moderation在创建后续探针时失败；临时Asset探针与Content探针同时存在会使配置CPU限额超过现有8核quota。最终夹具在已完成断言后提前删除前一组探针，不调整产品Deployment资源或namespace配额。该中间失败与最终源码不同，必须以随后正式完整kind命令为验收依据。

### 当前验证记录

| 证据/实际命令 | 退出码 | 范围 |
| --- | --- | --- |
| `recovery-unit-final`：`go test -race -tags=lab ./internal/identity -run "^TestKindRecovery" -count=10 -timeout=60s -json` | 0 | 5.025秒，30 PASS、0失败/跳过 |
| `recovery-static-final`：gofmt、Buf lint/generate、开发前真实基线breaking、独立重生成逐文件比较、常规/integration/lab vet、build、常规race | 0 | 12.070秒，323 PASS、0失败/跳过 |
| `recovery-integration-final`：`go test -race -tags=integration ./... -count=1 -timeout=240s -json` | 0 | 84.114秒，604 PASS＝323常规＋281标签新增，0失败/跳过；不是604项全部真实HTTP链路 |
| `recovery-root-final`：`npm run ci`、Python编译、`git diff --check` | 0 | 1.520秒，5 Node、34 Python；不是公网发布 |

### 最终正式源码验收与清理

**结论：本轮完整本地验收PASS，第七阶段可以进入提交/独立审查收尾；不是已提交、已发布或公网验收完成。** 原第15节失败和上述诊断/预检失败继续保留。

`PATH="$HOME/.local/bin:$PATH" python3 -u ops/lab/validate_modules.py` 在正式仓库执行，**退出0，965.685秒**（`recovery-kind-final.{json,log}`）。临时namespace为 `human-worth-verify-3d775ad4`。七项真实Pod业务与三个checkpoint夹具回归全部通过、无失败或跳过：

| 真实业务用例 | 秒 |
| --- | --- |
| AdditionalEntry | 52.69 |
| Asset | 186.75 |
| PublicAsset（含原草稿404断言） | 42.78 |
| Content | 3.49 |
| FileAuthorizationRecovery | 26.72 |
| Moderation（含原终审后待审文件404，另逐Asset验证） | 102.40 |
| Voting | 42.79 |

真实第六阶段镜像→第七阶段升级、旧Content排空、queued/applied拒绝不兼容回滚及前向恢复均通过；后续七种调用方网络允许/拒绝检查、坏镜像回滚、同SHA迁移失败重试、完整重复部署也全部通过。新的文件拒绝断言没有接受503代替404；每个固定Asset调用方的pending真实字节200与终审后404，以及正常Service/NodePort故障隔离均保留。

本轮只修正验收夹具与文档，运行时代码hash仍为 `b2e9cf80ae0240f9fa48f9430706c1638c80f9b801e3acc391f27a40e32aaae7`，本地制品标识仍为 `53a668a9b611f3a65b7ab5eec977bc0e85373259`，不是Git提交。与第15节相同的运行时在修正后的严格夹具下完整通过；诊断插桩未进入正式镜像。冻结后除了文档无运行时、测试或控制器输入变更，记录 `source-frozen.json`、`source-freeze-check.json` 与 `runtime-frozen.json`。

validator已删除最终namespace；四个本轮临时namespace另行查询均不存在。两只专用PostgreSQL/S3容器按本轮名称与所有权label核对后删除，受限凭据目录删除；逐文件扫描证据未发现本轮凭据原文，诊断快照指向正式Git目录的临时symlink也已移除。见 `cleanup-final.json`。未操作常驻业务namespace、没有安装或更新仓库外常驻控制器。首次发布前仍须遵循第15节与部署文档的可信控制器升级要求。

未覆盖范围不变：远端GitHub CI、真实Google端到端、公网发布/验收、生产S3/PG容量与HA、多账号防刷，以及未实现的Discovery/MCP工具传输/Challenge监督数据/前端。不追溯撤销已完成临界区的在途操作或已发送字节，不把本地完整验收等同整站产品完成。


## 后续补全：结论与监督数据（2026-10-25）

保留已上线的全部表、完整目录协议、只追加禁投事实和 RPC wire 编号；追加 `002_backend_completion.sql`。`CastVote` 改为 gate 排他锁，在同一事务内更新当前票与分类／翻转历史；统计关闭仍共享 gate 并锁账号 guard，锁序不变。此前“不同账号共享 gate 写票”的并发描述仅适用于第七阶段版本；新方案优先精确历史顺序，不声称单任务吞吐不变。

普通 Discovery 只读分类投影。首次启动对旧目录补齐缺失结论；至少 20 票且 95% Wilson 区间排除 50% 才判领先，否则保留不确定。永久资格不因这些投影更新而改变。

Challenge 两个既有监督 RPC 已实现；新增分类 RPC 仅 Discovery 可调用。监督数据窗口以本代目录激活为起点，只纳入见过本代候选的新投／改选，保留评论截止早于投票窗口；训练和独立验证任务集合不重用。完整授权、快照失效、窗口与抽样边界及执行证据见 [补全记录](backend-completion.md)。
