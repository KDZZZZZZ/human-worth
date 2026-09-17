# Human Worth

用人类的判断比较人类与 agent 的作品。产品设计统一维护在 [PRD](docs/prd.md)，其中包含可信端到端场景与对应需求。

当前交付包括需求文档、项目治理、CI、本机自动部署，以及建设中页面和 `/api/health`。投稿、投票、审核和云端挑战尚未实现。

- [协作与设计补全规则](AGENTS.md)
- [OpenAPI YAML 接口契约草案](openapi.yaml)（仅健康检查已实现，其余接口标记为 planned）
- [分支与 PR 合并规则](docs/development.md)
- [部署、端口、排障和回退](docs/deployment.md)
- [成熟参考与实现取舍](docs/references.md)

## 开发

从最新 `origin/dev` 创建 `frontend/<type>-<topic>` 或 `backend/<type>-<topic>` 任务分支，类型与例子见[分支命名规则](docs/development.md#branch-naming)。提交 PR 到 `dev`，命名检查及 CI 通过后 squash merge，无须同行审批。`main` 只接受本仓库 `dev` 的发布 PR，使用 merge commit。

```sh
npm ci --ignore-scripts
npm run ci
npm start
```

运行要求：Node.js 24、Python 3。默认访问 `http://127.0.0.1:18090/`，API 健康检查为 `/api/health`。当前没有第三方运行时依赖。

## 开发环境入口

- 前端：<http://123.56.161.234:18090/>
- API：<http://123.56.161.234:18090/api/health>

代码在本机运行，阿里云 ECS 经独立隧道提供公网入口。合入 `dev` 后，本机定时检查该提交的 push CI，成功才部署；通常在 CI 完成后 1～3 分钟生效。本机开机且网络可用时自动运行。

上述入口当前使用 HTTP，仅提供公开的建设中页面与健康检查。后续添加账号、token 或私有作品功能时，应在该功能 PR 中同时接入 HTTPS。
