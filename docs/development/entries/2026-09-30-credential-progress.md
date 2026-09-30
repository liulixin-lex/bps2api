# 凭证任务进度与退出历史删除（2026-09-30 UTC）

## 背景与目标
用户要求退出所有会话、更换 2FA 时显示转圈和进度，并允许删除退出历史。保留每页 10 条、最多两次 MFA、全自动及原请求幂等保护；GitHub/PR 继续暂停。

## 起点与当前状态
工作树 feat/account-twofa-rotation-20260929 / a9d9ef0e，保留未提交 main 合并及上一轮修改。部署起点 local-history-7f47476955。新事务原字节/哈希位于 docs-local/credential-progress-20260930；本轮只修改相关功能。

## 设计与实际进展
- 本轮静态核对：新增任务级白名单阶段回报，分开旧密钥登录和新密钥验证。不转发 URL、原始日志、验证码或令牌。
- 页面显示执行摘要、转圈、当前步骤及耗时；刷新失败标记状态未知，终态停止转圈，无虚构百分比。
- 退出历史勾选/本页全选删除；worker 在共享锁内整批验证，事务保存 request_id+HMAC 墓碑后删除，无凭据落盘。进行中及 needs_review 不可删除。
- 本轮实测：143 项 worker、137 项前端（15 文件，含 locale）通过；Go service/handler 两包定向、TypeScript、ESLint、前端/后端及 worker 构建通过。真实 SQLite 在隔离无网络容器内验证删除/重启幂等保护、旧轮换结果保护；不涉及真实账号。
- 构建初次因 .dockerignore 未放行新增 progress.py 失败，补白名单并保留失败日志后重建成功。候选 0.0.18-pr12.p1 / c5cd783961，原 0f662b1a 底座叠加上一轮及本轮改动，不部署未提交 main 全部合并。
- 本轮实测：12:44:19 UTC 已部署 0.0.18-pr12.p1；app/worker 均为 local-progress-c5cd783961 且健康，公网/本机 API、鉴权、no-store、无效删除输入保护通过。BASELINE → MODIFIED → ROLLBACK → FINAL 镜像回滚实测通过；原 17 条更换 / 5 条退出任务、rotation_requests、rotation_latest 行与设置哈希不变，Caddy/其他实例未改。
- 本轮实测：公网浏览器使用合成拦截任务验收两页转圈动画、阶段切换、耗时跳动、待验证终态停转、断线提示/恢复、跨页进度摘要、每页 10 条、保护行禁选、勾选 1/2 条删除与删后页码修正。真实页面只读加载通过，截图为合成或遮罩脱敏；未向真实账号提交更换、退出或删除历史。
- 当前状态：本轮完成；源码保留工作树，未提交、未推送，PR #12 继续暂停。

## 验证与四角色
源码四角色：docs-local/credential-progress-20260930/{MODIFIED_FILE,DIFF_FILE,VERIFICATION.txt,ROLLBACK.sh}；逐字原件/哈希、固定合成输入 BASELINE/MODIFIED/ROLLBACK/重施补丁及字节相等已验证。

部署四角色：/opt/bps2api-twofa-preview/evidence/credential-progress-20260930/ 同名四文件；数据库快照/哈希与 phases.jsonl 独立留证。离线镜像 persistence.log 证明退出墓碑在 SQLite 重启后仍拦截原请求，且上一轮最新密钥/旧结果保护继续有效。

浏览器 browser-result.json / ui/ 为本轮实测。真实 OpenAI 登录、更换 2FA、退出所有会话未在本轮重新执行，不以界面合成验收代替上游端到端结果。

## 回滚边界
源码四角色只在副本验证；部署脚本在关闭提交入口后重查活动任务，禁止中断活动操作。退出删除墓碑表为兼容新增表，保留不回删；旧 worker 不认识墓碑，因此回滚脚本在已有退出删除墓碑时拒绝退回旧版，以免同标识再次执行。
