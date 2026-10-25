# 后端功能补全与验收

本轮由人类明确要求“并行清理磁盘，完成后端所有必要功能”。工作分支为 `backend/feat-complete-services`，基线为 2026-10-25 的 `origin/dev`：`1fe4d3f926e0e67fddc1d2d457bbb27e1ae1308d`。随后人类明确授权“为本次改动创建 PR，CI 通过后合并并部署”。前端由前端同学负责；本次按 PR → dev push CI → 九模块部署推进。实际公网版本以 `/api/health` 的 revision、deploymentRevision 和 deployedServices 为准，接口实现和隔离验收不单独证明已上线。

## 功能与产品边界

| PRD 来源 | 后端能力 | 实现位置 / 行为 |
| --- | --- | --- |
| FR-01、FR-06 | 发现流、关键字与分类、热度榜、平台和分类看板 | Discovery 调用 Content 安全投影及 Voting 分类结论；不查询或预取单题票数。游标绑定排序、筛选和截止时间 |
| FR-02、FR-03、H7 | 文字／文件作品、独立任务附件、首发与任意用户追加 | Content 验证实际正文、文件所有权与附件云端许可；链接只作补充。审核通过即展示，不加公开开关；保留旧已审核链接作品的读取兼容 |
| FR-01、FR-05 | 全量候选单选、改选、统计永久禁投 | 复用 dev 已上线 Voting，保留完整目录、门控、永久事实和回执；浏览与 MCP 共用稳定账号。不能通过新增作品、换 token 恢复资格 |
| FR-04.2 | 评论、举报、审核与下架 | 评论分页与请求幂等；举报及管理员行动留审计。下架采用 Moderation outbox → Content 持久目录意图 → Voting 冻结／激活／确认，失败维持待处理或阻断，成功回执不重复应用 |
| FR-04.1、FR-04.7、H2 | 管理概览与统一审计 | 待审核任务／作品、举报、运行中／失败挑战；跨 Moderation／Challenge 审计分页。响应不暴露预算余额、消耗数值 |
| FR-04.3～FR-04.8、H4～H12 | 管理员挑战、训练和独立验证、P/R/E、登记与审核 | Challenge 接入真实 Content／Asset／Voting；材料版本与角色授权、租约、取消封闭、迟到结果拒绝及登记重试。E 在 gVisor Job 内执行，生成作品仍须正常审核 |
| FR-05 | MCP 传输和五个读取工具 | `POST /api/mcp`，支持 Streamable HTTP；`list_tasks`、`get_task`、`list_entries`、`list_comments`、`view_task_statistics`。统计工具明确披露永久禁投副作用，必须显式确认；无投稿、投票和云端控制工具 |
| FR-07 | Google 登录、会话与 MCP 凭据 | 沿用 Identity；每次读取检查当前凭据和账号状态，撤销立即拒绝 |

OpenAPI 0.8.0 和 Proto 是精确接口契约。已有前端页面的接入、用户实际体验和前端验收仍归前端任务，不从后端测试推断完成。

## 技术选择与升级

以下都是 Agent Self-Claimed 实现选择，不修改 PRD 中的人类要求。

