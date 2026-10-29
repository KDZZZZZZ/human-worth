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

## 2026-10-28 本机测试环境恢复与重新执行

本节是 `/home/cherry/human-worth-upstream-audit-20261025` 的新记录，不替代或扩写上述其他环境的历史结论。测试分支 `backend/test-upstream-20261028`，固定 Git 基线 `20e1432f3bf1ce0063f09605d14ae9acf2fb578a`。应用源码哈希 `44a4d18e0b1e3ef16ee4f91314fc33661e0157f4160a11bc488338005b7a9818`，派生镜像标识 `8f6bbc422ae15774a0e7f9b9f54ba3dc06493fae`，后者不是 Git SHA。

交接所指的 `/tmp/hw-upstream-e-sandbox-20261026.log` 与 `/tmp/hw-upstream-nine-modules-20261026.log` 已缺失，按人类要求不再追索，重新执行可重复的验证。以下是本轮新证据，不能描述为恢复了旧日志。今后此类任务缺少原始证据时，先核对代码、隔离环境与调用成本，再重跑并保存新记录；历史聊天摘要不代替原始日志。证据持久保存在仓库外 `/home/cherry/.local/state/human-worth/acceptance/20261028-round1-recovery-161034/`，其中 `report.md` 说明结论边界，`commands.jsonl` 保存准确命令、起止时间、退出码和日志哈希，`raw/` 保存输出。

- **环境故障已修复**：root 新建 inotify 在实例上限 128 时返回 errno 24，而进程 nofile 为 1048576；仅将 `fs.inotify.max_user_instances` 改为 4096 后，同一探针立即成功，三个 kube-proxy 自行恢复。新建 `/etc/sysctl.d/90-human-worth-kind-inotify.conf` 保留该值，未发现同项覆盖；没有重启宿主验证跨启动生效，也未确定此前上限回落的原因。Calico 随后仍等待旧 BGP 地址，刷新专用集群 Calico Pod 后，CNI、CoreDNS、CNPG 和存储组件恢复。未重启 Docker/containerd/节点，未修改常驻 `lab` 的 Pod 或配置。
- **代码检查 PASS**：在 backend 执行 `GOTOOLCHAIN=go1.27.1 go test -race ./... -count=1`、`go vet ./...`、`go build ./cmd/...`、`buf lint`；根目录执行 `npm ci --ignore-scripts` 与 `npm run ci`，均退出 0，后者 Node 5、Python 35 项通过。没有重跑 Buf generate/breaking 或全包数据库集成集合。首轮 runner 的 umask 0077 导致权限 fixture 误报，子进程恢复 0022 后通过，业务代码与断言未改；原失败的定向复现及一次同名日志覆盖的证据限制见报告。
- **真实 E 沙箱 PASS**：`TestBackendChallengeRealSandbox` 以 `-race -tags=integration -count=1 -timeout=180s -v` 单独执行，退出 0、14.35 秒，非 SKIP。真实 PostgreSQL、HTTP/mTLS、gVisor Job，7 次确定性 fixture 模型交互，产物登记、审核、下载和进入候选闭环通过。使用本次核对的 executor manifest `sha256:9be2afa7564082119acf4e23137c363ed85f24ec71ec6b1f1732751ebff812e0`。Asset 为 FileStorage，部分服务由测试进程组装，不等价完整 Pod/S3 或真实供应商模型验收。
- **原样完整九模块脚本 FAIL**：执行 `python3 -u ops/lab/validate_modules.py --cluster human-worth-backend-check --kubeconfig /home/cherry/.config/human-worth/backend-check/kubeconfig`，设置仓库外 `CHALLENGE_MODEL_CONFIG_FILE` 和 `GOTOOLCHAIN=go1.27.1`。真实旧版本升级、历史引导、拒绝不兼容回退、九模块双副本、HTTPS 和完成标记通过；Go 测试 484.437 秒，10 项顶层测试中 9 PASS、1 FAIL、0 SKIP。`TestKindAssetDeployment` 在真实 S3 恢复后，普通 Gateway Service 未满足原有 90 秒内恢复两个就绪端点的断言。失败后最终恢复，不构成限时恢复 PASS；根因尚未确定，未延长超时或放宽断言。脚本因此退出 1，首轮网络断言和后续发布恢复检查未执行，不能称整轮通过。日志 `raw/nine-modules.log` 保留，临时 namespace `human-worth-verify-7c739fcc` 及执行 namespace/RBAC 已由 finally 清理。
- **定向复验与发布恢复 PASS，间歇性故障仍未解决**：仓库外驱动只将原脚本的 Go `-run` 集合改为 `^TestKindAssetDeployment$`，差异、完整驱动和日志均留存；未改断言、超时或失败传播。该用例 160.57 秒通过，包含两个普通 Gateway 端点恢复、真实文件/Content 请求、S3 故障期间不提交偏移、相同 PVC 恢复和退出登录后的跨 Pod 拒绝。随后 134 项网络允许/拒绝断言、坏镜像回滚、失败迁移同版本重试和重复完整部署全部通过，驱动退出 0；日志 `raw/asset-recovery-and-release.log`。这是一轮选定用例及后续阶段补验，不是原样完整集合全部通过，也不证明首轮恢复超时已修复。后续应优先复现 AdditionalEntry → Asset 顺序并收集 DNS/gRPC/就绪原因，根因保持 INCONCLUSIVE。

