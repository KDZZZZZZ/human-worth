# Human Worth 前端原型 checkpoint

这是 Home、Discovery 和 Task Detail 的可运行静态原型，供设计与交互评审。所有任务、作品、评论和身份均为示例；没有真实 API 请求、上传、投票或发布。它没有替换生产入口，也没有接入部署流程。

## 本地预览

从仓库根目录执行（Python 3，无额外依赖）：

```sh
python -m http.server 8765 --bind 127.0.0.1 --directory prototypes/frontend
```

打开 http://127.0.0.1:8765/index.html；也可直接进入 discovery.html 或 task-detail.html。字体、授权文件和示例图片已随原型提供。浏览器刷新重置演示会话；Discovery 的查询、分页和返回位置在当前标签页保留。

## 本次包含

- Home：Editorial / Warm Minimalism 品牌入口、示例材料预览、Discovery 导航、移动菜单。
- Discovery：任务列表、搜索、分类、逐步加载、加载／失败／空状态、任务详情往返及位置恢复。
- Task Detail：任务说明、共同要求、Human / Agent 错位章节封面、双页阅读、Previous / Next、章节独立阅读位置和键盘操作。
- Judgment：逐篇预选、同任务单选、取消／改选、最终确认、另一章节的非阻断提醒、模拟资格／版本冲突／失败；查看统计必须确认永久关闭本任务的后续投票资格，失败不恢复资格。
- Add a work：Perspectives 旁入口、任务摘要、示例文字／本地文件选择、校验、演示登录、失败重试和返回保留输入。完成提示明确不代表真实投稿或审核成功。
- Discussion：作者／时间／正文的平铺评论，空校验、本地发表、加载／失败／空状态、cursor/limit 形状的本地分页、未登录演示。
- 视觉评审进度：任务材料暖灰纸面（边注与 Report task 在外）、21px 投稿入口、章节等大底纸左右分别偏移12px并下移12px、当前陶土底色85%不透明度。均保持可继续评审的状态。

## 如何评审

1. 从 Home 进入 Discovery，搜索或分类后进入任务，再返回检查列表位置。
2. 使用 concept-library 任务打开两章、翻页、切换章节、预选作品，再最终确认。
3. 点击顶部 Local prototype · Explore states，查看未登录、永久禁投、候选冲突及提交失败等模拟状态。切换会重置本地会话。
4. 点击 Add a work，尝试空输入、选文件、模拟登录、失败重试与返回。输入不会上传。
5. 在 Discussion 的 Prototype states 中测试空列表、加载失败、下一条评论失败和未登录；发表评论后可定位新评论。
6. 检查桌面及窄屏布局。原型无真实账户，不使用真实投票／投稿进行验收。

## 后端接入边界

基线 dev 为 `53d2d63453e2ca365e6d5b2683a895d23670a4e4`。此基线源码已注册追加作品与评论路由，不能继续把它们统一称为 planned；本原型仍没有接入这些接口，也未验收线上部署。

后续需依据当前 OpenAPI 完成身份／CSRF、任务和作品读取、候选与资格、投票／统计持久化、正式投稿字段／资产上传／审核结果、真实评论分页与错误映射。原型的示例输入不构成正式提交协议；真实身份和统计永久禁投规则必须由后端保证。

Board、Create / Edit Task、My Submissions、MCP Credentials 等入口仍是明确标记的目标页提示，不能描述为已完成页面。超长文本自动分页、复杂多媒体排版仍待后续处理；现有阅读器允许正文自然扩展。

## 文件与范围

运行入口为 index.html、discovery.html、task-detail.html；共享示例数据为 prototype-data.js，Detail 样式和交互为 task-detail.css / task-detail.js。fonts/ 含本地字体与 OFL 授权；library-context.png 是页面中明确标注的 AI 生成示意图。

仅纳入当前运行版本及必需资源；历史备份、截图、临时脚本和本机绝对路径均不纳入 checkpoint。团队既有 public/、后端、契约、CI 与部署配置保持不变。

## Checkpoint 验证（2026-10-25）

在整理后的副本上使用本地 Chrome / Playwright 实际验证，非真实后端验收：

- Home 图片／字体与示例预览；Discovery 搜索、分类、空结果、加载失败／重试；详情往返与浏览位置恢复：通过。
- Detail 两章独立位置、翻页、键盘、长文本、选择／改选／取消／最终确认、资格／冲突／移除及统计失败后禁投：通过。
- 投稿校验、示例登录、提交失败、关闭再进入；评论空／加载／失败、分页、过期异步结果及输入保留：通过。
- 三页1440/390/320px，Detail额外768/1024px：无横向溢出；HTTP预览无资源或脚本错误、无API请求。
- Node外部脚本语法检查、仓库文档链接及可信场景快照、PR方向和分支命名检查：通过。

验证复用了本地原型已有 check-reader.cjs、check-judgment.cjs、check-community-review.cjs、check-navigation-return.cjs，临时调整入口至本目录，并对打包后的三页做了HTTP浏览器检查。这些原始脚本含本机依赖路径，未作为团队可移植测试提交；上述手动评审步骤可用于重现主要流程。完整后端／数据库集成与公网部署未在本次本地运行，等待独立接入验收及远端CI。
