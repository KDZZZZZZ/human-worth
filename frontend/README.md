# Human Worth React + Vite 前端

## 范围与运行

前端文件、依赖、构建和测试输出均位于 `frontend/`。原有根工程、后端、设计稿、部署配置未修改。

```sh
npm ci --ignore-scripts
npm run dev
npm run build
npm test
npm run test:import
```

Node.js 使用项目要求的 24。Windows 测试可设置 `PLAYWRIGHT_CHANNEL=msedge` 使用已安装 Edge。

默认页面地址为 http://127.0.0.1:18100 。开发与生产均默认请求相对 `/api/*`。

- 开发模式仍使用相对 `/api/*`，由 Vite 代理到公网 `https://worth.oopsbox.cn`；可用 `FRONTEND_API_ORIGIN` 指定另一后端。直接跨域请求公网会在浏览器被拦截，因为后端当前不返回 CORS 头。
- 可用 `VITE_API_BASE` 覆盖 API 根地址，例如 `VITE_API_BASE=http://127.0.0.1:28090/api npm run dev`。开发模式直连其他来源时同样需要该来源允许跨域（含凭据）。
- 部署后由同源入口转发 API，Cookie 和 CSRF 保持同源。Google 登录页仅在当前页面和认证 API 同源且为 HTTPS 时启用授权入口。

**开发模式和生产模式都使用真实 API。** 不再存在运行时模拟账号、作品、投票、评论或草稿。旧 `VITE_PREVIEW_DATA` 和浏览器 preview 开关均不能启用模拟。接口错误直接显示，不用样例数据兜底。

## 登录与本地 HTTPS

Google 登录使用 `GET /api/auth/google` 的正常浏览器跳转，身份和 CSRF 来自 `GET /api/me`。只有后端返回有效会话才显示 Sign out。退出调用后端；业务请求返回 401 时清除前端会话状态，保留尚未保存的输入。502、非 JSON 响应和网络错误不会被当作操作成功。

会话内存持有服务端 CSRF token；sessionStorage 只缓存公开账号显示信息，不持久化 CSRF 或 OAuth 凭据。Login 始终等待 `/api/me` 重新确认身份，浏览器从后退缓存恢复页面时也重新查询。

Google 回调与 `__Host-` 安全 Cookie 绑定公网 HTTPS 域名，普通 localhost HTTP 不能继承公网会话。本地登录开发可运行：

```sh
npm run dev:auth
# 服务器已经运行，只重新打开窗口：
node scripts/dev-auth.mjs --open-only
```

该工具在 127.0.0.1:18101 启动 HTTPS Vite，打开独立 Edge 配置目录，并默认进入 `https://worth.oopsbox.cn/`。在这个窗口中，该域名映射至本地前端；Node 代理仍使用正常 DNS 连接公网 API。只为此开发窗口固定本地证书的公钥指纹，不修改系统 hosts、系统证书信任或用户日常浏览器配置。

脚本会读取 Windows 系统代理并显式传给 Edge，同时让 `worth.oopsbox.cn` 绕过代理；否则系统代理会自行解析域名，本地映射失效，页面会落到公网站点。可用 `DEV_AUTH_PROXY=http://127.0.0.1:7897` 覆盖检测结果。Google 页面继续通过该代理访问。

本地证书及浏览器资料放在忽略提交的 `.local/`，Vite 明确禁止读取该目录及私钥文件。证书有效期 30 天；过期后仅删除本工具生成的 `.local/dev.crt` 和 `.local/dev.key` 再运行。此工具目前面向 Windows + Edge，使用 Git 随附 OpenSSL，也支持 `OPENSSL_PATH`、`EDGE_PATH`。

后端没有预置默认管理员密码。Google 初次建号为普通用户；管理员角色通过后端受限的 `identity-admin` / `ops/lab/account.py` 运维流程授予，前端不能自行提权。当前真人登录、管理员状态和数据库写入验收尚未完成；2026-10-29 公网 health、tasks、board 实测均返回 502。

## 页面与真实调用

| 页面／操作 | 实现 |
| --- | --- |
| Home | 保留原稿开场与 Paper & Ink 风格，特色任务标题、摘要和材料从任务 API 读取；无任务时显示空态 |
| Discovery | 推荐／热度任务、搜索、分类和分页；分类独立遍历未筛选任务，不随当前筛选消失 |
| Task | 后端任务、附件、过审作品、完整候选、单选投票、永久关闭资格后的统计、评论与举报 |
| Submit | 新任务、草稿保存／更新／送审、独立追加作品及驳回重提，文件上传与续传 |
| My submissions | 按任务／追加作品和审核状态查询本人投稿；重新打开后从 API 读取内容 |
| Board 弹窗 | 按需读取公开汇总，不预取单题统计；热度入口使用 `order=hot` |

