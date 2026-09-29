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

## 最终验收与上线（本轮实测，更新前述进度）

- 应用提交 f9b7f408e36790764cf20746e544f1151076d500，版本标识 0.0.19-runtime.20260929，已本机部署。本轮不推送、不创建正式 Release，不移动既有 v0.0.19 标签。
- 匹配输入三态：BASELINE 14 pass / 17 fail（exit 1）；MODIFIED 31 pass / 0 fail（exit 0）；ROLLBACK 14 pass / 17 fail（exit 1）。独立回滚源码哈希与模式等于基线，补丁重建和再次应用均等于修改版。
- 最终 Service+basispoints unit 17,649 通过、4 跳过；Handler unit 1,728 通过、1 跳过，合计19,377通过、5跳过、零失败。相关race160节点通过。这不是全仓CI全绿声明，原lint/integration技术债没有在本次重新验收。
- PostgreSQL VALUES审计夹具BASELINE/ROLLBACK误分冒号形式的恢复事件（exit1），MODIFIED正确识别且保留终态429（exit0）。辅助副本首次git apply受umask影响导致模式对比失败；保留记录，显式022后在新副本重跑通过。已安装审计脚本同样保留原字节并实测副本回滚。
- 灰度基础4项及function/custom/namespace工具往返3项通过。新增Messages检查得到403；旧8096与新8097同输入都返回This group does not allow /v1/messages dispatch。保留原失败并明确是既有组权限边界，不宣称该端点端到端通过。
- 17:03:55.656769 UTC启动热切、17:03:58.397292完成Caddy reload，主流量进入sub2api-runtime-20260929 / 8097。真实SSE首字节在切前，结束于切后，唯一DONE、stop和结束标记均通过。公网4项基础及3项工具往返通过；切换39次、切后40次健康采样全部成功。
- 新实例实读池预算40 open / 8 idle / 1分钟idle。17:05:17 UTC PG总连接18（两应用9连接），原来为93。旧15实例保持停止；v019保留暖备和已发链接所有权。主实例重启数0。

17:03:55.656769–17:06:13.563620 UTC短窗口，新实例完成40个推理POST，HTTP均200、无匹配最终Ops错误、无重启。样本含canary且不是受控压力对照，不能由此推断永久零错误或量化429消除率。

## 完成状态与保留边界

上游共享TPM、无权限或暂停账号、无可调度账号，以及输出后断流仍可能返回真实429/403/503/502。工具和结构化内容验证失败、独立alpha端点不支持也继续明确报错。没有提高账号额度、扩大重试次数、换模型、强制解暂停或启用隐式原生回退。

源码四角色固定在/bps/artifacts/development-history-20260928/下，绑定应用f9b7f408e，收尾文档以补充记录扩展证据，不重新部署。原始两棵源码工作树及既有文档哈希核验未变。

运行配置是独立事务：退役三态与回滚脚本在/bps/artifacts/runtime-audit-20260929/operations；热切快照在/opt/sub2api/backups/runtime-errors-20260929/transaction。在线回退命令为python3 /opt/sub2api/deploy-runtime-20260929.py rollback，会核查旧主实例健康和并发修改，保留新实例图片所有权。本次源码、配置回滚在独立副本执行，没有为验收把生产切回旧缺陷。

本轮审计、联网依据、修复、三态、回归、竞态、构建、灰度、热切、公网验收完成。无需重复部署。私密原始日志和配置仅保存在受限证据目录。

## 用户授权正式发布 v0.0.20（2026-09-29 UTC）

用户在热修复验收后明确要求现在进行推送发版。已核对远端main仍为17db87971、最新正式Release为v0.0.19、v0.0.20未占用；先前未推送/不发版描述仅对应已完成的热修复阶段。本阶段将VERSION从0.0.19改为0.0.20，应用代码相对f9b7f408e不变。

版本说明见[0.0.20](../../bps2api-0.0.20.md)。使用既有完整Release矩阵，main与注释标签原子推送，不发送Telegram通知。推送、发布成功与产物验收尚待真实结果；生产继续运行已验收的0.0.19-runtime.20260929，不将发版等同于再次部署。证据追加到既有四角色，版本事务与发布命令在/bps/artifacts/runtime-audit-20260929/release。
