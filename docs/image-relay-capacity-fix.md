# 图片中继入口容量修复与发布说明

基线：`v2.8.13` / `6b0c0ddbd1649caad5d980e92368059b1a5d1158`。

## 故障与变更

旧版本开启 `excel_bps_image_relay_enabled` 后，压缩和未知长度请求预留
64 MiB × 8 = 512 MiB，直到调度、上游流及整个请求结束才释放。总预算也为
512 MiB，因此一个长流会阻挡其他小型请求，包括纯文本。

新版本使用两套共享于网关别名的进程内预算：

1. 先取得请求数和最小处理内存额度；容量已满时不读取请求体。
2. 取得短期读取/解压额度，再读取和解压。默认一次仅处理一个读取/解压请求，
   额度争抢时最多等待 250ms，期间不读取或解压请求体；等待者也占用处理请求名额，
   因而受 32 个活跃请求上限约束。等待超时或整体容量已满才拒绝。
3. 解压受输出大小、zstd 窗口、解码器工作内存及单解码器并发限制。
4. 实际请求体按 `max(decoded_bytes, 1 MiB) × 8` 计入处理预算；成功交接后
   立即释放短期额度，复用预读请求体，避免再次解压。
5. 实际请求体预算与请求数保留到请求处理结束，兼容重试、捕获和调度仍持有的引用。

预算为保守容量估算，不是 RSS 硬上限。解码临时缓冲区成为垃圾后仍可能在 GC 前
占用内存；部署时必须保留运行时、连接、缓存和 GC 余量。不得仅按压缩字节数计费。

单请求输出严格限制为 64 MiB（或更小的 gateway.max_body_size）；多读取一字节
检测超限，返回 413。无效压缩返回 400。读取超时返回 408。实例容量不足保留
503 / `basispoints_image_request_busy` / `Retry-After: 1`，兼容旧客户端。

## 配置

在现有 `gateway` 节点合并配置；不要复制覆盖整个配置文件。

```yaml
gateway:
  image_relay_admission:
    decode_max_concurrent: 1
    decode_wait_milliseconds: 250
    decode_budget_bytes: 536870912
    processing_budget_bytes: 536870912
    max_concurrent_requests: 32
    body_read_timeout_seconds: 60
  excel_bps_timeouts:
    first_output_seconds: 0
    idle_seconds: 300
    total_seconds: 1800
```

支持 `GATEWAY_IMAGE_RELAY_ADMISSION_*` 与 `GATEWAY_EXCEL_BPS_TIMEOUTS_*` 环境变量。
四种 Compose 模板已显式传入这些变量。自定义 Compose 也必须传入变量或挂载配置；
只写入 `.env` 不代表容器自动读取。
参数变更需要重启进程。预算为每进程限制，多实例不能视为共享全局内存池。

处理预算至少为 8 MiB；解码预算至少为 512 MiB。提高解码并发时必须同时为各
解码槽位保留 512 MiB 预算，并根据 RSS 压测决定是否扩大。32 是本地入口上限，
实际请求还要服从账号、用户及上游的既有额度。

图片中继转换、临时 URL、存储配额、清理机制继续使用原实现；图片生成并发限制
仍独立存在。本补丁不关闭图片功能，也不新增无限队列。文本仍经过通用入口保护，
但小型压缩文本不再占用整笔预算。

BPS 二进制兼容默认仍为关闭；部署模板明确设置上游空闲 300 秒、转发总时长
1800 秒，首输出不设额外限制。已有环境变量或挂载配置需要核对，不能假定新模板
已经应用。超过这些时长的正常业务应提高对应值，或显式设为 0；上线后分析
首输出和空闲分布再调整。
`first_output_seconds` 从上游发起时间计到首个输出；`idle_seconds` 计原始上游
字节活动，包括被协议转换器消耗的上游活动，不计本地发送的 keepalive；
`total_seconds` 限制 BPS 转发函数的执行时长，不包含其之前的账号调度。
如果要限制等待响应头，应结合总时长和既有响应头超时。

超时不会重放已发送请求。SSE 已开始时发送兼容的 `response.failed`；尚未开始时
返回 JSON 504。客户端取消保持原取消语义。空闲超时只针对上游无字节活动，
不因总推理时长超过五分钟而触发；总时长上限单独生效。

## 验证与发布门槛

```sh
cd backend
go test -p 1 ./internal/pkg/httputil ./internal/config ./internal/server/middleware ./internal/server/routes ./internal/service -run 'ExcelBPS|ReadRequestBody|BoundedBody|DecompressedBody|ImageRelayAdmission|OpenAISSEReadPump' -count=1
go test -race -p 1 ./internal/pkg/httputil ./internal/config ./internal/server/middleware ./internal/server/routes ./internal/service -run 'ExcelBPS|ReadRequestBody|BoundedBody|DecompressedBody|ImageRelayAdmission|OpenAISSEReadPump' -count=1
go build -p 1 ./cmd/server
SUB2API_ADMISSION_SOAK_DURATION=30m go test -p 1 ./internal/server/middleware -run '^TestExcelBPSImageAdmissionSoak$' -count=1 -timeout=35m -v
```

必须验证：长流并发小压缩请求、200 个请求过载、接口别名共享、压缩边界、慢上传、
取消与异常释放、图片转换和读取、BPS 超时与单次上游调用。测试不使用真实模型费用。
本地通过不等同生产 24 小时稳定性证明。