最终核对：两个临时验证 namespace 及其执行 namespace/RBAC、Job、relay Pod、PVC/PV 均已清理；真实 E 的临时数据库/角色已清理，专用 PostgreSQL 管理角色保留。测试所需九模块、PostgreSQL 和 S3 按需部署后清理，不作为常驻环境保留。仅删除本轮原先不存在、由 npm 生成的 `node_modules/` 与 `dist/`，保留镜像、缓存、证据及原有数据。专用集群三节点 Ready、基础设施全部就绪，kube-proxy 重启计数没有继续增长，节点容器启动时间前后相同。常驻 `lab` 最终只读采样的 Pod 也全部就绪，但本轮未对它做业务验收。系统盘剩余约 19 GiB。只读公网健康检查为 HTTP 200、revision 为本轮 Git SHA；不代表公网业务验收。

本轮未进行真实供应商生成请求、前后端最终联调、性能容量、生产 HA/灾备或公网业务验收；模型配置合法和无密钥的 HTTPS 网络探测不证明鉴权、结构化输出或工具调用通过。没有业务源码变更、commit、push、PR、merge 或公网部署。

## 2026-10-28 Gateway 恢复超时定向定位

沿用 Git 基线 `20e1432f3bf1ce0063f09605d14ae9acf2fb578a`、应用源码哈希及镜像标识。证据保存在仓库外 `/home/cherry/.local/state/human-worth/acceptance/20261028-gateway-diagnosis-170558/`，包括 `report.md`、准确命令/退出码/日志哈希 `commands.jsonl`、驱动差异、逐 Pod gRPC 日志与每约 2 秒的 Pod/EndpointSlice 时间线。仅临时 Pod 开启连接诊断日志，没有修改应用、原测试断言、90 秒期限或仓库部署配置。

原 AdditionalEntry → Asset 顺序两次完整复跑通过，其中保留旧版本升级流程的一次 Go 包用时 176.932 秒（52.47/123.43 秒）；后一次省略无关升级。尝试同库 `-count=3` 时，第二次 AdditionalEntry 的固定 DecisionId 引起幂等冲突，属于重复方法不满足 fixture 独立性，失败保留并中止，后续部分结果不算有效成对复验。原始恢复超时 FAIL 未被成功复跑覆盖。

**受控对照确认同类超时机制**：S3 停机使 Asset 全部不就绪，headless DNS 返回空地址，grpc-go v1.84.0 关闭旧子连接并对解析错误逐步退避；S3/Asset/Content 恢复后，Gateway 仍可能等下一次解析。A 组有意延长停机约 279 秒以进入较高退避阶段，两个 Asset Ready 后，90 秒处真实 EndpointSlice 为 Asset=2、Content=2、Gateway=1，最终恢复两 Gateway 用时 **119.15 秒，FAIL**；目标 Gateway 两次解析间隔 **121.94 秒**，新解析拿到地址后约 **1.93 毫秒**连接就绪。当时基础设施全部就绪，应用没有容器重启。

