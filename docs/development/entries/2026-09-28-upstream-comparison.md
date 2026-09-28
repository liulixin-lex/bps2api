# 2026-09-28：bps2api 与当前上游的差异

[开发历程](../../../DEVELOPMENT_HISTORY.md) · [整合记录](2026-09-28-upstream-integration.md) · [0.0.15 工具修复](2026-09-28-hosted-tools-fix.md)

## 背景、范围与固定版本

用户询问现在 bps2api 和 ranxi2001/sub2api 的差异。本轮核对代码和远端引用，补充开发记忆，不进行功能合并或生产切换。

- 本仓库比较点：main `d04914193431e30b500c5b4bc21bc847952bd39b`；应用版本 0.0.15，代码标签提交 `8c4d9d98b`。
- 上游比较点：production `3dafea660a0a9c1a607c914a0391ddf916673811`，已通过 git ls-remote 确认仍是当前远端 HEAD；VERSION 为 2.8.20。
- [上游说明](https://github.com/ranxi2001/sub2api)明确以 production 为生产开发基线，因此不拿其 main 分支代替。
- 共同祖先 `fe27f9895a75e12562f33eff70f21c660329a684`，即此前已整合的上游点。
- 冻结比较结果：304 个文件不同，13,603 行增加、5,028 行删除；包含文档、测试、部署和品牌变化，不能解释成 304 项功能差异。图上上游独有 6 条提交、本仓库独有 49 条，merge 提交计入其中。

## 已确认的差异

| 范围 | 上游 production | bps2api 0.0.15 |
| --- | --- | --- |
| BPS 模型路由 | openai_excel_bps_models 可选择走 BPS 的模型，其余仍可用原有通道 | 账号开启 BPS 后所有模型受该通道约束；旧名单只保留为元数据 |
| 自动处理 403 | opt-in 时将 openai_excel_bps 改为 false，允许后续使用原有路由 | opt-in 时写运行暂停标记、保留管理员配置，暂停账号不再被调度；不因此恢复原生 |
| API 入口 | BPS 分支在 Responses；Chat/Messages 有通用转换，但未接入对应 BPS 转发分支 | 新增 Chat Completions 与 Anthropic Messages 到 BPS 的适配及返回协议转换 |
| 强制工具选择 | BPS Prepare 限定 auto/none | 增加 required、指定 function/custom/namespace 和 allowed_tools，输出整批交付前校验选择约束 |
| 工具源码和历史 | 已有 function/custom/namespace、FUNCTION_CODE、FUNCTION_CMD、目录与回放 | 扩展 code/cmd/command/script/input 字段识别、字符串 schema 分支及本地引用、无歧义 custom 包装恢复、namespace 身份和重复 call_id 防护 |
| 可选 hosted 工具 | 收集阶段省略已知不可执行声明，并给模型能力提示 | 0.0.15 与此行为对齐；另去掉误导性开关并增加 X-BPS-Omitted-Hosted-Tools 诊断头 |
| 历史内容 | 较窄的内容形态转换 | 增加 reasoning/summary 文本及明确图片别名归一化，未知项报告安全 shape；不能把任意未知内容或密文视为已兼容 |
| 图片默认容量 | 每请求 20 张 / 32 MiB；图片在途预算 128 请求 / 1024 MiB | 每请求 500 张 / 50,000,000 去重字节；图片在途预算 32 请求 / 512 MiB，另有解码、处理和磁盘容量约束 |
| 流式与恢复 | 已有 BPS 流式和工具 transport 恢复 | 增加首输出/空闲/总超时、元数据边界、统一输出前恢复预算及更严格终态处理 |
| 模型权限与切换 | 已有 BPS 错误和冷却处理 | 精确识别 model_access_changed；输出前只切同实际模型的其他 BPS 账号，429 同样不切到原生/API-key |
| 运行与发布 | 更新来源 ranxi2001/sub2api；退出等待 5 秒；通用 Caddy 示例 | 更新来源 liulixin-lex/bps2api；默认退出等待 300 秒；单节点 Caddy 避免业务 503 摘除唯一后端，并补充队列深度观测 |

容量是默认值和本地保护边界，不是上游模型承诺或性能测量；管理员设置与部署硬限制可能改变有效值。32 个图片在途槽和 128 个处理阶段请求限制属于不同阶段，不能混为同一个总并发上限。

## 双方已有的功能

账号/API Key/分组、计费、调度、管理后台、代理/Mihomo 等通用底座仍来自同一项目。此前已吸收的观察员与分组隔离、2FA 导入、账号质量检测、403 移组、401 处理、BPS 429 冷却、原生附件、工具目录缓存与 schema 校验不是本轮独有改进。

上游当前也接受 `detail=original`，同样有结构化输出、基础工具历史和 transport 恢复。不能沿用旧文档印象说它完全不支持这些能力。双方都不能凭省略声明获得 BPS 不存在的托管搜索/生成能力；客户端工具最终由客户端执行。

本次检查的 backend/go.mod 和 backend/migrations 无差异，前端 package.json 仅多 packageManager 固定值；版本号 0.0.15 与 2.8.20 分属两套发布序列，不能按数字比较新旧。

## 上游已新增、尚未合入的三项功能

1. `833aa0131`：OpenAI OAuth RPM 控制，包含分钟请求硬限额、调度余量、实际发送/重试计数和管理界面。当前 bps2api 尚无这一套新增控制；既有其他限流仍存在。RPM 不等于 TPM，不能承诺合入即可解除当前上游 token 配额限制。
2. `3a3ebac44`：在上游声明列查询并展示余额，通过兼容上游的 `/v1/usage` 获取余额/订阅等信息，并做缓存和失效处理。不是给所有 OpenAI OAuth 账号恢复额度的功能。
3. `53bad81a1`：Pelican 每组一行横向展示、最新在左、支持滑块拖动。本项目已有 Pelican 功能，但未纳入这一展示更新。

以上另含 3 个 merge 提交，所以图上是 6 条上游独有提交。它们是在前次 fe27f9895 整合点之后出现，并非本轮已评估后判定不值得合入。若后续要求同步，应先检查 RPM 与本仓库 BPS 重试/调度规则的结合。

## 验证与证据边界

本轮执行了远端引用核验、Git 图与源码差异检查，未重新做两仓性能对照；不能仅据新增测试或代码行数断言整体性能和稳定性优于上游。0.0.15 的真实工具往返、图片、流式以及历史测试结果见工具修复条目；Messages 线上验证仍受现有分组权限限制。

证据位于 `/bps/artifacts/development-history-20260928/upstream-comparison-20260928`。文档通过隔离副本补充；沿用既有四角色路径并保留前一轮快照，源码事务的既有行为为 BASELINE hosted 默认拒绝、MODIFIED 接受、ROLLBACK 恢复旧行为与原哈希。本轮仅记录差异，不改变该业务行为。
