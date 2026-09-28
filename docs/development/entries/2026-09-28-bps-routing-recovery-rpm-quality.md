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
- 本轮工作仍是本地候选，尚未推送或部署；不能据此描述生产已启用模型名单路由或新恢复机制。

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
- 交付源码位于 `feat/bps-configurable-recovery`；提交身份以该分支 Git 历史与事务 `state.json` 为准。本轮不代表推送、发版、部署或生产压力验收完成。

## 注意事项

- RPM 按向上游发出的实际请求计数；重试与工具修复等补发需要新 slot，首次 ingress 的保留 slot 不应重复计数。
- 恢复次数和时间是请求级共享预算，不能由附件错误包装、代理重连或协议修复分支重置；取消和预算耗尽仍需及时终止。
- 输出已提交到客户端后不可安全重放；流中断只能保留可见的部分结果并结束当前响应。
- hosted 工具自动省略不应被解释成 hosted 工具已在代理内执行；只有兼容工具调用仍保持正常转发。
- 自动质量恢复不能覆盖管理员更改、账号版本冲突或运行时 403 暂停标记。
- 当前提高的是默认准入上限，不等于真实生产高并发压力验收完成。
