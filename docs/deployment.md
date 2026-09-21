# 部署与实验计划

**公网已连接本机 kind lab 的 gateway、Identity、Content 双副本及 PostgreSQL 一主两备。** 用户已确认真人 Google 登录成功，Content 的三个本人草稿接口已通过公网验收。Node.js 基座保留作旧 IP 入口和回退链路；其余业务模块仍是计划。

| 要了解什么 | 阅读位置 | 状态 |
| --- | --- | --- |
| 线上入口、开发代理、自动部署和回退 | [当前运行环境](#current-deployment) | Identity 与 Content 草稿已发布 |
| Identity 安装、私有配置与验证 | [当前 lab](#identity-lab) | 首批服务已运行 |
| CI 后发布 Content 与后续模块 | [模块自动部署](#module-deployment) | 控制器已升级，真实 Deploy Action 通过 |
| 宿主机到进程、完整副本分布与实施顺序 | [单机 kind 实验计划](#lab-plan) | 逐模块落地 |
| 工具参数、资源预算和 14 项实验 | [实施参数与实验清单](#implementation-details) | 实施时查阅 |

服务职责见[架构划分](backend-architecture.md)，身份与公共基座见 [Identity 设计](backend-identity.md)。版本：v2.3 · 2026-09-21。

<a id="current-deployment"></a>
## 1. 当前运行环境

已发布 Google 登录、会话、退出、MCP 凭据管理，以及本人任务草稿的创建、读取和整体替换。草稿仍属作者私有；送审、公开投稿、真人投票、审核、云端 agent 和 MCP 内容工具仍待后续模块实现。

### 实际拓扑

| 位置 | 职责 / 地址 |
| --- | --- |
| GitHub | `KDZZZZZZ/human-worth`；PR CI 与 dev / main push CI 使用托管 runner |
| 当前本机 | kind lab 运行 Go 服务和数据库；systemd 定时运行发布控制器 |
| 阿里云 CLI 当前账号的 cn-beijing ECS | 公网 IP `123.56.161.234`，Nginx 为域名提供 80/443；旧 IP 基座保留 18090 |
| 专用 SSH 隧道 | ECS `127.0.0.1:28443` → 本机 `127.0.0.1:18443` → gateway；原 28090 → 18090 保留 |
| 前端 / API | `https://worth.oopsbox.cn/` / `https://worth.oopsbox.cn/api/health` |
| Swagger 文档 | `https://worth.oopsbox.cn/docs/`；由 ECS Nginx 直接提供静态文件 |

同源 `/api/` 避免前端写死第二个主机和端口。`worth.oopsbox.cn` 的 DNS A 记录指向 `123.56.161.234`，Cloudflare 为 DNS-only；ECS 通过独立 `server_name` 配置共享既有 80/443。HTTP 308 跳转 HTTPS，Let’s Encrypt 证书由 certbot 定期续期。Nginx 校验私有 TLS 上游的 lab CA 和域名；28090、28443 都只监听 ECS loopback，不对公网开放。

### 前端开发与部署的 API 路由

用户明确要求开发使用公网 API，部署使用内部调用。前端代码始终使用相对 `/api/*`，通过服务入口区分环境：

| 环境 | 启动方式 | API 请求链路 |
| --- | --- | --- |
| 前端开发 | `npm run dev:frontend`，默认 `127.0.0.1:18100` | 浏览器 → 本地开发代理 → `https://worth.oopsbox.cn/api/*` |
| 已部署站点 | kind 应用 + systemd 发布控制器 | 浏览器 → 同源 Nginx `/api/*` → 私有 TLS 隧道 `28443 → 18443` → gateway → Identity |

开发代理只接管 `/api/` 前缀，保留方法、查询参数、请求体和响应状态；上游不可达返回 502，不回退到本地 API。开发上游可由 `FRONTEND_API_ORIGIN` 覆盖。生产环境禁止 `--frontend-dev`，正常部署启动不会读取该变量。浏览器不直接连接内部地址，内部转发由服务端完成。

<details>
<summary>保留的 Node 基座：自动部署、文件权限与回退命令</summary>

### 自动部署过程

1. PR 的 `CI` 通过后 squash 合入 dev；GitHub 再运行 dev push CI。
2. 本机 `human-worth-deploy.timer` 每 60 秒启动一次控制器，拉取最新 dev SHA。
3. 控制器只接受本仓库 `.github/workflows/ci.yml` 对该 SHA、dev、push 事件的最新一次成功运行。pending、失败、缺失或其他分支 / 事件均不部署。等待 CI 时每 180 秒重试 API，避免频繁查询。
4. 再确认 dev 没被新提交替代，按 SHA 解包到 `/opt/human-worth/releases/<sha>`；不在开发者工作区部署，不执行仓库里的安装脚本。
5. 原子切换 `current` 符号链接并重启项目服务；健康检查必须返回同一 SHA。失败切回上一版本并检查恢复结果，首次部署失败则停止服务。
6. 成功记录到 `/opt/human-worth/state.json` 与 journal。应用 `/api/health` 返回当前 revision，公网验收以该值为准。

本机通常在 CI 成功后 1～3 分钟部署。机器关机或离线时不部署，重新联网后继续调和最新 dev；ECS 入口此时可能返回 502。进程重启可能产生短暂连接中断，当前不承诺零停机。main 合并不触发本机版本切换。

本机网络环境下，旧 IP 控制器通过 `/etc/systemd/system/human-worth-deploy.service.d/network.conf` 的 `EnvironmentFile=-/home/oops/.config/human-worth/deploy.env` 复用 Go 控制器已有的私有代理设置。2026-09-21 检查发现未配置代理时拉取 GitHub 偶发超过 90 秒；相同受限用户及 `ProtectHome` 下复用该配置，`git ls-remote` 验证退出 0。私有代理值不写入仓库，CI 门槛与服务权限保持原规则。

### 文件与权限

| 项目 | 安装位置 / 权限 |
| --- | --- |
| 应用 | `human-worth` 系统账户；release 只读，无开发者家目录访问 |
| 部署控制器 | `human-worth-deploy` 账户；写 `/opt/human-worth`；sudo 仅允许 restart / stop `human-worth.service` |
| 已安装控制器 | `/usr/local/lib/human-worth/deploy.py`；root 拥有，不由普通代码部署自动覆盖 |
| 服务与 timer | `/etc/systemd/system/human-worth*.service`、`human-worth-deploy.timer` |
| 隧道 | 独立 `human-worth-tunnel` 账户和 SSH key，私钥仅在本机 `/var/lib/human-worth-tunnel` |
| ECS 入口 | `/etc/nginx/conf.d/human-worth.conf`；独立 SSH 用户仅允许反向转发到 28090，无交互 shell 能力 |

配置源在 [ops/systemd](../ops/systemd)、[ops/nginx](../ops/nginx)、[ops/ssh](../ops/ssh)、[ops/deploy.py](../ops/deploy.py)。本机初始化入口为 `sudo bash ops/install-local.sh`，之后将新公钥写入 ECS 专用用户，并经云助手核对主机公钥后写入本机 known_hosts，启动隧道和 timer。基础设施文件经 PR 修改后由运维安装；控制器不会给仓库代码任意系统管理权限。仓库和 GitHub Actions 不保存阿里云凭据或隧道私钥。

### 检查、回退与维护

```sh
systemctl status human-worth.service human-worth-deploy.timer human-worth-tunnel.service
sudo journalctl -u human-worth-deploy.service -n 50 --no-pager
curl --fail http://127.0.0.1:18090/api/health
curl --fail http://123.56.161.234:18090/api/health
```

常规回滚：从 dev 建任务分支，以 `git revert` 撤销问题变更，经 CI / PR 合入，由控制器部署新 SHA。失败激活会自动回退；如果 API 不可达，先查看应用、隧道与 Nginx 状态，分别定位本机和公网链路。

紧急冻结调和：`sudo systemctl stop human-worth-deploy.timer`，保留当前服务，处理问题后 `sudo systemctl start human-worth-deploy.timer`。不要通过关闭 GitHub 分支保护或 force push 回滚。停止公网入口时只停止 `human-worth-tunnel.service`，不要停整台 ECS 的 Nginx。

当前保留所有已部署 release 以便诊断；定期清理时至少保留 current 与最近成功版本，不在部署并行进行时删除。部署控制器单实例文件锁防止重叠。

HTTPS 已于 2026-09-19 建立；[worth.oopsbox.cn.conf](../ops/nginx/worth.oopsbox.cn.conf) 已安装到 ECS `/etc/nginx/conf.d/human-worth-domain.conf`，域名经私有 TLS 隧道访问已发布 Go 服务。认证路径关闭访问及错误日志，其他请求日志只记录方法与 `$uri`。证书目录 `/etc/letsencrypt/live/worth.oopsbox.cn/`，私钥不进入仓库。

</details>

<a id="swagger-docs"></a>
<details>
<summary>Swagger 静态文档构建、发布与回退</summary>

### Swagger 静态文档

2026-09-17，用户明确要求生成 Swagger 文档并部署。该授权用于独立静态文档发布，不自动授权创建 PR，也不改变应用从受保护 dev 和成功 CI 部署的规则。源码集成仍须遵循明确的人类 PR 命令、分支保护与 CI 门槛。

源文件为根目录 [openapi.yaml](../openapi.yaml) 和 [docs/swagger](swagger)。`npm ci --ignore-scripts && npm run build:docs` 生成 `dist/docs/`，包含固定版本 Swagger UI、许可证及逐文件 SHA-256 清单。浏览器只请求同源资源，关闭在线校验器和 Try it out，不执行 API 请求。线上 0.5.0-draft 共 44 个操作：健康检查 2 个、Identity 7 个已上线，其余 35 个为 planned。Identity 标记 `published`。

已配置 HTTPS 域名和用户提供的 Google Web 凭据，固定回调为 `https://worth.oopsbox.cn/api/auth/google/callback`。当前本地 HTTP 前端代理也尚未实现登录联调所需的回调与会话配置，具体要求见[后端身份设计](backend-identity.md)。

公网 `/docs` 重定向到 `/docs/`，对应 `/var/www/human-worth-docs/current/`。Nginx 配置只增加该前缀的静态路由，其他路径继续使用原有应用代理。静态文档不依赖本机应用在线；API 的可用性仍依赖原有隧道和服务。

发布流程：

1. 运行 `npm run ci`，在浏览器检查接口、schema、状态提示与下载。
2. 通过 ECS 云助手读取 `/etc/nginx/conf.d/human-worth.conf`，将实际内容保存到本地 `/tmp/human-worth-nginx.conf`。不要把仓库配置当成已核实的远端配置。
3. 运行 `python3 scripts/package-docs.py /tmp/human-worth-nginx.conf`。输出紧凑归档路径、SHA-256 和字节数；通过 ECS `SendFile`（Base64）上传到网关 `/var/tmp/human-worth-docs/`。大型官方资源由安装器按 lockfile 中的精确 URL 下载并校验 SHA-512；Logo 原图从公开仓库 `dev` 下载，并核对构建清单中的 SHA-256。Logo 变更须先按 PR 流程合入 `dev`；若远端图片与构建不一致，安装器在切换前拒绝发布。大文件不放进云助手归档，以满足 [SendFile 的 Base64 内容不超过 32 KB 的限制](https://www.alibabacloud.com/help/en/ecs/developer-reference/api-ecs-2014-05-26-sendfile)。
4. 通过云助手传入 [ops/install-docs.py](../ops/install-docs.py)，在网关以 root 执行 `python3 install-docs.py <归档路径> <SHA-256>`。使用 `RunCommand` 传 Base64 内容时必须显式设置 `ContentEncoding=Base64`，不要依赖 CLI 默认值。
5. 检查公网文档与 YAML、逐文件清单、浏览器渲染及 `/api/health`。部署不应改变应用 revision。

安装器在修改前核对归档、依赖、逐文件摘要及远端配置，配置漂移时拒绝覆盖。release 保存在 `/var/www/human-worth-docs/releases/<归档 SHA-256>`，旧 Nginx 配置保存在非公开的 `backups/`。通过 `nginx -t` 后原子切换文档链接、reload Nginx；发布检查失败则还原先前配置与链接。需要手动回退时恢复对应配置备份及上一 release 链接，先运行 `nginx -t`，再 `systemctl reload nginx`，复核文档和应用健康。

不要把工作区或项目根目录直接映射到公网；只有构建清单内的公开文件进入文档 release，安装归档、Nginx 配置和备份不在公开目录内。

2026-09-17 发布验收：文档归档 SHA-256 为 `46124e692ca7a77241bb633ad0f343b1fddee67faaac6a8b33fb6e98b2f8d997`。`npm run ci`、网关 `nginx -t`、公网 10 个文件摘要与私有路径拒绝检查均通过；Chrome 验证 38 个操作、展开状态、YAML 下载入口、桌面/手机布局，没有脚本异常或外部资源请求。本地开发页面的 `/api/health` 确认返回公网版本；发布前后应用 revision 均为 `813b5a8bafde7731886244dc831b9985c5884035`。首次安装发现网关 Python 3.6.8 的 pathlib 兼容性问题，修正后发布成功，现有应用服务保持正常。未实现的业务 API 不在此次运行验收范围内。

</details>

<a id="identity-lab"></a>
## 当前 lab：只部署 Identity 所需部分

先记住这一条链路：**公网 Nginx → 私有 TLS 隧道 → 本机 `127.0.0.1:18443` → gateway 两份 → Identity 两份 → PostgreSQL 一主两备**。Google 请求只由 Identity 经受限代理发出。所有数据都在同一台宿主机，数据库副本不等于异机备份。

| 组件 | 实际配置 |
| --- | --- |
| 节点 | kind 0.33.0、Kubernetes 1.36.4；1 控制平面 + 3 工作节点 |
| 资源 | 每节点容器 1 CPU / 2GiB，共 4 CPU / 8GiB；kubelet 同步限制可分配量 |
| 网络/数据库管理 | Calico 3.32.2、CloudNativePG 1.30.0；镜像和下载清单均固定摘要 |
| 应用 | gateway × 2、identity × 2、content × 2，按主机名分散；维护命令为一次性 Job |
| 数据库 | PostgreSQL 18.4 × 3，每节点独立 2GiB PVC；同步确认一份备库，启用 failover quorum；smart shutdown 20 秒、总停机窗口 90 秒 |
| 数据权限 | Identity、Content 各自的 owner 仅拥有本模块 schema，runtime 受限 DML；`identity_operator` 只能更新账号状态/角色/版本并追加审计 |
| Google 出口 | Squid × 1，只允许 CONNECT 到 Google token/JWKS 的两个域名；不缓存、不记访问 URL；不是高可用出口 |
| 入口 | Kubernetes API `127.0.0.1:16443`；gateway TLS `127.0.0.1:18443`；端口不直接暴露公网，域名经专用隧道访问 gateway |
| 尚未部署 | 其他五个业务模块、worker、对象存储、Prometheus/Grafana/Tempo、pprof |

### 安装和检查

前置工具为 Docker、Go 1.27.1、kind 0.33.0、kubectl 1.36.4、Python 3、OpenSSL。使用 [ops/lab](../ops/lab) 的一套配置；脚本固定 `kind-lab` context，私有 kubeconfig 位于 `~/.config/human-worth/lab.kubeconfig`，不借用其他集群。

```sh
python3 ops/lab/infra.py
python3 ops/lab/app.py
kubectl --kubeconfig ~/.config/human-worth/lab.kubeconfig --context kind-lab get pods -n human-worth -o wide
curl --noproxy '*' --cacert ~/.config/human-worth/lab/ca.crt \
  --connect-to worth.oopsbox.cn:443:127.0.0.1:18443 https://worth.oopsbox.cn/api/health
```

`infra.py` 只调和本项目的节点、网络、数据库 Operator 与命名空间；`app.py` 创建私有 Secret、数据库角色、迁移 Job，构建镜像并部署。若宿主机使用 loopback HTTP 代理，脚本只在 kind 节点内部把地址改为 Docker bridge gateway，并限制 Google 出口到这一代理；不改宿主机代理配置。节点镜像拉取走节点代理，应用 Google 调用仍经过 Squid 域名限制。

密钥均在仓库外 `~/.config/human-worth/`：Google JSON 权限 600；lab 目录权限 700，数据库密码、签名/加密 keyring、CA 私钥权限 600。Google Secret 只挂载 Identity，gateway 没有数据库或 Google 密钥。lab 服务证书 30 天，安装器在到期前重新签发并滚动更新；CA 到期需人工按新旧信任重叠流程轮换，不能直接丢弃旧信任。Kubernetes Secret 的 Base64 不是加密，实验 kubeconfig 与节点存储也须视为管理员权限。

首次公开发布前使用 `local-<源码摘要>` 标识实验镜像；正式发布使用通过 CI 的 dev SHA。`~/.config/human-worth/lab/build.json` 记录源码摘要、基线 Git SHA、镜像 tag/ID；`release-revision` 标记已发布环境，阻止普通 `app.py` 再用未合入工作区覆盖它。

需要改变已有账号角色或状态时，使用受限运维 Job，填写真实账号及当前版本：

```sh
python3 ops/lab/account.py --account '<account-id>' --role user --state disabled \
  --expected-version 1 --operator '<trusted-operator>'
```

修改角色/状态会使该账号所有旧凭据失效。此命令需要 lab 管理员 kubeconfig，不能从网站或 MCP 调用；不要把合成测试账号当成已验证 Google 用户。

### 可重复的运行验收

```sh
# 真正的 Calico/Squid 允许与拒绝路径；临时探针不会进入应用 Service 端点。
python3 ops/lab/check_network.py

# 会创建并清理合成账号，替换一个 Identity Pod 和数据库主 Pod。
cd backend
HUMAN_WORTH_LAB_CHECK=1 go test -tags=lab ./internal/identity -run TestKindIdentity -v -count=1 -timeout=420s
```

后一条是明确的故障测试开关，只对这套 lab 使用；不访问公网应用。它检查授权 URL、真实 Google 无效授权码的拒绝路径、跨副本会话、CSRF、MCP 创建防重/撤销、Pod 恢复和数据库切换后的已确认状态。真人 Google 授权成功仍需浏览器单独验收。

<details>
<summary>2026-09-19 首次公开发布前的实验验收记录</summary>

实验镜像为 `local-9bd2c09b36f07d4b`，基线为 `8e2184f62ae031367312f4dadeedacae27b99a8a`；这不是公开发布的 Identity 版本。

| 检查 | 实际结果 |
| --- | --- |
| `npm run ci` | 退出 0：文档与 Swagger 构建、5 个 Node HTTP 测试、9 个 Python 部署测试通过 |
| `buf lint`、`buf generate`、`go vet ./...`、`go build ./cmd/...` | 退出 0；生成前后文件摘要一致，Go 格式及页面脚本语法检查通过 |
| `go test -race -tags=integration ./... -count=1 -timeout=120s` | 退出 0；真实专用 PostgreSQL、受限数据库角色、并发登录与凭据、mTLS、故障拒绝路径通过；OIDC 成功路径使用测试供应方 |
| `python3 ops/lab/check_network.py` | 退出 0；真实 Calico/Squid 的允许与拒绝路径通过，临时探针已清理 |
| `TestKindIdentity` | 退出 0，45.18 秒；两个 Identity 实例均处理会话 RPC；替换实例后约 3 秒就绪且原会话可用；主库从 `database-3` 切换到 `database-1`，约 35 秒恢复，已确认账号与凭据撤销状态保留；运维 Job 角色变更和审计通过 |
| 真实坏镜像滚动更新与恢复 | gateway 新副本出现 `ErrImageNeverPull` 时旧副本继续提供健康响应；发布控制器的恢复函数恢复原 Deployment 模板与镜像，健康 revision 一致 |
| 发布资格与入口配置 | 现有 dev 不含 Identity，`release.py --check-only` 按预期拒绝激活；两个新 systemd 单元检查退出 0；ECS 上使用独立临时配置和公开 lab CA 执行候选 Nginx 语法检查退出 0，未切换现网 |
| 现网回归 | `https://worth.oopsbox.cn/api/health` 仍返回基线 SHA 和 `stage: foundation`；`/docs/` 返回 200 |

最初实验发现迁移角色没有数据库级 `CREATE` 权限，现已改为复用其拥有的 schema，并用相同受限角色加入集成测试。初次数据库切换超过测试窗口，查明是原 smart shutdown 等待时间过长；调整为表中 20/90 秒后重测通过，未降低同步确认要求。

本轮替换的是 Pod，不能据此声称物理节点故障、异机容灾或全部分布式实验已通过。真实 Google 无效授权码被拒绝只证明出口和失败处理可达，不证明真人登录成功。首次 Chrome 导航检查因无头浏览器配置超时，当时未计为 PASS；后续浏览器与真人登录证据见下节。Prometheus/Grafana/Tempo 尚未部署，不宣称已验证完整追踪展示。

</details>

### Identity 公网发布与后续更新

2026-09-19 用户在确认公网接口范围后回复“好，就这样做”，授权本次 Identity 的 PR、合并与发布。[PR #17](https://github.com/KDZZZZZZ/human-worth/pull/17) 实现功能，[PR #18](https://github.com/KDZZZZZZ/human-worth/pull/18) 修复首次正式发布发现的字段管理冲突和过早健康判定；均经 PR CI、squash 合入 dev 和 push CI 后执行。首次成功公开版本为 `f597b9089734da24c2290465664bb03e7c033dbd`，后续版本以 `/api/health` 为准。

公网 HTTPS、GET/HEAD 健康版本、未登录接口拒绝、私有路径 404、Google 授权发起/取消/重放拒绝已通过。Chrome 已从公网入口到达 Google 账号登录页；用户随后明确回复“登录成功，能看到账号”，完成真人登录确认。公网浏览器另用合成会话通过 MCP 创建/撤销、会话失效提示、退出、转义和手机布局检查，测试账号已清理；Swagger 渲染 44 个操作，无脚本异常或执行按钮。未把合成账号当作真实 Google 登录证据。

Nginx 已启用经 CA 验证的私有 HTTPS 上游，其他站点配置摘要保持不变；原域名基座配置保存在 ECS `/var/backups/human-worth/identity-20260919/domain-before.conf`。原 IP 的基座与 SSH 转发继续运行。11 个 Python 部署测试通过，真实重新发布也确认能从回退后的字段归属继续更新。

已经安装 [Identity 发布控制器](../ops/lab/release.py) 并启用 [timer](../ops/systemd/human-worth-identity-deploy.timer)。后续更新仍须有当前任务的开 PR 命令、合入 dev 并通过该 SHA 的 push CI。现有 Node 控制器保留，Go 由独立控制器调和：

1. 从公开仓库读取当前 dev 和匹配的成功 push CI，按不可变 SHA 归档；不运行归档内的部署脚本。
2. 使用**安装在仓库外、经人工安装的可信运维代码**构建镜像，镜像与健康 revision 使用该 SHA；激活前再次检查 dev/CI。
3. 执行独立迁移 Job，依次滚动 Identity、gateway，并留出约 45 秒确认 HTTPS 健康版本。应用更新/回退共用 `human-worth-release` 字段管理器，只对两个应用的声明配置接管字段；基础设施不强制接管。失败恢复先前 Deployment，数据库迁移不自动回退，要求扩展兼容。
4. 定时复查最新 dev；服务证书距过期一天时也滚动续发。成功发布后写入 `release-revision` 标记，普通 `app.py` 拒绝把未合入工作区覆盖到这套已发布环境。

以上是已安装的 Identity 控制器行为。控制器源码与基础设施权限变更需要另行安装，不从应用归档自动升级；新版本的模块声明由下节规定随已通过 CI 的应用版本读取。lab 的运维账号 `oops` 已有本机 Docker 与该集群管理权限；这套控制器沿用它，适用于当前学习环境。镜像构建上下文为公开代码归档，密钥只在仓库外和集群 Secret 中。

<a id="module-deployment"></a>
### CI 后的统一模块部署（2026-09-21）

人类要求“补 action 让通过 CI 之后自动部署”，并补充后续模块也要自动部署，随后明确要求合入 dev 生效。通用控制器已安装，**2026-09-21 首次发布已通过真实 GitHub Deploy 与公网接口验收**，记录见本节下方。

1. `dev` 的 push `CI` 完整成功后，[Deploy workflow](../.github/workflows/deploy.yml) 通过 `workflow_run` 启动；PR、main、fork、失败或未完成的 CI 不进入部署检查。Deploy 与 CI 分开，避免本机等待 CI 完成而 CI 又等待部署。
2. 本机既有 timer 每分钟拉取最新 dev，重复核对本仓库、分支、事件、不可变 SHA 与最新 CI 尝试。**实际发布继续由本机拉取控制器驱动**；Action 等待结果并使失败可见，不需要托管 runner 登录宿主机，也不新增云端密钥。
3. [模块清单](../ops/lab/services.json) 随该 SHA 读取。目前登记 Identity、Content、gateway；其余服务没有条目，不生成空壳部署。控制器按清单构建镜像、生成各自证书、准备隔离数据库角色及 schema、运行迁移和授权，再按依赖顺序滚动；gateway 始终最后。原 `identity-admin` 运维镜像继续构建，不作为常驻模块。
4. 每个模块的应用 YAML 同样来自该 SHA，经 [模块校验](../ops/lab/services.py) 限定为自身命名空间资源、固定镜像、双副本、健康探针及允许的凭据引用；不接受 RBAC、跨模块 Secret、其他命名空间或新增公网 NodePort。控制器不执行归档内的 shell/Python 部署脚本。基础设施模板和部署程序仍是仓库外安装的可信代码。
5. 数据库模块使用 `<module>_owner` 迁移、`<module>_runtime` 运行；密码保存在私有状态目录和 Secret。授权 SQL 在数据库内以模块 owner 连接执行，不获得 postgres 管理员权限，不允许 psql 元命令。CNPG 的新增角色、HBA 和数据库 NetworkPolicy 由登记模块生成，保留 Identity 原有运行与运维角色。
6. 所有模块滚动和 API 检查通过、再次确认 dev/CI 后，控制器更新 `release-status` ConfigMap。gateway 通过目录挂载读取完成状态，在 `/api/health` 增加可选 `deploymentRevision` / `deployedServices`。Action 必须同时确认当前 gateway SHA、完成 SHA、完整模块集合与登记的只读 API 检查；仅 gateway 已更新，或遗漏没有公开 API 的 worker，都不能标记部署完成。ConfigMap 更新可能延迟，控制器最多等待 180 秒传播。
7. 迁移或滚动失败时，恢复原有 Deployment、NetworkPolicy 和完成标记；首次加入的失败模块缩为 0 副本。保留数据库迁移和数据。失败迁移 Job 在下次同 SHA 重试时重建，已完成的 Job 不重复运行。删除已有模块不等于普通发布，需另外执行明确的退役操作。

后续新增模块在实现任务中同时提交以下内容，合入 dev 并通过 CI 后进入同一流程，**无需再新增 Action 或在控制器里追加服务名循环**：

| 输入 | 约定 |
| --- | --- |
| 可执行入口 | `backend/cmd/<module>/main.go`，使用现有 Dockerfile 的 `SERVICE` 构建参数 |
| 模块条目 | `ops/lab/services.json`：`dependencies`、明确的 `database` 布尔值、`checks`；数据库模块还需 `grants` 文件路径 |
| 应用声明 | `ops/lab/<module>.yaml`：模块自己的 ServiceAccount、Deployment、NetworkPolicy，按需 Service/PDB；两个副本，启动/存活/就绪探针，镜像占位符 `human-worth/<module>:local` |
| 数据库 | `migrate` 子命令读取 `<MODULE>_DATABASE_URL_FILE`；迁移可重复且兼容回退，授权 SQL只授予本模块运行账号需要的权限 |
| 服务联通 | 依赖目标环境变量、调用方/被调用方 NetworkPolicy 与真实接口集成测试一起维护 |
| 部署验证 | `checks` 只包含无凭据 GET 路径、预期状态和可选错误 code；worker 可为空，但仍必须通过就绪检查并出现在完成模块集合中 |

允许的模块名对应既有七业务服务、gateway 与 challenge-worker。新增特殊外部凭据、存储或超过 lab 容量的工作负载需先完成实际资源配置；清单不能凭空生成外部账号或资源。当前 CPU limit 配额从 4 调到 6，为 Content 双副本、滚动副本和迁移留出空间；未改变已有 Pod 的资源请求或数据库存储量。

#### 一次性升级已安装的控制器

在本次变更经人类明确命令开 PR、合入 dev、该 SHA 的 push CI 成功后，从对应 checkout 执行。先暂停 timer 并等待正在进行的发布结束，避免覆盖运行中的 Python 文件：

```sh
sudo systemctl stop human-worth-identity-deploy.timer
while systemctl is-active --quiet human-worth-identity-deploy.service; do sleep 5; done
sudo install -m 0644 ops/deploy.py ops/deployment_health.py /usr/local/lib/human-worth/
sudo install -m 0644 ops/lab/app.py ops/lab/release.py ops/lab/services.py ops/lab/infra.py ops/lab/postgres.yaml ops/lab/namespace.yaml ops/lab/egress.yaml ops/lab/versions.json /usr/local/lib/human-worth/lab/
sudo install -m 0644 ops/systemd/human-worth-identity-deploy.service ops/systemd/human-worth-identity-deploy.timer /etc/systemd/system/
kubectl --kubeconfig ~/.config/human-worth/lab.kubeconfig --context kind-lab apply --server-side -f ops/lab/namespace.yaml
sudo systemctl daemon-reload
sudo systemctl start human-worth-identity-deploy.service
sudo systemctl enable --now human-worth-identity-deploy.timer
```

服务名与已有私有目录保持兼容。只有这次通用控制器及基础设施升级需要安装；后续符合契约的模块声明、授权 SQL和应用代码随已过 CI 的 SHA 发布。部署 Action 最多等待 30 分钟，超时会失败并提示查看本机 journal；不能把 Action 超时当成数据库事务未执行。

#### 本地验收入口

`python3 ops/lab/validate_modules.py` 在现有 kind lab 内创建临时 `human-worth-verify-*` 命名空间，使用真实模块声明与独立 PostgreSQL，gateway 改为 ClusterIP 并通过本机临时转发验证；不接入公网、不会复用产品数据。测试源码先复制为固定快照，结束删除临时命名空间和状态文件。它覆盖真实草稿 HTTP/mTLS/数据库链路、Content 重启后读取、网络允许与拒绝路径、完成标记传播、错误镜像回退、失败迁移同 SHA 重试及重复发布。临时数据库只有一个实例，不把此结果当作生产主备切换验收。

其他检查为 `npm run ci`、Go 的 Buf / vet / race 集成检查，以及 `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/ci.yml .github/workflows/deploy.yml`。公网实际启用还需对应 dev SHA 的真实 Deploy 运行和公网接口证据。

本轮本地执行记录（2026-09-21）：

| 命令 / 检查 | 实际结果 |
| --- | --- |
| `npm ci --ignore-scripts`；`npm run ci` | 退出 0；文档/分支检查、Swagger 构建、5 个 Node HTTP 测试及 18 个 Python 测试通过 |
| backend 下 `buf lint`、`buf generate`、`git diff --exit-code -- gen`、`go vet ./...`、`go test -race -tags=integration ./... -count=1 -timeout=120s`、`go build ./cmd/...` | 均退出 0；使用单独的临时 PostgreSQL Docker 容器，完成后删除容器及 DSN 文件；另通过 Go 格式及 account.js 语法检查 |
| 上述 `actionlint@v1.7.12` 命令 | 退出 0，CI / Deploy 两个 workflow 校验通过 |
| `python3 ops/lab/validate_modules.py` | 最终退出 0；`human-worth-verify-1dec2f8f` 内全部链路、22 项网络判据、完成标记、回退、迁移重试和重复部署通过，命名空间已删除；原始日志 `/tmp/human-worth-module-validation-verified.log` |
| `python3 ops/verify_deployment.py 5b9de6c6f43299495c78d54aed1fa9a3a0b3cb0e --timeout=0` | 按预期退出 1：当前公网只有 Identity，不能把原健康接口的 200 当成模块全部上线 |
| 公网 `/api/health`、临时资源清理 | 公网仍是原版本 `5b9de6c` 且健康；没有遗留验证命名空间 |

初次网络探针发生一次数据库连接失败；原探针未记录具体错误，因此不把原因确定为 DNS 或策略故障。探针已补上错误诊断、最长 12 秒初始化等待，并要求 DNS 失败不能算网络拒绝，最终所有允许与拒绝路径通过。更早一次运行与编辑中的声明混用，之后将测试应用与声明复制为固定快照再运行。这些未完成运行不计为 PASS；上表是合并前的本地记录，公网发布记录如下。

#### 首次公网发布记录（2026-09-21）

[PR #21](https://github.com/KDZZZZZZ/human-worth/pull/21) 在 PR CI 通过后 squash 合入 dev，首次发布提交为 `cbca132e93e7a9d2e52ed1b128b2fba7f2785ef5`。该提交的 [push CI](https://github.com/KDZZZZZZ/human-worth/actions/runs/35566673284) 与自动触发的 [Deploy](https://github.com/KDZZZZZZ/human-worth/actions/runs/35566745552) 均成功。

- 从该提交升级仓库外的控制器、基础设施模板与 systemd 单元，核对 12 个已安装文件与源码一致且由 root 持有；原控制器备份在 `/var/backups/human-worth/controller-20260921T055952Z`。定时器已恢复自动运行。
- Content、Identity 迁移 Job 均成功；Identity、Content、gateway 各 2/2 就绪，PostgreSQL 三实例健康。控制器于北京时间 14:06:56 完成发布，日志保存在 `/tmp/human-worth-module-first-public-release.log`。
- 公网 `/api/health` 同时返回上述 `revision`、`deploymentRevision`，以及 `deployedServices: [identity, content, gateway]`。`stage: identity` 是保留的历史字段，完整模块状态以完成标记为准。
- 真实公网 HTTPS 使用两个临时合成账号验证：创建 201、同键重试找回同一草稿、本人读取 200、整体更新 200、新版本持久可读、旧版本冲突 409、跨账号读取/更新 404、无会话 401、缺少 CSRF 403、退出后会话失效。合成账号、凭据、草稿及其测试审计记录均已清理；未读取既有用户凭据或作品。
- `python3 ops/verify_deployment.py <首次发布 SHA> --timeout=240` 与 `/tmp/human-worth-public-smoke <首次发布 SHA>` 均退出 0；原始输出为 `/tmp/human-worth-public-deployment-verification.log`、`/tmp/human-worth-public-content-smoke.log`，临时验收程序为 `/tmp/human-worth-public-smoke.go`。

这次公网验收没有重复执行真人 Google 授权或数据库主备切换；前者沿用用户此前确认的结果，后者沿用 Identity lab 既有实验记录，不扩大本次证据范围。

<details>
<summary>首次安装与入口切换的操作参考</summary>

在已经核对目标 dev SHA 与成功 CI 的 checkout 中，安装可信控制器（首次安装前可运行 `python3 ops/lab/release.py --check-only`，只验证资格，不激活）：

```sh
sudo install -d -m 0755 /usr/local/lib/human-worth/lab
sudo install -m 0644 ops/deploy.py ops/deployment_health.py /usr/local/lib/human-worth/
sudo install -m 0644 ops/lab/*.py ops/lab/*.yaml ops/lab/*.json ops/lab/*.sql ops/lab/*.go /usr/local/lib/human-worth/lab/
mkdir -p ~/.local/share/human-worth-identity
sudo install -m 0644 ops/systemd/human-worth-identity-deploy.service ops/systemd/human-worth-identity-deploy.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl start human-worth-identity-deploy.service
```

需要构建代理时，把 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY` 写在私有 `~/.config/human-worth/deploy.env`，不从聊天或仓库读取凭据。控制器首次健康检查通过后，再 `sudo systemctl enable --now human-worth-identity-deploy.timer`。

公网切换顺序：保留 ECS 实际 Nginx 配置备份 → 在 SSH 服务配置与专用公钥的 `permitlisten` 限制中补 `127.0.0.1:28443`（同时保留 28090）→ 安装更新后的隧道 service 并确认 `28443 → 本机 18443` → 只把 lab **公开 CA 证书**放到 ECS `/etc/nginx/human-worth/lab-ca.crt` → 安装域名 Nginx 候选配置并 `nginx -t` → reload。Go 上游是经过 CA 验证的 HTTPS；域名、回调和 Cookie 都保持 worth.oopsbox.cn。原 IP 的 18090/28090 基座链路继续保留。

检查公网 `/api/health` 的 SHA、浏览器 Google 登录/刷新/退出/再次登录，再同步发布 Swagger 0.5.0。入口异常时恢复已保存的域名配置、`nginx -t` 后 reload，退回 28090 基座；应用内部失败由控制器恢复先前模板。进行人为故障实验前暂停 Identity timer，结束后恢复，避免控制器与实验同时变更副本。

以上安装与首次切换已经执行，自动发布控制器运行中。入口回退配置已保存；本轮未人为切回公网基座，不能将配置备份当作已完成公网回退演练。真实 Google 首次登录由用户确认，跨设备等完整矩阵仍按后续业务进展扩展。

</details>

<a id="lab-plan"></a>
## 2. 单机 kind 实验计划

**先在一台服务器上搭一套名为 `lab` 的实验环境，逐步把 Go 服务放进去，再练习故障处理和独立发布。所有实验共用这套配置。**

**按模块推进：Identity 已完成接口与实现，并先放入 lab；下一步再设计 Content。** 不等待所有模块设计结束才验第一条真实链路。

### 要部署什么

只使用一套 `lab`：**1 个控制平面节点 + 3 个工作节点**。每种应用先跑两份，数据库保持 1 主 2 备。下面两张图分别说明“里面有什么”和“副本放在哪里”。

#### 从宿主机到 Go 进程

图中展开的是投票服务的一个副本；其他业务 Pod 使用同样的结构。框表示包含关系，箭头表示创建或启动关系。

```mermaid
flowchart TB
    subgraph physicalHost["宿主机：一台 Linux 服务器"]
        kindCli["kind 命令：创建 lab 集群"]
        dockerEngine["Docker：运行节点容器"]

        subgraph controlNode["lab-control-plane：控制平面节点容器"]
            controlComponents["kubelet、containerd、控制平面系统 Pod"]
        end

        subgraph workerNode["lab-worker：工作节点 1 的节点容器"]
            kubeletAgent["kubelet：管理本节点的 Pod"]
            nodeRuntime["containerd：运行应用容器"]
            subgraph votingPod["Pod：voting 的第 1 个副本"]
                subgraph votingContainer["应用容器：voting"]
                    votingProcess["Go 主进程：voting"]
                end
            end
            kubeletAgent -->|"请求启动容器"| nodeRuntime
            nodeRuntime -->|"运行其中的容器"| votingPod
        end

        otherWorkers["另两个工作节点容器：同样有 kubelet、containerd 和各自的 Pod"]
        kindCli -->|"调用"| dockerEngine
        dockerEngine -->|"运行"| controlNode
        dockerEngine -->|"运行"| workerNode
        dockerEngine -->|"运行"| otherWorkers
    end
```

kind 负责搭建集群。运行时，控制平面负责调度，节点里的 kubelet 和 containerd 负责落实 Pod 配置、启动容器。**业务容器内运行 Go 程序，无需安装 Docker。**

本项目首版每个业务 Pod 放一个应用容器，启动一个业务主进程。进程可以有多个 goroutine；挑战执行器也可能启动任务子进程，这些不另算服务副本。节点内运行时与 Pod 的依据见[分层参考](references.md#microservices-lab)。

#### 每个服务如何部署

每行独立构建镜像、独立创建一个 Deployment（维护该应用副本的 K8s 配置），设置 `replicas: 2`。一个服务可以单独更新、扩容和重启。gateway、identity 已有程序并运行双副本，其余进程按下面的目标逐步加入。

| 应用与职责 | Go 主进程名 | Pod 副本数 |
| --- | --- | --- |
| 网关：接收 HTTP / MCP 请求 | `gateway` | 2 |
| 身份：账号、会话、凭据和角色 | `identity` | 2 |
| 内容：任务、作品、投稿和评论 | `content` | 2 |
| 文件：上传、下载和文件访问权限 | `asset` | 2 |
| 投票：单选、永久资格和统计 | `voting` | 2 |
| 审核：审核决定、举报和审计查询 | `moderation` | 2 |
| 挑战：运行状态、任务分配和租约 | `challenge` | 2 |
| 发现：推荐、热度和公开看板 | `discovery` | 2 |
| 挑战执行器：领取任务、调用模型、上报结果 | `challenge-worker` | 2 |
| **合计** | **9 类应用** | **18 个业务 Pod** |

因此，业务全部实现并稳定运行后，是 **18 个业务 Pod → 18 个应用容器 → 18 个业务主进程**。系统组件、数据库和监控另算；发布或扩容期间也可能临时增加 Pod。

原来的 `platform` 作为公共 Go 代码使用，`Execution` 作为 Challenge 内部模块使用，不额外部署进程。七个业务 RPC 服务的数据和接口边界见[架构划分](backend-architecture.md)。

#### 三个工作节点上的副本分布

下面是一次正常调度可能得到的示例，每个框都是一个独立业务 Pod。①、②只是帮助阅读的副本编号，实际 Pod 名称由 K8s 生成。

```mermaid
flowchart LR
    subgraph firstWorker["工作节点 1：lab-worker"]
        direction TB
        gatewayOne["gateway ①"]
        identityOne["identity ①"]
        contentOne["content ①"]
        assetOne["asset ①"]
        votingOne["voting ①"]
        moderationOne["moderation ①"]
        gatewayOne ~~~ identityOne ~~~ contentOne ~~~ assetOne ~~~ votingOne ~~~ moderationOne
    end
    subgraph secondWorker["工作节点 2：lab-worker2"]
        direction TB
        gatewayTwo["gateway ②"]
        identityTwo["identity ②"]
        contentTwo["content ②"]
        challengeOne["challenge ①"]
        discoveryOne["discovery ①"]
        executorOne["challenge-worker ①"]
        gatewayTwo ~~~ identityTwo ~~~ contentTwo ~~~ challengeOne ~~~ discoveryOne ~~~ executorOne
    end
    subgraph thirdWorker["工作节点 3：lab-worker3"]
        direction TB
        assetTwo["asset ②"]
        votingTwo["voting ②"]
        moderationTwo["moderation ②"]
        challengeTwo["challenge ②"]
        discoveryTwo["discovery ②"]
        executorTwo["challenge-worker ②"]
        assetTwo ~~~ votingTwo ~~~ moderationTwo ~~~ challengeTwo ~~~ discoveryTwo ~~~ executorTwo
    end
    firstWorker ~~~ secondWorker ~~~ thirdWorker
```

这里只展示放置关系，服务之间的调用见[调用拓扑](backend-architecture.md#2-目标调用拓扑)。例如工作节点 1 停止后，Voting ② 仍在工作节点 3；它能否继续处理请求，还要看数据库等依赖是否可用。

**配置要求是同一应用的两个副本分散到不同节点，不固定绑定上图的位置。** 节点重启、故障恢复或更新后位置可能改变，仍须检查分散情况。18 个业务 Pod 平均每节点 6 个只是示例，实际还要结合资源请求和配套组件调度。

#### 数据库和配套组件放在哪里

这些组件也在工作节点上运行，但不计入上面的 18 个业务 Pod。

| 组件 | 配置与位置 | 用途 |
| --- | --- | --- |
| PostgreSQL | 3 个数据库 Pod，三个工作节点各 1 个；角色为 1 主 2 备，主库位置可切换 | 七个业务服务使用同一数据库集群，各自拥有 schema 和账号 |
| 文件存储 | 1 个实例与持久卷，调度到一个工作节点 | 保存作品文件；Asset 服务管理访问，文件不保存在 Asset Pod 的临时目录中 |
| Prometheus、Grafana、Tempo | 服务实例初期各 1 个 | 查看指标、日志关联和跨服务请求耗时 |
| 集群配套组件 | CoreDNS、Calico、CloudNativePG 管理程序等，副本与落点按安装清单配置 | 提供域名解析、网络、数据库管理；不计入业务副本数 |

控制平面节点只承担集群管理；API 服务、调度器、控制器和 etcd 等系统组件在那里运行，业务不调度到该节点。完整资源预算仍包含所有系统、数据和观测组件。

所有实验共用这套配置。扩容、停机、断网都是临时实验动作，实验结束后恢复固定配置，再做下一项。

### 按什么顺序做

| 顺序 | 做什么 | 做到什么算完成 |
| --- | --- | --- |
| **1. Identity** | 本模块设计、9 个 RPC、SQL、platform、登录与凭据 | 真实数据库与并发验证通过，再检查 lab 与真实 Google 登录 |
| 2. 首批 lab | 先部署网关、Identity 双副本与数据库三实例 | 会话跨副本、网络隔离、故障恢复可验；当前正在这一阶段 |
| 3. Content | 完成本模块设计和 Proto，再实现真实内容读写 | gateway → Identity → Content 鉴权链路成立；非法请求拒绝 |
| 4. 补齐业务 | 逐步加入文件、审核、投票、发现、挑战和执行器 | 投稿、审核、投票、查看统计和挑战流程可以实际使用 |
| 5. 逐项演练 | 先测正常情况，再练故障、升级和性能 | 有请求结果、数据记录和恢复时间，能说明哪里符合预期、哪里失败 |

每个阶段都在同一套方案上推进。业务模块按开发进度加入，无需现在一次性写完或部署全部服务。

公网迁移放在实验验证之后，作为单独的发布任务处理：完成 HTTPS、切换入口、验证回退，并遵守[PR 与发布规则](../AGENTS.md#pr-authorization)。前端仍用相对 `/api`：开发时代理到公网，部署后由入口转发到内部网关。

### 能练习哪些情况

| 可以做的实验 | 主要想弄明白什么 |
| --- | --- |
| 停掉一个应用副本或工作节点 | 请求会不会失败，多久恢复，剩余副本是否真的接管 |
| 给服务之间加延迟、制造断连 | 请求能否及时结束，重试会不会重复写入数据 |
| 同时投票、查看统计，或重复提交结果 | 并发时规则是否仍成立，例如看过统计后永久不能再投该任务 |
| 运行中取消挑战，再让旧执行器提交结果 | 迟到结果是否被拒绝，作品是否会重复登记 |
| 停掉数据库主库、重启文件存储、恢复备份 | 已确认的数据是否保留，恢复后权限和投票资格是否正确 |
| 单独升级服务、增加副本和请求量 | 新旧版本是否兼容，哪里出现瓶颈，扩容是否有效 |
| 停掉唯一控制平面，再启动它 | 管理功能中断时业务表现如何，恢复后能否继续管理集群 |

每次按“**记录正常表现 → 注入一种故障 → 检查业务结果 → 撤销故障 → 再查数据**”进行。先做单个故障，之后再组合实验。

完整的 14 项检查放在[文末实验清单](#experiment-checklist)，实施对应功能时再看。

### 这套环境的边界

它能帮助我们学习服务通信、并发、重试、故障恢复和独立发布。所有节点共用一台物理机，因此无法证明服务器整机断电后仍可用，也不能代表多台机器的真实性能。

控制平面和文件存储目前都只有一份，可以练习中断与恢复；它们的高可用不在本配置范围内。现有公网入口和 SSH 隧道的可用性也需要单独验证。

**下文 D01～D14 是完整业务实验清单；本轮只验 Identity 对应的部分，不能将整个清单标为通过。**

<a id="implementation-details"></a>
## 3. 实施参数与实验清单

以下内容按需展开，不影响先理解上面的方案。

<details>
<summary>工具、配置参数与实现约束</summary>

### 工具各自做什么

| 工具 | 在本计划中的作用 |
| --- | --- |
| kind | 调用 Docker 创建 K8s 节点容器、组装 lab 集群 |
| Docker | 在宿主机上运行外层节点容器 |
| containerd | 在节点内部运行 Pod 中的应用容器，由 kind 节点镜像提供 |
| Calico | 管理集群网络，让服务访问限制生效 |
| CloudNativePG | 管理 PostgreSQL 实例、复制和主备切换 |
| Prometheus + Grafana | 采集指标并展示曲线 |
| OpenTelemetry + Tempo | 跟踪一个请求经过了哪些服务、每段花多久 |
| Go pprof | 分析某个 Go 进程的 CPU、内存等开销 |
| k6 | 发送测试请求，测量负载增加后的表现 |

安装时固定 kind、Kubernetes、Calico、CloudNativePG 等工具的版本和镜像摘要，并检查版本兼容性。只维护一套 `lab` 声明式配置；使用独立 kubeconfig、实验数据和命名空间。选型依据见[成熟参考](references.md#microservices-lab)。

### 资源与副本落点

- 规划依据是本机 4 核 8 线程、约 16GB 内存。初始实验总预算以 4 个逻辑 CPU、8GiB 内存为起点，包含控制平面、网络、DNS、三个数据库实例、文件存储和监控；先测空载开销，给宿主机和现有服务留出余量。这不是容量已足够的保证。
- 每个 Pod（K8s 调度应用实例的单位）配置 CPU/内存的 requests 和 limits。kind 各节点共用宿主机，不能把节点显示容量相加；限制节点容器时还要核对 kubelet 报告的可分配资源。
- 应用保留双副本、数据库保留三实例。资源不足先降低压测并发、遥测采样和保留时间；仍不足则记录待测项或增加宿主机资源。观测组件及 Tempo 所需存储全部计入预算。
- 挑战执行器每个副本先只同时运行 1 个任务，租约和执行记录保存在 Challenge 服务，执行器的临时工作目录可以丢弃。
- 应用使用独立 Deployment；控制平面不承载业务。按服务设置 `topologySpreadConstraints`，使用 `kubernetes.io/hostname`、`maxSkew: 1` 分散副本，并核对实际落点。滚动更新预留临时副本容量，避免过严的反亲和规则阻塞发布。[拓扑分布约束](https://kubernetes.io/docs/concepts/scheduling-eviction/topology-spread-constraints/)
- 三个工作节点支持失去一个节点后继续分散业务副本，前提是剩余容量和依赖可用。故障检测、驱逐、调度、镜像与卷都会影响恢复时间，需要实测。可用区标签只模拟调度规则，不增加物理隔离。

### 通信与权限

- HTTP/MCP 通过网关；业务服务仅提供内部 gRPC。使用 Kubernetes DNS 寻址，首版采用 Headless Service 与客户端 `round_robin`。实际检查地址更新和各副本请求量，不能仅凭副本数量判断流量已均衡。[gRPC 均衡策略](https://grpc.io/docs/guides/custom-load-balancing/)
- 请求沿调用链传递超时期限；只在一个明确层级重试，限制次数和总时间。写请求只有定义了“重试不重复执行”的规则后才允许自动重试。
- kind 关闭默认 CNI 后安装 Calico。应用网络默认拒绝未声明的访问，再按调用清单放行 DNS、RPC、数据库、文件存储和监控。NetworkPolicy 依赖支持它的网络插件，namespace 本身不构成隔离。[Calico on kind](https://docs.tigera.io/calico/latest/getting-started/kubernetes/kind)
- 服务连接使用 mTLS（双方验证证书），身份断言限定接收方和用途，服务仍需检查业务权限。挑战执行器只访问 Challenge 和受控模型出口，不直连业务库、审核或投票；动态模型域名经受控出口代理处理，普通 NetworkPolicy 不负责域名授权。
- 实验入口和 Kubernetes API 先只绑定本机，使用独立端口；不占用现有 18090、18100 或 SSH 隧道。数据库、内部 RPC、pprof 不开放到公网。

### 数据与文件

- 七个业务服务使用各自的数据库 schema 和账号，不跨服务直接读写表。数据库连接总量按所有副本计算，包含滚动更新的临时副本。
- CloudNativePG 维护 1 主 2 备，三个实例使用独立 PVC 和 PGDATA，分布到三个工作节点。初始同步策略为 `method: any`、`number: 1`、`dataDurability: required`，并按选定版本配置、验证 failover quorum。关键事务使用同步提交；副本不足时可以阻塞或拒绝写入，不自动降低持久性。已固定 CloudNativePG 1.30.0、PostgreSQL 18.4 和 `failoverQuorum: true`，摘要见 [versions.json](../ops/lab/versions.json)。
- 权限和永久禁投资格读权威主库。切换时隔离旧主，只向有效主端点写入；证据不足时不强制提升落后副本。检查复制位置、写入回执和重连时间。提交超时可能意味着结果未知，重试须按业务唯一标识查回结果。
- 数据库副本不能共用同一个 PGDATA。local PV 有节点约束，节点故障后不能假设卷自动迁移；主备切换依靠各副本自己的数据。文件存储先使用一个内部实例和持久卷，应用临时目录不保存唯一作品文件。
- 恢复备份后检查作品、选票和资格。若较旧备份丢失永久禁投记录且无法补全，受影响范围保持不可投票。异机备份和异机恢复才可进一步验证整机损坏后的恢复能力。

### 发布与观测

- 每服务独立镜像和 Deployment，镜像绑定代码 SHA 与 digest。Proto 和数据库变更按“先扩展、再迁移、最后清理”兼容旧版本；回滚服务不等于回滚数据库。
- 配置启动、就绪和存活探针。下游暂时不可用不能直接触发全部服务重启。收到 SIGTERM 后停止接收新请求，限时处理在途工作；挑战执行器停止领取任务，按租约结束或等待接管。[探针说明](https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/)
- 双副本服务初始设置 `maxUnavailable: 0`、`maxSurge: 1`。PDB 约束 drain 等自愿驱逐，不防止节点宕机，也不代替滚动更新策略。[中断说明](https://kubernetes.io/docs/concepts/workloads/pods/disruptions/)
- 记录延迟、错误率、CPU/内存、GC、连接池、队列和租约，并关联请求 trace。监控不读取产品具体投票统计，不采集凭据、作品正文或具体选票；管理日志不展示额度和消耗。
- 初期手动调整副本，接入指标后再测 HPA 自动扩缩容。等待调度的副本不算扩容成功。k6 在集群外发压，容量压测尽量从另一台机器发起；同机发压时记录资源竞争。

</details>

<a id="experiment-checklist"></a>
<details>
<summary>完整业务实验清单：14 项，随模块逐项验收</summary>

只测试已实现的业务链路。每次从健康的固定配置开始，结束后恢复节点、业务副本和数据库复制。数据库切换实验要求控制平面和 Operator（数据库管理程序）可用；控制平面实验不同时注入数据库或工作节点故障。

| 编号 | 操作 | 需要看到的结果 |
| --- | --- | --- |
| D01 | 持续发请求，把某服务从 2 份扩到 3 份，再回到 2 份 | 地址和连接更新，新旧副本都有可查的请求量；状态不依赖固定副本；重试不重复写入 |
| D02 | 分别终止应用 Pod、drain 工作节点、突然停止工作节点 | 记录不同故障下的错误和恢复时间；结合该节点上的数据库、文件存储解释业务是否可用，并检查数据 |
| D03 | 对指定链路加延迟、丢包、单向或双向断连 | 请求有明确超时，资源能释放，重试有界；依赖无法核实时不泄露未审核内容或开放投票 |
| D04 | 写入已提交后丢弃响应，再重试；重复、乱序投递事件 | 选票不重复计数，作品不重复登记；重复回执稳定；旧事件不能恢复已下架内容 |
| D05 | 两个投票副本同时处理首次投票和查看统计，包含资格记录尚不存在的情况 | 同一账号在同一任务上的操作顺序明确；先永久关闭资格再返回统计，丢响应、换 token、重启都不能恢复资格 |
| D06 | 审核决定应用中断、重放旧决定，同时下架与投票、下载 | 中断可恢复，旧决定不能覆盖新状态；未完成不假报成功；下架完成后的新投票、下载被阻止 |
| D07 | 两个执行器交接任务，旧执行器迟到提交；同时取消和登记 | 新租约生效后旧执行器失效；终态后不新增作品；重复登记找回原结果且不重置审核 |
| D08 | 分别停止数据库主进程、让主节点失联、切断主备网络 | 只有有效主库可写；按同步策略确认成功的数据保留；无法安全切换时停止写入 |
| D09 | 中断上传、重启文件存储、恢复数据库和文件备份 | 半成品不能引用，完成文件的摘要一致；下载仍校验权限，恢复不重新开放已关闭的投票资格 |
| D10 | 单独升级一个服务并回滚，保留旧调用者和在途请求 | Proto、HTTP、数据库迁移兼容；新旧版本都遵守业务规则 |
| D11 | 停止唯一控制平面，再恢复同一节点 | 记录 API、调度和 Operator 中断及恢复，检查已有业务的实际表现；此项不验证控制平面高可用或 etcd 多节点选举 |
| D12 | 限制 CPU/内存，逐渐增加请求和副本 | 记录节流、内存不足退出、排队与恢复；测吞吐、延迟和错误率，检查重试是否重复写入 |
| D13 | 执行器越权访问、MCP token 调用管理接口、伪造身份，同时发送合法请求 | 非法请求被相应网络或权限规则拒绝，合法请求成功；同账号在网站与 MCP 的投票资格一致 |
| D14 | 准备含 10、100、1000 件过审作品的任务，同时混入未过审作品 | 候选列表包含该版本全部过审作品，用户仍只选一件，不泄露他人计票；测响应大小、序列化、内存和前端渲染；失败不能悄悄截断候选 |

每项通过都需要业务响应、持久化数据和故障记录共同支持，只看到 Pod 就绪不算通过。

执行时补充以下记录和操作约束：

- 网络故障用 `tc/netem` 和定向规则，限定在实验节点或 Pod 的网络空间，并准备自动撤销；不修改宿主机全局公网和 SSH 链路。[tc-netem](https://man7.org/linux/man-pages/man8/tc-netem.8.html)
- 重复请求、事件由实验驱动器发送；重复 TCP 数据包不等于重复执行业务操作。涉及租约交接时核对租约代次，保证旧执行器不能继续提交。
- 投票和查看统计使用独立测试账号与任务，检查业务成功，不能把快速返回 403 当作有效吞吐。模拟模型仅用于受控实验，真实供应方集成另验。
- 保存代码 SHA、镜像、配置、随机种子、数据规模、故障与清理时间、响应、日志、trace、数据库核对结果。业务延迟记录 p95/p99，性能阈值按实测确定；按[研究记录规则](../AGENTS.md#research)保存实验制品。

</details>

<details>
<summary>方案依据与文档检查范围</summary>

用户明确要求分布式 Go 架构、增加微服务学习内容，并先只使用一套实验配置。kind、工具组合、节点数、副本数、资源预算和文档组织属于 Agent Self-Claimed 的技术选择。服务边界见[架构划分](backend-architecture.md)，技术来源见[成熟参考](references.md#microservices-lab)。

本节保留宿主机到进程的分层图、九类应用的副本清单与节点分布示例。文档检查覆盖可信场景快照、本地链接、图示及副本数量的一致性。这些检查不代表 Go 服务、集群或上述实验已经完成。

2026-09-18 图示验证记录：`python3 scripts/check_docs.py` 与 `git diff --check` 退出码均为 0；两张 Mermaid 图经 Mermaid 10.9.3 与本机 Chrome 渲染、查看通过。另核对架构中的九类应用与部署清单一致，图中十八个副本无遗漏，同服务两份位于不同工作节点。渲染工具仅用于本次文档检查，未加入项目依赖。

</details>
