# 2026-09-29：BPS 429 重试提前结束

## 背景与证据

用户报告 v0.0.17 上线后仍有 429 并停止请求。2026-09-29 09:25:32 UTC，请求 c23b5eaa-ec33-4389-a59a-ea4728701894 在账号 49 / gpt-6-astra 上返回 429；Ops 7163 记录过一次 pre_output_provider_retry，随后切号并报告 no available accounts。不是完全没有重试。

同一时段的上游错误明确为 tokens-per-minute 限流。09:37 UTC 只读实测显示：数据库和完整缓存 sched:acc:47 均有 08:39 UTC 的 BPS 403 暂停标记，轻量 sched:meta:47 却没有。仍保留的 v0.0.5–v0.0.12 图片 owner 使用相同元数据键，旧投影不保留该标记；未捕获具体最后写入者。

## 根因与修改

excelBPSReserveAccountSwitch 原先只检查摘要候选，在第二次 SSE 429 时把最后一次发送预留给实际不可用账号。真实调度器加载完整账号后拒绝它，导致提前结束。现在对候选执行完整记录与数据库终检，复用正式模型、分组、权限、能力及 RPM 资格检查；没有真实同模型 BPS 替代账号时，当前账号保留第三次尝试。

excelBPSClassifyProviderFailure 原先漏读 SSE error.headers 内的 Retry-After / Retry-After-Ms。实测错误同时给出 1 秒 header 和 52ms 文本提示，修复后遵守较长的 1 秒。

默认总发送上限仍为 3 次、恢复预算仍为 30 秒。不会自动解除 403 暂停、启用被禁账号、切换模型、回落原生通道或重播已经产生文字/工具输出的请求。

## 已执行验证

确定性故障序列：前两次返回 SSE 429，第三次成功；摘要看似可用、完整记录或数据库已暂停。旧代码实际只发送两次并返回切号错误；补丁发送三次并完成。模型白名单、BPS scope、映射变化、通道禁用、健康替代和已有输出边界也纳入定向测试。

2026-09-29 09:47 UTC，4 个顶层 / 23 个含子测试节点全部通过。最终同输入回滚与扩大回归继续留证于 incident-429/hotfix-tests；部署验收在后续记录补充，不把本地通过等同上线。

此前 v0.0.17 的回归与公网 canary 确实运行，但候选切换用例直接向已选第二账号转发，没有覆盖混合版本摘要与完整记录不一致。本次补上该缺口。

## 原始证据与回滚

目录：/bps/artifacts/selected-integration-20260929/rollout-release/incident-429。
沿用 /bps/artifacts/development-history-20260928 下 MODIFIED_FILE.tar.gz、DIFF_FILE.patch、VERIFICATION.txt、ROLLBACK.sh 四角色；先保存上一事务，再记录本次已确认状态至新候选的可逆变更。生产热切与源码回滚分别验证。
