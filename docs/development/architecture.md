# 当前架构与实现约束

[返回开发历程](../../DEVELOPMENT_HISTORY.md) · [维护规则](maintenance.md)

核对日期：2026-09-28（UTC）。第 1–8 节保留文档建立时 `2f54db96e19ca06d54d73c990ccd1d750474ade8` / `0.0.13` 的静态快照；第 9 节补充本日上游整合后的变化。历史“未执行”仅指文档建立阶段，当前验证见整合条目。

## 1. 组件与职责

- Go 后端位于 `backend/`：HTTP 网关、账号调度、协议转换、用量计量、管理接口与后台任务。
- Vue 前端位于 `frontend/`：账号、分组、系统设置、用量和诊断等管理界面。
- PostgreSQL 保存账号、配置、业务记录；后端通过已有 repository / Ent 数据层访问。
- Redis 承担缓存、调度快照、并发队列等运行协调；不是图片数据或源码的永久归档。
- Caddy 部署模板位于 `deploy/`：处理外部 HTTP 入口与反向代理，应用层错误与代理错误应分开观察。
- BPS 是被选账号的上游推理通道；本项目本身没有可由服务端直接操作的 Excel 工作簿。
- 客户端声明工具并执行调用；Office.js、shell 等实际执行环境属于客户端，不属于网关。

本 fork 继承 upstream 历史与目录命名；Go module 仍为 `github.com/Wei-Shaw/sub2api`。
应用版本在 `backend/cmd/server/VERSION`，不能用前端 package 的 `1.0.0` 判断发布版本。

## 2. 通道选择先于能力判断

核心文件：[account.go](../../backend/internal/service/account.go)。

| 字段或函数 | 当前含义 |
| --- | --- |
| `openai_excel_bps` | 管理员保存的 BPS 通道选择 |
| `IsExcelBPSConfigured()` | 只对实际 OpenAI OAuth 生效，排除 shadow、agent identity、PAT |
| `IsExcelBPSEnabled()` | 已选择 BPS，且没有运行暂停标记 |
| `IsExcelBPSEnabledForModel(_ string)` | 保留调用接口，模型参数不再决定是否绕过 BPS |
| `openai_excel_bps_models` | 旧配置元数据，不再作为恢复原生通道的模型白名单 |
| `openai_excel_bps_paused_on_403_at` | 运行暂停标记，不等于用户关闭 BPS 开关 |

其他供应商及 OpenAI API-key 不因保留同名 extra 字段就改走 BPS。
`basispoints/route.go` 的 `NativeFallbackReason()` 是历史能力诊断，不能据此恢复自动回退。
开启 BPS 后，不支持的工具、历史或参数应返回明确错误，不能改模型或静默改走原生。

## 3. 三种下游协议与上游边界

| 客户端入口 | 实现锚点 | 选中 BPS 后 |
| --- | --- | --- |
| `/v1/responses` 及既有别名 | `openai_gateway_forward.go` / `Forward` | 进入共用 BPS 转发 |
| `/v1/chat/completions` | `openai_gateway_chat_completions.go` / `ForwardAsChatCompletions` | Chat 转 Responses 后转换返回格式 |
| `/v1/messages` | `openai_gateway_messages.go` / `useExcelBPS` | Anthropic 请求适配，返回 Messages JSON 或流 |

客户端请求格式不等于账号供应商；其他厂商仍沿用各自路由。
选中 BPS 的 OpenAI OAuth 上游固定为 `https://bps.openai.com/basispoints/api/responses`。
`openai_excel_bps.go` 的 `forwardExcelBPSAttempt()` 在本地校验前记录实际 endpoint，
并设置 `X-Codex2API-Upstream: basispoints`，使本地校验失败也能归因到正确通道。
独立 Images API 与 alpha search 不共享该 Responses 协议；BPS 通道返回
HTTP 400 / `basispoints_endpoint_unsupported`，不悄悄调用原生服务。
实现锚点为 `openai_images.go` / `ForwardImages` 和 `openai_excel_bps_endpoint.go` / `rejectExcelBPSNativeEndpoint`。

## 4. 403 暂停与恢复

自动暂停是 `openai_excel_bps_auto_disable_on_403` 控制的 opt-in，不是每个 403 都修改账号。
`openai_excel_bps.go` 中 `disableExcelBPSOn403()` 为历史命名，实际保持通道选择并暂停运行。
`repository/account_repo_excel_bps.go` 的 `DisableExcelBPSOn403()` 通过事务条件更新暂停标记；
条件匹配账号类型、当前 credentials、开关与 opt-in，避免过期请求覆盖后来修改的设置。
成功更新后写 scheduler outbox，并同步调度快照；暂停账号不参与正常调度。
直接进入 BPS 分支的暂停账号返回 HTTP 403 / `basispoints_routing_paused`，不能借暂停转原生。
`frontend/src/components/account/EditAccountModal.vue` 展示暂停状态和恢复操作；
管理员核对权限后清除暂停标记，或明确关闭 BPS 以选择原生通道，两种操作语义不同。

## 5. 图片兼容与资源边界

`basispoints/content.go` 的 `normalizeImageReference()` 统一语义明确的图片引用，
`basispoints/image_detection.go` 的 `RequestHasInlineImages()` 与中继使用对应形态。
支持识别 `image`、`image_url`、`input_image`、`output_image`、`screenshot`、
`computer_screenshot`、`provider_image`；冲突引用、额外二进制内容、非字符串 detail 不静默降格。
`detail=original` 必须保留；不能通过降为 high 或文字描述假装处理原图。
内联 data URL 经 bounded image relay 转为 HTTPS 临时引用；BPS wire 不直接接收内联数据。
重复内容共享解码与存储 reservation，同时保留每次图片出现的位置、次数和 detail。
入口保护位于 `server/middleware/excel_bps_image_admission.go`；中继位于 `service/basispoints/image_relay.go` 系列。

