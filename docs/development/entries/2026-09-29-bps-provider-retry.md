# 2026-09-29：BPS 瞬时错误与共享恢复预算

[返回开发历程](../../../DEVELOPMENT_HISTORY.md)

> 状态：分支实现和专项回归完成，等待主集成事务验收。更新时间：2026-09-29 UTC。
> 证据：本轮本地实测与静态核对；没有推送、部署或账号数据库变更。

## 背景与范围

用户要求合入选定上游改动，同时修复 429、502、503 等瞬时错误导致请求直接停止的问题。本分支基于 `444be79071e0789cb83870e5e5b41d14547a23f1`，候选位于 `/bps/worktrees/bps-retry-20260929`，分支为 `fix/bps-retry-20260929`。其他上游功能由主集成事务处理。

## 实际完成

- `excelBPSClassifyProviderFailure` 统一识别 HTTP 408/429/500/502/503/504，以及 HTTP 200 流中的已知 `error` / `response.failed` 瞬时错误。401、403、永久额度和策略拒绝不进入瞬时恢复。
- `forwardExcelBPS` 将恢复状态保存在请求的 Gin context 中；同账号重试、同模型 BPS 换账号、传输修复和工具修复共享发送次数、恢复时间与总请求时限。已知存在可用同模型 BPS 账号时，为切换保留最后一次发送机会。
- Retry-After 支持秒数、小数、HTTP 日期、重复头以及已知毫秒/秒字段；选择较长提示，带有限抖动的退避不缩短上游要求。超出预算则终止恢复，不无限等待。
- 在输出提交前处理瞬时错误，等待前关闭旧流并释放实际代理/附件 lease，每次真实上游发送重新获取 RPM slot。取消、预算耗尽以及已交付文本或客户端工具调用后均不重放。
- 恢复时限在正常语义输出开始后解除，独立的调用方取消和总请求时限仍有效；避免健康长流被短恢复窗口截断。加密内容修复后的入口请求得到保留，后续 HTTP/SSE 恢复不会重新带回已被拒绝的密文。
- 工具修复桥保留上游错误的类型信息，瞬时错误由外层输出保护统一处理；Responses、Chat、Messages 和既有 hosted 自动兼容语义保持专项回归覆盖。

## 已执行验证

证据目录：`/bps/artifacts/selected-integration-20260929/retry/`。命令数组、原始 stdout/stderr 和退出状态分别保存在各检查的 `*-command.json`、`.stdout`、`.stderr`、`*-result.json`。

| 检查 | 实测结果 |
| --- | --- |
| `final-baseline` | 退出 1；相同输入的 SSE 瞬时错误、HTTP 429、加密修复后恢复、健康长流 4 个顶层回归均失败，复现原问题 |
| `modified4` | 退出 0；service/handler 专项共 153 个顶层测试通过，包括上述 4 个回归及输出、取消、共享预算、lease、RPM 和跨协议边界 |
| `protocol` | basispoints 和 apicompat 全包测试均退出 0 |
| `finalize-hash-verification.json` | 13 个应用/测试文件与通过检查时记录的 SHA256 全部相同；基线与候选的固定回归输入字节相同 |
| `git diff --check` | 退出 0 |

固定输入为 `final-fixture.go`，SHA256 为 `b4cbdfbc431e1f2811a808f7f29d000baf4da72184b235f5cd6f8164c7ef0088`。最初的 `openai_excel_bps_provider_recovery_test.go` 证据副本是较早输入，不替代最终固定输入。保留早期失败检查；部分失败来自旧测试的一次 429 即停止假设及误用权限错误体，修正后才形成 `modified4` 的成功结果。

## 交接边界

下一动作是主分支择取此本地提交、解决质量检测等并发变更的重叠，再执行整合验收。最终同输入 MODIFIED / ROLLBACK、原始哈希恢复、补丁重建与四角色重开由主事务完成；本分支未声称已完成这些整合步骤。沿用固定角色：

- `/bps/artifacts/development-history-20260928/MODIFIED_FILE.tar.gz`
- `/bps/artifacts/development-history-20260928/DIFF_FILE.patch`
- `/bps/artifacts/development-history-20260928/VERIFICATION.txt`
- `/bps/artifacts/development-history-20260928/ROLLBACK.sh`

专项合成测试通过不等于生产压力验证。BPS 暂停不静默回退原生，真实凭据或权限问题仍需要按原有边界处理。
