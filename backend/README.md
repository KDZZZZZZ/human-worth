# Go 后端

当前已发布 `gateway`、`IdentityService`、`ContentService` 四个本人草稿接口；受限账号运维命令同时保留。`ChallengeService`、challenge-worker 与 P/R/E Completion 适配器已本地实现，真实 PostgreSQL 与依赖 fake 联调已有记录，真实 E 沙箱及完整依赖闭环仍待验收，未发布。见 [Identity](../docs/backend-identity.md)、[Content](../docs/backend-content.md)、[Challenge](../docs/backend-challenge.md#implementation)和[部署文档](../docs/deployment.md#identity-lab)。

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
  go test -race -tags=integration ./... -count=1 -timeout=120s
```

GitHub `CI` 自动启动一次性 PostgreSQL，并执行格式、Proto 生成一致性、vet、上述并发集成测试和所有入口构建。根目录 `npm run ci` 仍负责文档、Node 基座及现有部署控制器；完整本地验收需要两组检查。

## 程序入口与配置

| 入口 | 用途 | 必需配置 |
| --- | --- | --- |
| `cmd/identity` | mTLS gRPC `:8443`；内部探针/指标 `:8081` | `IDENTITY_DATABASE_URL_FILE`、`IDENTITY_ENCRYPTION_KEYS_FILE`、`IDENTITY_SIGNING_KEYS_FILE`、`PUBLIC_ORIGIN`、`SERVICE_CERT_FILE`、`SERVICE_KEY_FILE`、`SERVICE_CA_FILE` |
| `cmd/gateway` | HTTPS/HTTP `:8080`；内部探针/指标 `:8081` | `PUBLIC_ORIGIN`、`IDENTITY_TARGET`、三个 `SERVICE_*_FILE`；实验 HTTPS 再提供 `HTTP_TLS_CERT_FILE`、`HTTP_TLS_KEY_FILE` |
| `cmd/content` | Content mTLS gRPC `:8443`；内部探针/指标 `:8081` | `CONTENT_DATABASE_URL_FILE`（运行账号）、`IDENTITY_TARGET`、三个 `SERVICE_*_FILE`；证书身份为 `content` |
| `cmd/content migrate` | 独立 Content 迁移，不访问 Identity 表 | `CONTENT_DATABASE_URL_FILE`（schema owner） |
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

`SERVICE` 可取 `identity`、`gateway`、`content`、`identity-admin`、`challenge`、`challenge-worker`。此镜像内的 worker 支持 P/R；启用 E 的可信 worker 还需要固定版本的 `kubectl`。运行镜像为非 root、只读文件系统友好的静态 Go 程序，没有 shell 和编译工具；现有自动部署清单只启用 Identity、Content 和 gateway；Challenge 待真实依赖与执行隔离验收后纳入。

## Challenge 启动与验证

先用 owner 执行迁移，再为运行账号授予 `challenge` schema 的 USAGE、`schema_migrations` 的 SELECT、`runs/claims/receipts/model_calls` 的 SELECT/INSERT/UPDATE，以及 `audit_events` 的 INSERT 和 identity sequence 的 USAGE。运行账号不得拥有 DDL、审计修改或其他 schema 的权限；集成测试使用独立 owner/runtime 账号验证这些边界。

gateway 配置 `CHALLENGE_TARGET` 后启用六个管理员 HTTP 操作；仍通过 Identity 校验管理员网站会话、用途及写请求 CSRF。API 对照见 [OpenAPI](../openapi.yaml)。依赖 fake 仅存在于测试二进制，正式服务必须连接真实 Content/Asset/Voting；后台待审登记并不代表作品审核通过。

模型配置采用仓库外权限 `0600` 的普通 JSON 文件，字段为 `base_url`、`model`、`protocol`、`api_key`。P、R、E 的 `protocol` 均为 `completion`，直接请求 `/v1/chat/completions`；E 默认复用同一份配置。每个 worker 只绑定一份 P/R 模型配置，启用的模型白名单应与实际 worker 能力一致；不能混用不同供应商路由并沿用旧 R 拟合记录。下面的检查只发送极小文本请求，不包含业务数据：

```sh
CHALLENGE_MODEL_CONFIG_FILE=/path/to/private/model.json \
  go test -tags=live ./internal/challenge/adapter/execution -run 'TestLiveProvider|TestLiveStructuredResult|TestLiveExecutorCompletion' -count=1 -timeout=100s
```

2026-09-20，用户提供的 `gemini-3.8-flash` 基础 Completion、read_material 工具往返，以及 E 经中继的 run_command 工具往返、最终文本作品和预算结算均通过。E 协议测试返回固定工具观察，没有在宿主机执行模型命令；多模态、真实排名质量和真实隔离执行需各自验证。测试密钥留在仓库外。

启用 E 时配置 `CHALLENGE_EXECUTOR_PROFILE_FILE`、`RELAY_CERT_FILE` 和 `RELAY_KEY_FILE`。默认使用 `CHALLENGE_MODEL_CONFIG_FILE`；只有选择不同模型时才另外设置 `CHALLENGE_EXECUTOR_MODEL_CONFIG_FILE`，该文件也必须是 Completion 配置。profile 字段为 `kubeconfig`、`context`、`namespace`、`image`（必须带 SHA-256 digest）、`runtime_class`（gvisor/runsc，handler 为 runsc）、`relay_url`（HTTPS、9443 端口、指向该 worker 实例）、`ca_file`。worker 使用受限 kubeconfig，业务证书与供应商密钥不进入 E Pod；每个实例必须使用独立客户端证书，不能只靠实例名隔离身份。

[执行策略清单](../ops/challenge/execution-isolation.yaml)提供命名空间、RBAC、网络白名单和总资源限制；worker 校验该命名空间仅存在这份网络策略。部署前仍必须确认 CNI 实际执行拒绝规则。使用 [Dockerfile.executor](Dockerfile.executor) 和已核验、固定 digest 的 `EXECUTOR_IMAGE` 构建 E 镜像，只要求包含 `/bin/sh` 及任务所需语言工具。执行镜像、gVisor、网络隔离和命令进程限制尚未在真实集群验收；不能通过 privileged 或禁用沙箱绕过失败。现有发布控制器不会自动安装这组配置。

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
