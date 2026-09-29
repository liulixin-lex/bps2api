# 2026-09-29：择优适配调度、成本与自动配置

## 固定对象与范围

- 基线：444be79071e0789cb83870e5e5b41d14547a23f1；候选分支 feat/scheduling-selected-20260929。
- 按本轮用户选择适配上游 #187、#198、#194、#196；最终行为参考上游 7b9fb3332，不引入已被删除的 Teams 固定采购/回本方案。
- 仅本地候选，未推送、部署、修改账号或生产数据库；主代理负责最终综合合并和四角色事务。

## 适配决定

1. 优先调度、OAuth 初始模板与成功次数升并发均默认关闭，管理员分别显式启用。保留 BPS/native 分池、映射模型、sticky、RPM、slot 和原健康准入。
2. 成本倍率留空为未知；明确 0 才是零成本。只按人工估算重算运营利润，不改账单、余额或用户费率。
3. 近期 usage 以请求模型、当前映射模型与实际上游端点归属；缓存包含路由/映射身份。账号级 EWMA 缺通道身份，不用于本策略评分，原健康门槛保留。
4. 与质量分支约定只消费结果快照 pelican_config.test_provenance：account_id、requested_model、upstream_model、upstream_endpoint。仅成功完成生成的 BPS-only 观察由服务端盖章，SQL 按完整轮次计数，不依据可变计划推测路由；旧记录和无证据通道保持未知。
5. 初始模板只作用于新建匹配平台 OAuth；四个模板值会覆盖导入相应值，显式告知。并发升级按成功次数驱动，不是测得容量；各平台选定分组均可纳入。
6. 自动升档在账号行锁中再次核查 BPS 暂停、API 配额、账号状态、限流/过载和管理员更新时间。管理员更新前已开始的成功请求不推进计数。任何同账号请求尝试错误、HTTP/SSE/JSON 终态错误不会被最终成功掩盖；取消不加成功次数。超出有界观测范围的非流响应不作为升档证据。
7. 队列/存储错误停止本实例升档；默认不自动升并发。进度条仅在能力开关启用且读取成功时显示。

## 验证与证据

- 原始未修改源归档与 SHA-256：/bps/artifacts/selected-integration-20260929/scheduling/baseline.tar、baseline.sha256。
- 前端定向 10 文件 256 测试通过，修正 nullable 人工估算展示后 typecheck 通过。
- 首轮后端定向包含两个新增 JSON HTTP200 样本失败，已定位并修复为读取有界 probe；修复后后端定向 102 顶层测试通过；原始失败输出与修复输出均保留。
- 新增 SQL 集成回归：证明 BPS 观察只影响匹配账号/请求与映射模型的 BPS 路由；native、错端点/账号/模型和不完整轮次不计入；修改计划不会重写历史归属。
- 全部命令原始输出保留在上述 scheduling 目录；尚无生产吞吐、实际容量或真实盈利验收结论。

## 最终回归与事务

- 隔离 PostgreSQL/Redis 真实集成 4 项通过：并发事务和字段保护、模型/使用日志归属、独立成本读写、不可变 BPS 观察凭据。
- 根代理复核后补强非流成功定义：Responses completed 且有 output、Chat 终态 choices、Messages 最终 stop/content、Gemini 完成候选。HTTP 200 的空对象、queued、in_progress 不计成功；各协议正负样本和 SSE capture 回归通过。
- 同一前端命令在 BASELINE / MODIFIED / ROLLBACK 分别为 217 / 256 / 217 项通过，均 exit 0。ROLLBACK.sh 已在 MODIFIED_FILE 的独立副本上执行，恢复 tar 字节 SHA-256 与原件一致。未回滚正在开发的候选分支。
- 本子任务四角色为 scheduling/MODIFIED_FILE.tar、DIFF_FILE.patch、VERIFICATION.txt、ROLLBACK.sh，属于主事务补充证据；主代理继续沿用全项目四角色并完成综合验证。