保存草稿后 URL 记录服务端 ID，刷新可恢复。追加作品页只要求当前这一件作品，不要求重交原任务或双方作品。版本冲突可查看服务端内容再选择；请求结果不确定时保留原幂等键。文件在 ready 后才可被引用；每件任务附件的云端授权需明确选择。

统计操作要求显式确认永久禁投；普通浏览不读取具体票数。来源、作品正文及后端错误内容不翻译。品牌和浏览器标题固定为 Human Worth。

任务作品阅读由 `state/usePublicWorks.js` 独立调用公开 `/api/tasks/{taskId}/entries`，按 `nextCursor` 读取全部页；匿名、可投票和永久禁投账号共用阅读器。ballot 仅决定能否选择并提交偏好，统计、资格查询失败或清空 ballot 都不清空作品。重载失败保留已读取内容并允许重试；普通阅读不读取统计。`tests/public-works.spec.js` 覆盖禁投后的阅读、刷新、重载、重新登录、新增过审作品、分页和失败重试；这些为浏览器契约测试，未代替真实账号与数据库验收。

未实现完整管理后台、挑战管理和 MCP 凭据管理页面。Board 当前是已有导航弹窗中的真实汇总，并非完整独立排行榜页面。

## 设计稿数据导入

原稿提取数据已移出应用运行目录，放入 `scripts/data/design-data.json`。它仅供导入工具和隔离测试使用，不作为产品请求响应。

开发入口：`/dev/import-design`。此路由只在 Vite 开发模式加载，生产构建不包含导入界面或数据。需要真实管理员会话，使用正常投稿、送审、审核决定和评论 HTTP 接口；不直接写数据库，不绕过权限。

当前数据内容：

- 图书馆任务：四件原稿作品，双方齐全，可送审并发布。
- 其余七个任务：原稿未提供作品，保存为草稿。
- Submit 原稿：保留为独立草稿，包含四件作品。
- 六条原稿评论：正文保留，归属实际执行导入的账号，不伪造原稿虚构读者身份。
- 不导入虚构票数、统计结果或账号；当前导入包为文本数据，不包含图片上传。

导入工具保存账号范围内的 ID 和幂等键以便恢复，并使用浏览器锁避免同账号多个标签页同时执行。审核前核对源内容和快照，等待 Content 确认应用后才称发布成功。恢复时若草稿被修改会停止，不覆盖已有编辑。保留导入日志所在的浏览器资料，不将清空本地日志后的重复执行视为全局去重保证。

**工具已编写并通过隔离协议测试，但尚未实际导入数据库。** 用户已要求在 502 期间先完成其他前端功能。

## 组织与验收

`pages/` 按页面拆分；`components/` 管理共用 UI；`state/` 用 Context 和独立 hooks 管理会话、投稿和投票；`api.js`、`upload.js` 负责 HTTP、CSRF 与上传协议。

`tests/*.spec.js` 使用 Playwright 拦截 HTTP 响应测试请求方法、载荷、CSRF、幂等、错误恢复及界面；测试样例仅在 `tests/fixtures/`，不会进入运行时。它们不代替真实数据库／Google 登录联调。`tests/import-design.test.mjs` 检查原始数据保留、发布门槛、重试不重复、权限拒绝及已修改草稿保护。

`node scripts/check-preview.mjs` 是只读真实接口检查与截图，不拦截网络；接口故障时记录失败并退出非零。`node scripts/review-motion.mjs` 使用隔离测试响应检查动效，不能称为真实业务验收。设计文字与响应布局截图由 `tests/design-copy.spec.js` 产生。

导航悬停原位放大 4%，主按钮上浮 2px；页面链接淡出 160ms、入场 360ms，保留历史和离页确认；减少动态效果设置关闭动效。所有样式保留统一顶部栏与原英文界面。



2026-10-29 验证：26 项浏览器协议／交互测试、4 项导入协议测试、动效脚本和生产构建均退出 0；桌面和手机截图已查看。生产包不含模拟身份、导入界面和原稿数据，开发私有路径实测返回 403。真实 API 检查退出 1：health、tasks、board 均为 502，故真人登录、数据库写入及完整业务联调仍待服务恢复后验证。证据：`artifacts/live-api/checks.json`、`artifacts/current-preview/`、`artifacts/motion/`。

