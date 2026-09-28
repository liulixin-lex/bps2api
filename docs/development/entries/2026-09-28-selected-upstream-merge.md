# 2026-09-28：按明确范围适配上游修复

[返回开发历程](../../../DEVELOPMENT_HISTORY.md) · [前置审计](2026-09-28-post-v016-audit.md)

> 状态：限定范围适配、代码/构建与三态回滚验证完成；交付本地分支 `feat/selected-upstream-v016`。未推送、发版或部署。
> 日期使用 UTC；本轮测试、失败和修正保存在仓库外原始证据中。

## 背景与目标

用户从前置审计中明确选择协议、质量规则、WS/取消、配额四组内容，要求兼容性合并，避免改变原本正常的 BPS 请求，并允许并行开发。此轮不把整个上游分支合入，不引入新余额查询、403 后台恢复、定价、2FA 持久化、新 hosted 开关或版本升级。此前发现的两条 BPS 重试风险不是本轮获选范围，不把它们写成已修复。

## 起点与目标对象

- 原始 TARGET：`/bps/worktrees/configurable-recovery-20260928`，HEAD `1d0000848f319ff1491402be8fbe50638575c05b`；原始工作树保持不改。
- 候选：`/bps/worktrees/selected-upstream-20260928`，分支 `feat/selected-upstream-v016`。
- 比较上游：`ranxi2001/sub2api` 的 `production`，固定提交 `62ac3a5dd125f76b23d655c491c5bf2c5816fb5b`。
- 基线源码树：`7927417640add2e9c22057c8c1d6107f56c008f3`。归档 SHA-256：`8d2ca64ed98d50ae9ea863382573ee27822120e259c499c95d4bc0c320e6092b`。
- 生产仍为既有 `0.0.16` / `9b76a651f`；开发候选与生产分别验收。

## 适配内容与取舍

| 范围 | 上游提交 | 本地适配与兼容保护 |
| --- | --- | --- |
| 工具参数保留 | `53cc2e05c` | 保留 tool-use 起始事件内联 input；后续参数 delta 优先，工具之间不得串值；保持 custom/namespace 恢复链 |
| 空终帧正文恢复 | `7ee00e189` | terminal output 缺失正文时使用已收集文本补齐；已有终帧内容优先，避免重复或合成空消息 |
| GPT-6 推理识别 | `1f639dc6d` | 识别 GPT 代际；保留本地 gpt6sol/luna 的 reasoning_effort=none 与采样参数语义，不引入额外 thinking.disabled 改动 |
| 质量规则筛选与探针资格 | `8cbee4474` | 批量筛选与跨页选择；跳过不支持的后台探针并释放租约；Agent Identity 不进入订阅探针；BPS 自定义模型、空范围、403 暂停和 managed recovery 保持原规则 |
| WS 上下文换窗 | `a4fc1684b`、`af52c4efc` | 使用客户端原帧 window ID 判断边界，避免规范化后的握手值覆盖新窗；换窗断旧 previous_response_id；原生及 HTTP bridge 换窗仅补当前工具输出对应的调用，不携带旧正文；同步截断 bridge 的普通/账号 failover 历史 |
| 客户端取消 499 | `419673b5f` | 取消在各转发入口记为 499；纯客户端取消按原开关过滤运维事件，已发生上游错误继续留证；保留 BPS/RPM 专用错误处理 |
| 配额暂停、用卡缓存与查询退避 | `8a365a12a`、`6913461a6`、`c6b524b12` | native 旧快照有未来 reset 时延续暂停；无卡缓存、查询失败退避与调度通知合并；保留用卡失败原幂等重试 |

### BPS 配额兼容边界

直接合入上游的未来重置暂停会让过期 Codex 快照新增阻断 BPS。四个请求级调用改用 `shouldAutoPauseOpenAIAccountByQuotaForModel`：仅实际映射到 BPS 的模型继续采用旧快照过期自愈；native、未匹配和无效 BPS 配置仍走新规则。新鲜快照、运行态 403 暂停、RPM 不豁免。

更早的 `ApplyAccountSchedulingThreshold` 会写整个账号的临时暂停，入口没有请求模型。`openAIThresholdCandidates` 对确有 BPS 路由且快照过期的账号保留原行为，防止先被账户级过滤。显式空数组、null、空白、错误类型配置不视作 BPS 路由。取舍：混合账号若仅启用全账户阈值、未启用请求级 quota auto-pause，其 native 部分也保留原有过期快照自愈；未为本次合并扩展所有路由的模型上下文。

另外修正上游查询退避在窗口刚重置时被状态清理覆盖的问题；通知冷却使用 CAS，避免同一账号的并发筛选重复通知。