持续负载测试保留一个压缩长流，同时用四个工作协程发送压缩和未压缩请求。
它报告成功数、入口限流数、异常数及 GC 后堆大小；并发解码槽位已满时的
503 单独计数，不能将这些请求算作成功。该低负载回归要求入口限流数为零；
另有明确过载测试验证容量不足时仍拒绝，不能据此宣称所有 503 已消除。
此测试覆盖入口容量与释放，不代替真实上游、反向代理和完整生产系统的压测。

上线前固定源码 commit 和镜像 digest，保存 Compose、应用配置、数据库配置备份。
先在隔离环境进行至少 30 分钟混合负载，采集进程 RSS、延迟、错误数和上游调用数。
测试实例禁用或隔离后台任务，避免重复处理生产任务。

生产灰度先限测试账号/API Key；若架构不支持安全多实例，不直接复制生产容器。
切换新连接后排空旧 SSE，再退出旧进程。上线后观察至少 24 小时，包括高峰。
正常请求成功率下降、OOM、额度泄漏、图片回归时回滚旧镜像及对应配置。
没有新增数据库迁移。恢复入口旧逻辑会重新出现旧版的容量问题，需要降低并发。

继续保留此前 Caddy 修复：业务 503 不应触发唯一上游摘除。不可通过删改压缩头
假装关闭压缩，真正关闭必须由客户端重新以未压缩字节发送。

## 观测

结构化日志组件：`gateway.image_relay_admission`。拒绝记录原因：
`active_requests`、`decode_capacity`、`processing_bytes`；附带已使用预算、额度、
原始长度、解压长度和编码。未解压时长度为 -1，不凭空推测。关联 ID 由现有日志
上下文提供，日志不记录请求体或凭证。接受详情为 debug 级，定位期间短期开启。

通过日志采集器按拒绝原因做速率统计，避免把请求 ID 当指标标签。区分本地 503、
账号不可用、上游 503 与 Caddy 错误。初始告警建议：本地容量拒绝率 5 分钟超过
1% 且至少 20 次；OOM、容器重启立即告警。阈值需结合正常业务基线校准。

本补丁提供日志字段；监控面板、告警规则与生产灰度属于部署阶段，不能宣称已经
安装。不要把客户端无限重试作为修复；只对明确未转发的容量拒绝进行有上限的
指数退避并加随机抖动。

## 线上审计后追加的稳定性修复

- `deploy/Caddyfile` 的单后端模板移除摘除与重试配置。三个业务 503 后，
  健康的首页仍应得到 200；`deploy/tests/caddy-single-upstream-test.py` 用真实
  Caddy 与本地模拟上游验证这一点。生产已有配置需要单独比较后应用。
- `inferResponsesFailedOpsErrorType`、`inferStreamFailureStatus` 与业务限制分类
  显式识别 `gateway_queue_full` / `gateway_concurrency_limit`。即使 SSE 终止帧
  只含 code、HTTP 已提交为 200，运维错误状态仍为 429；BPS 超时为 504。
  请求终止失败与已经恢复的中间重试继续分开统计。
- Redis `GetConcurrencyQueueDepth` 通过已有活跃索引同时采样用户和账号队列，
  不依赖账号是否还可调度。分页去重、分批读取、饱和求和，不刷新队列 TTL。
  样本读取失败返回未知，而不是虚假的零；旧缓存适配器保留兼容路径。
- BPS 单次已声明工具调用可解开完整 Markdown 代码围栏或 JSON 字符串包装，
  保留工具身份、参数精度及原始重放记录。任意程序、多次调用、未知工具仍拒绝；
  这不能保证所有模型产生的非法协议都能自动修复，也不会重试已执行的工具。
- 二进制、YAML 示例、环境示例与 Compose 的数据库默认池统一为打开 50、
  空闲 10。生产显式设置的 256/128 不会被新默认值自动覆盖；应按数据库
  可用连接数和实例数量设置。用户并发与模型计费配置也不会被补丁擅自扩大。
- 四种 Compose 模板传递全部图片入口预算与 BPS 超时参数。部署空闲上限
  300 秒、总上限 1800 秒可显式调整为 0 禁用，避免后台请求无限占用槽位。
- 前端 `packageManager` 固定为 `pnpm@9.15.9`，与现有 CI/镜像所用 pnpm 9
  保持一致，避免嵌套构建脚本被全局 pnpm 12 自动安装行为改写依赖。
  使用 Corepack 和 `--frozen-lockfile` 验收；本补丁不升级锁定依赖。
- 全量前端验收发现恢复时间测试把 UTC+8 写死。测试改为构造本地日历时间，
  保持现有页面的本地时间显示语义，并在 UTC、上海、洛杉矶三个时区复核。
- 后端全量测试发现两项环境敏感断言：OAuth 超时测试使用虚拟时间确保
  取消先于迟到响应发生；工具 schema 分配测试在独立子进程中运行，防止
  其他测试的后台任务污染进程级分配计数。超时断言和 200 次分配上限保持不变。

- 监控参数校验矩阵使用公网 IP 字面量作为有效端点，不再依赖外部 DNS。
  测试只执行参数校验，不发送 HTTP 请求；必填字段断言和 SSRF 校验规则保持不变。

审计中的模型渠道定价不匹配、上游过载和访问权限变化属于配置或外部依赖，
应修正渠道价格覆盖范围、核对账号权限并监控上游，不通过无限重试掩盖。
本地测试全绿只能证明已执行的验收范围；生产仍需灰度与持续观测。
