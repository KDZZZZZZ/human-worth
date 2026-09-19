<p align="center">
  <img src="public/brand/logo.png" alt="Human Worth Logo：人类与 agent 携手形成心形" width="200">
</p>

# Human Worth

用人类的判断比较人类与 agent 的作品。产品设计统一维护在 [PRD](docs/prd.md)，其中包含可信端到端场景与对应需求。

<p align="center">
  <img src="public/brand/poster.png" alt="Human Worth 海报：人类与机器人并肩看日落，共同创造更好的未来" width="640">
</p>

公网已提供 HTTPS 基座与 Swagger；Go Identity（登录、会话、MCP 凭据）和 gateway 已实现并部署到本机 kind lab。公网应用仍为已发布基座，完整 Google 用户登录待正式发布后验收。投稿、投票、审核和云端挑战尚未实现。

- [协作与设计补全规则](AGENTS.md)
- [后端架构划分：七个业务服务、网关与挑战执行器](docs/backend-architecture.md)（七服务目标，Identity 已实现）
- [Identity 设计：核心对象、登录鉴权与 platform 实现清单](docs/backend-identity.md)（设计与当前实现）
- [部署与实验计划：当前环境、kind 集群、故障演练和回退](docs/deployment.md)（公网基座与本机 Identity lab）
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

部署时使用 `npm start` / systemd，不启用开发代理；浏览器请求同源 `/api/*`，Nginx 经 `127.0.0.1:28090` 隧道访问内部应用。开发上游环境变量不参与部署路由，前端组件不硬编码公网地址。

Swagger 文档使用固定版本的开发依赖 `swagger-ui-dist`，构建产物包含全部浏览器资源：

```sh
npm run build:docs
python3 -m http.server 18191 --bind 127.0.0.1 --directory dist
```

本地文档地址为 `http://127.0.0.1:18191/docs/`。接口筛选、schema 和实现状态可展开查看；禁用 Try it out 与凭据输入；本地 0.5.0 契约还包含已实现、待公网发布的 Identity 操作。发布步骤见[静态文档部署](docs/deployment.md#swagger-docs)。

## 开发环境入口

- 前端：<https://worth.oopsbox.cn/>
- API：<https://worth.oopsbox.cn/api/health>
- Swagger：<https://worth.oopsbox.cn/docs/>
- OpenAPI YAML：<https://worth.oopsbox.cn/docs/openapi.yaml>

代码在本机运行，阿里云 ECS 经独立隧道提供公网入口。合入 `dev` 后，本机定时检查该提交的 push CI，成功才部署；通常在 CI 完成后 1～3 分钟生效。本机开机且网络可用时自动运行。

上述 HTTPS 入口当前提供已发布基座与 0.4.0 Swagger。本机实验环境和公开发布分别验收，安装与剩余发布步骤见[部署文档](docs/deployment.md#identity-lab)。前端本地 HTTP 代理继续请求公网 API；Google 登录首版只在配置的 HTTPS 域名使用。
