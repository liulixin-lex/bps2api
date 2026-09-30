# 2026-09-30：明确拒绝提交的状态与恢复

[返回索引](../../../DEVELOPMENT_HISTORY.md)

## 背景与起点

用户反馈测试账号提交后一直显示“提交未确认”，刷新无效。当前本机分支 feat/account-twofa-rotation-20260929，起点 c35c19a84d5842456dd5ddabe3d87f30329f61c3，工作树干净，测试站 ece34aa0。仅本机开发、测试站部署与 PR #12 更新，不改远端生产。

## 已确认与边界

- 本轮接续前只读数据库：9 个轮换任务、9 个幂等记录、0 个退出任务，无活动任务；关联旧任务 eddbfb3cae3540d8b96f22632049c095 的关闭旧 2FA 操作发生 HTTP 500，无新密钥 checkpoint，仍需保护。
- 静态核对：worker 在记录请求之前对未解决任务返回 409/account_has_unresolved_job；Go 将其变成普通错误和 503，前端所有错误均被显示成提交不确定，导致输入持续锁定。
- 尚未对用户账号发送本轮 POST；不把源码推断说成真实 HTTP 409 实测。不重放真实轮换/退出，不直接改库解除旧任务保护。

## 本轮修改

Go 仅识别新提交路径上的固定 409 detail 白名单，响应固定 reason 和 submission=not_accepted；明确拒绝与真正不确定保持区别。前端释放被明确拒绝项及其内存凭据，批量后续项继续，显示原因与相关历史任务链接；未知响应和通信失败保留原 ID。共享退出流程同步修复。本地输入校验错误明确标记未接收。

## 验证与交付

已完成拒绝/未知冲突/断网/混合批量回归、同输入源码三态与重建、测试站部署及实际回滚、浏览器验收。源码四角色位于 docs-local/submission-rejection-20260930；修改前逐文件原字节/哈希已保存，新文件基线为不存在。旧任务保护及历史凭据导出限制不变。

### 已完成的本地回归

61 项前端定向、105 项 worker、Go TwoFA/SessionLogout/WorkspaceLogin service 与 handler 定向回归通过，vue-tsc 与修改文件 eslint 通过。三个固定 UI 输入在 BASELINE 全部复现、MODIFIED 全部通过、ROLLBACK 再次复现、DIFF 重建再次通过；逐文件原字节恢复与补丁重建哈希一致。后续构建和部署验收亦已完成，详见下节。

## 测试部署与浏览器实测

2026-09-30 09:01:24 UTC 最终启用应用业务提交 e72f486a2201c70839d88a58e07c86777364a4d1，版本 0.0.18-pr12.e72f486a，镜像 bps2api:pr12-e72f486a。worker 保持 bps-twofa-worker:pr12-ece34aa0，容器身份未变。

- 测试站 BASELINE → MODIFIED → ROLLBACK → FINAL 实际切换完成；公网与回环健康、版本、最新静态资源字节、鉴权、no-store、前置拒绝 envelope 均验证。合成 confirmed=false 请求停在前置校验，不触发 worker。
- Chromium 在实际部署的页面中拦截三个合成提交：第一次模拟响应丢失保留原 ID，刷新只读取，第二次用原 ID 得到明确拒绝后清除凭据并恢复输入；退出页明确拒绝也恢复输入。关联任务链接可见可点击，旧任务不出现重放/导出按钮。未向真实 worker 转发这些合成提交。
- 浏览器另读真实任务历史，9 条记录完整，eddbfb3cae3540d8b96f22632049c095 仍显示关闭旧 2FA 返回 5xx 的待核查状态。截图已人工视觉检查；真实历史截图隐藏邮箱和任务标识。
- worker 数据只读哈希：jobs 9、rotation_requests 9、session_logout_jobs 0，三态及最终记录字节摘要一致。Postgres、Redis、worker、旧 XY2API 容器身份与 Caddy 配置未改变。维护服务配置亦不变。
- 本次没有用用户的密码/TOTP 发起登录、轮换或退出；不能声称本账号已更换成功、旧 500 已解决，或真实全部设备退出已验证。

首次在仓库根直接调用 Vite 缺少 vue-tsc PATH，构建立即停止；改在 frontend 目录补齐本地 .bin PATH 后构建通过。保留既有 Browserslist、依赖弃用与大 chunk 提示，不声称全仓库 CI 全绿。

## 交付状态

本轮提交状态修复及测试站验收完成；PR #12 已更新，保持 open、未合并。旧任务最终变更状态仍待独立核验，不允许改库解除保护或重放；前端明确拒绝修复不等于完成了账号更换。

四角色：源码 docs-local/submission-rejection-20260930/{MODIFIED_FILE,DIFF_FILE,VERIFICATION.txt,ROLLBACK.sh}；部署 /opt/bps2api-twofa-preview/evidence/submission-rejection-20260930 同名四角色。源码回滚脚本仅恢复本事务列出的字节；部署回滚脚本仅切回原应用镜像及部署证据，不改数据库。账号凭据不写入 Git、日志或本记录。
