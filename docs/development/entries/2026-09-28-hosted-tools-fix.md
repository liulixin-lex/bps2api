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

前端 175 项相关测试、lint、含类型检查的生产构建已通过；BPS/服务/handler 的工具、历史、传输、混合目录定向回归均 exit 0。新增混合目录回归第一次错误要求继承目录继续包含已被省略的 hosted 声明；实际 catalog 只缓存可执行客户端工具，修正测试为验证客户端目录和调用仍可用，失败日志保留。

同输入三态验证使用 8 个固定样本：BASELINE 在省略字段 false 时拒绝 web_search/image_generation/tool_search；MODIFIED 全部接受，6 个带 hosted 声明的 auto 样本含能力提示；ROLLBACK 在独立副本恢复旧拒绝，所有源码文件字节和模式等于基线，再应用补丁精确重建候选。四角色沿用原绝对路径，前一轮角色归档至 hosted-tools-fix/previous-role-snapshot。

真实 BPS 验证覆盖 function/custom/namespace 各一次完整往返，目录混合 web_search/tool_search，required 发起调用。客户端核验参数后执行带随机盐的收据计算，再以结果续接，模型精确返回该收据。暂存端口和切换后公网各 6 个请求全部 200，12 条 usage 记录均为 OpenAI OAuth / BPS / `/basispoints/api/responses`。文本、Chat SSE（单个终态）和 detail=original 原图输入在两阶段各 3 项通过。

第一组真实混合样本含 image_generation，被现有账号组策略在进入 BPS 前以 403 拦截。相同输入对照 0.0.14 与 0.0.15，均为 `Image generation is not enabled for this group`；后续三类工具往返移除这一未获分组授权的声明，离线混合目录仍包含 image_generation。未更改分组权限，不能把此 403 误记为 BPS 修复失败或真实图片生成成功。`/v1/messages` 的已有组限制另见 0.0.14 条目。

工具参数由客户端执行；网关做协议转换，不把 shell/Office 源码在服务器执行。BPS/参考上游不具备的托管能力仍不能伪造成功。可选声明不再阻断，但明确强制的不可用托管操作仍需真实能力边界。

上线观察窗口为 06:53:34–07:01:04 UTC：43 条成功用量均为 BPS；19 条错误中，本轮 hosted 拒绝和 type=unknown 各 0。另有 17 条真实上游 gpt-6-astra TPM 限流（429）及合成分组权限对照产生的 2 条 403。此短窗口不等于全流量零错误，也不代表代码能解除上游配额；没有静默换模型或绕行原生。

## 部署、发布与恢复

- 源码与标签：`8c4d9d98b1ddc78cfdc346791bede4b11976ad4a` / `v0.0.15`，2026-09-28 06:54 UTC 已原子推送 main 与 tag。发版工作流 [36388791867](https://github.com/liulixin-lex/bps2api/actions/runs/36388791867) 全部成功；[正式 Release](https://github.com/liulixin-lex/bps2api/releases/tag/v0.0.15) 于 07:01:29 UTC 发布。
- 发布包含 5 个平台压缩包及 checksums.txt 共 6 个附件；下载 linux/amd64 并验证 SHA256 为 `cbc36ab9e69151202d0c8fea5f2ca0591e16d6b18c74e77e12693a48330d1d6c`，二进制 `--version` 为 0.0.15 / 8c4d9d98b。GHCR 0.0.15 清单包含 linux/amd64 与 linux/arm64；unknown/unknown 是构建证明清单。线上本地构建和远端构建的代码提交相同，构建时间和文件哈希不同。
- 按用户要求先线上后远端：06:53:34 UTC 公网主路由切至 `sub2api-v015` / 8090。版本命令与镜像标签均指向上述代码提交；本地镜像 ID `sha256:963fdccef2248f4754a224b3189a7a25164de5d9880eabb43de6e09bb9aabfb8`。
- 旧服务未停止或重建；图片所有者链为 8090、8089、8088、8087、8086、8085、8083、8084、8082、8080、8081。所有旧容器 ID 保持，health 为 200/ok。本轮无数据库迁移或账号配置修改。
- 部署快照：`/opt/sub2api/backups/v015-20260928/transaction`。生产路由恢复命令：`python3 /opt/sub2api/deploy-v015.py rollback`，恢复 8089 主路由并保留新图片所有者。候选恢复路由已通过 caddy validate；本轮没有在公网执行回滚，已执行的是独立源码副本回滚。
- 曾误将本地 node_modules 符号链接纳入未推送候选，发布前移出索引、修正本地排除并重新构建；旧本地构建日志保留，实际部署和发布只使用 8c4d9d98b。

证据目录 `/bps/artifacts/development-history-20260928/hosted-tools-fix`；命令及原始输出位于同级 `upstream-20260928/hosted-fix-*`。四角色为 `/bps/artifacts/development-history-20260928/{MODIFIED_FILE.tar.gz,DIFF_FILE.patch,VERIFICATION.txt,ROLLBACK.sh}`，回滚依赖同目录 BASELINE_SOURCE.tar.gz。本轮修复、部署、推送、发版与产物核验完成。后续按新任务观察真实负载、权限及上游配额。