B 组仅在临时 Asset Service 发布未就绪发现地址，保持相同停机时长、应用镜像、mTLS 和 NetworkPolicy；停机期间 Asset/Content/Gateway 仍退出就绪。恢复两 Gateway 用时 **3.25 秒，PASS**，实际新 Gateway 的 Asset 解析更新未出现空地址。驱动整体退出 1，保留 A 失败。B 是因果隔离实验，未覆盖单副本路由与全部业务拒绝，不能直接当作正式修复或上线验收；开关已恢复，临时资源已清理。

**结论边界**：当前发现/连接恢复策略可导致真实入口恢复超时，工程风险已确认；A 的故障时长和恢复相位经过控制，不是原失败时序的原样复现。原始单次失败缺少同步解析日志，精确历史归因仍 INCONCLUSIVE，正式修复未实施。后续应在保留业务就绪/权限拒绝语义的前提下，处理发现与就绪耦合或客户端解析恢复上界，再复验原顺序、短/长故障、单健康副本、每个调用方及真实文件/Content 请求；不以延长断言制造通过。

三轮临时 namespace/执行 namespace、RBAC、Pod、Job、PVC/PV 和 scoped kubectl 进程最终核对无遗留；专用三节点及基础设施健康，专用 PostgreSQL 正常，私有配置均 0600。没有业务源码修改、commit、push、PR、merge、真实供应商调用或公网部署。

## 2026-10-28 Gateway 恢复修复与定向回归

在 `backend/test-upstream-20261028`、Git 基线 `20e1432f3bf1ce0063f09605d14ae9acf2fb578a` 上完成本地未提交修复，未推送、PR、合并或公网部署。应用源码哈希 `ec569d59cd2c711612129a090b958e69c61f19d2288424a21f17a11cb334e5bf`，镜像 revision `9eb4c0ef44c1a454587210036f91d6375acc3806` 是源码派生标识，不是 Git SHA。证据保存于仓库外 `/home/cherry/.local/state/human-worth/acceptance/20261028-gateway-fix-185346/`；`report.md`、`commands.jsonl`、补丁、驱动及差异、逐 Pod 日志和 EndpointSlice 时间线保留首轮失败，不依赖已丢失的历史 `/tmp` 日志。

修复范围是 Gateway → Asset/Content、Content → Asset 三条依赖边：新增只用于内部发现的 `asset-discovery`、`content-discovery` headless Service，保留未就绪 Pod 地址；原 Service 继续按 Pod Ready 过滤。三条连接使用 grpc-go 原生逐副本 `Health/Watch`，仅向 `SERVING` 副本发送业务。Asset 只增加 Gateway/Content 的 Watch 权限，Content 现有 Gateway 流权限适用；不全局启用 Identity 等连接的健康订阅。Watch 随连接取消，不套普通服务端 2 秒期限，业务期限、mTLS、NetworkPolicy、S3 就绪检查与禁用写操作自动重试保留。readiness 状态变化增加安全的依赖/连接/RPC code 诊断，不输出原始私有错误。

