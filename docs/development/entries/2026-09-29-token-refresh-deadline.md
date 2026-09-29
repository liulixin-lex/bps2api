# 2026-09-29：刷新返回与截止回调之间的竞态

[返回开发历程](../../../DEVELOPMENT_HISTORY.md)

> 状态：专项验证完成；2026-09-29 UTC。本条属于选定上游 PR 与恢复整合的追加修复，未发布、未部署。

## 背景与来源

整合候选 107e4eb8f 的后端完整受影响包验证中，TestTokenRefreshService_LateSuccessPastAttemptDeadlineIsRejected 返回 nil，未产生期望的 attempt 超时错误。该候选的 token_refresh_service.go 与 oauth_refresh_api.go 和原始基线 444be7907 完全同 SHA-256；这两份生产代码没有来自本轮选定 PR 的改动。

原测试在同一候选独立重复 30 次全部通过，但重跑成功不能排除实际竞态。现有逻辑只检查 Context.Err()：运行时繁忙时，provider 恢复执行可能早于已经到期的取消回调。截止时间已过、Err() 尚未更新时，过晚的返回仍被视为成功。

## 最小修复与边界

- oauthRefreshContextError 先保留已交付的取消错误，再使用 context 的绝对截止时间检查是否已经到期。
- 统一 OAuth API 与兼容直接刷新路径，在 provider 返回后、凭据持久化之前检查该边界。
- refreshWithRetryWithRateGate 同时检查父截止时间，父请求/周期已结束时直接返回，不将其误记为 attempt 失败而触发账号冷却。
- 保留已提交凭据 CAS 的语义：只有内部 attempt 时间在后续有界清理期间耗尽时，不重试已提交的刷新、不错误触发熔断；父取消仍返回取消并清理缓存。
- 不改变刷新配置、重试次数、上游请求内容、账号数据或生产环境。

## 可复核验证

证据目录：/bps/artifacts/selected-integration-20260929/deadline-check/。

- candidate-repeat-*：原失败测试重复 30 次，exit 0。
- base-candidate-production-comparison.json：两份生产文件的 444 基线与 107 候选哈希一致。
- oauth_refresh_deadline_boundary_test.go：固定输入使用虚拟时钟和延迟取消通知的 context，不依赖真实主机的调度速度。
- boundary-baseline-*：未修生产代码运行该输入，两个过期但 Err()==nil 用例失败，取消优先级用例通过，exit 1。
- boundary-modified-*：修改后 12 组相关边界各重复 20 次，240 次顶层执行全部通过，exit 0。原失败测试未修改；覆盖父截止无冷却、取消优先、内部超时冷却、已提交成功及取消后的缓存清理。
- apply-attempt-1-error.json：保留第一次自动应用脚本的断言计数错误；修正为实际 7 个父错误检查点后继续。此工具错误不是运行时测试结论。

基线与修复发生在独立工作树，原 /bps/bps2api 未修改。整体三态回滚、固定四角色及最终整包验证由父整合事务统一完成，本专项不冒充其完成结果。
