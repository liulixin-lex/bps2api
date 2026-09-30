# 2026-09-30：更换 2FA 的登录失败诊断

[返回开发历程](../../../DEVELOPMENT_HISTORY.md)

## 用户反馈与范围

用户在 f8d68ca5 测试站提交后，截图中的 4 条任务显示“ChatGPT 登录失败，未更换 2FA”，并询问为何此前成功、是否仍使用参考仓库方法。本轮继续本机开发/测试站验证/PR #12，不改远端生产，不重放真实密钥更换或退出会话。

## 本轮确认

- 起点 HEAD 5b361257，工作树原本干净。当前任务库 9 条，截图 4 条均 pre_rotation:login_failed、没有新密钥 checkpoint；worker healthy、OOM=false、重启零。原始记录只有通用错误码，不能倒推所有历史失败都是同一个原因。
- 比较真实成功时 fe4267f3、随后 1bf2a621、b0bbeae3、当前 f8d68ca5 的 browser_login.py、patch_upstream.py、login_guard.py、固定 session_phase.py、_nextauth_bootstrap.py，5 个文件 SHA-256 在四镜像完全一致。仍使用固定参考仓库浏览器流程，未因增加菜单/复制功能替换登录引擎。此比较不证明外部访问结果恒定。
- 最新截图账号的受限登录诊断：CSRF 200、signin 200，authorize 403 且 cf-mitigated=challenge；没有提交密码或 TOTP。错误链是 SessionError 包装 LoginBootstrapError(login_interaction_required)，桥接层只看外层文本，因此被误降为 login_failed。
- 另一轮同镜像/账号探针 authorize 302→password page 200，密码验证及 TOTP 验证均 200，随后等待会话 Cookie 超时。说明此轮不能一概归因为 Cloudflare，也没有证据判定密码或当前密钥错误；后续终态正在定位。
- 探针读取加密凭据仅在进程内解密，数据库只读，禁止安全设置与退出写请求，输出仅状态码/白名单阶段/脱敏路径/错误类，不保存 token/Cookie/密码/TOTP。各次结束任务表哈希不变，未发起真实轮换/退出。

## 正在修复

已保留可信 LoginBootstrapError 异常链中的白名单错误码，修复固定上游再次包装异常后丢失原因的问题。不从任意上游正文抓取码，不外泄原异常内容；循环异常链有终止保护。91 项 worker 离线测试通过，尚未部署此增量。

源码/诊断证据：docs-local/login-failures-20260930/；其中 login-image-comparison.json 为镜像逐文件核对，probe-*.jsonl 为受限实测。仍不将修复提示称为已经解决真实登录问题。


## 明确根因与候选实测成功

进一步受限诊断确认：TOTP verify 200 的 continue_url 指向 auth.openai.com/workspace，页面包含 Choose a workspace / Personal account。固定参考仓库的浏览器流程没有工作区选择步骤，直接等待 session Cookie，必然停在该页面直到超时。这是已定位的流程缺口，不能误报为密码/密钥错误或一律归因于 Cloudflare。

已在原 Cookie 等待前补一次原生个人工作区选择：严格官方来源/路径、明确 Personal account 标签、唯一可见控件；团队/未知工作区不猜测，控件缺失或多义失败即停，不自动重复点击。回调和完整会话仍按原流程校验账号邮箱、token 和 Cookie。新增两项白名单错误码贯穿 worker/Go/两种操作界面。

候选镜像 bps-twofa-worker:workspace-candidate 使用同一截图账号的只读凭据诊断实测：authorize 302→password page 200，password verify 200、TOTP verify 200、workspace select 200、callback 302、会话接口 200，get_browser_session 返回成功。随后 mfa_info 200，MFA 启用、原 TOTP 因子可解析。jobs.db 哈希不变，真实更换/退出请求均为零。原前置挑战失败、未修复 Cookie 超时记录分别保留，不把成功覆盖旧失败。

105 项 worker 回归通过；前后端回归、构建及源码/部署回滚正在执行。该实证仅证明候选已修复此账号的工作区卡点并能取得可读 MFA 的会话，不声称所有挑战都可自动通过或已再次更换真实密钥。


### 同输入回归

105 项 worker、39 项前端定向测试、Go service/admin handler 的 TwoFA/SessionLogout/WorkspaceLogin 回归通过；类型与改动文件 ESLint 通过。源码同输入 BASELINE/MODIFIED/ROLLBACK/重建实际执行固定上游的 Cookie 等待代码块：旧版在 workspace 不点击而超时，新版唯一一次个人选择后取得 Cookie；包装后的 challenge 在旧版变成 login_failed，新版保留 login_interaction_required。回滚字节与基线一致，重施恢复候选，错误输出不包含模拟敏感内容。


## 2026-09-30 08:34 UTC：部署与界面验收完成

业务提交 ece34aa0d795e19b116a7bd3824951691c31fab2，测试站版本 0.0.18-pr12.ece34aa0。app/worker 镜像为 bps2api:pr12-ece34aa0 与 bps-twofa-worker:pr12-ece34aa0；后者与真实候选成功登录/MFA 200 的镜像身份一致。

- 实际部署 BASELINE 08:34:08 → MODIFIED 08:34:22 → ROLLBACK 08:34:35 → FINAL 08:34:48 UTC，回滚恢复 f8d68ca5 后重新上线。每阶段健康、版本、原生 workspace 补丁有无、权限/no-store/未确认请求拒绝、守护配置及线上静态资源核验通过。
- 数据库：jobs.db 原 9 条、rotation_requests、session_logout_jobs 的行哈希均不变；两个幂等/退出表结构不变，退出任务仍为空。未改写旧失败任务、未重放轮换、未执行退出。
- 原 XY2API、PG/Redis 容器身份和健康不变，Caddy 未改；远端生产未修改。
- 公网 Chromium 使用本地合成 GET 返回验证两个操作页均能显示工作区/会话未完成原因，额外验证提示不被降为通用错误；真实旧失败记录仍显示其原通用原因，不新增重试按钮。所有管理员写操作拦截，实际转发数零。截图脱敏并查看，版本 ece34aa0 可见。
- 105 项 worker、39 项前端及 Go 定向回归、类型、lint、生产构建已通过；既有 Browserslist/大 chunk/Starlette 提示保留。

源码四角色在 docs-local/login-failures-20260930/；部署四角色在 /opt/bps2api-twofa-preview/evidence/login-workspace-20260930/，均为 MODIFIED_FILE、DIFF_FILE、VERIFICATION.txt 和可执行 ROLLBACK.sh。源码副本回滚/重建与部署镜像回滚已实际执行，交付前重开。通过现有 PR #12 更新，不合并、不发正式版；发布远端 SHA 核对记录见本机 pr-publication.json。

当前完成：已修复并实测这个账号的个人工作区登录分支，已部署测试站。用户刷新确认 ece34aa0 后可自行创建新的更换任务；旧失败记录不会自动变成功。仍未重试历史关闭旧 TOTP 500 任务，也未在本轮实测再次更换密钥或退出全部设备。Cloudflare 仍可独立阻止某次初始化，修复不代表普遍解除挑战。
