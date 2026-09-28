# 2026-09-28：BPS 路由、恢复、RPM、质量规则与图片容量

## 背景

在 0.0.15 上线后，对照 sub2api 上游与本 fork 的差异，决定吸收上游可自定义 BPS 模型、RPM 控制、质量检测自动启用/恢复，以及更高的图片并发和内存预算。短暂失败需要自动补救；客户端不应因为短时错误手工重试。hosted 工具继续自动兼容，不暴露管理员手动省略开关。用户所称“0.1.5”指该策略，本仓库实际发布标签为 `v0.0.15`；本轮没有改换 hosted 策略或创建 `v0.1.5`。余额查询不纳入本轮。

## 目标与决定

- 保持 BPS 只用于实际 OpenAI OAuth 账号，并支持管理员按模型名单路由；路由名单以请求模型映射后的上游模型精确匹配。
- 模型名单未配置时选择所有映射模型；显式空名单不选择模型；已配置但暂停的 BPS 不静默回退原生。
- hosted 工具按 v0.0.15 策略自动兼容：移除 BPS 不支持的 hosted 声明，继续执行兼容的工具调用，不增加人工开关，也不赋予网关执行宿主工具代码的能力。
- 在有界尝试次数与恢复时间预算内自动重试瞬时传输、HTTP、流终止错误；客户端可见输出后不重放请求。每次上游发送均进入 RPM 控制。
- 吸收上游 BPS 质量规则的自动启用与恢复，同时保留管理员选择、持久化冲突检测与运行时 403 暂停保护。
- 图片准入默认提升到 128 MiB 请求体、1 GiB 解码预算和 128 个并发请求；具体限制仍受管理员配置与 admission 控制。

## 实际进展

候选实现位于 `/bps/worktrees/configurable-recovery-20260928`，分支 `feat/bps-configurable-recovery`，基线 `8e2e59e40f640c8142397fca71919fe7c01b811c`。

- 后端增加映射后精确匹配的模型范围判断，并用于 Responses、Chat Completions、Messages 与 failover 路由。
- 账号表单和批量编辑提供模型选择，但 hosted 工具策略仅显示自动兼容说明，不提供旧版手动省略字段。
- 图片默认容量提高；配置示例与中间件预算同步。
- 增加有界的传输/HTTP/流恢复与 RPM slot；WebSocket ingress 和实际上游发送接入 RPM 控制。
- 附件上传的 RPM 错误保留原始错误类型并传递至 handler，避免被转换为一般附件失败而绕过正确的限流响应。
- 代理重连、加密内容修复与工具修复补发共同消耗请求的尝试次数和恢复时间预算，避免各自重试导致实际发送超出配置上限。
- 修复图片递归恢复前未释放真实代理 lease 的单槽自等待；重试前释放真实所有权，延迟清理只执行一次，HTTP、EOF、transport 三种场景均有回归覆盖。
- Retry-After 不再使用旧硬编码 2 秒门槛，统一由最大退避配置和剩余时间预算判断，不缩短上游要求的等待时间。
- 质量规则加入 BPS 自动启用/恢复，保留 403 暂停和旧数据兼容。
- 本地验收阶段尚未推送或部署；后续用户明确要求先平滑切换再推送，并指定版本 0.0.16。实际生产结果见下方“部署与仓库同步”。

## 验证与执行记录

- 前端 `candidate-frontend-regressions-r2`：**11 个测试文件、312 项测试通过**；变更文件 ESLint、国际化完整性、TypeScript 检查和 Vite 构建均通过。构建仍有 chunk 体积与静态/动态 import 混用提示，不影响退出码。
- 先前测试失败既包含模型范围语义变化后的过时断言，也包含真实的恢复预算漏洞；已修正相应断言、预算路径并补充回归测试，不能将所有失败归为测试陈旧。
- `candidate-backend-r1` 覆盖 service、basispoints、handler、admin、dto、repository、config、server、middleware、routes，除 config 的旧默认值断言外全部通过。修正该断言并增加环境变量覆盖后，`candidate-config-r2` 全包通过，首次失败记录保留。
- 代码冻结后的 `candidate-final-bps` 再跑 service、handler、basispoints 的 BPS/RPM 相关测试，全部通过；`candidate-server-build` 服务端编译通过。六个最终 gate 退出码均为 0，命令及原始 stdout/stderr 位于本事务目录。
- 本机可复用现有 `node_modules/.bin` 工具，但没有可直接调用的 `pnpm`；优先使用本地依赖执行验证，避免反复通过容器下载依赖。
- 四角色沿用固定位置，由本轮主执行流程更新并重新打开验证，不另建替代角色：
  - `MODIFIED_FILE`：`/bps/artifacts/development-history-20260928/MODIFIED_FILE.tar.gz`
  - `DIFF_FILE`：`/bps/artifacts/development-history-20260928/DIFF_FILE.patch`
  - `VERIFICATION.txt`：`/bps/artifacts/development-history-20260928/VERIFICATION.txt`
  - `ROLLBACK.sh`：`/bps/artifacts/development-history-20260928/ROLLBACK.sh`