入口区分短期读取/解压额度与请求处理额度；前者完成后释放，后者随请求生命周期释放。
部署默认上限、运行时设置和每进程预算共同约束容量；管理界面不能突破部署硬上限。
预算是保守估算，不是 RSS 硬上限；多实例不能被视作一个共享全局内存池。
图片下载、过期清理、租户签名、磁盘容量和滚动部署旧 token 路径仍需独立验证。
详见[容量说明](../image-relay-capacity-fix.md)；其中参数示例不代表本轮已读取生产配置。

## 6. 工具原文、结构化输出与 SSE

工具协议实现集中于 `backend/internal/service/basispoints/`：`tools.go`、`catalog.go`、
`function_code_transport.go` 等负责 FUNCTION、FUNCTION_CODE、CUSTOM 的声明与往返。
源码字符串、工具身份、元数据与历史应保留；转换不授予工具权限，也不在网关执行源码。
Schema 引用仅解析受限的本地文档引用，不能借远程引用读取任意网络或本地文件。

结构化输出通过 developer 指令表达要求，再在本地校验终态；不等于上游 constrained decoding。
无效 JSON/schema 不应自动修补、去掉围栏后伪装通过或替换为编造结果。
结构化文本待终态验证后才释放，普通文本保持增量；工具续接和明确 refusal 保留协议语义。
详见[适配器说明](../../backend/internal/service/basispoints/README.md)。

`openai_sse_read_pump.go` 使用真实上游字节活动计算 idle；本地 keepalive 不刷新上游活跃时间。
完成、incomplete、failed、EOF 和 timeout 必须保留实际终态，不能把 HTTP 200 当作业务成功。
恢复受次数与 deadline 限制，只能发生在符合条件的输出前阶段；部分输出后不能任意重放。
Chat 与 Messages writer 负责协议终态转换；Messages 失败不能再发成功 stop。
`openai_excel_bps_messages.go` 限制累计 wire 为 64 MiB、事件数为 65,536，防止无限元数据增长。

## 7. 验证入口与本轮边界

以下均为后续代码变更的**待执行命令**，不是本轮结果。当前环境无 Go/pnpm 可执行，
本轮仅创建和核对文档，未运行业务测试、编译、真实上游或生产探测。
后端声明 Go `1.27.0`；前端声明 pnpm `9.15.9`；CI 前端使用 Node 20。

```bash
# 待执行：严格路由、暂停、Messages、探测边界
cd /bps/bps2api/backend
go test -p 1 -count=1 -timeout=120s ./internal/service -run 'TestStrictBPS|TestExcelBPSMessages|TestExcelBPSModelSelection|TestExcelBPSPause|TestExcelBPSLegacyModels|TestExcelBPSToolProbe'
# 待执行：图片别名、原始精度、工具传输
go test -p 1 -count=1 -timeout=120s ./internal/service/basispoints -run 'TestImageAlias|Test.*Original|Test.*Transport'
# 待执行：仓库既有全量门槛
cd /bps/bps2api
make -C backend test-unit
make -C backend test-integration
make test-frontend
```

`make test-frontend` 包含 lint:check、typecheck 和指定关键 Vitest；并不等于所有前端用例。
集成测试先准备隔离依赖，不能指向生产 PostgreSQL/Redis；安装依赖、构建与测试结果分别留证。
测试源码存在不等于测试通过；本地夹具成功不等于上游权限、长期容量或生产稳定性得到保证。

## 8. 历史文档的适用范围

[0.0.13 说明](../bps2api-0.0.13.md)与本页反映当前严格通道策略。
[0.0.11 协议审计](../BPS_PROTOCOL_COMPATIBILITY.md)中的自动 native fallback 是历史阶段设计，
已被 0.0.13 覆盖；追溯动机时可读，不能作为当前路由实现依据。

## 9. 2026-09-28 整合增量

- 固定整合上游 `fe27f9895`，保留本 fork `VERSION=0.0.13`；提交不代表新发版。
- 新增显式 `image_mode=relay/native`，默认 relay 且图片总开关默认关闭。原生附件需要主动开启；原有 32 槽/512 MiB/500 张、50,000,000 去重字节及 original 精度继续保留。
- 403 暂停之外新增独立自动移组；只对明确的普通 403 生效。精确模型权限 403 和 429 可在输出前切换同实际模型的其他 BPS 账号，耗尽返回对应错误，禁止原生绕行。
- 新增 401 凭据失效状态处理、BPS 专用冷却；不把模型权限拒绝变成全局账号停用。
- 缓存与 schema 校验有界；工具选择支持 required/指定客户端工具及 allowed_tools，在整批交付前落实约束。未知目标、工具封装、HTTP 与 EOF 恢复共享一次预算。
- 加密历史默认明确拒绝；账号 opt-in 可用不可用标记有损继续，不能当成解密。未知内容仅增加安全结构诊断。
- 纳入观察员角色/分组隔离、2FA 导入、Mihomo/IP 池质量管理、用量与请求诊断等更新。数据库新增 `252_user_observer_groups.sql`；迁移在隔离 PostgreSQL/Redis 上验证，未操作生产 schema。
- 新的代码入口、测试日志和未验证范围见[上游整合](entries/2026-09-28-upstream-integration.md)和[多轮错误排查](entries/2026-09-28-runtime-errors.md)。
