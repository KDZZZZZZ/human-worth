# Human Worth

用人类的判断比较人类与 agent 的作品。产品设计统一维护在 [PRD](docs/prd.md)，其中包含可信端到端场景与对应需求。

当前交付包括需求文档、项目治理、CI、本机自动部署，以及建设中页面和 `/api/health`。投稿、投票、审核和云端挑战尚未实现。

- [协作与设计补全规则](AGENTS.md)
- [OpenAPI YAML 接口契约草案](openapi.yaml)（仅健康检查已实现，其余接口标记为 planned）
- [Swagger 在线文档](http://123.56.161.234:18090/docs/)（只读浏览与 YAML 下载）
- [分支与 PR 合并规则](docs/development.md)
- [部署、端口、排障和回退](docs/deployment.md)
- [成熟参考与实现取舍](docs/references.md)

## 开发

从最新 `origin/dev` 创建 `frontend/<type>-<topic>` 或 `backend/<type>-<topic>` 任务分支，类型与例子见[分支命名规则](docs/development.md#branch-naming)。只有人类明确命令为当前任务开 PR 后，才提交 PR 到 `dev`；命名检查及 CI 通过后 squash merge，无须同行审批。`main` 只接受本仓库 `dev` 的发布 PR，使用 merge commit。

```sh
npm ci --ignore-scripts
npm run ci
npm start
```

运行要求：Node.js 24、Python 3。默认访问 `http://127.0.0.1:18090/`，API 健康检查为 `/api/health`。当前没有第三方运行时依赖。

前端开发使用 `npm run dev:frontend`，访问 `http://127.0.0.1:18100/`。前端统一请求相对路径，例如 `fetch('/api/health')`；开发服务将 `/api/*` 转发到公网 `http://123.56.161.234:18090`，页面资源仍来自本地。可用 `FRONTEND_API_ORIGIN` 覆盖开发上游。

部署时使用 `npm start` / systemd，不启用开发代理；浏览器请求同源 `/api/*`，Nginx 经 `127.0.0.1:28090` 隧道访问内部应用。开发上游环境变量不参与部署路由，前端组件不硬编码公网地址。

Swagger 文档使用固定版本的开发依赖 `swagger-ui-dist`，构建产物包含全部浏览器资源：

```sh
npm run build:docs
python3 -m http.server 18191 --bind 127.0.0.1 --directory dist
```

本地文档地址为 `http://127.0.0.1:18191/docs/`。接口筛选、schema 和实现状态可展开查看；禁用 Try it out 与凭据输入，仅 `/api/health` 的 GET、HEAD 已实现。发布步骤见[静态文档部署](docs/deployment.md#swagger-docs)。

## 开发环境入口

- 前端：<http://123.56.161.234:18090/>
- API：<http://123.56.161.234:18090/api/health>
- Swagger：<http://123.56.161.234:18090/docs/>
- OpenAPI YAML：<http://123.56.161.234:18090/docs/openapi.yaml>

代码在本机运行，阿里云 ECS 经独立隧道提供公网入口。合入 `dev` 后，本机定时检查该提交的 push CI，成功才部署；通常在 CI 完成后 1～3 分钟生效。本机开机且网络可用时自动运行。

上述入口当前使用 HTTP，仅提供公开的建设中页面、健康检查和只读接口文档。后续添加账号、token 或私有作品功能时，应在该功能 PR 中同时接入 HTTPS。
