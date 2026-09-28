# 2026-09-28：交接接续与开发记忆建立

[返回入口](../../../DEVELOPMENT_HISTORY.md) · [维护规则](../maintenance.md) · [来源索引](../sources/handoff-2026-09-28.json)

> 状态：完成（文档、同输入验证、补丁重建和副本回滚已完成）。日期：2026-09-28 UTC。
> 本轮是文档和接续机制开发；业务结论来自静态核对，生产结论仅来自历史记录。

## 背景

用户上传中文交接和 evidence JSON，希望继承开发思想，先全面理解项目，再建立每轮背景、目标、实际完成、注意事项记录，支持渐进式披露和 agent 主动更新。

此前工作涉及 BPS 协议、工具源码、图片精度/容量、长流、账号配置和暂停、严格路由；材料混合旧环境、历史观察和验证缺口，直接复制容易把历史当现在。

## 目标与验收

完整读取两份交接，核对本地源码、Git 和版本文档；建立根入口、架构、历史、维护规则、模板、会话与来源索引，逐字保留原件。Git 能收录新文档；同输入检查 BASELINE / MODIFIED / ROLLBACK，补丁能重建，回滚恢复原哈希，四角色逐一重开。

## 起点

- ACTIVE_OBJECT / TARGET：`/bps/bps2api`；修改前 `git status --short` 无输出。
- 分支 `main`，HEAD `2f54db96e19ca06d54d73c990ccd1d750474ade8`，VERSION `0.0.13`。
- GOAL：新增长期记忆和主动更新规则；调整 README 导航、`.gitignore` 收录分支。
- INPUT_PATHS：两份上传逐字副本在 `docs-local/development-history/sources/`，哈希见来源索引。
- 修改前字节在 `/bps/artifacts/development-history-20260928/baseline/`；`baseline-files.json` 记录存在/缺失状态，`baseline-tracked-hashes.json` 记录全部 4606 个跟踪文件。
- 原交接 `c5a0323b6` 与本机合并 HEAD 源码树相同；历史四角色本机缺失，只有上传索引。

## 实际完成

| 文件/目录 | 完成内容 |
| --- | --- |
| `DEVELOPMENT_HISTORY.md` | 当前状态、关键约束、分层阅读、待办与最近记录 |
| `AGENTS.md` | 后续 agent 必读链、主动更新触发点、证据规则 |
| `docs/development/architecture.md` | 路由/字段、暂停、图片、工具、SSE 与验证入口 |
| `docs/development/history-through-v0.0.13.md` | 13 版本脉络、设计取舍、历史结果与纠正 |
| `maintenance.md`、`TEMPLATE.md` | 分层维护、最小记录、证据等级、会话模板 |
| 本条目与来源 JSON | 本次开发事实、可解析来源、旧原件缺失状态 |
| `README.md` | 开发历程导航 |
| `.gitignore` | 精确放行根 AGENTS 与 docs/development；原始材料仍忽略 |

业务源码、应用版本、依赖和部署配置没有本轮修改。没有提交、推送、发布、访问生产或改数据库。

## 决策与注意事项

- 入口简短，专题承载细节，原证据按需读；两名 agent 独立核对源码和历史，再写相应专题。
- 实时更新指工作过程的增量记录，不宣称后台自动监听。
- 原件留在被忽略的本机目录，入库摘要不复制私人地址、账号 ID 或凭据。
- 明确保留 v0.0.8 未部署、v0.0.13 替代 native fallback、Messages 无历史线上 canary、Office 仅合成 echo 等边界。
- 本轮文档四角色不能替代或冒充缺失的历史源码事务。

## 验证

本轮已实际运行同一检查命令；固定输入是两份逐字上传副本，固定目标路径通过符号链接依次指向基线、补丁生成的候选、独立回滚副本。新增文档在基线不存在，检查失败是预期结果。

命令（工作目录为项目根）：

```sh
python3 /bps/artifacts/development-history-20260928/check_history.py --root /bps/artifacts/development-history-20260928/work/subject --sources /bps/bps2api/docs-local/development-history/sources
```

