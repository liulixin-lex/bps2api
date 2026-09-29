# 2026-09-29：线上错误复查与分层修复

## 对象与证据

用户要求再次检查本机 bps2api 线上和仓库、联网分析并落地。本轮绑定生产 sub2api-v019 / 8096（应用 3f79c13e0 / 0.0.19）及其已确认源码 /bps/worktrees/recovery-hardening-20260929，HEAD 17db87971；原始 /bps/bps2api 和该已发布工作树保持原字节，在 /bps/worktrees/runtime-errors-20260929、fix/runtime-errors-20260929 上修改。证据根 /bps/artifacts/runtime-audit-20260929，沿用既有四角色。

## 当前线上错误分类

固定 UTC 窗口 2026-09-29 14:31:35–16:35:27，共 194 个独立 Ops request_id：29 个已恢复、165 个非恢复错误；其中 185 个匹配 v019，8 个匹配旧 v018 排空请求，1 个 alpha 请求晚于首次日志采样。不能将所有事件都叫 v019 错误。成功用量 2,317 行与错误事件是不同数据集，不能相除得到错误率。HTTP 200 也不能单独证明 SSE 成功。

| 类别 | 事件数 | 分析与处理 |
| --- | ---: | --- |
| 上游 TPM 429 | 42 | astra 35 / sol 7；保持有界等待、Retry-After 与最终真实错误，不增加虚构配额 |
| 本地账号 RPM 429 | 36 | 全部误记 upstream；修正为 routing / gateway / business_limited，响应仍 429 |
| 无可调度账号 503 | 33 | 与 account_select_failed 一致，16 次全池 not_schedulable、17 次无可用账号；不强行解暂停 |
| 已恢复事件 | 29 | 24 配额、4 传输、1 工具纠错；修复审计脚本遗漏冒号前缀的问题 |
| 流提前结束 502 | 19 | 复现前输出重试先释放 lease 导致失败出口不记健康；修复报告顺序 |
| alpha search 不支持 400 | 11 | BPS 通道不支持独立该端点；保留明确能力错误 |
| 流超时 504 | 7 | 内部恢复预算可触发 context canceled，不能据此误判客户端断连 |
| 无健康代理 503 | 4 | 保留真实容量边界，修复坏出口健康反馈可降低重复选择 |
| 工具 envelope 格式错误 502 | 4 | 无证据支持放宽原文/工具身份校验 |
| BPS 403 | 4 | 2 暂停、2 拒绝；上游权限/策略仍需合法恢复 |
| 其他各 1 | 5 | 无效 JSON、provider 503、request timeout、传输恢复耗尽、纠错批次变更；timeout Ops 502→504 已复现 |

## 数据库连接耗尽与落地操作

本轮读到 PostgreSQL max_connections=100，16:36:20 UTC 总连接 93，16 个应用实例共有 84 个空闲连接，其中 15 个旧实例占 78。运行日志实际出现 pq: sorry, too many clients already，影响订阅、订单过期和 warm-account 等任务。旧实例保留图片所有权不意味着必须永久运行完整后台任务。

16:45:14 UTC 已完成配置副本 BASELINE/MODIFIED/ROLLBACK、Caddy/Compose 校验与哈希恢复；再次核对旧实例无 8080 活动连接、40 分钟无推理、共享 relay 图片文件为零后，仅保留 8096 图片路由，停止旧 15 实例。容器、镜像和基线配置均保留；旧服务加 retired profile，避免默认 compose up 拉起。新的主实例配置池预算为 max_open=40 / max_idle=8 / idle=1min，旧退役服务配置 5/1；当前 v019 进程不重启，候选上线加载新预算。

16:52:01 UTC 重读 PG 总连接 15、应用连接 6；20 次主实例健康检查全部通过。16:45:14–16:54:48 UTC 主实例未再见 too many clients already，重启数 0。此为观察窗口，不能写成永久零错误。运营配置回滚证据在 operations/，不拿源码回滚证明生产配置已回滚。

## 联网依据与决策

web 工具实际返回 basispoints_endpoint_unsupported，已保留错误；随后直接联网获取以下官方正文，均为 HTTP 200（2026-09-29 16:36 UTC），不是只读取搜索摘要。原始 HTML、提取文本和元数据保存在 references/。

- [OpenAI rate limits](https://developers.openai.com/api/docs/guides/rate-limits)：有界指数退避和随机错峰；失败重发也会消耗额度。用于原则核对，不把公开 API 的账户限额套用到非公开 BPS 端点。
- [OpenAI error codes](https://developers.openai.com/api/docs/guides/error-codes)：分别处理配额、认证权限、服务过载；不能靠代理重试恢复被拒权限。
- [Go database connections](https://go.dev/doc/database/manage-connections)：显式约束 open/idle pool，闲置超时释放连接；限制会使操作排队，不能无界增大池。
- [PostgreSQL connections](https://www.postgresql.org/docs/current/runtime-config-connection.html)：连接受全局 max_connections 与保留槽控制；本次按全实例预算缩减池而非重启数据库抬高上限。
- [Caddy reverse_proxy](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)：配置 reload 的流关闭行为独立于上游业务错误；保持已有 stream_close_delay，并用跨切换 SSE 验证。

## 最小源码修复与验证

1. forwardExcelBPSAttemptWithAcquire：前输出可恢复流失败在 releaseLease 前 ReportStreamFailure；排除本地 idle/first-output/deadline、provider 终态与取消。基线 six EOF/HTTP2 场景均 3 次选择坏出口、0 次健康反馈、最终 502，预算耗尽也漏反馈；同输入修改版专项 exit 0。
2. openAIRPMAdmission.retryAfter：仅 ErrOpenAIRPMExhausted 设置 markOpsRoutingCapacityLimited；基线 4 个场景错误归 upstream，修改后 routing，实际上游 429 不被掩盖。首次负控断言拼写错误及无宿主 go 的工具失败保留，随后用缓存 golang:1.27.0 离线容器重跑。Handler 第一轮完整 unit 1,727 节点通过、1 跳过。
3. inferResponsesFailedOpsErrorType / inferStreamFailureStatus：basispoints_request_timeout 与 stream_timeout 同归 504；真实 SSE fixture 基线 expected504/actual502、exit1。
4. deploy/audit-request-health.py：恢复事件匹配改为 Recovered upstream error%，兼容冒号与旧空格形式；PostgreSQL 只读 VALUES 同输入基线 exit1、修改 exit0，终端 429 负控不变。

当前完整同输入三态、最终回归、候选构建与灰度验收进行中。源码测试不代表线上所有 429/403/503 已消除。未提高账号 RPM/TPM、未改模型、未解除暂停、未启用隐式原生回退、未发布或移动版本标签。