- 推荐先采用最新发布时间／稳定 ID 倒序的可复现基线。热度为最近七天的不同评论账号数与过审作品数之和，完全不使用票数；搜索对标题、摘要、描述作大小写不敏感的字面子串匹配，`%` 不作为通配符。不宣称已完成个性化推荐或防刷。
- 领先结论要求至少 20 张当前有效选票，且 95% Wilson 区间排除 50%；否则 `undetermined`。只描述参与账号的判断，不推断全人类。跨过不确定区间后另一方取得领先计一次翻转；重放不增加事件。写票和目录激活在同一任务 gate 的排他锁内更新结论，牺牲单任务写并发以保持历史顺序；不同任务互不串行。普通看板只读取已保存分类，不现场计票。
- 监督数据固定当前目录、授权材料、评论截止与投票窗口，只纳入该 generation 中的新投票／改选。历史选票继续参与公开统计；不能假定旧票已看过新候选。训练最多取候选集合的一半，为独立验证留出未见任务；每批最多 20 题，每次最多检查 256 题，响应不超过 1 MiB。账号的永久资格不被机器监督读取改变；训练标签只允许 Challenge mTLS。`current-choice-catalog-holdout-v2` 快照 24 小时失效，训练／验证共享永久的任务占用记录，过期不会重新用作该运行的独立验证。
- Content 的 `007_voting_catalog.sql`、Voting 的 `001_voting.sql` 和旧校验和保持不变。新增 Content `008_backend_collaboration.sql` 和 Voting `002_backend_completion.sql`。保留 `statistics_access` 的只增不可改权限；不搬迁或删除已有选票、禁投资格、目录门控和审核回执。Voting 启动前对历史目录补齐缺失分类投影。
- 新任务附件必须显式提供 `cloudUseAllowed`；任务附件与作品文件分开。缓存的 Asset 描述来自真实完成的资产校验；缺少可恢复元数据的旧文件仍可依旧审核状态公开读取，但不伪装成可用训练材料。
- P/R 单次 Completion；程序完整读取获准 UTF-8 文本、JSON、PNG/JPEG/WebP，超限或不支持的格式明确拒绝。E 使用固定 Python／shell 镜像及文本工具观察；不包含未实现的原生多模态观察或任意 skills 装载。文件可上传和下载不代表每种格式都能由模型评估。

