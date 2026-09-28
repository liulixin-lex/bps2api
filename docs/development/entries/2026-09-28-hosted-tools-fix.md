# 2026-09-28：恢复工具兼容与执行链路

[开发历程](../../../DEVELOPMENT_HISTORY.md) · [根因](2026-09-28-hosted-tools-root-cause.md)

## 背景与目标

用户明确要求网关支持工具调用，不接受新增的直接拒绝，并要求参考 ranxi2001/sub2api 改进。本轮覆盖已确认的 0.0.14 hosted 声明回归及客户端工具执行、返回结果与多轮继承；延续本轮线上修复、推送和发版流程。

## 固定输入和上游依据

- 基线 ea23be6c8；候选 fix/bps-hosted-tools-compat，位于 /bps/worktrees/hosted-tools-fix-20260928。
- 已 fetch 并核对上游 production 最新 3dafea660a0a9c1a607c914a0391ddf916673811。该版本 BPS collectTools 无额外 opt-in 检查，已知 hosted 声明被记录后继续收集 function/custom 工具。
- [固定上游工具实现](https://github.com/ranxi2001/sub2api/blob/3dafea660a0a9c1a607c914a0391ddf916673811/backend/internal/service/basispoints/tools.go)与原 0.0.13 一致；上游不是任意托管工具执行服务。本轮不合入最新无关 RPM/管理 UI 更新。

## 修改

- 删除 collectToolsAtDepth 中本 fork 新增的强制拒绝；Prepare、图片预处理、catalog 继承都使用同一自动兼容策略。
- 缺失/false/true 的旧省略字段均不阻断客户端工具；保留字段和 PrepareOptions 兼容性，不批量修改生产账号。
- 账号编辑与批量编辑去掉无实际作用的省略开关，显示自动兼容说明。媒体/密文的独立开关不受影响。
- 保留并测试 function/custom/namespace、required/指定工具/allowed_tools、源码原文、结果回放、目录继承和完整历史跨进程重建。
- 新增 X-BPS-Omitted-Hosted-Tools，仅列固定类型，不暴露工具参数；向模型保留真实能力限制提示。

## 验证与注意

前端 175 项相关测试、lint、含类型检查的生产构建已通过；BPS/服务/handler 的工具、历史、传输、混合目录定向回归均 exit 0。新增混合目录回归第一次错误要求继承目录继续包含已被省略的 hosted 声明；实际 catalog 只缓存可执行客户端工具，修正测试为验证客户端目录和调用仍可用，失败日志保留。下一步进行版本构建及真实上游混合工具往返。

工具参数由客户端执行；网关做协议转换，不把 shell/Office 源码在服务器执行。BPS/参考上游不具备的托管能力仍不能伪造成功。可选声明不再阻断，但明确强制的不可用托管操作仍需真实能力边界。

当前：候选实现与定向测试完成中，下一动作是回归、版本构建、同输入回滚与线上验证。证据目录 /bps/artifacts/development-history-20260928/hosted-tools-fix。
