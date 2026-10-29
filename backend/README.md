# Go 后端

当前后端功能与验证统一见 [后端补全记录](../docs/backend-completion.md)。本次基于 dev `1fe4d3f`，保留原有六模块和数据库事实，新增 Discovery、Challenge／worker 部署以及评论、举报、看板、完整 MCP，共九个部署模块。公网版本和完成状态以 `/api/health` 为准。

下面按日期保留早期阶段记录，其中“未实现／未发布”只描述该次历史验收；当前范围和限制以上述补全记录为准。

## Asset 3C：过审作品文件读取（本地分支，未发布）

2026-10-16 已完成真实 PostgreSQL/HTTP 集成及隔离 kind 双 Asset Pod 验收，最终模块部署检查退出 0；完整审核产品与公网发布仍未完成。

`GET /api/tasks/{taskId}/entries/{entryId}/assets/{assetId}/content` 对匿名提供经本次 Content 审核状态检查的文件流。Asset `CONTENT_TARGET` 指向 Content，校验 mTLS 身份，关闭重试；未配置时公开请求 503，不影响本人私有上传/管理。Content 的 `CheckAssetAccess` 仅接受 asset 服务；`ApplyAssetReview` 仅接受 moderation 服务并以 decision ID、确切来源版本和 expected revision 幂等应用决定，没有 HTTP 直批入口。

先执行 Content owner 的新迁移 `003_reviewed_assets.sql` 和 `ops/content/grants.sql` 再滚动应用；runtime 不能改审核快照、作者或回执。首次批准需已保存的完整人类+agent 草稿包，返回冻结快照的稳定 entry ID。公开读取不接受 query 或 owner 身份绕过，撤销/下架后新请求 404，Content/存储故障不使用缓存授权；私有工作副本编辑不会自动改变审核快照。实际产品 Moderation 工作流和公开任务/作品详情仍未实现。

验证：

```sh
# backend；独立 PostgreSQL，fixture 自动创建随机数据库/受限角色
IDENTITY_TEST_DATABASE_URL_FILE=/path/to/private/test-dsn \
  go test -race -tags=integration ./internal/identity -run '^TestReviewedAssetHTTPAccess$' -count=1 -timeout=120s
# 根目录；真实进程/共享 S3/临时 namespace，包含 3A/3B 相邻回归
DOCKER_CONFIG="$HOME/.config/human-worth/lab/docker" python3 -u ops/lab/validate_modules.py
```

