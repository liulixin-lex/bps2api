# 2026-09-29：九项择优合入与 BPS 瞬时故障恢复

[返回开发历程](../../../DEVELOPMENT_HISTORY.md)

## 对象与授权

用户批准按前轮方案整合上游 #186、#192、#193、#197、#187、#198、#189、#194、#196，并修复 429 / 502 / 503 等请求提前停止的问题。并行实现后已汇入本地 integrate/selected-20260929，应用提交为 345130adb；本条记录的后续提交只补充开发文档。

候选从 origin/main 已发布源码 444be79071e0789cb83870e5e5b41d14547a23f1 创建，工作树 /bps/worktrees/selected-integration-20260929。原 /bps/bps2api 工作树及其用户文档保持原字节。此次操作是本地源码集成与验证，未推送、升级版本、切换生产或更新生产账号数据；二进制版本仍为 0.0.16。

整合范围固定于前轮评审的上游 7b9fb3332f3b84110b88d81f517132a047f808e3；参考检索发现的新提交不自动扩大本次范围。以下为按本仓库约束适配的能力，不代表原样合并九个上游 PR 的全部实现。

## 九项适配结果

| 项目 | 已完成的本地适配与边界 |
| --- | --- |
| #186、#193、#197 | 质量规则共存、探测计划和 BPS 观察模式；状态操作持有行锁并先恢复既有动作所有权。BPS 观察不改变账号状态/分组/冷却，parallel_count 固定为 1；原生测试保留 1–8。默认 30 分钟，保留自定义 cron，403 自动暂停和 cache_creation 默认关闭。 |
| #187、#198 | 可选优先级调度和显式估算成本；按实际路由、请求模型及映射模型隔离统计。缺失样本/成本显示未知，显式成本 0 保持零，不改用户计费。质量证据必须来自完成的真实 BPS 探测且账号/模型/端点匹配。 |
| #189 | 只适配被动分组卡片和 18 段真实时间窗，保留详细统计。has_samples 经隐私遮蔽仍保留；缺流量为未知，不新增主动 candy 探测请求。 |
| #192 | 旧版凭证守护纳入可恢复 error 账号，排除 disabled / shadow / 非目标类型，保存失败连续计数，分离探测/修复预算；不引入凭证运维 v2 或扩大密码/TOTP 持久化。 |
| #194、#196 | 可选的新 OAuth 初始并发模板和自动并发提升，均默认关闭。行锁下重查暂停、配额、冷却和人工更新时间；只接受已完成的真实生成，HTTP 200 空壳、排队中、失败/取消流均不提升。 |

详细设计与专项证据见 [质量规则](2026-09-29-selected-quality-rules.md)、[调度与自动配置](2026-09-29-selected-scheduling-auto-config.md) 和 [BPS 恢复](2026-09-29-bps-provider-retry.md)。

## 429 / 502 / 503 等停止问题

- excelBPSClassifyProviderFailure 识别 HTTP 408/429/500/502/503/504，以及 HTTP 200 内已知 SSE error / response.failed 的瞬时故障；401/403、永久额度与策略拒绝不按瞬时错误重试。
- forwardExcelBPS 将同账号重试、同模型 BPS 换账号及传输/工具修复纳入同一发送次数和时间预算。存在已确认可用的同模型 BPS 账号时为切换保留最后一次机会，实际每次发送都计算 RPM。
- Retry-After 采用重复头、秒/小数、HTTP 日期与已知毫秒/秒字段中的较长等待要求；预算不足则终止，不缩短上游要求或无限重试。等待前关闭旧响应并释放 lease，取消立即停止。
- HTTP 输出判断改为 openAIStreamClientOutputStarted(c, IsResponseCommitted(c))。只发出 keepalive 注释不再阻断恢复；已经交付文本、工具调用或终态后不重放。正常语义输出开始后解除恢复短时限，调用方取消及总时限仍生效。
- 保留已有密文修复结果和工具语义；BPS 暂停不回退 native，显式模型路由、detail=original、function/custom/namespace 源码语义与 hosted 自动兼容规则不变。

