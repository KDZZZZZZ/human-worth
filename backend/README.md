# Go 后端

当前实现 `gateway`、`IdentityService` 和受限账号运维命令。设计见 [Identity](../docs/backend-identity.md)，集群运行命令见[部署文档](../docs/deployment.md#identity-lab)。其余六个业务模块尚未实现。

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
  go test -race -tags=integration ./... -count=1 -timeout=120s
```

GitHub `CI` 自动启动一次性 PostgreSQL，并执行格式、Proto 生成一致性、vet、上述并发集成测试和三个入口构建。根目录 `npm run ci` 仍负责文档、Node 基座及现有部署控制器；完整本地验收需要两组检查。

## 程序入口与配置

| 入口 | 用途 | 必需配置 |
| --- | --- | --- |
| `cmd/identity` | mTLS gRPC `:8443`；内部探针/指标 `:8081` | `IDENTITY_DATABASE_URL_FILE`、`IDENTITY_ENCRYPTION_KEYS_FILE`、`IDENTITY_SIGNING_KEYS_FILE`、`PUBLIC_ORIGIN`、`SERVICE_CERT_FILE`、`SERVICE_KEY_FILE`、`SERVICE_CA_FILE` |
| `cmd/gateway` | HTTPS/HTTP `:8080`；内部探针/指标 `:8081` | `PUBLIC_ORIGIN`、`IDENTITY_TARGET`、三个 `SERVICE_*_FILE`；实验 HTTPS 再提供 `HTTP_TLS_CERT_FILE`、`HTTP_TLS_KEY_FILE` |
| `cmd/identity migrate` | 单独执行有锁、带摘要检查的迁移 | 仅 `IDENTITY_DATABASE_URL_FILE`，使用 schema owner；运行服务不负责 DDL |
| `cmd/identity-admin` | 角色/状态变更、身份版本递增和审计 | 运维专用数据库文件、`OPERATOR_IDENTITY`；见 lab 的 `account.py` |

Identity 启用 Google 时设置 `GOOGLE_OAUTH_CLIENT_ID`、`GOOGLE_OAUTH_CLIENT_SECRET_FILE`、`GOOGLE_OAUTH_REDIRECT_URI`、`GOOGLE_OAUTH_CONFIG_VERSION`。缺少整组 Google 配置时已有会话认证可运行，新登录返回 503；Client ID 已设置但必要字段缺失时启动失败。正式程序只能使用 Google 官方端点。

`PUBLIC_ORIGIN` 是精确 HTTPS origin；不从外部 Host 推导回调。gateway 直接提供 HTTPS 时无需信任代理头；确需 TLS 终止代理时，才设置 `TRUSTED_PROXY_CIDRS` 并由该代理覆盖 `X-Forwarded-Proto`。站点图标复用 `public/brand/logo.png`，镜像中由 `SITE_LOGO_FILE=/assets/logo.png` 指定。

两个密钥环分别为 AEAD 和内部断言使用独立随机 32 字节密钥，文件形状为 `{"active":"key-id","keys":{"key-id":"<base64>"}}`。它们只由 Identity 持有；原始会话/MCP token 只存摘要，Google token 不持久化。实验脚本生成私有文件，仓库不保存可用示例密钥。

从**仓库根目录**构建单个应用镜像：

```sh
docker build --provenance=false --sbom=false -f backend/Dockerfile \
  --build-arg SERVICE=identity --build-arg REVISION=development \
  -t human-worth/identity:development .
```

`SERVICE` 可取 `identity`、`gateway`、`identity-admin`。运行镜像为非 root、只读文件系统友好的静态 Go 程序；没有 shell 和编译工具。lab 安装器会构建并加载到三个工作节点，记录源码摘要和镜像 ID。

## 可观测性

业务日志只记录方法、状态、耗时、request ID、trace ID。指标包含 HTTP/RPC 延迟、Go runtime、进程及数据库连接数；`/metrics` 仅位于内部探针端口。数据库 span 不带 SQL/参数，Google span 不带授权码或 token。

设置 `OTEL_EXPORTER_OTLP_ENDPOINT` 后向明确接收端导出 trace，默认采样 10%、有界队列 512、导出超时 2 秒。当前 lab 尚未安装 Prometheus/Grafana/Tempo，也未开放 pprof 或运行容量压测。没有接收端时仍生成日志关联用 trace ID。
