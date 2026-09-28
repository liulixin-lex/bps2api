# 2026-09-28：hosted tools 升级回归根因

[返回开发历程](../../../DEVELOPMENT_HISTORY.md) · [发布记录](2026-09-28-release-0.0.14.md)

后续进展（2026-09-28 06:53 UTC）：用户授权优化后，已在 0.0.15 取消新增拒绝并上线，真实客户端工具往返通过，详见[修复与执行链路](2026-09-28-hosted-tools-fix.md)。下文保留根因轮当时的事实和范围。

## 背景与目标

用户报告升级后再次出现 `basispoints does not support hosted tools; explicitly enable omission or select a compatible channel`，要求找根因。本轮定位与复现，不修改业务代码或生产策略开关。

## 已证实根因

合并提交 `a87bff01a` 在 `backend/internal/service/basispoints/tools.go` 的 `collectToolsAtDepth` 中新增 `if !b.options.OmitUnsupportedTools { return error }`。`git blame` 明确归属本次整合；原 v0.0.13 与固定上游 fe27f9895 的同一分支都没有该检查。

v0.0.13 在 auto 模式碰到已知不支持的托管工具时，跳过这些声明、保留客户端 function/custom 工具，并向模型加入真实的能力限制提示。v0.0.14 改为先要求账号 `extra.openai_excel_bps_omit_unsupported_tools == true`；缺失字段也视为 false，遇到任一托管工具声明便在调用上游前拒绝整条请求。它不等待模型真正使用该工具，只有工具目录中出现便足以触发。

因此这是整合时新增严格校验却没有处理已有账号行为兼容所造成的升级回归。上一轮将这些错误笼统列为“不支持工具”不够准确，未指出此次新增分支是直接原因。不是 required 选择支持失效，也不是上游凭据或模型权限拒绝。

## 线上证据

- 原故障窗口：2026-09-28 06:08:25–06:11:02 UTC（北京时间 14:08:25–14:11:02），账号 31 共 17 次，HTTP 400，upstream_status_code 全为空。
- 账号 31 的省略字段缺失；此后新账号 32 也缺失该字段，因此单纯换账号仍可能重现同一错误。06:21 UTC 账号 32 已可调度，纠正上一窗口“当前无账号”的时效边界。
- 17 次故障没有匹配的请求捕获，错误文本也未记录具体 tool type，不能断言它们全部来自 web_search。判断分支可确定是已知托管工具之一，实际种类仍缺原请求证据。
- 用明确声明 web_search、auto 和客户端 echo 的合成请求调用新线上实例，06:24:16 UTC 真实得到同一 400 / basispoints_request_invalid。

## 同输入复现

同一 Go 探针和 JSON 夹具分别加载 v0.0.13 基线与 v0.0.14 源码，两次命令 exit 0；探针只调用 Prepare，不请求上游。

| 夹具 | v0.0.13 | v0.0.14 |
| --- | --- | --- |
| web_search / image_generation / tool_search + auto，未启用省略 | 接受并附能力限制提示 | 拒绝，复现原错误 |
| 相同托管声明，显式省略 | 旧 API 没有此选项，仍接受并提示 | 接受并提示 |
| 只有客户端 function | 接受 | 接受 |
| 托管声明 + tool_choice=none | 接受 | 接受 |

首次探针因旧 Prepare 不接受新选项而编译 exit 1，保留错误后改用反射适配新旧签名；另纠正了探针的能力提示字符串匹配，不改源码或实测输出。实际命令、输入与输出在 `/bps/artifacts/development-history-20260928/hosted-tools-root-cause`。

## 测试缺口与处理边界

新策略测试将“省略关闭时返回 400”作为预期；上线 canary 测了客户端函数工具，没有混入通常自动声明的托管工具。因此测试通过不等于保留了旧客户端体验。

正确修复需区分可选声明与明确要求执行：兼容既有自动模式时保留真实能力提示，明确强制的托管调用仍不可伪装成功；或按用户选择启用账号省略。省略开关不会实现联网搜索、图像生成等服务端工具。不能把所有账号批量开启有损省略当成无损功能修复。本轮只完成根因确认，未擅改线上开关。

LAST_CONFIRMED_RESULT：已用源码差异、账号字段、17 条错误元数据、同输入离线探针及线上合成请求确认根因。
NEXT_EXECUTABLE_ACTION：本轮定位完成；后续修复需落实明确的兼容策略与回归用例。