追加纠正了 isOpsTerminalSSEFrame 对顶层裸 error 的判定：原生 Chat 流中的错误加 [DONE] 不再被当成成功生成而提升并发；正常文本含 error 字样及 error:null 不会被误伤。

## 最终整合检查

证据根目录：/bps/artifacts/selected-integration-20260929/。所有结果均来自命令执行，失败记录保留。

| 检查 | 已观察结果 |
| --- | --- |
| 前端完整测试 | 382 个文件、2,999 项通过。 |
| 最后 UI 增量与构建 | BPS 单次观察及 i18n 共 55 项通过；vue-tsc -b 与 Vite 生产构建均 exit 0。 |
| 后端九个受影响核心包 | 最终 service 8,409 个顶层测试通过、4 个原有跳过；其余八包前轮 2,370 个顶层通过、2 个原有跳过。合计 10,779 个顶层通过，6 个跳过；不是全仓所有 CI/lint 的宣称。 |
| 数据库专项 | 独立 PostgreSQL/Redis 环境的质量所有权及观察边界 13 项、调度证据隔离 4 项通过；未连接生产数据。 |
| 追加刷新截止竞态 | 原始代码的确定性输入 2 失败/1 通过；修复后 12 组各重复 20 次，共 240 次通过。 |
| 最终可执行产物 | 带前端静态资源的 Go embed 构建 exit 0；-version 实测为 Sub2API 0.0.16 / commit 345130adb。未执行服务启动或部署。 |

第一次核心后端检查确有 1 项失败：TestTokenRefreshService_LateSuccessPastAttemptDeadlineIsRejected。两个相关生产文件和原始基线字节一致；独立重复 30 次通过不能否定竞态。追加 oauthRefreshContextError 对绝对截止时间的检查，防止取消回调延迟时持久化过晚凭据，并保护父取消、已提交 CAS 与缓存清理语义。原失败测试未放宽，最终 service 整包通过。详见 [刷新截止记录](2026-09-29-token-refresh-deadline.md)。

早期测试 mock、原子 upsert 仓储桩与内存不足等执行失败均保留原始日志；后续通过只对应修正后的实际命令。上述本地回归不替代生产压力、真实上游权限及所有 CI 的验收。

## 同输入事务与四角色

固定回归输入由 guard、quality、BPS recovery 三份文件组成，共 16 个顶层测试。原始源码基线已实际执行：1 项通过、15 项失败，exit 1。候选和实际回滚副本使用同样输入；完整原始输出、退出码、全树清单哈希、补丁重建以及重新应用改动的结果统一写入固定 VERIFICATION.txt 和 transaction/triad-summary.json，避免把预期结果当成实测。

沿用四角色绝对路径；前轮版本在替换前存入同目录 history/：

- /bps/artifacts/development-history-20260928/MODIFIED_FILE.tar.gz
- /bps/artifacts/development-history-20260928/DIFF_FILE.patch
- /bps/artifacts/development-history-20260928/VERIFICATION.txt
- /bps/artifacts/development-history-20260928/ROLLBACK.sh

MODIFIED_FILE 为已提交源码快照，DIFF_FILE 是从 444be7907 重建该快照的完整二进制兼容补丁；可执行回滚脚本接受提取后的目标副本，以同目录 BASELINE_SOURCE.tar.gz 恢复原字节。回滚返回原有失败行为是预期验证结果，不表示修复后仍失败。原始工作树不作为回滚目标。

## 参考检索

用户列出的 sub2api-magic、MGE_Sub2API、ranxi2001/sub2api、sub4api、gpt-load、new-api、CLIProxyAPI 均已读取固定提交的相关源码，提交、许可证、所读文件和 SHA256 保存于 references/index.json，取舍说明见 REFERENCES.zh-CN.md。

重点采用首段流错误缓冲、错误分类、Retry-After、取消、同账号/跨账号共享预算的设计经验，未整仓复制其他项目的调度或鉴权。截图里的 star 数和项目描述未作为代码可靠性依据。