- **PASS，代码检查**：backend 下 `GOTOOLCHAIN=go1.27.1 go test -race ./... -count=1`、`go vet ./...`、`go build ./cmd/...` 均 exit 0；Python `python3 -m unittest discover -s tests -p 'test_*.py'` 36 项通过。新增真实 TCP 健康门控测试覆盖单健康/全部不健康/恢复、逐副本轮询、跨 2 秒订阅持续性、端点删除与关闭后释放，以及方法授权、普通流期限和安全日志。仓库外 overlay 去掉健康门控或 Watch 期限例外时，回归均按预期 FAIL，证明断言能识别遗漏。首轮 Python 旧 DNS 地址断言 FAIL 已更新为新发现入口，原失败日志保留。
- **PASS，新增真实 kind 故障回归**：`TestKindDependencyRecovery` 320.15 秒，实际执行非 SKIP。一个健康 Asset 加一个 S3 配置故意错误但可发现的 Asset 时，重建 Content/Gateway 调用方后，20 次正常 NodePort 文件下载通过。S3 停机保持 280 秒并替换 Asset Pod，Asset/Content/Gateway 全部拒绝就绪，发现地址保持；两 Asset Ready 后，两正常 Gateway 用时 **3.261 秒**恢复，原 `waitGatewayEndpoints` 90 秒判据不变；真实 S3 文件内容、Content 请求和退出后的 401 通过。该测量起点是两 Asset Ready，原 Asset 用例起点为指定 Asset `/readyz=204`，不混写为同一测量方法。
- **发现并解决测试转发耦合**：前两轮原 Asset 用例在正常 Gateway 两端点/文件/Content 恢复检查通过后，后续上传因本机转发端口拒绝连接而 FAIL（37.70/90.95 秒）。第二轮转发日志证实，大上传快速返回预期 503 后，未读完请求体的连接发生 RST，`kubectl port-forward` 报 `lost connection to pod` 并退出，导致后续复用该隧道失败；容器没有重启。第一轮转发日志已被旧 `t.TempDir` 清理，不能补造该次完整历史。修正仅将故障大上传置于独立转发进程，仍指向同一个 reader Gateway/Asset；保留原请求体、503/504 Problem code、数据库双 offset=0、恢复同一会话上传、文件一致性和撤销拒绝，不重试、不自动重启隧道、不放宽断言。转发日志改在私有验证 state 留存，诊断驱动删除 state 前归档。
- **PASS，修正后的原顺序复验**：独立 namespace 的 `go test -race -tags=lab ./internal/identity -run '^TestKind(AdditionalEntry|Asset)Deployment$' -count=1 -timeout=1200s -v`，AdditionalEntry 53.55 秒、Asset 44.87 秒，包 99.466 秒、exit 0、0 SKIP。故障专用隧道仍观察到 RST 退出，原 reader 隧道保持正常，恢复上传、原 90 秒检查、S3/PVC 一致性和跨 Pod 会话撤销全部通过。1200 秒是定向驱动的组合包预算；仓库原业务集合 900 秒预算未改。

标准 `validate_modules.py` 已在原业务集合后单独接入新增长故障用例，600 秒总预算覆盖有意保持的 280 秒故障；不延长原 90 秒恢复判据。以上结果验证已确认的发现/就绪耦合机制修复；2026-10-28 原始历史单次失败的精确归因仍受原日志缺口限制。其他调用边尚未统一迁移，也未据此声称全套九模块业务、真实旧版本升级、所有数据库集成、真实模型推理、性能容量、生产或公网验收通过。

最终补充：第三轮外层驱动退出 0，随后原网络检查 **134 项 PASS、0 FAIL**，覆盖 10 个调用方；准确命令、驱动差异、网络逐项输出和汇总见上述证据目录。最终环境核验 PASS：三轮临时 namespace/执行 namespace、相关 RBAC/PV/scoped kubectl 进程无遗留，15 个基础设施 Pod 的 UID、容器 ID、就绪和重启次数与测试前一致；专用 PostgreSQL 正常、gVisor 保留、三个私有配置仍 0600。没有清理原有卷/数据或测试镜像，未修改宿主运行时和系统参数；完整验收脚本本轮未原样重跑，前两轮 FAIL 原样保留。

## 2026-10-28 第二轮关键业务、契约、MCP 与模型故障验收

固定本地提交 `7c47abb123c841de0d8c37a075483c729f048bec`，分支 `backend/test-upstream-20261028`。证据持久目录 `/home/cherry/.local/state/human-worth/acceptance/20261028-round2-200343/`：`report.md`、`commands.jsonl`、`business-results.json`、54 操作矩阵 `api-operations.csv`、逐请求记录、四类问题及最小复现均已保存。历史 `/tmp` 缺失日志未恢复、不作为本轮新证据；可重复验证使用本轮新日志。

