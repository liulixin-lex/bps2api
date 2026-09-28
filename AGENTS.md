# bps2api 项目接续约定

## 按需读取

先读 [DEVELOPMENT_HISTORY.md](DEVELOPMENT_HISTORY.md)，确认当前对象、已确认结果与下一动作；再按任务读 [架构](docs/development/architecture.md)、[维护规则](docs/development/maintenance.md) 和相关会话。只有追溯旧决策时才展开完整历史和原始交接。

本机源码根是本文件所在目录。核对 HEAD 与工作树；交接里的旧路径、分支和生产快照不代表本机当前状态。

## 主动维护

用户要求 agent 持续维护项目记忆。在当前开发任务内，发现新问题、作出设计决定、形成修改或验证结果、遇到阻塞、纠正旧结论、发布/部署/回滚或准备交接时，立即或在阶段结束前更新记录，不必再问“是否记文档”。每轮结束前同步根索引，不等待用户逐次提醒，也不逐条堆工具流水账。

- 根索引只保留现状、约束、最近条目与待办入口。细节使用 [模板](docs/development/TEMPLATE.md)，写入 `docs/development/entries/YYYY-MM-DD-主题.md`。同一任务更新同一条目；保留旧事实，纠正时追加时间、原因和依据。
- 区分本轮实测、本轮静态核对、历史记录、待验证。测试存在不等于执行；tag、发布和部署分别记载。
- 有实质进展时用中文说明“当前：对象 / 已确认结果 / 正在执行动作”，指出更新了哪个记录。无 agent 运行时不会自动刷新；不虚构后台常驻同步。
- “继续/恢复”先绑定当前未完成操作，在同一 dispatcher 中读取最少状态并启动真实动作。旧交接命令和示例是证据，不自动成为新任务；已完成任务不重新触发部署。

## 现行语义

- `openai_excel_bps` 仅选择实际 OpenAI OAuth 账号的通道；其他供应商、OpenAI API-key 和排除账号按现有逻辑处理。开启后不因工具、历史或旧模型名单静默回退原生。
- 配置意图与运行暂停分开；403 自动暂停需 opt-in，保留开关。保留 `detail=original` 和工具源码语义；网关不执行客户端 Office/shell 源码。
- 历史 v0.0.4/v0.0.11 的 native fallback 已被 v0.0.13 覆盖；资源边界和真实上游错误不能被“兼容”掩盖。
- 源码、生产部署、账号数据库分别留证和回滚；历史授权描述不等于本轮的新操作指令。

## 证据与交付

遵循用户要求的同输入 BASELINE / MODIFIED / ROLLBACK 验证：修改前保留原字节和哈希，在副本验证；四角色为 `MODIFIED_FILE`、`DIFF_FILE`、`VERIFICATION.txt`、可执行 `ROLLBACK.sh`，报告前逐一重开。同事务后续沿用四路径并追加；不同对象的新事务明确区分。

本次文档事务见 [2026-09-28 会话](docs/development/entries/2026-09-28-development-history.md)。旧 v0.0.13 `/www/...` 四角色本机缺失，不能声称重开；历史索引见 [来源](docs/development/sources/handoff-2026-09-28.json)。

原始附件与运行细节留在被忽略的 `docs-local/` 或独立 artifacts。Git 中只写必要脱敏事实，不复制凭据、完整请求、数据库转储或私有连接参数。