## 验证与当前证据

原始证据目录：`/bps/artifacts/development-history-20260928/selected-upstream-20260928/`。各子目录记录命令数组、stdout、stderr、退出码及来源。

- BASELINE：复用前置审计的同输入协议探针，10 个顶层场景中 5 失败、5 通过，退出 1。失败对应参数丢失、空终帧正文丢失和 gpt6astra 未识别。
- 协议包独立验证：471 个顶层测试通过（611 含子测试）。
- 质量前端局部验证：3 文件 / 45 项测试及相关 ESLint 通过。
- 集中后端：小包 741、service 987、handler 134、repository 37 个顶层测试通过，合计 **1,899**；含子测试 **3,799**，无失败/跳过。覆盖 BPS 路由、工具、SSE、恢复、RPM、403/CAS、质量资格、WS/HTTP bridge、499 与配额边界。
- 前端全量：375 文件、**2,930** 项通过；vue-tsc、Vite 及后端 `-tags=embed` 构建均退出 0。
- 同一命令数组和输入：BASELINE 5 fail / 5 pass，退出 1；MODIFIED 原 10 项全部通过，另外 3 项新增工具兼容回归也通过，退出 0；ROLLBACK 重新出现基线同一 5 fail / 5 pass，退出 1。后者是成功复原旧行为，不是候选测试失败。
- `ROLLBACK.sh` 实际执行退出 0；恢复树 SHA-256 与基线均为 `55e2717c4fa559762d6592efa77453fa607ebc83d23256196a6cb7b50af04bb5`。重施 binary patch 后与候选源码树逐字节相同。
- 原始工作树保持干净；只读健康检查返回 `{"status":"ok"}`，生产容器仍从 10:34:15 UTC 运行且重启计数为 0。没有将候选测试宣称为生产流量验收。
- 第一轮小包集中命令误写 `internal/pkg/basispoints`，目录不存在，退出 1；已改为实际 `internal/service/basispoints` 重新执行，首次原始日志留存。

## 四角色与回滚

沿用固定角色，更新前保存上一轮版本并延续账本：

1. `/bps/artifacts/development-history-20260928/MODIFIED_FILE.tar.gz`
2. `/bps/artifacts/development-history-20260928/DIFF_FILE.patch`
3. `/bps/artifacts/development-history-20260928/VERIFICATION.txt`
4. `/bps/artifacts/development-history-20260928/ROLLBACK.sh`

四角色已实际生成并重新打开。完整三态验收和补丁重建见 `selected-upstream-20260928/transaction/final-transaction-summary.json`，字面命令/输出保留在对应 run 目录及固定账本。文档闭合后的归档使用独立 refresh：只允许这份条目与根历程文档变化，应用源码字节必须与已测试版本完全相同，再实际验证回滚与补丁重建；最终 HEAD 和角色哈希记录在外部闭合清单，避免文件自引提交哈希。

## WS 适配边界

只读复核发现直接应用上游换窗逻辑会丢失工具结果的关联，HTTP bridge 也会先拼入旧窗口正文。因此本地追加兼容修复：只从会话历史提取当前 output 所需的具体 tool-call，已有完整上下文原样保留；无法找到 call_id 对应上下文的无效新窗不发送到上游。真实 WS 会话 fixture 验证原生/HTTP bridge 换窗、工具恢复、完整工具、缺失调用及下一轮续链。透明 passthrough 保持既有透明转发，未声称其上游具有相同换窗保证。

独立审查逐字节核对的 25 个 BPS/basispoints/RPM 关键源码未变；通用调用处的兼容性由集中回归验证。

## 当前动作

本轮选定功能及兼容性验证完成，保留本地提交与四角色。此次任务不切换生产、不推送远端、不发布新版本；透明 WS passthrough、此前两条 BPS 重试风险等未获选内容保持原边界。后续部署应使用独立发布流程及真实请求验收。

## 执行中发现与纠正

- 归档校验第一次把 Git 默认转义的两个中文文件名当作缺失路径。已改 `ls-tree -rz` 并按 NUL 解析；4,849 路径核对一致，不是源文件遗漏。失败 run 保留。
- 三态校验第一次要求候选测试集合与基线严格相等；新增的 3 条兼容回归同样命中原正则，13 项实际全通过。已改为原 10 项必须存在且全部通过，新增项也必须通过。没有跳过失败测试或放宽行为断言。
- 一次进度统计 helper 对未带 Test 字段的事件做数字相加报 TypeError，改布尔计数后恢复；应用源码与测试进程未受影响。