- **PASS，业务与权限**：18 个选定顶层、71 个含子测试事件，最终 0 FAIL/0 SKIP；包含身份/CSRF/跨用户和服务身份、投稿/独立审核、幂等回执、永久禁投、投票/统计两种提交顺序、提交未知和恢复、管理员/挑战取消迟到结果。首批因未配 S3 而 SKIP 的项目补齐专用真实 S3 后实际执行，不以父测试 PASS 覆盖子测试 SKIP。
- **PASS，Challenge 恢复专项**：5 个顶层、8 个含子测试事件；真实 PostgreSQL、合成依赖，不称为真实模型或 Pod 验收。
- **PASS，模型故障**：provider/worker/relay/executor 9 个顶层、62 个含子测试事件；新增 `provider_fault_test.go` 覆盖 401/429/500/503、重定向拒绝、空/畸形/截断/超限响应、取消与超时、单次调用及错误脱敏，既有测试验证未知结算与重复派发拒绝。没有真实供应商调用。
- **PASS，常规 Gateway/契约测试**：29 个顶层、147 个含子测试事件。路由/引用静态检查通过不等价于真实响应符合 schema。
- **PASS，独立 MCP**：不导入项目客户端的 Python HTTP 客户端协商 `2025-11-25`；五工具清单、统计副作用明确确认、永久禁投、其他用户可投、拒绝写工具、Origin/Accept 拒绝、撤销后 401 通过。与原 Go MCP 集成证据分别记录。
- **FAIL，API 响应契约**：Schemathesis `4.32.7` 固定依赖/哈希，仓库外独立工具环境；新隔离数据下 189 次请求、27 个 API 操作、18 个失败响应检查，归为四类已复现且未修复的问题：公开任务附件错误复用完整 Asset schema；暂存资产空哈希不符固定 64 位约束；下载真实 mediaType 不符唯一 octet-stream 声明；评论显示名 null 不符 Author string。19 个操作在采样内未发现失败、8 个操作有失败、其余 27 个操作未做独立 API 探索，不能称 54 项逐条通过。相关 OpenAPI 与响应代码在 Gateway 修复前后内容相同，属于本轮发现的既有差异。

S3 补验首轮因临时存储的 4 个 volume 槽耗尽而上传 503；原失败保留，仅重建本轮 tmpfs S3 为每卷 8MiB/64 槽，保持 512MiB 上限、镜像与业务断言，五个相关顶层复验 PASS。API 驱动最初的第三方响应头/异常组适配，以及 staging 下载预期与幂等键长度错误均已修正；最终独立复验无驱动/语义断言异常，schema 检查未放宽，四类问题由最小复现再次确认。业务代码与 OpenAPI 未修改，仅新增模型故障测试及本节记录，未 commit/push/PR/merge/公网部署。

独立入口使用 Go overlay 的真实 HTTPS/mTLS/PostgreSQL/FileStorage fixture，服务进程内装配，不称为九模块 Pod/S3；S3 业务补验使用独立 Docker SeaweedFS，非生产 HA。最终临时服务/测试凭据已清理，无 Docker 卷创建，数据库与角色集合、15 个基础设施 Pod 状态与测试前一致，专用 PostgreSQL 正常，三个原私有配置仍 0600。未运行真实模型推理、MCP Inspector UI、Bruno、性能/安全全扫描、备份灾备、前端最终验收或公网业务验收。第二轮整体结论为 **FAIL：四类契约问题 OPEN**，不能用其他专项 PASS 覆盖；具体建议修复方案和边界见报告。

## 2026-10-29 第二轮响应契约修复与复验

基线 `7c47abb123c841de0d8c37a075483c729f048bec`，当前审计 worktree 为 detached HEAD；2026-10-29 已重新获取并核对上游 dev 为 `20e1432f3bf1ce0063f09605d14ae9acf2fb578a`，上游前端任务分支在相关文件范围与 dev 一致。本次改动尚未提交、推送、PR、合并或部署。前一节的 FAIL 是保留的历史结果，本节记录修复后的新证据，不覆盖首轮失败。历史 `/tmp` 日志没有恢复，可重跑项目使用本轮持久证据。

**Human Design**：用户批准修复第二轮四类响应契约问题，并重新执行第二轮验证；保护正在开发的前端与既有权限边界。**Agent Self-Claimed**：独立 `PublicAttachment`、资产状态与实际哈希条件约束、明确列举下载媒体类型、评论复用 `PublicAuthor`、回归样例及测试组织。

OpenAPI 本轮明确修改：公开附件仅含现有五字段；staging 的实际哈希为空，ready 必须为64位小写十六进制，failed/deleted 可保留实际验证结果；本人/任务附件/过审作品/管理员审核四个下载入口声明现有五种支持媒体类型，同时保留原 octet-stream 声明；评论作者改用已有 nullable `PublicAuthor`。请求路径、operationId、请求格式、认证授权及实际 HTTP 响应行为保持，不用预期哈希冒充实际验证结果，不补造公开生命周期字段，不修改 `Session.Author`。公开数据和暂存语义来自现有 Proto、可信数据来源与已记录边界，不以关闭校验替代修复。

