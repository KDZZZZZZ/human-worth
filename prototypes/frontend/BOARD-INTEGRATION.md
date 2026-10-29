# Board 集成记录

## 来源与范围

- 源码来自用户上传的 `human-worth-board-source.zip`，集成基线为 dev / frontend/feat-board-integration 的 `d4f0fde651e79b2ec09b876b717aea28e0db2d14`。
- 新增 Board 页面、样式、脚本及共享导航样式。Home、Discovery、Detail 接入真实 Board 链接；Detail 仅改写返回链接，允许同源同目录 Discovery / Board，保留全局 Discovery 导航。
- 上传版 Submit 是不同的页面结构，且包内没有其配套 submit.js / submit.css，因此没有替换。仓库 Submit 只增加 Board 导航链接；其脚本、样式、表单、存储与模拟提交行为保持不变。
- prototype-data.js 与基线相同，不重复提交。未带入其他页面的全局字体、内容按钮或 Detail 字重调整；Board 字体仅在 Board 页面设置。

## 行为边界

看板使用已有任务示例与本地结论、讨论顺序；不是实时统计或后端排名。分类仅过滤 Most Discussed，分页每次增加三条；URL 保留分类与显示数量，sessionStorage 保存滚动位置。错误、空、加载中、缺失及零值状态可用 previewState 参数查看，moreError 可模拟一次分页失败。

本次仅交付 prototypes/frontend 静态原型。未修改 Node 正式入口、API 或部署配置，合入代码不等于 Board 已作为正式页面发布。

## 本轮验证（2026-10-29）

- Node 24.19.0：node --test tests/*.test.mjs，7/7 通过；其中新增两项覆盖返回 URL 白名单、全局导航保持不变、统计一致性、分类及分页参数边界。
- board.js / task-detail.js 语法检查、文档快照与链接检查、PR 方向检查、npm run build:docs 通过。npm ci --ignore-scripts 成功。
- 本地 Chrome 真实 HTTP：Board 分页、分类、Detail 返回、刷新、浏览器前后退、分页错误重试、各预览状态通过。
- 1280、768、390 像素宽度：五页导航可用；移动菜单可展开；无页面级横向溢出，分类表格在容器内滚动。检查了桌面与手机截图。
- Submit：示例作品、预览、本地保存、刷新恢复、失败后输入保留、重试送审模拟和编辑锁定通过；浏览器未报告 JavaScript 页面错误。
- Windows 无 fcntl，部署 Python 测试不能完整执行；Go / PostgreSQL 集成检查未在本机运行。完整 Linux CI 与合并状态以 GitHub PR 的实际结果为准，本记录不预先宣称通过或部署完成。
