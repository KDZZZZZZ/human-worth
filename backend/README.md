# Go 后端

当前实现 `gateway`、`IdentityService`、`ChallengeService`、challenge-worker 和受限账号运维命令。Identity 已发布；Challenge 已完成使用真实 PostgreSQL、mTLS gRPC 和有状态依赖 fake 的模块联调，E 提供 Kubernetes/Completion 适配器，同一 Gemini 配置的工具往返已通过，真实沙箱尚待联调。Content、Asset、Voting 仅落地 Challenge 所需 Proto，其他业务实现仍待完成。见 [Challenge 设计与验收](../docs/backend-challenge.md#implementation)及[现有部署](../docs/deployment.md#identity-lab)。

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

`SERVICE` 可取 `identity`、`gateway`、`identity-admin`、`challenge`、`challenge-worker`。此镜像内的 worker 支持 P/R；启用 E 的可信 worker 还需要固定版本的 `kubectl`。运行镜像为非 root、只读文件系统友好的静态 Go 程序，没有 shell 和编译工具；现有 lab 安装器仍只发布 Identity 链路。

## Challenge 启动与验证

先用 owner 执行迁移，再为运行账号授予 `challenge` schema 的 USAGE、`schema_migrations` 的 SELECT、`runs/claims/receipts/model_calls` 的 SELECT/INSERT/UPDATE，以及 `audit_events` 的 INSERT 和 identity sequence 的 USAGE。运行账号不得拥有 DDL、审计修改或其他 schema 的权限；集成测试使用独立 owner/runtime 账号验证这些边界。

gateway 配置 `CHALLENGE_TARGET` 后启用六个管理员 HTTP 操作；仍通过 Identity 校验管理员网站会话、用途及写请求 CSRF。API 对照见 [OpenAPI](../openapi.yaml)。依赖 fake 仅存在于测试二进制，正式服务必须连接真实 Content/Asset/Voting；后台待审登记并不代表作品审核通过。

模型配置采用仓库外权限 `0600` 的普通 JSON 文件，字段为 `base_url`、`model`、`protocol`、`api_key`。P、R、E 的 `protocol` 均为 `completion`，直接请求 `/v1/chat/completions`；E 默认复用同一份配置。每个 worker 只绑定一份 P/R 模型配置，启用的模型白名单应与实际 worker 能力一致；不能混用不同供应商路由并沿用旧 R 拟合记录。下面的检查只发送极小文本请求，不包含业务数据：

```sh
CHALLENGE_MODEL_CONFIG_FILE=/path/to/private/model.json \
  go test -tags=live ./internal/challengeworker -run 'TestLiveProvider|TestLiveExecutorCompletion' -count=1 -timeout=100s
```

2026-09-20，用户提供的 `gemini-3.8-flash` 基础 Completion、read_material 工具往返，以及 E 经中继的 run_command 工具往返、最终文本作品和预算结算均通过。E 协议测试返回固定工具观察，没有在宿主机执行模型命令；多模态、真实排名质量和真实隔离执行需各自验证。测试密钥留在仓库外。

启用 E 时配置 `CHALLENGE_EXECUTOR_PROFILE_FILE`、`RELAY_CERT_FILE` 和 `RELAY_KEY_FILE`。默认使用 `CHALLENGE_MODEL_CONFIG_FILE`；只有选择不同模型时才另外设置 `CHALLENGE_EXECUTOR_MODEL_CONFIG_FILE`，该文件也必须是 Completion 配置。profile 字段为 `kubeconfig`、`context`、`namespace`、`image`（必须带 SHA-256 digest）、`runtime_class`（gvisor/runsc，handler 为 runsc）、`relay_url`（HTTPS、9443 端口、指向该 worker 实例）、`ca_file`。worker 使用受限 kubeconfig，业务证书与供应商密钥不进入 E Pod；每个实例必须使用独立客户端证书，不能只靠实例名隔离身份。

[执行策略清单](../ops/challenge/execution-isolation.yaml)提供命名空间、RBAC、网络白名单和总资源限制；worker 校验该命名空间仅存在这份网络策略。部署前仍必须确认 CNI 实际执行拒绝规则。使用 [Dockerfile.executor](Dockerfile.executor) 和已核验、固定 digest 的 `EXECUTOR_IMAGE` 构建 E 镜像，只要求包含 `/bin/sh` 及任务所需语言工具。执行镜像、gVisor、网络隔离和命令进程限制尚未在真实集群验收；不能通过 privileged 或禁用沙箱绕过失败。现有发布控制器不会自动安装这组配置。

每个 E Attempt 固定 Job 名，禁止自动重试；独立卷、非 root、只读根文件系统、无 ServiceAccount token，限 1 CPU/2 GiB 内存/1 GiB 临时存储。成功回传后先删除 Job 再提交工作；清理失败会失败收尾，同实例重启按标签清理旧 Job，Job TTL 及 Secret ownerReference 提供额外回收。重启时的 `WORKER_INSTANCE_ID` 应保持稳定，多个活跃实例不能复用。

首版不提供额外 skills 装载；默认 harness 为 `completion-shell`，E 只使用 `run_command` 读写文件和验证作品。命令最多 30 秒、输出最多 64 KiB 并标明截断；超时／取消结束进程组。E 当前只传文本消息，附件下载到隔离目录，原生多模态观察尚未实现。轮数均限制 1～100，预算单位固定 `model_calls`、整数 1～100000；一次 Attempt 最多 8 次模型调用及 16 次工具调用，总期限 2 小时。P/R 支持 UTF-8 文本、JSON 和 PNG/JPEG/WebP；未知格式明确失败，单文件上限 16 MiB、图片 8 MiB；P/R 读取前还限制整批原始材料不超过 16 MiB，编码后的模型请求也不超过 16 MiB。工具按 64 KiB 分页并核对完整摘要。超过完整材料或上下文容量时不截断评测。

服务端只保存当前优化提示和当前完整反馈，不创建提示历史树。每次调用均预留一个调用单位，未知结果保留预留且不能重发；管理员响应不包含余额或已用数值。模型调用、登记和完成回执用于恢复，长期部署前需按业务保留期补充这些回执的归档策略。

## 可观测性

业务日志只记录方法、状态、耗时、request ID、trace ID。指标包含 HTTP/RPC 延迟、Go runtime、进程及数据库连接数；`/metrics` 仅位于内部探针端口。数据库 span 不带 SQL/参数，Google span 不带授权码或 token。

设置 `OTEL_EXPORTER_OTLP_ENDPOINT` 后向明确接收端导出 trace，默认采样 10%、有界队列 512、导出超时 2 秒。当前 lab 尚未安装 Prometheus/Grafana/Tempo，也未开放 pprof 或运行容量压测。没有接收端时仍生成日志关联用 trace ID。