成熟参考与差异见 [补全参考](references.md#backend-completion-references)。没有进行模型效果实验，不声称拟合度测试证明真实泛化或作品质量。

## 部署与验证方法

九个模块全部进入 [模块清单](../ops/lab/services.json)：Identity、Asset、Voting、Content、Moderation、Discovery、Challenge、challenge-worker、gateway。数据库只由各 schema owner 迁移，运行账号依最小权限授权。下述业务、故障与隔离验证在独立临时集群完成；公网发布要求既有 PR 治理和匹配 SHA 的成功 CI，发布后另验公网接口与九模块完成标记。

部署需要预装 Calico 等实际执行 NetworkPolicy 的 CNI、CNPG，以及固定 gVisor。`ops/lab/execution.py` 只在明确选择的 kind 节点安装 20261019.0 的完整 bundle，并核对固定 SHA-512，不改宿主机 Docker 默认运行时。供应商 Completion 配置存仓库外 0600 文件；部署前预检私有配置与 `handler=runsc`，不在缺失时降级为普通容器。安装可信控制器与前提见 [部署增量](deployment.md#backend-completion-deployment)。

```sh
# backend/，独立测试 PostgreSQL / S3；私有文件不得连接产品库
env GOTOOLCHAIN=go1.27.1 \
 IDENTITY_TEST_DATABASE_URL_FILE=/path/to/test-dsn \
 CHALLENGE_TEST_DATABASE_URL_FILE=/path/to/test-dsn \
 ASSET_TEST_DATABASE_URL_FILE=/path/to/test-dsn \
 ASSET_S3_TEST_CONFIG_FILE=/path/to/test-s3.json \
 go test -race -tags=integration -p=1 ./... -count=1 -timeout=600s

# backend/，沿用上述专用数据库和 S3 环境变量，指定独立执行集群及已加载镜像
CHALLENGE_E2E_KUBECONFIG=/path/to/isolated-kubeconfig \
 CHALLENGE_E2E_IMAGE=docker.io/human-worth/challenge-executor@sha256:411b7628ad2f8630b27a16066bfabbe4ac9f60c879d0db092249556fc2b9f251 \
 GOTOOLCHAIN=go1.27.1 go test -race -tags=integration ./internal/identity \
 -run '^TestBackendChallengeRealSandbox$' -count=1 -timeout=180s -v

# 根目录，已有测试集群；每次独立 namespace，失败也清理
GOTOOLCHAIN=go1.27.1 \
 CHALLENGE_MODEL_CONFIG_FILE=/path/to/private/model.json \
 python3 -u ops/lab/validate_modules.py
```

`validate_modules.py --cluster human-worth-backend-check --kubeconfig /path/to/isolated-kubeconfig` 可用于 [三节点隔离集群配置](../ops/lab/verification-kind.yaml)，必须先安装匹配网段的 CNI、CNPG 和 gVisor。它验证真实旧 v6→新协议升级、历史任务引导、持久待办与拒绝旧版本回退、九模块滚动、真实业务、允许／拒绝网络、错误镜像回退、失败迁移同 SHA 重试和重复部署，不使用公网 namespace 或数据。

真实 E 测试是 `TestBackendChallengeRealSandbox`：真实 PostgreSQL、HTTP、mTLS 和全部业务服务，独立 kind／Calico／gVisor 中运行 Python，验证非 root、只读根目录、无 ServiceAccount token、网络拒绝、文件上传、登记、审核、公开下载和加入投票列表。模型响应为确定性 Completion fixture，保证沙箱检查可复现；另用真实供应商测试 Completion 协议与工具往返，两项证据不混称“真实模型端到端优化效果”。

## 执行记录

测试基线为最新 dev 加本地工作分支改动。历史失败日志保留；代码可编译、测试启动或配置合法都不能代替真实部署 PASS。

- 全量 `go test -race -json -tags=integration -p=1 ./... -count=1 -timeout=600s` 退出 0：613 项测试／子测试 PASS，0 FAIL。专用真实 PostgreSQL 与 S3 已配置。仅 `TestBackendChallengeRealSandbox` 因该命令没有提供独立集群配置而跳过，随后单独运行并通过。日志 `/tmp/human-worth-all-backend-final-2.jsonl`。测试集合同时包含单元、组件 fake 与真实跨服务集成，不能把 613 项全部称为真实端到端测试。
- `TestBackendChallengeRealSandbox` 单独退出 0，18.49 秒；真实 gVisor 执行、7 次确定性 Completion 协议往返、产出上传／登记／审核／下载和投票候选闭环通过。日志 `/tmp/human-worth-backend-kind/e2e-final.log`。随后使用最终部署的 executor digest `411b7628ad2f8630b27a16066bfabbe4ac9f60c879d0db092249556fc2b9f251` 重跑，29.21 秒 PASS，日志 `/tmp/human-worth-backend-kind/e2e-deployed-image.log`。测试器的 TCP 就绪探测会留下 TLS EOF，不是业务调用失败。
- 新增真实业务测试覆盖公开投票／举报／MCP、材料授权／登记关闭、并发结论翻转、MCP 协议与撤销、取消／重跑／迟到结果、监督数据并发幂等及独立验证、目录意图待处理时的新公开读取拒绝。监督快照额外连续三轮通过，日志 `/tmp/human-worth-preferences-final-3.log`。
- `buf lint`、`buf generate` 前后生成文件哈希一致、相对 `origin/dev` 的 `buf breaking`、`go vet ./...` 均退出 0。`npm run ci` 退出 0，5 项 Node 和 35 项 Python 检查通过；日志 `/tmp/human-worth-npm-final-6.log`。
- 完整隔离部署中的旧 v6 升级、历史目录引导、拒绝旧版本回退、九模块滚动与完成标记检查通过。随后真实 kind 业务回归退出 0，用时 409.441 秒：追加投稿、Asset、公开文件、Content、逐调用方授权恢复、Moderation、Voting，以及新增评论／发现流／看板／管理员概览全部通过；134 项真实网络允许／拒绝断言全部通过。日志 `/tmp/human-worth-backend-kind/deploy-final-4.log`。该轮随后在回退后的单次 HTTP 检查失败，故整个验证脚本退出 1，不能把该文件描述为完整脚本 PASS。
- 运维恢复补验退出 0：重新安装九模块并验证 HTTPS／完成标记，错误镜像回退、失败迁移同 SHA 重试、重复部署及自动清理全部通过。日志 `/tmp/human-worth-backend-kind/release-recovery-final.log`。该次专用驱动明确跳过已通过且应用代码未变的旧版本升级、Go 业务和网络检查；各阶段的合并证据支持本轮部署验收，没有声称最后一次运行重新执行了所有阶段。
- 已有数据升级补验 `TestVotingCompletionUpgradePreservesPermanentFacts` 退出 0：先运行已发布的 Voting 001，写入 20 张旧选票、禁投、ballot 与目录回执，再迁移和重复启动分类回填；旧事实逐项保持、领先结论正确且不重复翻转，旧票不伪装为新目录监督样本。日志 `/tmp/human-worth-voting-upgrade-final.log`。
- gVisor 官方固定 bundle 已安装到三个临时节点，退出 0。日志 `/tmp/human-worth-backend-kind/execution-install-3node.log`。
- 真实供应商基础 Completion、结构化输出和 executor 工具协议测试退出 0。日志 `/tmp/human-worth-model-live.log`。没有输出或提交供应商密钥。
- 磁盘清理由独立 subagent 完成，证据 `/tmp/human-worth-disk-cleanup.Zukju6`。最后一轮只移除 13 个无容器引用的旧测试镜像标签、回收 22.52 GB 普通构建缓存并执行 `fstrim`；系统盘可用空间由 132.46 GiB 增至 170.16 GiB，Docker 虚拟盘可用空间由 48.78 GiB 增至 68.22 GiB。系统盘净释放 37.70 GiB，包含主线程删除临时集群后回收的空闲物理块，不能与缓存报告重复相加。保留最终应用／executor、当前公网／回退镜像、产品数据卷、源码和全部 stash；最后一轮 Docker 未重启。
- 验证完成后删除专用三节点 `human-worth-backend-check` 集群，以及本轮专用 S3 容器和其匿名数据卷；原有 `lab` 集群与专用 PostgreSQL 测试容器保留。临时验证 namespace 均已清理。清理后公网 `/api/health` 仍为 `ok`，revision 为 `1fe4d3f926e0e67fddc1d2d457bbb27e1ae1308d`，六个已发布模块均正常；证据见上述清理目录的 `third-*` 文件和 `/tmp/human-worth-backend-kind/cluster-cleanup.log`。
- 创建隔离集群时曾将宿主机 `fs.inotify.max_user_instances` 从 128 临时提高到 4096。清理后现存 root 进程仍持有 130 个 inotify 文件描述符记录（可能含重复描述符，不能直接视为 130 个独立实例）；为避免恢复旧上限妨碍现有服务新建监控句柄，本轮保留运行时值 4096，没有新增持久 sysctl 配置。该环境变化与原值记录于 `/tmp/human-worth-backend-kind/inotify-before.json`，不属于应用发布配置。

本轮回归识别并修复了监督快照空排除列表和并发回执窗口、待处理目录期间任务附件的公开读取门控；测试 S3 的绑定地址与卷容量配置也经过真实上传校验。合入最新 dev 后，原监督 RPC 的“未实现”断言和并发测试的旧锁位置已按新契约更新，仍保留两种提交顺序、永久禁投、统计及持久化结果的严格断言。真实旧版本读取兼容用例保留原链接作品；新投稿测试改为实际文字／文件，以符合 PRD H5。

隔离升级回归发现 Discovery 把 Voting 连接状态计入全局就绪，导致投票停机同时关闭普通发现流。已将就绪依赖限制为 Identity／Content；看板继续逐请求检查 Voting，新增真实 Pod 停机回归要求任务列表 200、看板 503／504。该进程装配变更已编译／vet，并通过上述完整 kind 业务回归。

回退验证原先在 Pod rollout 完成后只调用一次 `healthy`，与正式控制器等待 headless DNS／gRPC 恢复的行为不同。验证脚本现复用既有 `release.wait_health`，最多等待 45 秒；每次请求的时限、预期状态、错误码和 revision 判据保持严格。上述运维恢复补验通过；这项修改修正验证方法，没有提高业务 RPC 的超时。

一次较早的完整运行在管理员概览得到 504，失败时未保留服务日志，根因未确认。没有延长超时或放宽成功断言；单独部署测试和随后完整重跑均通过。完整重跑中该 HTTP 请求为 200／45 ms，Moderation RPC 为 2 ms、Challenge RPC 为 11 ms；日志 `/tmp/human-worth-full-deployment-diagnostics`，请求 ID `1uWhtI3Pza9zd9CooXL27w`。这说明本轮复验通过，不宣称已定位或修复该次偶发超时。

最终隔离部署的应用镜像标识为 `9f9cfc574c0dfa65d6042df6bc669514ceb55869`，这是验证脚本由源码派生的标识，不是 Git 提交。应用源码 SHA-256 为 `3591ea0ff243372b427f33644c87af1210d7a08401fedbedd736ae79255cd155`。较早一轮业务测试编译前曾只同步新增 `discovery_unavailable` 的测试错误码断言，应用源码哈希前后不变，记录 `/tmp/human-worth-backend-kind/lab-test-refresh.json`；最终完整重跑使用新复制的固定源码快照，没有中途修改测试输入。
