# 2026-09-28：择优融合 fork 上游更新

[返回开发历程](../../../DEVELOPMENT_HISTORY.md)

> 状态：本地源码整合、回归、四角色事务和提交完成。生产仍运行原 v0.0.13，未推送或部署。

## 背景与目标

用户要求先提交此前文档，再比较 ranxi2001/sub2api 新内容，吸收更好的更新并保留 bps2api 二开的优势，完成后再次提交。本轮不包含推送、发版或生产部署。

## 起点与固定输入

- TARGET：/bps/bps2api；先完成文档提交 3ae017adc0d7fecf85e432a0dcb4ceb9b0332228。
- 上游：ranxi2001/sub2api，默认分支 production；本轮 fetch 固定 fe27f9895a75e12562f33eff70f21c660329a684。
- 共同祖先：e39898c680ecd69381e549ae54c97011107f1757（上次整合的 v2.8.14）。
- 上游差异：163 条提交，其中 93 条非 merge；454 个文件，32451 行新增、1670 行删除。
- 隔离候选：/bps/worktrees/upstream-integration-20260928；分支 sync/upstream-20260928。
- 已执行 merge --no-commit --no-ff，36 个冲突由协议、后端、前端三路处理，主 agent 负责身份、文档和整体验证。

## 取舍标准

保留我方严格 OpenAI OAuth BPS 路由、403 暂停保留配置、Messages 适配、original 图片、无损工具传输、终态/deadline、两阶段有界资源与独立发布身份。吸收可兼容的账号、代理池、诊断、工具历史和管理功能；新能力显式 opt-in，不默认丢内容或恢复隐式原生回退。

上游默认 128 槽/1024 MiB 不覆盖我方 32 槽/512 MiB；新增可配置图片数量不以默认 20 倒退原有 500 张边界。代码合并不等于验证通过，冲突选择和功能矩阵持续补齐。

## 实际完成与注意事项

- 文档提交完成。首次因 Git 作者未配置退出 128，改用单次命令的 Codex <codex@localhost> 署名后成功，不改全局配置。
- 网页工具返回 basispoints_endpoint_unsupported，已通过官方 Git 与 GitHub REST 读取上游；失败请求不用于推断仓库状态。
- 初次 no-commit merge 也因 committer 身份缺失退出 128；补充局部署名后启动，退出 1 为待解决冲突。
- 保留 bps2api VERSION 0.0.13，不以 2.8.20 冒充本 fork 发布号；贡献流程和 CI 入口改为本仓库。

### 吸收的上游改进

- BPS 原生附件上传、可配置中转限额、按会话有界的工具目录/回放、schema 与工具传输纠错；均与本地严格路由、终态和共享恢复预算对齐。
- 独立 403 自动移组/退组及事务 outbox、401 认证失效处理、BPS 429 冷却、模型额度恢复。
- 观察员角色与分组访问隔离、2FA 导入、账号质量与定时检测、批量规则按原账号构造 patch。
- Mihomo/IP 池、探测与质量管理，连接诊断、用量时序、请求捕获细节、Pelican 预览、导航和 Select 组件等。
- 新迁移为 `252_user_observer_groups.sql`。未执行生产迁移、未改生产账号或协议开关。

### 保留和改造的 fork 行为

- BPS 仍由实际 OpenAI OAuth 账号选择，旧模型名单不是隐式绕行条件；403 opt-in 暂停保留配置，不能改成直接关闭通道。
- 保留 Responses/Chat/Messages、original 图片精度、FUNCTION/FUNCTION_CODE/FUNCTION_CMD/CUSTOM 原文与大整数、正确终态/deadline。
- 默认 32 个在途、512 MiB 预算、500 张、50,000,000 去重字节；新增默认总 MiB 显示 48，但运行时保留精确 50 MB 边界。管理员不会因提高并发数自动扩大内存预算。
- 托管工具省略、图片忽略、加密内容替换与 native 附件需要显式选择；不默认丢弃内容或走原生。
- 有界缓存、schema 校验、整批工具校验与一次输出前恢复预算；不叠加两套重试次数。

### 合并及排查中修复的问题

