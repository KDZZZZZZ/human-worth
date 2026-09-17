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

同源 `/api/` 避免前端写死第二个主机和端口。现有 ECS 80、443、8080、18082～18084 已有其他用途，新增配置只使用本项目端口。安全组只新增 TCP 18090；28090 不对公网开放。

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

当前公网 HTTP 仅用于公开说明和健康检查。引入身份认证、API token 或私有作品前，同一功能交付必须建立 HTTPS；不通过当前 HTTP 入口传输这些数据。
