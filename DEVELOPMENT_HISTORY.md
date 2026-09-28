# bps2api 开发历程

> 项目长期记忆入口：当前状态 → 专题与会话 → 原始证据。
> 最近更新：2026-09-28（UTC）。[agent 主动更新约定](AGENTS.md)。

## 当前确认状态

| 项目 | 状态与证据边界 |
| --- | --- |
| 当前对象 | `/bps/bps2api`，从 sub2api 派生的 BPS 协议代理项目 |
| 本地身份 | `main`；HEAD `2f54db96e19ca06d54d73c990ccd1d750474ade8`；VERSION `0.0.13`；本轮本机读取 |
| 与交接的关系 | 旧分支 `c5a0323b6` 已由 PR #11 合入当前 HEAD，两提交源码树一致；旧 `/www/...` 不是本机目录 |
| 当前路由 | 实际选中的 OpenAI OAuth 账号开启 BPS 后固定走 BPS；关闭后使用原生，其他供应商不受影响 |
| 上次生产观察 | 交接记录为 2026-09-27 18:56:06 UTC 切至 v0.0.13；本轮没有重查生产 |
| 当前任务 | 已完成：开发历程、来源索引、模板与 agent 维护约定；文档检查和副本回滚已通过 |
| 下一动作 | 本轮无待执行操作；下一开发任务先读本页，再按相关专题和会话接续 |

## 渐进式阅读

| 想知道什么 | 继续打开 |
| --- | --- |
| 系统如何工作、修改应落在哪 | [架构、字段与验证入口](docs/development/architecture.md) |
| 为什么发展到当前版本 | [v0.0.1–v0.0.13 演进与决策](docs/development/history-through-v0.0.13.md) |
| 何时主动记录、怎样维护 | [维护规则与证据口径](docs/development/maintenance.md) |
| 开始下一轮开发 | [会话模板](docs/development/TEMPLATE.md) |
| 本轮实际完成、验证与四角色 | [2026-09-28：交接接续与开发记忆](docs/development/entries/2026-09-28-development-history.md) |
| 原交接如何核验 | [机器可读来源索引](docs/development/sources/handoff-2026-09-28.json)；需要时再读本机 `docs-local/development-history/sources/` |

## 继承的目标与关键决定

- 面向大量用户、图片与长上下文，尽量保留原生工具体验；这是目标，不能写成高并发验收已完成。
- 路由依据实际账号 `platform/type`，而非兼容 JSON 格式。`openai_excel_bps_models` 是历史元数据，不能恢复成隐式绕行条件。
- 保留配置意图、图片 `detail=original`、FUNCTION/FUNCTION_CODE/CUSTOM 的工具身份和源码语义；区分暂停、无权限、内容无效和容量耗尽。
- 准入、存储与恢复有界；旧实例保留图片 URL 生命周期不等于推理负载均衡。发布、生产切换、数据库修改分别验收。

## 待继续验证

以下为继承的待办，未在本轮执行；后续按用户任务选择。

1. 重建实时生产身份、健康、账号池与错误窗口；原交接最后 SSH 复查失败，不证明服务正常或异常。
2. `/v1/messages` 真实端到端、真实 Office 宿主工具往返、更完整图片矩阵；现有 canary 只覆盖 Responses/Chat 和合成工具。
3. 目标负载下的并发、长流、慢消费者、断连、取消、资源释放与图片生命周期。
4. 带 request ID 和原始形态的 unknown 内容诊断；canary 读完响应再关联 usage，同时核对 platform/type。
5. 账号集中不可调度与 403 的根因；带 `integration` 标签的持久化回归；过时的模型名单注释和文档。

## 易误读的历史材料

- v0.0.8 是 **未部署候选**，v0.0.9 修复恢复超时终态。不能把每个 tag 写成依次上线。
- [旧协议审计](docs/BPS_PROTOCOL_COMPATIBILITY.md) 与部分旧发布说明的 native fallback 已被 [0.0.13 规则](docs/bps2api-0.0.13.md) 取代。
- 历史“4/4、5/5”是限定合成样本；25 张图是重复红图，Office 是参数 echo，10 分钟审计窗口含切换前时间。
- 原 `state.json` 停在发布前 focused tests，不是当前待执行动作。历史四角色缺失和失败日志不能掩去。

## 最近开发记录

| 日期（UTC） | 背景与目标 | 实际进展 |
| --- | --- | --- |
| 2026-09-28 | 接续交接，建立可持续项目记忆 | 已完成；8 份文档、38 个相对链接、来源哈希、补丁重建与副本回滚通过 |
| 2026-09-27 | 严格 OpenAI OAuth BPS，避免静默原生绕行 | 本地提交/tag 可核对；生产与限定 canary 为交接历史观察 |

新条目置顶；首页保留摘要，详细经过向专题和会话展开。
