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