- 同一 Go 探针和输入已观察到：BASELINE 的显式/空模型名单均仍走全量 BPS，500 后只发送 1 次并返回 500；MODIFIED 的显式名单只选择所选模型，空名单不选择，500 后发送 2 次并返回 200；ROLLBACK 恢复 BASELINE 行为。三者的 hosted 自动省略与客户端函数兼容完全一致，回滚源码哈希等于 baseline。最终封包、补丁重建和四角色重读结果记录在固定 `VERIFICATION.txt`。
- 回滚演练最初因 tar 解包将 0664 规范为 0644 而触发模式校验，退出 2；按 Git 可执行位语义修正后重跑通过，SHA256 字节校验未放宽，失败保留在 ledger。
- 交付源码位于 `feat/bps-configurable-recovery`；实现提交 `81809a5ebf0bcb31047a45c72bd9973485c28841`，版本提交 `9b76a651f2f52cebb8bb74fa4b70bc88b81d9678`。本地测试本身不代表生产压力验收，部署和仓库同步另行留证。

## 部署与仓库同步

- 用户要求“无感替换线上服务，然后进行仓库推送”。首次使用构建标识 `0.0.15+81809a5`，在 2026-09-28 10:29:39 UTC 切至 8091；随后用户明确要求 `0.0.16`，因此修改唯一版本源 `backend/cmd/server/VERSION`，重新嵌入前端、编译并核验二进制版本，而非只修改容器标签。
- 0.0.16 实际运行源码为 `9b76a651f2f52cebb8bb74fa4b70bc88b81d9678`；本机镜像 ID 为 `sha256:2d3337220fffc674b901aeb4562ff84c2cded8312b9cad21d6e09061b64d9899`。后续部署文档提交不改变运行源码。
- 2026-09-28 **10:35:05 UTC** 通过 Caddy 热加载切至 `sub2api-v016` / `127.0.0.1:8092`。旧 8091、8090 及全部旧图片 owner 仍保留，原容器身份不变；保留 `stream_close_delay 1h`，没有停止旧服务。相对于基线没有数据库 migration 变更。
- 新实例的文本、required tool、Chat SSE、原图输入 4 项验证通过；公网同样 4 项加上 function/custom/namespace 3 类客户端工具回传均通过，使用 OpenAI OAuth BPS。切换期间 **30 次健康检查无失败**，切换前已开始的真实 SSE 在切换后收到唯一 `[DONE]` 和正常 stop；这属于限定实测，不等于所有连接及压力场景均已验证。
- 首次 8091 公网复验曾失败，记录不覆盖：10:30:16 UTC 上游返回请求级 `403: This request was blocked by our usage policy.`，触发 v0.0.15 已有的账号 BPS 暂停，随后无可用账号而产生 503；不是 RPM 拒绝，也不是 `basispoints_model_access_changed`。该暂停实现与旧版字节一致，回滚二进制不会恢复共享 DB 状态。另一有效账号接替后已观察到真实 200，0.0.16 最终公网验证通过；本轮没有强制解除暂停或关闭 403 保护。
- 部署入口和回滚入口为 `/opt/sub2api/deploy-v016.py` 的 `cutover` / `rollback`；回滚保留新实例图片 owner，旧配置与 `.env` 快照保存在权限受限的 `/opt/sub2api/backups/v016-20260928/transaction`。三态配置校验与源码副本回滚独立记录，不能将配置校验说成已执行生产回滚。
- 新证据位于 `/bps/artifacts/development-history-20260928/configurable-recovery-20260928/rollout-v016/`，首次切换与 503 脱敏根因保存在相邻 `rollout-81809a5/`。仍沿用既有四个固定角色，追加部署证据并刷新版本/文档封包。
- 按“先部署、后推送”顺序，部署记录随实现正常快进同步到 `origin/main`；实际远端提交和推送退出码以 `rollout-v016/state.json`、`push-result.json` 及固定验证 ledger 为准。不得提前以文档声明代替推送执行。本次版本号为 **0.0.16**，不把部署等同于已创建新的 GitHub Release/tag。

## 注意事项

- RPM 按向上游发出的实际请求计数；重试与工具修复等补发需要新 slot，首次 ingress 的保留 slot 不应重复计数。
- 恢复次数和时间是请求级共享预算，不能由附件错误包装、代理重连或协议修复分支重置；取消和预算耗尽仍需及时终止。
- 输出已提交到客户端后不可安全重放；流中断只能保留可见的部分结果并结束当前响应。
- hosted 工具自动省略不应被解释成 hosted 工具已在代理内执行；只有兼容工具调用仍保持正常转发。
- 自动质量恢复不能覆盖管理员更改、账号版本冲突或运行时 403 暂停标记。
- 当前提高的是默认准入上限，不等于真实生产高并发压力验收完成。