- 清除自动合并重新引入的原生回退、模型名单控制与错误容量默认值；修正文档和 settings 初始化值。
- 导入弹窗新增 observer store/i18n 依赖后，旧测试缺 mock；补齐测试环境。
- 新的 429 failover 不能在 BPS 候选耗尽后调用原生 OAuth/API-key；跳过不同实际模型的候选并释放槽位。精确模型权限 403 同样只在输出前切换同模型 BPS 账号。
- 修复 Messages 取消后追加 502、BPS failover 耗尽后可能追加通用错误、401 提前返回丢失合法 Retry-After。
- 最终复核修正了候选筛选与实际转发的模型解析差异：Chat 别名规范化、Messages 账号映射优先于 dispatch 的顺序保持一致。新增 403/429 × 流式/非流式的 8 项回归；原实现 overlay 实测 exit 1，修复后定向及完整 handler exit 0。
- 用户追加多轮报错排查，修复强制客户端工具选择限制，明确 encrypted_content 与未知内容诊断。线上受限观察见[独立事件记录](2026-09-28-runtime-errors.md)。

## 验证与证据

以下为已实际执行的结果，命令、stdout/stderr、退出码位于 `/bps/artifacts/development-history-20260928/upstream-20260928`：

| 检查 | 结果与边界 |
| --- | --- |
| 基线 BPS/图片/严格路由 | Go 1.27.0 容器中 exit 0 |
| 前端全量 `vitest run` | 371 个文件、2887 项测试通过，exit 0 |
| 前端类型、lint | exit 0 |
| 前端生产构建 | exit 0；保留大 chunk 和静态/动态 import 警告 |
| 后端 `go test -tags=unit ./...` | 可写隔离副本中 exit 0；包含服务、handler、schema 与中间件 |
| 定向 PostgreSQL/Redis 集成 | ExcelBPS / Observer / 配额恢复 / 用量过滤 / 取消等匹配用例 exit 0，迁移成功 |
| BPS 包 `-race` | exit 0 |
| 多轮内容、工具选择、混合池、取消、模型权限 | 定向回归 exit 0；最终映射修复后的 service/handler 定向与完整 handler 再验 exit 0 |

首次全量后端失败包含只读挂载下 Ent 测试写 `.entc` 的环境问题；改用可写隔离副本，不改测试去假通过。另修正历史测试对 429“service 立即写响应”的旧断言。前端构建首次因子脚本找不到 pnpm 退出 1；使用本次 artifacts 内 Corepack shim 后通过，未更改全局工具配置。所有失败日志保留，不能省略后冒充首次成功。

同输入事务使用 auto、required 和第 61 项含 encrypted_content 的脱敏历史：BASELINE 接受 auto、拒绝 required、密文显示 unknown；MODIFIED 接受 auto/required，密文默认拒绝并标明 encrypted_content；ROLLBACK 输出逐字等于 BASELINE。恢复后全部文件哈希/模式一致，重新应用补丁后逐字恢复候选并再次得到 MODIFIED 输出。初次补丁重建受本机 umask 077 影响，字节一致但权限为 0600；独立副本中显式 umask 022 后完整验证 exit 0，失败证据保留。

最终四角色归档覆盖本次源码树；完整提交 ID、树 ID、两个父提交和 main 状态记录在同目录 `upstream-20260928/completion.json`，验证账本追加提交事件，避免在提交内容内自引用未生成的哈希。

沿用 /bps/artifacts/development-history-20260928 下 MODIFIED_FILE.tar.gz、DIFF_FILE.patch、VERIFICATION.txt、ROLLBACK.sh 四角色。上轮原字节保存在 upstream-20260928/previous-role-snapshot；本轮选择、命令与验证作为补充，最终扩展既有角色交付。

## 当前结果与下一步

LAST_CONFIRMED_RESULT：36 个冲突全部解决；主回归、集成、构建、竞态、最终模型解析回归及同输入回滚通过；merge 提交接入本地 main，四角色重开；线上证据已脱敏整理并清理采集。
NEXT_EXECUTABLE_ACTION：无。本轮源码任务完成，未安排自动推送、发版或部署。
ACCEPTANCE_EVENT：已满足：保留优势并吸收改进，验证与回滚通过，四角色重开，整合提交落在本仓库。
