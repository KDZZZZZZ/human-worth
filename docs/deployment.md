# 本机开发环境与公网部署

部署基座提供建设中页面与健康检查。它不代表 PRD 中的投稿、真人投票、审核或云端 agent 已实现；此处运行的是 Web/API 服务，云端挑战环境另按可信场景设计。

## 实际拓扑

| 位置 | 职责 / 地址 |
| --- | --- |
| GitHub | `KDZZZZZZ/human-worth`；PR CI 与 dev / main push CI 使用托管 runner |
| 当前本机 | systemd 运行应用和拉取控制器；应用只监听 `127.0.0.1:18090` |
| 阿里云 CLI 当前账号的 cn-beijing ECS | 公网 IP `123.56.161.234`，Nginx 监听 TCP `18090` |
| 专用 SSH 隧道 | ECS `127.0.0.1:28090` → 本机 `127.0.0.1:18090` |
| 前端 / API | `http://123.56.161.234:18090/` / `http://123.56.161.234:18090/api/health` |
| Swagger 文档 | `http://123.56.161.234:18090/docs/`；由 ECS Nginx 直接提供静态文件 |

同源 `/api/` 避免前端写死第二个主机和端口。现有 ECS 80、443、8080、18082～18084 已有其他用途，新增配置只使用本项目端口。安全组只新增 TCP 18090；28090 不对公网开放。

## 前端开发与部署的 API 路由

用户明确要求开发使用公网 API，部署使用内部调用。前端代码始终使用相对 `/api/*`，通过服务入口区分环境：

| 环境 | 启动方式 | API 请求链路 |
| --- | --- | --- |
| 前端开发 | `npm run dev:frontend`，默认 `127.0.0.1:18100` | 浏览器 → 本地开发代理 → `http://123.56.161.234:18090/api/*` |
| 已部署站点 | systemd / `npm start` | 浏览器 → 同源 Nginx `/api/*` → 网关 `127.0.0.1:28090` → 隧道内的本机应用 |

开发代理只接管 `/api/` 前缀，保留方法、查询参数、请求体和响应状态；上游不可达返回 502，不回退到本地 API。开发上游可由 `FRONTEND_API_ORIGIN` 覆盖。生产环境禁止 `--frontend-dev`，正常部署启动不会读取该变量。浏览器不直接连接内部地址，内部转发由服务端完成。

## 自动部署过程

1. PR 的 `CI` 通过后 squash 合入 dev；GitHub 再运行 dev push CI。
2. 本机 `human-worth-deploy.timer` 每 60 秒启动一次控制器，拉取最新 dev SHA。
3. 控制器只接受本仓库 `.github/workflows/ci.yml` 对该 SHA、dev、push 事件的最新一次成功运行。pending、失败、缺失或其他分支 / 事件均不部署。等待 CI 时每 180 秒重试 API，避免频繁查询。
4. 再确认 dev 没被新提交替代，按 SHA 解包到 `/opt/human-worth/releases/<sha>`；不在开发者工作区部署，不执行仓库里的安装脚本。
5. 原子切换 `current` 符号链接并重启项目服务；健康检查必须返回同一 SHA。失败切回上一版本并检查恢复结果，首次部署失败则停止服务。
6. 成功记录到 `/opt/human-worth/state.json` 与 journal。应用 `/api/health` 返回当前 revision，公网验收以该值为准。

本机通常在 CI 成功后 1～3 分钟部署。机器关机或离线时不部署，重新联网后继续调和最新 dev；ECS 入口此时可能返回 502。进程重启可能产生短暂连接中断，当前不承诺零停机。main 合并不触发本机版本切换。

## 文件与权限

| 项目 | 安装位置 / 权限 |
| --- | --- |
| 应用 | `human-worth` 系统账户；release 只读，无开发者家目录访问 |
| 部署控制器 | `human-worth-deploy` 账户；写 `/opt/human-worth`；sudo 仅允许 restart / stop `human-worth.service` |
| 已安装控制器 | `/usr/local/lib/human-worth/deploy.py`；root 拥有，不由普通代码部署自动覆盖 |
| 服务与 timer | `/etc/systemd/system/human-worth*.service`、`human-worth-deploy.timer` |
| 隧道 | 独立 `human-worth-tunnel` 账户和 SSH key，私钥仅在本机 `/var/lib/human-worth-tunnel` |
| ECS 入口 | `/etc/nginx/conf.d/human-worth.conf`；独立 SSH 用户仅允许反向转发到 28090，无交互 shell 能力 |

配置源在 [ops/systemd](../ops/systemd)、[ops/nginx](../ops/nginx)、[ops/ssh](../ops/ssh)、[ops/deploy.py](../ops/deploy.py)。本机初始化入口为 `sudo bash ops/install-local.sh`，之后将新公钥写入 ECS 专用用户，并经云助手核对主机公钥后写入本机 known_hosts，启动隧道和 timer。基础设施文件经 PR 修改后由运维安装；控制器不会给仓库代码任意系统管理权限。仓库和 GitHub Actions 不保存阿里云凭据或隧道私钥。

## 检查、回退与维护

```sh
systemctl status human-worth.service human-worth-deploy.timer human-worth-tunnel.service
sudo journalctl -u human-worth-deploy.service -n 50 --no-pager
curl --fail http://127.0.0.1:18090/api/health
curl --fail http://123.56.161.234:18090/api/health
```

常规回滚：从 dev 建任务分支，以 `git revert` 撤销问题变更，经 CI / PR 合入，由控制器部署新 SHA。失败激活会自动回退；如果 API 不可达，先查看应用、隧道与 Nginx 状态，分别定位本机和公网链路。

紧急冻结调和：`sudo systemctl stop human-worth-deploy.timer`，保留当前服务，处理问题后 `sudo systemctl start human-worth-deploy.timer`。不要通过关闭 GitHub 分支保护或 force push 回滚。停止公网入口时只停止 `human-worth-tunnel.service`，不要停整台 ECS 的 Nginx。

当前保留所有已部署 release 以便诊断；定期清理时至少保留 current 与最近成功版本，不在部署并行进行时删除。部署控制器单实例文件锁防止重叠。

当前公网 HTTP 仅用于公开说明、健康检查和只读接口文档。引入身份认证、API token 或私有作品前，同一功能交付必须建立 HTTPS；不通过当前 HTTP 入口传输这些数据。

<a id="swagger-docs"></a>
## Swagger 静态文档

2026-09-17，用户明确要求生成 Swagger 文档并部署。该授权用于独立静态文档发布，不自动授权创建 PR，也不改变应用从受保护 dev 和成功 CI 部署的规则。源码集成仍须遵循明确的人类 PR 命令、分支保护与 CI 门槛。

源文件为根目录 [openapi.yaml](../openapi.yaml) 和 [docs/swagger](swagger)。`npm ci --ignore-scripts && npm run build:docs` 生成 `dist/docs/`，包含固定版本 Swagger UI、许可证及逐文件 SHA-256 清单。浏览器只请求同源资源，关闭在线校验器、Try it out 和授权输入；38 个操作中只有健康检查的 GET、HEAD 已实现，其余 36 个是契约提案。

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