## Board / Login 接入（2026-10-30）

- `/board`（兼容 `/board.html`）：按同步后的 Board 原稿实现 The Gap、By Kind、Most Discussed 及 SVG 滚动线条。`pages/Board.jsx` 组合 `components/board/`，`hooks/useBoardMotion.js` 管理自适应曲线、动效与滚动位置，页面 CSS 限定在 `.board-site` 内。
- 平台和分类汇总只调用 `GET /api/board`；热榜只调用 `GET /api/tasks?order=hot&category=...&limit=...&cursor=...`。展示任务数量，不把热度或结论数显示成得票率。分类仅筛选热榜，读取和分页不访问单题 statistics 或 ballot，不改变投票资格。失败保留已加载列表并可重试。API 没有列表总数，显示已加载数量；初始返回列表最多恢复 100 件，后续继续按 3 件分页。
- 任务详情只接受 Board / Discovery 的同站列表返回路径，拒绝外站、协议相对地址和管理/API 路径；返回保留分类与已展示数量。原稿的本地示例提示替换为真实 API 数据说明，普通运行没有示例数据兜底。
- `/login`（兼容 `/login.html`）沿用原稿英文卡片和访客入口；`state/useLoginStore.js` 管理授权开启、取消、退出与结果未确认状态；共用 SessionProvider 和 API 层。`/?login=cancelled` 显示取消提示，不接收客户端账号或角色参数。登录请求用顶层链接 `/api/auth/google`，不 fetch Google，不在浏览器提交 OAuth 回调或自行创建身份。
- Google 主按钮在匿名、加载和错误状态下始终展示原稿外观；本地 HTTP、加载中或接口失败时禁用，并关联状态说明，避免本地预览缺少主操作。同源 HTTPS 且会话查询正常时可点击；已有 `npm run dev:auth` 可提供 HTTPS 开发入口。浏览器契约测试用隔离 HTTPS 页面验证授权顶层导航；这不能代替真人 Google 授权，当前没有重新进行真人授权或线上写入。

`tests/board-login.spec.js` 覆盖实际 HTTP 方法、热榜分页与失败重试、汇总隔离、零数据、详情安全返回、取消回调、身份校验、CSRF 退出、退出未确认及重查、401、403、404、502、HTTPS 授权导航，以及 1440 / 390 / 320px 响应布局。`node scripts/review-board-login.mjs` 在临时只读原稿服务器与 React 页面分别截图，使用相同 API 测试数据，输出 `artifacts/board-login-comparison/index.html` 和几何记录；已人工比对页面构图。Login 卡片沿用原稿样式，主按钮始终可见并按环境启用；Board 去掉本地原型提示条，内容据真实接口动态变化，共享导航保持首页规格。

`node scripts/check-board-login-live.mjs` 不拦截网络，经 Vite 公网代理实测 health、board、热榜为 200，匿名 me 为 401，当前公开任务数为 0；读取没有写入或单题统计请求。证据保存在 `artifacts/board-login-live/`。真实数据中的新增任务、真人账号退出及线上发布仍需对应环境验收。

PR 隔离验证使用 FRONTEND_TEST_ORIGIN 指定临时服务（默认仍为 18100）。Login 设计源尚未合入 dev，对照所需的原始 HTML/CSS/JS 逐字保存于 tests/fixtures/login-reference/，不进入生产 bundle；原稿提交号在该目录 README 中记录。

2026-10-30 PR 隔离验证：基于最新 origin/dev，36 项浏览器测试、4 项导入测试、生产构建、原稿对照截图与真实 API 只读检查均退出 0。Windows 本地为 Node 26.5.1，npm 安装提示项目要求 Node 24；GitHub CI 的 Node 24 检查仍以远端运行结果为准。未重新进行真人 Google 授权或线上写入。

Submit 就绪规则与 Content 的 `validate`、`bundleRequired` 和 `workBodyRequired` 对齐：新任务双方至少各一件，本次送审的每件作品都需完整；追加作品单件送审。章节与封面分别显示已添加数、完整数；缺失项在作品登记、编辑器和送审错误中定位。云端权限允许与拒绝均有效，但新作品须明确选择。服务端仍负责文件就绪、归属和其他契约校验；前端就绪提示不代表服务端已接受。