PostgreSQL/FileStorage 测试不是共享存储验收；kind 另运行 `TestKindPublicAssetDeployment`，覆盖两个真实 Asset Pod 的公开文件、撤销/下架/旧决定重放和 Content 停机/恢复。其 Moderation 证书只是测试调用方，非已部署服务。完整契约、边界和实际结果见 [Asset 3C](../docs/backend-asset.md#13-3ccontent-权威授权与过审作品文件agent-self-claimed-实现方案)。没有公网发布或生产 HA 声明。

## Asset 审查修复（2026-10-16，本地未部署）

Challenge 同一 run/attempt/upload 的整条流现在由 PostgreSQL session advisory lock 跨实例互斥；并发重试返回 `Aborted upload_in_progress`，原流结束后可重试找回同一 ready Asset。新上传及 final-before-commit 恢复均从对象存储实际读取并验证大小、SHA-256 与媒体类型，不信任声明值。进行中的 Challenge 流占用一个池连接，取消/异常时释放锁或丢弃连接，不保持长事务。这些属于 Agent Self-Claimed 实现选择，不改变公开权限或 Proto。

`gateway.TestOpenAPIContract` 严格解析源 OpenAPI（包含重复键、本地引用和关键路由检查），随普通 Go 测试执行。新增真实 PostgreSQL/mTLS/共享 S3 回归可用上述私有测试配置运行：

```sh
go test -race -tags=integration ./internal/asset \
  -run '^TestChallenge(UploadPostgresRegression|ConcurrentS3Retry)$' -count=5 -timeout=120s
```

本轮针对性 60 个测试/子测试和最终全量集成 231 个测试/子测试均 PASS、无跳过；不是 kind 重部署或公网验收，完整证据见 [Asset 审查修复](../docs/backend-asset.md#14-2026-10-16-审查缺陷修复agent-self-claimed)。

## Identity 内部分层

`cmd/identity` 手工装配 `transport/grpc → application → domain`；`repo/postgres` 实现应用层仓储与事务端口，`adapter/google`、`adapter/security` 实现外部协议与加密。应用输入输出放在 `application/dto`，不引用 Proto；domain 只依赖标准库。完整目录和调用链见 [Identity 分层](../docs/backend-identity.md#layered-architecture)。

数据库事务由用例通过 `WithinTx` 定义，账号、流程、凭据和审计仓储共享一条事务连接。Google 换码在事务外执行；`identity-admin` 单独装配受限账号用例。迁移 SQL 位于 [repo/postgres/migrations](internal/identity/repo/postgres/migrations)，内容和校验和保持不变。根 `internal/identity` 仅保留集成和架构检查，现有 lab 测试入口不变。

## Challenge 分层与 agent 核心

Challenge 同样由 `cmd/challenge` 装配 `transport/grpc → application → domain`，`application/dto` 使用纯 Go 类型，`repo/postgres` 统一实现 SQL 和事务。完整目录与阅读顺序见 [Challenge 分层](../docs/backend-challenge.md#layered-architecture)。

核心逻辑集中在两个包：`domain.Engine` 管理 R→P→E 的角色输入、结果校验与迭代推进；`agent` 管理一次 Attempt 的领取、续租、P/R 单次结构化调用和 E 命令工具循环。`adapter/rpc`、`adapter/completion`、`adapter/execution` 分别连接外部 RPC、模型 HTTP、Kubernetes 与隔离工作区，核心不直接访问网络、文件或启动进程。数据库格式与 HTTP 字段保持兼容；R 标准生成输入在 Proto 增加可选的完整任务上下文和初始包。

## 开发与检查

使用 Go **1.27.1**、Buf **1.72.0**。依赖固定在 `go.mod/go.sum`；Buf 配置通过固定版本的本地 Go 插件生成代码，不需要另装 protoc。

```sh
cd backend
buf lint
buf generate
git diff --exit-code -- gen
go vet ./...
go test -race ./...
go build ./cmd/...
node --check internal/gateway/web/account.js
```

集成测试需要一套**专用、可创建/删除测试数据库的 PostgreSQL 18**。DSN 保存到仓库外权限 600 的文件；测试每次创建随机数据库与受限账号，结束后清理。不要连接产品数据库。

```sh
IDENTITY_TEST_DATABASE_URL_FILE=/path/to/private/test-database-url \
CHALLENGE_TEST_DATABASE_URL_FILE=/path/to/private/test-database-url \
ASSET_TEST_DATABASE_URL_FILE=/path/to/private/test-database-url \
  go test -race -tags=integration ./... -count=1 -timeout=120s
```

GitHub `CI` 自动启动一次性 PostgreSQL，并执行格式、Proto 生成一致性、vet、上述并发集成测试和所有入口构建。根目录 `npm run ci` 仍负责文档、Node 基座及现有部署控制器；完整本地验收需要两组检查。

Asset 3A 的 `internal/asset/integration_test.go` 使用真实 PostgreSQL migration、随机 owner/runtime 角色、临时 FileStorage 和正式 mTLS gRPC client/server；它会重建 Server 并从同一数据库和目录读取完成资产。3B 的 `internal/asset/s3_integration_test.go` 以隔离 SeaweedFS 4.13/random bucket 验证两个 S3 adapter/Asset Server 实例共享读写、恢复与清理；`internal/identity/asset_s3_integration_test.go` 进一步验证进程内真实 HTTP → Identity → mTLS Asset/Content → PostgreSQL → S3。`internal/identity/asset_integration_test.go` 另用真实 Identity/PostgreSQL 验证 Asset audience、full method、MCP 与撤销。Challenge 边界测试验证完成/取消 Attempt 不能继续上传。上述 Go 集成测试在测试进程内运行。另于 2026-10-16 实跑扩展后的 `ops/lab/validate_modules.py`，最终退出 0：四模块真实 kind rollout、Asset 跨 Pod 文件上传/下载和 Content 绑定、重启/存储故障恢复、网络允许/拒绝及重复部署通过，临时 namespace 已清理；生产 HA S3 和公网部署仍未验收。命令与证据边界见 [Asset 验证状态](../docs/backend-asset.md#10-3b-存储与-kind-lab-状态)。

### Asset 的本地 kind 业务验收

已有 `kind-lab` 基础设施、Docker 权限及所需工具时，从仓库根目录运行：

```sh
DOCKER_CONFIG="$HOME/.config/human-worth/lab/docker" \
  python3 -u ops/lab/validate_modules.py
echo "validation-exit=$?"
```

`DOCKER_CONFIG` 指向不依赖失效 Desktop credential helper 的私有本地配置目录；不覆盖原 Docker 配置，不在此目录或日志中提交凭据。该脚本创建随机 `human-worth-verify-*` namespace，复制代码快照，再以正式服务镜像、证书、受限数据库角色、NetworkPolicy 和真实 SeaweedFS/PVC 部署；不切换公网入口、不修改默认业务 namespace，结束时清理临时资源。

新增 `internal/identity/asset_lab_test.go` 与既有 Content lab 测试复用 `kind_lab_test.go` 的合成账号、会话、CA 和 HTTP 夹具。脚本对快照执行 `go test -race -tags=lab ./internal/identity -run '^TestKind(Content|Asset|PublicAsset)Deployment$' -count=1 -timeout=480s -v`；不带有效临时 namespace 的直接执行会 `SKIP/INCONCLUSIVE`，不算通过。

Agent Self-Claimed 验证方案：从正式 Gateway 模板创建临时 probe Pod，仅将其 `ASSET_TARGET` 固定到两个不同 worker 上的 Asset Pod IP；保留 mTLS/安全配置，并以未满足的 readiness gate 排除出普通 Gateway Service。由此定向验证 HTTP 上传/续传/完成/下载确实跨 Pod，而非依靠随机均衡碰运气。测试还检查 A/B 隔离、幂等、Content 绑定及 revision、实际 S3 对象与数据库摘要、过期/取消清理、Asset Pod 重建、copy+delete 后数据库未提交的故障恢复，以及暂停/恢复 S3 后 `/readyz` 和写入/下载 fail-closed。

探针和对象存储观察仅在测试 namespace 内工作；其额外 Gateway Pod、loopback port-forward 和测试凭据随测试清理。单实例 SeaweedFS/PVC 故障恢复不是生产 HA 演练；FileStorage 仍仅供显式单进程测试。

## 程序入口与配置

| 入口 | 用途 | 必需配置 |
| --- | --- | --- |
| `cmd/identity` | mTLS gRPC `:8443`；内部探针/指标 `:8081` | `IDENTITY_DATABASE_URL_FILE`、`IDENTITY_ENCRYPTION_KEYS_FILE`、`IDENTITY_SIGNING_KEYS_FILE`、`PUBLIC_ORIGIN`、`SERVICE_CERT_FILE`、`SERVICE_KEY_FILE`、`SERVICE_CA_FILE` |
| `cmd/gateway` | HTTPS/HTTP `:8080`；内部探针/指标 `:8081` | `PUBLIC_ORIGIN`、`IDENTITY_TARGET`、三个 `SERVICE_*_FILE`；实验 HTTPS 再提供 `HTTP_TLS_CERT_FILE`、`HTTP_TLS_KEY_FILE` |
| `cmd/content` | Content mTLS gRPC `:8443`；内部探针/指标 `:8081` | `CONTENT_DATABASE_URL_FILE`（运行账号）、`IDENTITY_TARGET`、可选 `ASSET_TARGET`、三个 `SERVICE_*_FILE`；证书身份为 `content` |
| `cmd/content migrate` | 独立 Content 迁移，不访问 Identity 表 | `CONTENT_DATABASE_URL_FILE`（schema owner） |
| `cmd/asset` | Asset mTLS gRPC `:8443`；内部探针/指标 `:8081` | `ASSET_DATABASE_URL_FILE`（运行账号）、`IDENTITY_TARGET`、公开文件读取再配 `CONTENT_TARGET`、三个 `SERVICE_*_FILE`、`ASSET_STORAGE_BACKEND=s3`、`ASSET_S3_ENDPOINT`、`ASSET_S3_BUCKET`、`ASSET_S3_REGION`、`ASSET_S3_ACCESS_KEY_FILE`、`ASSET_S3_SECRET_KEY_FILE`、`ASSET_S3_FORCE_PATH_STYLE`；filesystem 仅显式测试开关 |
| `cmd/asset migrate` | 独立 Asset 迁移，不访问 Identity/Content 表 | `ASSET_DATABASE_URL_FILE`（schema owner） |
| `cmd/identity migrate` | 单独执行有锁、带摘要检查的迁移 | 仅 `IDENTITY_DATABASE_URL_FILE`，使用 schema owner；运行服务不负责 DDL |
| `cmd/identity-admin` | 角色/状态变更、身份版本递增和审计 | 运维专用数据库文件、`OPERATOR_IDENTITY`；见 lab 的 `account.py` |
| `cmd/challenge` | Challenge mTLS gRPC、探针及持久化待办调和 | `CHALLENGE_DATABASE_URL_FILE`、`IDENTITY_TARGET`、`CONTENT_TARGET`、`ASSET_TARGET`、`VOTING_TARGET`、`CHALLENGE_MODELS`（逗号分隔白名单）、三个 `SERVICE_*_FILE` |
| `cmd/challenge migrate` | 单独迁移 challenge schema | `CHALLENGE_DATABASE_URL_FILE`，使用 schema owner |
| `cmd/challenge-worker` | 按租约运行 P/R；选配 E Job 与 HTTPS 中继 | `CHALLENGE_TARGET`、`WORKER_INSTANCE_ID`、三个 `SERVICE_*_FILE`；P/R 配置 `CHALLENGE_MODEL_CONFIG_FILE` |
| `cmd/challenge-executor` | 固定隔离镜像中的 Completion 执行入口 | 由 worker 注入 `/control`、`/work` 和 `CHALLENGE_RELAY_URL`；不作为宿主机命令运行 |

Identity 启用 Google 时设置 `GOOGLE_OAUTH_CLIENT_ID`、`GOOGLE_OAUTH_CLIENT_SECRET_FILE`、`GOOGLE_OAUTH_REDIRECT_URI`、`GOOGLE_OAUTH_CONFIG_VERSION`。缺少整组 Google 配置时已有会话认证可运行，新登录返回 503；Client ID 已设置但必要字段缺失时启动失败。正式程序只能使用 Google 官方端点。

`PUBLIC_ORIGIN` 是精确 HTTPS origin；不从外部 Host 推导回调。gateway 直接提供 HTTPS 时无需信任代理头；确需 TLS 终止代理时，才设置 `TRUSTED_PROXY_CIDRS` 并由该代理覆盖 `X-Forwarded-Proto`。站点图标复用 `public/brand/logo.png`，镜像中由 `SITE_LOGO_FILE=/assets/logo.png` 指定。

两个密钥环分别为 AEAD 和内部断言使用独立随机 32 字节密钥，文件形状为 `{"active":"key-id","keys":{"key-id":"<base64>"}}`。它们只由 Identity 持有；原始会话/MCP token 只存摘要，Google token 不持久化。实验脚本生成私有文件，仓库不保存可用示例密钥。

从**仓库根目录**构建单个应用镜像：

```sh
docker build --provenance=false --sbom=false -f backend/Dockerfile \
  --build-arg SERVICE=identity --build-arg REVISION=development \
  -t human-worth/identity:development .
```

`SERVICE` 可取 `identity`、`gateway`、`content`、`asset`、`moderation`、`voting`、`discovery`、`challenge`、`challenge-worker`、`identity-admin`。普通镜像默认 `service` target；worker 使用 `--target worker`，包含固定版本 kubectl。E 使用独立 `Dockerfile.executor` 的 Python／shell 镜像，按 digest 引用；主供应商密钥和集群凭据留在可信 worker。

## Challenge 启动与验证

先用 owner 执行迁移，再为运行账号授予 `challenge` schema 的 USAGE、`schema_migrations` 的 SELECT、`runs/claims/receipts/model_calls` 的 SELECT/INSERT/UPDATE，以及 `audit_events` 的 INSERT 和 identity sequence 的 USAGE。运行账号不得拥有 DDL、审计修改或其他 schema 的权限；集成测试使用独立 owner/runtime 账号验证这些边界。

gateway 配置 `CHALLENGE_TARGET` 后启用六个管理员 HTTP 操作；仍通过 Identity 校验管理员网站会话、用途及写请求 CSRF。API 对照见 [OpenAPI](../openapi.yaml)。依赖 fake 仅存在于测试二进制，正式服务必须连接真实 Content/Asset/Voting；后台待审登记并不代表作品审核通过。

模型配置采用仓库外权限 `0600` 的普通 JSON 文件，字段为 `base_url`、`model`、`protocol`、`api_key`。P、R、E 的 `protocol` 均为 `completion`，直接请求 `/v1/chat/completions`；E 默认复用同一份配置。每个 worker 只绑定一份 P/R 模型配置，启用的模型白名单应与实际 worker 能力一致；不能混用不同供应商路由并沿用旧 R 拟合记录。下面的检查只发送极小文本请求，不包含业务数据：

```sh
CHALLENGE_MODEL_CONFIG_FILE=/path/to/private/model.json \
  go test -tags=live ./internal/challenge/adapter/execution -run 'TestLiveProvider|TestLiveStructuredResult|TestLivePackerResult|TestLiveExecutorCompletion' -count=1 -timeout=6m
```

`TestLivePackerResult` 使用真实供应商回归文件任务的规划阶段：P 将管理员确认的任务整理为执行计划，任务里的工具操作由后续 E 完成；P 本次不调用工具。返回仍经过严格 JSON 解析，拒绝文本不能当作有效计划。

E 的文件交付遵守 Asset 的现有类型限制：`text/plain`、PNG、JPEG、WebP 和 PDF。CSV、JSON、源码等 UTF-8 文本作品保留原文件名与内容格式，以 `text/plain` 交付；Asset 仍核对实际内容、大小与摘要，不因模型声明跳过检查。

完整真实模型回归使用独立 PostgreSQL、一次性 `kind-human-worth-backend-check` 集群、Calico、gVisor 和固定 digest 的执行器镜像；测试拒绝生产集群上下文。先按 `ops/lab/verification-kind.yaml` 准备集群及隔离运行时，再运行：

```sh
IDENTITY_TEST_DATABASE_URL_FILE=/path/to/private/test-dsn \
CHALLENGE_MODEL_CONFIG_FILE=/path/to/private/model.json \
CHALLENGE_E2E_KUBECONFIG=/path/to/private/verification.kubeconfig \
CHALLENGE_E2E_IMAGE='human-worth/challenge-executor@sha256:<verified-digest>' \
CHALLENGE_LIVE_OUTPUT_DIR=/path/to/validation-output \
  go test -tags=integration,live ./internal/identity -run '^TestBackendChallengeLiveModel$' -count=1 -timeout=25m
```

两项任务分别生成精确字节的文本文件、读取 CSV 附件并汇总分类净收入。P/R/E 均调用配置的真实模型，E 在 gVisor 中实际执行命令；检查管理员登记与重放、审核前不可见、审核后下载内容及投票候选更新，并可导出作品和运行回执。业务服务使用真实 PostgreSQL、HTTP 和 mTLS，资产存储使用测试文件系统；登录、原有作品和训练投票是合成夹具，不据此证明 S3、真人偏好拟合质量或生产发布。

2026-09-20，用户提供的 `gemini-3.8-flash` 基础 Completion、read_material 工具往返，以及 E 经中继的 run_command 工具往返、最终文本作品和预算结算均通过。E 协议测试返回固定工具观察，没有在宿主机执行模型命令；多模态、真实排名质量和真实隔离执行需各自验证。测试密钥留在仓库外。

启用 E 时配置 `CHALLENGE_EXECUTOR_PROFILE_FILE`、`RELAY_CERT_FILE` 和 `RELAY_KEY_FILE`。默认使用 `CHALLENGE_MODEL_CONFIG_FILE`；只有选择不同模型时才另外设置 `CHALLENGE_EXECUTOR_MODEL_CONFIG_FILE`，该文件也必须是 Completion 配置。profile 字段为 `kubeconfig`、`context`、`namespace`、`image`（必须带 SHA-256 digest）、`runtime_class`（gvisor/runsc，handler 为 runsc）、`relay_url`（HTTPS、9443 端口、指向该 worker 实例）、`ca_file`。worker 使用受限 kubeconfig，业务证书与供应商密钥不进入 E Pod；每个实例必须使用独立客户端证书，不能只靠实例名隔离身份。

[执行策略清单](../ops/challenge/execution-isolation.yaml)提供命名空间、RBAC、网络白名单和总资源限制；worker 校验该命名空间仅存在这份网络策略。部署前仍必须确认 CNI 实际执行拒绝规则。使用 [Dockerfile.executor](Dockerfile.executor) 和已核验、固定 digest 的 `EXECUTOR_IMAGE` 构建 E 镜像，只要求包含 `/bin/sh` 及任务所需语言工具。最新隔离实测见补全记录。可信控制器在部署前预检 gVisor，再安装固定隔离策略；gVisor 节点运行时由 `ops/lab/execution.py` 单独准备。不能通过 privileged 或禁用沙箱绕过失败。

每个 E Attempt 固定 Job 名，禁止自动重试；独立卷、非 root、只读根文件系统、无 ServiceAccount token，限 1 CPU/2 GiB 内存/1 GiB 临时存储。成功回传后先删除 Job 再提交工作；清理失败会失败收尾，同实例重启按标签清理旧 Job，Job TTL 及 Secret ownerReference 提供额外回收。重启时的 `WORKER_INSTANCE_ID` 应保持稳定，多个活跃实例不能复用。

首版不提供额外 skills 装载；默认 harness 为 `completion-shell`，E 只使用 `run_command` 读写文件和验证作品。命令最多 30 秒、输出最多 64 KiB 并标明截断；超时／取消结束进程组。E 当前只传文本消息，附件下载到隔离目录，原生多模态观察尚未实现。轮数均限制 1～100，预算单位固定 `model_calls`、整数 1～100000；P/R 每个 Attempt 只允许 1 次模型调用、0 次工具调用；E 最多 8 次模型调用及 16 次工具调用，总期限 2 小时。P/R 支持 UTF-8 文本、JSON 和 PNG/JPEG/WebP；未知格式明确失败，单文件上限 16 MiB、图片 8 MiB；P/R 读取前还限制整批原始材料不超过 16 MiB，编码后的模型请求也不超过 16 MiB。程序按 64 KiB 流式读齐文件并核对完整摘要，再一次性提供给 P/R；模型不调用读取工具。超过完整材料或上下文容量时不截断评测。

R 校准每轮为 LLM 生成标准 → 独立 LLM 判断；达标后固定标准。标准生成可读完整授权业务内容，包括初始包、评论、作品和真人训练偏好；判断使用独立样本，P/E 的材料边界保持不变。服务端只保存当前优化提示和当前完整反馈，不创建提示历史树。每次调用均预留一个调用单位，未知结果保留预留且不能重发；管理员响应不包含余额或已用数值。模型调用、登记和完成回执用于恢复，长期部署前需按业务保留期补充这些回执的归档策略。

## 可观测性

业务日志只记录方法、状态、耗时、request ID、trace ID。指标包含 HTTP/RPC 延迟、Go runtime、进程及数据库连接数；`/metrics` 仅位于内部探针端口。数据库 span 不带 SQL/参数，Google span 不带授权码或 token。

设置 `OTEL_EXPORTER_OTLP_ENDPOINT` 后向明确接收端导出 trace，默认采样 10%、有界队列 512、导出超时 2 秒。当前 lab 尚未安装 Prometheus/Grafana/Tempo，也未开放 pprof 或运行容量压测。没有接收端时仍生成日志关联用 trace ID。

## Content 草稿本地接入

1. 按 [Content 数据库初始化](../docs/backend-content.md#database) 创建隔离的 `content_owner` / `content_runtime`，先迁移再授予最小运行权限。
2. 运行 Content 时使用 **content 服务证书**、运行账号的 DSN 文件、`IDENTITY_TARGET`；不提供 Identity 签名密钥、加密密钥或数据库密码。
3. gateway 设置 `CONTENT_TARGET`（例如 `dns:///content:8443`），客户端同时校验 DNS SAN `content` 和 SPIFFE 服务身份 `content`。没有设置时保留 Identity-only 启动，草稿请求明确返回 503；设置后 gateway 就绪检查也包括 Content。
4. 本机并排运行多个程序时为 `GRPC_LISTEN` / `HEALTH_LISTEN` 分配不同端口，不能直接共用默认端口。
5. 使用登录 Cookie，写操作再带 `Origin`、`X-CSRF-Token`；四个 HTTP 路径、分页参数与 JSON 示例见 Content 文档。没有前端页面改动。

Content 集成测试复用 Identity 测试供应方与登录流程，启动 **两个实际 Content OS 进程**、两个 HTTPS gateway、mTLS RPC 及受限 PostgreSQL 账号；停止并重启一个 Content 后再次读取详情与本人分页列表。入口是 `internal/identity/content_integration_test.go`（放在 Identity 测试包仅为复用现有真实 OIDC/证书/数据库 fixture，生产 Content 不导入 Identity 实现）。既有 `go test -race -tags=integration ./...` 自动包含此检查。

Content 已接入 [模块自动部署](../docs/deployment.md#module-deployment)：数据库角色、证书、网络规则、迁移、双副本及部署检查进入同一 CI 后流程。2026-09-21 首次发布 `cbca132` 的 Deploy Action 与公网草稿创建、读取、更新和权限验收均通过；完整记录见部署文档。

后续模块实现时同时登记 [services.json](../ops/lab/services.json) 和自己的应用 YAML，声明依赖、数据库与只读部署检查；不为每个模块另写 Action。`/api/health` 的可选 `deploymentRevision` 与 `deployedServices` 由控制器在全部模块成功后写入，普通 `revision` 只表示当前 gateway 版本。

## 阶段 4：正式送审与 Moderation（本地增量，未发布）

设计与兼容规则见 [Moderation](../docs/backend-moderation.md)。Asset PR #27 已合入上游 dev；本轮 `backend/feat-moderation-review` 从 `b2a7399` 开始。下述技术选择为 **Agent Self-Claimed**。

- `POST /api/me/tasks/{taskId}/review-requests`：网站＋CSRF，`{"expectedRevision":1}`；齐备human/agent与ready本人file后，同事务保存不可变快照、pending状态及outbox。相同任务/版本找回原记录。待审不可编辑；驳回后PUT产生新revision/draft，再送审产生新案件，历史保留。
- 管理员 `GET /api/admin/reviews`、`GET /api/admin/reviews/{reviewId}`、`POST /api/admin/reviews/{reviewId}/decision`；拒绝普通用户/MCP，approve可选reason并原样保存，reject必填非空白reason；有效UTF-8、无NUL、最多4000字节。决定落库返回202/pending，收到Content回执才200/applied，旧决定重放不改变新版本或恢复展示；相同决定但不同理由409。新增Moderation `002_approval_reason.sql`兼容旧案件，保留001校验和。
- `GET /api/admin/reviews/{reviewId}/assets/{assetId}/content`：管理员专用Asset/Content断言＋确切pending快照引用，Open后再次验证，非通用管理员下载/owner绕过。Asset无审核状态。
- `cmd/moderation` 独立服务，`MODERATION_DATABASE_URL_FILE`、`IDENTITY_TARGET`、`CONTENT_TARGET` 和已有 `SERVICE_*_FILE`；服务身份moderation。`cmd/moderation migrate`只用owner DSN，运行时用受限runtime。新增 `ops/moderation/bootstrap.sql` / `grants.sql`。Content新增004迁移并重跑其grants，不改001～003；迁移不由运行服务执行。
- Content和gateway增加 `MODERATION_TARGET`。双方PostgreSQL outbox领取提交后才调用gRPC，15秒持久租约，1秒轮询、每条5秒RPC，关闭自动RPC重试。Content就绪不依赖Moderation、Moderation就绪不依赖Content，避免启动环；Gateway就绪也不依赖Moderation，审核路由单独503，登录/草稿/上传与Content送审继续可用。catalog部署排序不同于请求期readiness；其余必要依赖故障仍fail-closed。
- 旧 `ApplyAssetReview` 保留编号和历史回执及下架/撤销；新草稿首次批准必须正式送审，正式投稿不能通过旧RPC重新批准。既有3C测试明确作为旧已部署快照升级兼容测试，不能冒充真正Moderation验收。

真实组件测试复用Identity专用数据库/OIDC/PKI夹具（不是产品库）：

```sh
cd backend
IDENTITY_TEST_DATABASE_URL_FILE=/path/to/private/test-dsn \
 go test -race -tags=integration ./internal/identity \
 -run '^TestModeration(HTTPOutbox|IdentityAuthorization|ApprovalMigration)$' -count=1 -timeout=120s
```

真实kind：根目录运行 `DOCKER_CONFIG="$HOME/.config/human-worth/lab/docker" python3 -u ops/lab/validate_modules.py`，含真实Moderation两Pod和 `TestKindModerationDeployment`。一次性namespace使用动态私有NodePort，不占公网固定端口；Moderation停机后通过正常Service/ready endpoints、新TCP连接验证其他接口可用及审核503，再恢复投递。Content停机的回执恢复子测试仍用诊断port-forward，不声明全局Content故障隔离。不使用测试reviewer证书替代新审核工作流。此前3C测试证书记录只证明兼容应用端。普通/真实PG/FileStorage/共享S3/kind分别记录；执行结果追加到Moderation文档，不从命令启动或Proto生成推断PASS。

validator的登录发起检查使用临时合成OAuth client，仅验证真实Identity登录流程持久化和302构造，不跟随跳转/换码、不读取真人凭据，不代表真人Google登录成功。配置与隔离namespace一起清理。

本轮不实现公开任务/作品详情、追加投稿、举报平台、Voting、Discovery、Challenge完整联调、自动审核/病毒扫描、生产S3 HA或公网部署。最多声明“审核及过审文件链路”，不能声明“公开内容浏览”。本地kind双副本共享SeaweedFS不等于生产HA。

## 阶段五：公开详情与过审作品分页（本地增量，未发布）

从已合入阶段四的dev `e2466a3`（PR #28）新建 `backend/feat-public-task-browse`。实现 `GET /api/tasks/{taskId}` → Content.GetTask、`GET /api/tasks/{taskId}/entries?limit=20&cursor=...` → Content.ListApprovedEntries。匿名/网站/MCP均由Identity方法级断言与Content mTLS复核，失效凭据不降级匿名；只读冻结过审快照/当前published+approved。三次固定RPC、每操作一条SQL，无逐条Identity/Asset/Moderation，无统计/资格副作用，不依赖Moderation在线。

具体投影/排序/可空字段均为 **Agent Self-Claimed**：author.id为稳定作者，displayName/heat暂null，旧publishedAt/approvedAt未知为null；新增批准才记录真实数据库时间；taskAttachments当前[]，不从作品文件推导。公开契约不返回agentConfiguration/permissions/审核信息。当前兼容合法仅外链作品；不自动抓取。完整规则、契约调整及curl示例见 [Content阶段五](../docs/backend-content.md#10-阶段五公开任务详情与过审作品分页2026-10-17未发布)。

先以owner执行Content `005_public_browse.sql`（只加可空时间），Readiness再放行；001～004不变，现有grants无需扩权，runtime不能更新时间/快照/作者或跨schema。复用既有mTLS/CONTENT_TARGET与文件入口，Asset无新状态/迁移。

```sh
# backend；必须专用PG及真实S3测试配置，缺失为INCONCLUSIVE而非通过
IDENTITY_TEST_DATABASE_URL_FILE=/path/to/private/test-dsn \
ASSET_S3_TEST_CONFIG_FILE=/path/to/private/s3-test-config.json \
 go test -race -tags=integration ./internal/identity \
 -run '^TestPublic(TaskBrowsing|BrowseMigrationUpgrade)$' -count=1 -timeout=120s
# 根目录，隔离namespace、正常私有NodePort，不改公网入口
DOCKER_CONFIG="$HOME/.config/human-worth/lab/docker" python3 -u ops/lab/validate_modules.py
```

本阶段只补已知task ID的公开阅读；不实现任务推荐/搜索流、Discovery/Voting、ballot、追加作品、MCP传输或前端。当前正文版本为source_revision；展示收紧使用另一内部revision。游标不是权限，每页重新检查展示；作品撤销/任务下架后新读取和文件拒绝，旧批准重放不能恢复。不把本地测试描述为生产部署。

2026-10-17本轮本地验收：普通/race各138测试通过，真实PG/S3全量race集成262测试通过，公开专项连续三轮24测试通过，均无失败/跳过；最终完整kind validator退出0（两次完整重跑通过，双Content读取及Moderation停机走正常Service/NodePort），根目录CI为5个Node/28个Python通过。独立Proto重生成一致，breaking基线为开发前dev `e2466a3`。首次kind既有文件503未稳定复现，保留观察项与失败日志；完整命令、预期生成差异退出1及证据见Content文档执行记录。可提交审查，仍未发布，不代表生产S3 HA、Voting或公网验收。

### 2026-10-22 第五阶段恢复整合（未提交、未发布）

从实际fetch后相同的 `origin/dev` / `upstream/dev@665bf4d9c76fab14e58124594dc1975d4f70a40f`（Issue整改PR #30）创建 `backend/feat-public-task-browse-integration`，无提交地移植封存 `15fa65e`。原封存和Issue分支不变。保留操作目录驱动策略/超时/Identity目标、显式Gateway handler、共享outbox/技术错误/request ID以及Asset分层、5MiB multipart与恢复。只恢复上述公开正文及作品分页，不新增业务。

以下为 **Agent Self-Claimed** 整合方案：`GetTask` planned descriptor例外移除，其他合理例外保留；005在最新Content基线未被占用，可追加，runtime无需扩权。owner先迁移，再滚动Content/Gateway；如要求升级窗口的新批准均有精确时间，先暂停决定投递并排空旧Content，全部升级后恢复，旧未知时间始终NULL。两个404发布探针不代替业务验收。真实命令、验收矩阵及本次结果见 [恢复整合记录](../docs/backend-content.md#11-第五阶段恢复整合2026-10-22未发布)；不引用2026-10-17历史数量作为本次结果，不声称远端CI/公网或生产S3 HA完成。


本次重新验收（2026-10-22）：普通及race各220测试/子测试、公开专项三轮24、真实专用PG/S3全量race集成411，均退出0且无失败/跳过；完整kind validator退出0（678.82秒，4项真实业务测试及网络/恢复/重复部署通过），隔离namespace已清理。独立Proto重生成一致，breaking对比本次dev `665bf4d`，根目录CI为5个Node/28个Python通过。必要本地验收PASS，可提交独立审查；原第五阶段/Issue/第四阶段分支不变，未commit/push/PR/公网发布。完整命令、证据与后续追加作品边界见上述恢复整合记录，不使用历史测试数量替代本次结果。

## 第六阶段：向公开任务独立追加作品（本地验收完成，未发布）

**Human Design**：任何网站用户可向公开任务单方追加，只审核追加作品，驳回可按版本重提；不改变原任务/初始包或投票资格。**Agent Self-Claimed**：基线 `dc8e5758926b685e8bc4152190ddf82fce7abbbd`（PR #31，已包含第五阶段），任务分支 `backend/feat-additional-entry-review`；独立投稿/快照/单件决定回执、兼容RPC及迁移方案见 [Content第12节](../docs/backend-content.md#12-第六阶段独立追加作品2026-10-23本地实现与验收未发布)。

新增 `POST /api/tasks/{taskId}/entries`（单件EntryInput＋Idempotency-Key）、`GET/PUT /api/me/entries/{entryId}`（PUT expectedRevision＋content）、`GET /api/me/submissions?kind=entry`。POST 201只表示本地接受，Moderation停机仍保留pending/outbox；不是远端案件确认。仅本人网站会话、写操作CSRF，MCP不能写。默认kind=task及其draft筛选保持；kind=entry默认全部追加作品审核状态，初始/cloud不通过此入口返回。

先执行Content006和Moderation003 owner迁移与各自grants，再全部滚动Content/Moderation，最后开放新Gateway入口。旧实例不能解释单件案件，不能仅凭Proto兼容就在混合版本中开放。公开列表保留初始顺序并按实际作者合并追加冻结作品；旧v1游标仍可继续，追加位置使用v2；每页重查当前展示资格和2MiB字节预算。Asset协议/存储/持锁上传不重构，不新增审核状态。

验收要求：专用PG/S3＋真实HTTPS/mTLS身份链路，再隔离kind namespace的真实Moderation/双Content与正常Service链路。实际命令/退出码和未覆盖范围见Content记录；本地通过不等于远端CI、公网发布、生产HA或Voting完成。无自动commit/push/PR权限。


2026-10-23本轮最终验收：普通/race各322测试/子测试PASS；配齐专用PG/S3的全量race integration命令556项PASS（包含322常规＋234标签新增项，组件fake与真实链路边界见Content记录），无测试失败/跳过。完整kind validator退出0（730.31秒），5项真实业务、网络隔离、正常Service/NodePort故障、失败rollout/迁移重试及重复部署通过，临时namespace、独立PG/S3和凭据已清理。Buf正式生成/独立比较及相对开发基线breaking、各vet/build、根CI（5Node/28Python）均0。修复了并发重提版本查询窗口的幂等误冲突，保留修复前证据。可提交独立审查，当前50个文件未暂存/未提交；未push/PR/合并/公网发布，生产容量/HA及Voting不在本轮验收内。

## Voting 第七阶段（2026-10-25，本地实现；未发布）

**最新补验状态：第七阶段完整本地验收通过，可进入提交与独立审查。** 已复现Asset→Content连接恢复窗口，补逐调用方checkpoint、确定性回归与真实Pod故障恢复；原两项文件断言仍严格要求404。正式完整kind退出0（965.685秒），常规race323项、PG/S3全量integration604项、checkpoint连续10轮30项均通过。历史失败、修复、实际证据与未覆盖范围见[Voting文件503补验](../docs/backend-voting.md#16-文件503定位与逐调用方恢复回归2026-10-25)；未commit/push/PR/合并/公网发布。

[完整设计、锁序与验收矩阵](../docs/backend-voting.md#voting-stage7)。原1～12节为保留的规划历史；当前实现以第13节及后续实际执行记录为准。Human Design为完整过审集合单选、显式统计先永久禁投、网站/MCP同账号与管理员无豁免；改选、ballot期限、目录协议、预算及DDL属于Agent Self-Claimed。

| HTTP | 正式Voting RPC | 用途 |
| --- | --- | --- |
| `GET /api/tasks/{taskId}/voting-eligibility` | GetVotingEligibility | 网站/MCP本人资格；不返回统计 |
| `GET /api/tasks/{taskId}/ballot` | GetBallot | 网站；一次全部过审作品及本人当前票 |
| `PUT /api/tasks/{taskId}/vote` | CastVote | 网站+CSRF；ballotId/entryId/expectedVoteRevision |
| `POST /api/tasks/{taskId}/statistics-access` | ViewTaskStatistics | 网站+CSRF或MCP；明确true后先持久禁投再统计 |

`cmd/voting`独立进程，`VOTING_DATABASE_URL_FILE`、`IDENTITY_TARGET`、`CONTENT_TARGET`及统一`SERVICE_CERT_FILE/SERVICE_KEY_FILE/SERVICE_CA_FILE`。`voting migrate`只由owner执行；随后应用`ops/voting/grants.sql`。Content/Gateway增加`VOTING_TARGET`，但不把它加入全局readiness；Voting就绪只依赖本schema和Identity连接，避免Content↔Voting递归探针。缺配置不能伪装业务成功。

升级必须先Content新增007迁移、Voting001及最小grants，再滚动所有支持目录栅栏的Content/Voting实例；运行`content bootstrap-voting`完成旧公开任务目录引导，最后开放Gateway。引导失败返回非零，重跑找回持久意图；blocked需查明原因，不能通过清空gate/删除关闭事实恢复。`services.json`登记Voting双副本与网络；部署排序为Identity→Asset/Voting→Content→Moderation→Gateway，不等于请求期readiness依赖。回滚先停止投票写入并处理在途/冻结，禁止直接恢复无栅栏旧Content。

首次六→七升级由可信控制器排空旧Content，接受一次短暂中断；后续兼容实例才滚动更新。`content catalog-protocol-version`无数据库/密钥依赖，用于核验实际镜像是否支持目录栅栏。持久启用标记后不兼容回滚直接拒绝，保留当前Content/Voting前向恢复；先更新仓库外安装的控制器，不能只更新业务镜像。具体探针、状态备份与恢复约束见[部署边界](../docs/deployment.md#voting-第七阶段滚动与回滚边界2026-10-25agent-self-claimed)。

ballot有效期15分钟，最多4096项且完整Proto/HTTP响应各限2MiB（预留逐项null/default envelope）；不分页、不截断。候选只用冻结白名单，目录hash绑定控制generation与各业务版本。Voting每进程限制同时处理两个目录业务，额外调用在方法deadline内等候，不在等候时加载完整目录；这不是宿主机/生产容量基准。关闭事实只允许INSERT/SELECT，不授予runtime更新/删除；选票改选只更新一条，统计用同一RR只读快照，含0票项，conclusion=undetermined。旧Challenge两个RPC继续Unimplemented且只允许Challenge身份，不提供监督数据或统计旁路。

专用PG/S3配置方式沿用既有测试；Voting跨服务专项位于Identity integration包，使用真实HTTPS/mTLS与两个独立服务实例，非多进程Pod。kind的`TestKindVotingDeployment`另由完整`ops/lab/validate_modules.py`执行，使用独立namespace和正常NodePort/Service；不要单独运行常驻namespace测试。实际退出码、失败和未覆盖范围见Voting执行记录，不复用阶段六数量。本轮不实现MCP工具传输、Discovery、前端、生产HA或公网部署。

首次实施验收（历史，第14节）：常规race323项，全量integration576项（323常规＋253标签新增，混合包含组件fake；Voting真实链路专项19测试/子测试），均退出0、无失败/跳过。完整kind联合validator退出0，866.524秒；六项真实Pod业务、正常Service/NodePort故障隔离、网络允许/拒绝、失败rollout恢复、同SHA迁移重试及重复部署通过。前两轮kind失败及修复保留在Voting第14节，不以单项成功替代最终完整重跑。隔离namespace、独立PG/S3及临时凭据已清理；可提交独立审查，未commit/push/PR/合并/公网发布。
