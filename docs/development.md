# 分支、PR 与合并规则

治理来源：用户在 2026-09-17 明确要求公开仓库、`dev` 为日常基线、`dev` / `main` 仅经 PR 合入、无同行评审门槛、`dev` 合入前 CI、合入后部署本机。下列具体合并方式和控制器方案为 agent 在授权范围内选定的实现。

仓库：<https://github.com/KDZZZZZZ/human-worth>。默认分支为 `dev`。

| 分支 / 流向 | 用途 | 合并方式 | 门槛 |
| --- | --- | --- | --- |
| `codex/<type>-<topic>` → `dev` | 一条任务分支对应一个 PR；type 可用 feat、fix、docs、chore、refactor、test | 仅 squash | 最新目标基线上的 `CI` 成功；通过 PR；审批人数为 0 |
| 本仓库 `dev` → `main` | 发布已集成的版本；不在 main 直接开发 | 仅 merge commit | `CI` 成功；通过 PR；审批人数为 0 |

两条长驻分支均禁止直接 push、force push 和删除，无管理员或机器人绕过名单。GitHub ruleset 约束 PR、检查来源、合并方式和禁止操作；CI 检查 PR 流向。CI 的 GitHub Actions App ID 固定为 `15368`，不能用其他身份提交的同名状态替代。

`dev` 要求 PR 相对最新目标分支通过检查。`main` 的 CI 检查发布 PR 合并结果，不启用严格“源分支包含目标最新提交”的设置：main 独有的发布 merge commit 不应反向污染 dev。发布分支不接受独立补丁；多个发布 PR 串行处理，每次合并前重新确认目标和检查。

## 日常流程

1. 检查工作区与远端；保留无关用户修改。必要时使用 worktree 隔离。
2. `git fetch origin`，从最新 `origin/dev` 创建任务分支。
3. 遵循可信场景；设计缺口先主动查找成熟参考，记录采用理由、差异和验收方式。
4. 运行 `npm ci --ignore-scripts`、`npm run ci`，推送任务分支并创建面向 `dev` 的 PR。
5. PR 正文区分 Human Design / Agent Self-Claimed / Validation；通过 CI 即可由有合并权限的作者自行合并，无须寻找同行审批。
6. 合并前确认 PR 不是 draft，head SHA 与验证对象一致，GitHub 允许合并；使用 `gh pr merge --squash --match-head-commit <sha>`。禁止 `--admin` 绕过。
7. 合入后检查 push CI、本机部署日志和公网 `/api/health` 的 revision。合并成功不等于部署成功。
8. 已合并的任务分支删除；`dev` 和 `main` 永久保留。

同步任务分支时，可以把 `origin/dev` merge 进任务分支；个人独占的任务分支也可 rebase 后 `--force-with-lease` 更新。不要改写他人协作分支历史，绝不 force push 长驻分支。共享分支优先 merge 同步。

修复、回滚和紧急补丁同样从 dev 建分支，经 CI 与 PR 进入 dev；需要进入 main 时再发 dev → main PR。回滚用 `git revert` 产生新提交，不重置长驻分支。cherry-pick 仅用于任务分支恢复已确认需要的提交，仍须重新测试和走 PR；不直接 cherry-pick 到 dev / main。

## 初始化记录与后续权限

空仓库无法通过 PR 产生首个共同祖先。仅初始化时把已有文档提交为根提交 `839a850`，建立 main / dev，随后启用保护。后续基础设施变更也通过任务 PR 进入 dev，再以发布 PR 同步 main；初始化不是日常直接推送例外。

本次用户已授权创建仓库、分支保护、CI、公网入口与本机自动部署及其必要验证。后续在本仓库明确要求的日常开发，按本流程完成分支、CI、PR、合并 dev 和部署验证；不得为同一授权重复询问。main 发布是否包含在后续任务内按当时需求判断。

GitHub 配置定义见 [ops/github](../ops/github)。默认分支自动删除开关开启，受保护的 dev / main 不随 PR 删除。