前端联动：当前上游原型的任务、评论和投稿仍使用示例/内存/浏览器存储，实际探测为 `/api/health`；另有 Gateway 内置账户页真实使用 `/api/me` 和 MCP 凭据接口，本轮保持其 Session 契约。前端后续应使用公开附件类型、处理 null 显示名、按资产 state 判断可引用性；OpenAPI 类型/Mock/生成客户端仍须同步检查，不能宣称零兼容影响。可复用的全合成响应样例见 [api-response-contract.json](../tests/fixtures/api-response-contract.json)，不含真实凭据或作品。

新增 [响应契约回归](../ops/lab/test_api_response_contract.py) 使用 JSON Schema 2020-12 校验正反样例，6项测试在旧契约下失败、修复后全部通过。保留 ready 非法/缺失哈希、staging 伪验证值、公开额外私有字段、错误作者类型、Session 空姓名与未支持媒体类型的拒绝。独立验收工具依赖固定在 [requirements-api-contract.txt](../ops/lab/requirements-api-contract.txt)，不是应用运行时依赖或默认 CI 隐含依赖；在仓库根目录以隔离 Python 环境运行：

```sh
python3 -m venv "$HOME/.local/share/human-worth/test-tools/api-contract"
"$HOME/.local/share/human-worth/test-tools/api-contract/bin/python" -m pip install -r ops/lab/requirements-api-contract.txt
"$HOME/.local/share/human-worth/test-tools/api-contract/bin/python" ops/lab/test_api_response_contract.py
```

本轮实际复用既有仓库外 Schemathesis 4.32.7 环境中的同版本依赖，无全局安装。完整命令、cwd、退出码、日志 SHA-256、首轮方法失败和复验保存在 `/home/cherry/.local/state/human-worth/acceptance/20261029-round2-contract-fix-140202/`。静态样例测试不能替代下列真实入口证据：

- **PASS**：18项业务顶层/71个含子测试事件、Challenge恢复5项/8个事件、模型provider/worker/relay/executor 9项/62个事件、Gateway/内部契约29项/147个事件，全部实际重跑，0 FAIL、0 SKIP。真实 S3 从一开始即配置，未以跳过代替执行。
- **PASS**：独立 Schemathesis 客户端195次请求、29个操作，0失败、0生成错误；保留原八个失败操作并新增过审作品/管理员审核下载、管理员/跨用户/待审文件拒绝；独立 MCP 的五工具、确认后永久禁投、其他用户不受影响、拒绝写工具、Origin/Accept和凭据撤销检查通过。仍有25个操作未做独立探索，不称全量54接口逐条验收。
- **PASS**：11个真实 HTTP 响应符合修复后契约，同一响应对照旧契约时10个受影响响应失败，ready-only本人列表作为兼容对照仍通过。四个下载上下文实测 text/plain 字节、媒体类型、attachment、nosniff与no-store保持；其他受支持媒体类型本轮仅做契约检查，不声称全部真实下载重跑。
- **方法问题已纠正**：附加对照驱动首轮误要求本人资产列表也必须违反旧契约；源码及实际结果确认该列表仅返回ready资产，本就兼容。首轮日志/驱动/结果保留，仅修正比较预期，未放宽新契约、业务断言或超时。前一轮叙述若包含“暂存哈希导致本人列表失败”，以原始失败记录和本轮ready-only核对为准。

临时 S3 使用已有固定镜像、loopback随机端口、512MiB tmpfs、8MiB卷/64槽，未使用产品数据或 Docker 数据卷；独立 API 入口为进程内真实 HTTPS/mTLS/PostgreSQL/FileStorage，非九模块Pod/S3。临时fixture、S3和随机凭据已清理，数据库/角色集合和15个基础设施Pod的UID、容器ID、重启与就绪状态不变，三个原私有配置仍0600。没有重跑完整kind、真实E沙箱、真实供应商、性能容量、安全全扫描、备份灾备、前端最终或公网业务验收。