| 状态/检查 | 退出码 | 已观察结果 |
| --- | --- | --- |
| BASELINE | 1 | status=missing，缺少本次新增的 8 份文档 |
| APPLY_PATCH | 0 | 用 DIFF_FILE 在 pristine 副本重建 10 个变更文件，逐文件哈希等于候选 |
| MODIFIED | 0 | 8 份文档、38 个相对链接、2 份来源、Git 收录检查通过 |
| 执行 ROLLBACK.sh | 0 | stdout 为 Restored baseline source bytes |
| ROLLBACK | 1 | 输出与 BASELINE 完全相同，恢复原本没有新增文档的状态；不是回滚脚本失败 |
| 哈希恢复 | 0 | 全部 4606 个基线跟踪文件哈希一致，8 个新增文件已删除 |
| 重施 GOAL / 重查 | 0 / 0 | 候选哈希恢复，文档检查再次通过 |
| 实际工作区 / diff 检查 | 0 / 0 | 工作区保留目标文档；git diff --check 无输出 |
| 原件/四角色 | 0 | 两上传哈希与大小一致；归档、补丁、UTF-8 账本和可执行回滚逐一重新打开 |

MODIFIED 的字面 stdout（stderr 为空）：

```json
{"documents": 8, "errors": [], "git_tracking": "ready", "local_links": 38, "sources": 2, "status": "pass"}
```

完整 BASELINE/MODIFIED/ROLLBACK 的命令、输入、stdout、stderr 和退出码见固定 VERIFICATION.txt；相同目录的 transaction-events.json、baseline-files.json、baseline-tracked-hashes.json、modified-hashes.json 与 roles-sha256.json 补充机器可读索引。initial 阶段验证后回填本条目，final 阶段对最终文档重新执行相同事务，保留两阶段事件。

本次共 10 个文件变更，其中原有 README/.gitignore 各新增 5 行，其余 8 个为新增文档。4604 个无关跟踪文件字节未变，HEAD/分支未变。此处三态行为是**文档存在与可读性**，不是历史 BPS 严格路由测试。

本机 `command -v go` 和 `command -v pnpm` 退出 1；Python 3、Git、Node、Docker 可定位。未拉取工具链、未重跑 Go/前端业务测试，文档验证不代表业务兼容或生产健康通过。

## 四角色与来源

本次文档事务固定路径，同任务后续检查复用并追加：

1. MODIFIED_FILE：`/bps/artifacts/development-history-20260928/MODIFIED_FILE.tar.gz`
2. DIFF_FILE：`/bps/artifacts/development-history-20260928/DIFF_FILE.patch`
3. VERIFICATION.txt：`/bps/artifacts/development-history-20260928/VERIFICATION.txt`
4. ROLLBACK.sh：`/bps/artifacts/development-history-20260928/ROLLBACK.sh`

回滚接受目标副本目录；恢复原 README/.gitignore，删除本事务新增文件。前提为 POSIX shell 与 Python 3，基线字节内嵌，先在独立副本实际运行。它不回滚历史业务源码或生产状态。

## 当前结果与下一步

LAST_CONFIRMED_RESULT：交接、源码和历史已核对；分层记录与主动维护机制已落地，文档/来源/补丁/回滚验证通过。
NEXT_EXECUTABLE_ACTION：无；本轮已完成。下轮按新任务复用入口和相关会话，不重放旧发布动作。
ACCEPTANCE_EVENT：已观察补丁重建一致、三态输出可复核、恢复字节等于基线、四角色重开且工作区保留目标文档。

## 增量更新与纠正

- 首次在 `/bps` 执行 git status 退出 128；已固定源码 TARGET 为 `/bps/bps2api`。
- `python` 退出 127，改用已安装的 `python3`。
- 一次补丁因同路径 delete/add 被拒绝，改为单操作；一次辅助 Python 因换行转义解析退出 1，程序体未运行；一次 JavaScript 字符串解析错误未启动命令。更正后写入成功，错误保留在本机 `context.json` 和最终账本。
- 架构子任务另有一次工具传输解析失败和一次链接检查 regex 退出 1，均已修正并读回成功；失败不记为应用缺陷。

- 独立文档审查发现模板复制到 entries/ 后返回链接层级变化；已在模板明确改为 ../../../DEVELOPMENT_HISTORY.md。其余架构、13 版本事实、来源与隐私检查无阻塞。
