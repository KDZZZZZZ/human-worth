# 成熟参考与设计补全

用户要求：当人类设计不清晰时，agent **主动寻找合适的成熟参考并补全方案**，不把所有缺口退回给人类。先理解可信场景和约束，选相似问题、可核实实现与维护活跃的参考，再形成适合本项目的可验证决定。

2026-09-17 核实以下官方来源。本轮未发现已安装的 CI/CD 专门技能，因此依据官方资料完成；不是凭技能名称假设能力。

| 来源 / 版本 | 解决的问题 | 本项目采用与差异 |
| --- | --- | --- |
| [GitHub repository rules API](https://docs.github.com/en/rest/repos/rules)；核实于 2026-09-17 | PR-only、0 审批、状态检查、分支限定合并方式 | dev squash / main merge，均无 bypass；dev 要求最新基线 CI |
| [GitHub Actions secure use](https://docs.github.com/en/actions/reference/security/secure-use)；核实于 2026-09-17 | 公开仓库 CI 与本机权限边界 | PR 在 GitHub 托管 runner 执行；本机只读取 protected dev 及对应成功 CI，不注册公开仓库的自托管 runner |
| [OpenGitOps 四项原则](https://opengitops.dev/) v1.0 | 版本化期望状态、主动拉取、持续调和 | 使用 systemd 定时拉取 dev；这是轻量部署控制器，不宣称实现完整 GitOps 平台 |
| 本机 `man systemd.timer` / `systemd.service`；[systemd 上游](https://github.com/systemd/systemd) | 开机恢复、自动调度、独立账户、服务重启 | 60 秒定时检查，失败保留旧版；安装后的控制器为本机 root 管理文件，不执行未合并 PR |
| [Nginx proxy_pass](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_pass)；[OpenSSH ssh](https://man.openbsd.org/ssh#R) | 公网网关反向代理到本机 | 独立公网端口 18090；SSH 仅监听网关 loopback 28090，应用在本机 18090 |
| [阿里云 ECS RunCommand](https://www.alibabacloud.com/help/en/ecs/developer-reference/api-ecs-2014-05-26-runcommand)；本机 CLI 3.4.11 | 已有服务器增量配置与结果查询 | 从当前 CLI 账号识别现有 cn-beijing ECS，通过云助手安装独立配置，不新购资源 |
| [AGENTS 生成技能](https://github.com/KDZZZZZZ/codex-agents-md-generator)；安装目录摘要 `235448749c077e373af59aa7bd2b7666b09f6ca0` | 指令来源、实际治理与充分验收 | 按用户指定技能修订 AGENTS；目录摘要不是 Git commit |

新增参考记录需说明：待解决缺口、来源链接及版本 / commit、适配点、不适配点、采用决定、验证证据。必要时查看参考项目的真实代码和测试。不要堆砌品牌、盲目复制，或把参考设计标成 Human Design。

与明确的人类要求冲突时遵从人类要求；既有授权内能用成熟惯例解决的选择直接推进。仅在成熟参考仍无法消除实质歧义，或会产生未授权的不可逆影响、显著费用时，提出一个具体问题，同时继续独立工作。
