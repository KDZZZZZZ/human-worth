<p align="center">
  <img src="public/brand/logo.png" alt="Human Worth Logo：人类与 agent 携手形成心形" width="200">
</p>

# Human Worth

用人类的判断比较人类与 agent 的作品。产品设计统一维护在 [PRD](docs/prd.md)，其中包含可信端到端场景与对应需求。

<p align="center">
  <img src="public/brand/poster.png" alt="Human Worth 海报：人类与机器人并肩看日落，共同创造更好的未来" width="640">
</p>

公网已提供 Go Identity（Google 登录、会话、MCP 凭据）与 Swagger，后端运行在本机 kind lab。2026-09-19 用户已确认真人 Google 登录成功并看到账号。Challenge 已完成本地核心实现与依赖 fake 联调，三角色 Completion 及 E 工具往返已验证，尚未发布；真实 E 沙箱待联调。投稿、投票和审核服务仍待实现。

- [协作与设计补全规则](AGENTS.md)
- [后端架构划分：七个业务服务、网关与挑战执行器](docs/backend-architecture.md)（七服务目标，Identity 已实现）
- [Identity 设计：核心对象、登录鉴权与 platform 实现清单](docs/backend-identity.md)（设计与当前实现）
- [Challenge 设计：打包、执行、排序三个 agent](docs/backend-challenge.md)（设计草案；先 R 拟合达标，再固定 R 优化 P／运行 E；P 不看任何评论）
- [部署与实验计划：当前环境、kind 集群、故障演练和回退](docs/deployment.md)（公网 Identity 与保留的基座回退链路）
- [OpenAPI YAML 接口契约草案](openapi.yaml)（健康检查与 Identity 已有代码；区分实现及公网发布状态）
- [Swagger 在线文档](https://worth.oopsbox.cn/docs/)（只读浏览与 YAML 下载）
- [分支与 PR 合并规则](docs/development.md)
- [成熟参考与实现取舍](docs/references.md)

## 开发

从最新 `origin/dev` 创建 `frontend/<type>-<topic>` 或 `backend/<type>-<topic>` 任务分支，类型与例子见[分支命名规则](docs/development.md#branch-naming)。只有人类明确命令为当前任务开 PR 后，才提交 PR 到 `dev`；命名检查及 CI 通过后 squash merge，无须同行审批。`main` 只接受本仓库 `dev` 的发布 PR，使用 merge commit。

```sh
npm ci --ignore-scripts
npm run ci
npm start
```

现有基座运行要求：Node.js 24、Python 3。默认访问 `http://127.0.0.1:18090/`，API 健康检查为 `/api/health`。Node 基座没有第三方运行时依赖；Go 后端使用 Go 1.27.1，见 [Go 构建与测试](backend/README.md)。

前端开发使用 `npm run dev:frontend`，访问 `http://127.0.0.1:18100/`。前端统一请求相对路径，例如 `fetch('/api/health')`；开发服务将 `/api/*` 转发到公网 `https://worth.oopsbox.cn`，页面资源仍来自本地。可用 `FRONTEND_API_ORIGIN` 覆盖开发上游。

公网域名由 Nginx 经 `127.0.0.1:28443` 私有 TLS 隧道转发到集群 gateway，再调用 Identity；保留的 Node 基座使用 `npm start` / systemd 和 28090 隧道。部署不启用开发代理，前端组件不硬编码公网地址。

Swagger 文档使用固定版本的开发依赖 `swagger-ui-dist`，构建产物包含全部浏览器资源：

```sh
npm run build:docs
python3 -m http.server 18191 --bind 127.0.0.1 --directory dist
```

本地文档地址为 `http://127.0.0.1:18191/docs/`。接口筛选、schema 和实现状态可展开查看；禁用 Try it out，不执行 API 请求。本地 0.7.0 草案包含已上线的 Identity，以及已本地实现的 Challenge 管理接口、管理员初始包、三角色配置与收尾登记契约，以及待实现的任务附件和作品业务接口；线上仍为 0.5.0。发布步骤见[静态文档部署](docs/deployment.md#swagger-docs)。

## 开发环境入口

- 前端：<https://worth.oopsbox.cn/>
- API：<https://worth.oopsbox.cn/api/health>
- Swagger：<https://worth.oopsbox.cn/docs/>
- OpenAPI YAML：<https://worth.oopsbox.cn/docs/openapi.yaml>

代码在本机运行，阿里云 ECS 经独立隧道提供公网入口。合入 `dev` 后，本机定时检查该提交的 push CI，成功才构建镜像、迁移和滚动部署；所需时间取决于构建与调度。本机开机且网络可用时自动运行。

上述 HTTPS 入口当前提供 Identity 与 0.5.0 Swagger；实际应用版本以 `/api/health` 的 revision 为准。安装、发布证据和回退见[部署文档](docs/deployment.md#identity-lab)。前端本地 HTTP 代理继续请求公网 API；Google 登录首版只在配置的 HTTPS 域名使用。
