# 2026-09-29：PR #12 独立测试部署

[返回开发历程](../../../DEVELOPMENT_HISTORY.md)

> 状态：2026-09-29 18:30 UTC app/worker 更新为 b0bbeae3；关闭旧 TOTP 的 HTTP 500 原因提示已部署，旧成功记录的过期操作已隐藏。
> 证据：用户真实任务已更换并验证成功；本轮仅读取既有结果、验证剪贴板和页面、演练镜像回滚，没有新建轮换或继续验证请求。

## 背景与目标

用户在 PR #12 创建后追加授权：拉取 BPS2API 最新主分支、叠加 PR，并让 test.aibong.cn 临时使用此版本；保留原 XY2API 测试实例以便回切。远端生产源码与生产应用不属于本次部署范围。

## 起点与版本

- 仓库 liulixin-lex/bps2api；分支 feat/account-twofa-rotation-20260929。
- 已同步并复核的 main：2dfb76d7fb361a42824545cc406f9c2ef92ff8ac。
- 初次部署业务代码：a7106a31aef11ae8cb7328c5449e3d2def1380eb；版本 0.0.18-pr12.a7106a31。此后的记录提交仅改文档，不能混作二进制 revision。
- 构建嵌入已验证前端，Go 1.27.0、CGO_ENABLED=0；runtime 使用官方 0.0.18 固定 digest。worker 固定上游提交 9fa8b481a2e5252ce2782833f6d1e0623c68fdce。
- 原测试域名反代 xy2api-promotion-preview-app:8080；新反代 bps2api-twofa-preview-app:8080。

## 实际完成

部署目录 /opt/bps2api-twofa-preview，使用独立 PostgreSQL、Redis、app-data 与 worker 加密数据卷，未导入旧站业务数据。管理员初始化信息沿用原测试实例的初始化配置；新数据库密码、JWT/TOTP 加密材料、worker token 与 Fernet key 独立生成，仅存放于权限 0600 的本地 env 文件。原 XY2API 应用、数据库和 Redis 持续运行。

新应用仅绑定本机 127.0.0.1:18081，通过已有 Caddy 接入测试域名。worker 不发布宿主端口，以非 root、只读根目录运行。仅原地修改 Caddyfile 的 test.aibong.cn upstream，保留单文件 bind mount inode；其他域名配置字节保持不变。

管理入口为 /admin/token-guard（智能运维 → 凭证守护）。前端已包含本 PR 的 2FA 面板，私有 worker URL/token 已通过管理接口保存。此 worker 用于 2FA 更换，不替代原独立 OAuth 登录/重登导入服务。

## 本轮验证

| 对象 | 实测结果 |
| --- | --- |
| 应用、PostgreSQL、Redis、worker | 四个容器 healthy；/app/sub2api -version 匹配部署版本 |
| 本机与公网 HTTP | /health 为 200 且 JSON status=ok；管理员登录成功 |
| 前端 | /admin/token-guard 可达；TokenGuardView 静态文件与本地 PR 构建字节一致 |
| 鉴权 | 未登录访问 2FA 管理接口为 401；worker 无 bearer 为 401 |
| worker | 应用容器可访问 worker health；worker 已鉴权任务列表为空、Cache-Control=no-store |
| 回切同输入 | BPS → XY2API → BPS 三次 /health 均 200；回滚后整个 Caddyfile 哈希恢复基线，重施后恢复候选 |
| 原实例 | 容器 ID、镜像和 mounts 与部署前一致，仍 healthy；原 DB/Redis 持续运行 |
| 管理接口 | 确认前为 423；用户明确授权后，本机/公网版本查询及 worker 列表往返均 200；未确认提交 400 且列表仍为空 |

未执行浏览器交互自动化、真实账号登录/轮换或推理请求。测试站使用空的独立业务数据库，不应将健康检查等同于推理可用。CI 仍沿用限定结果口径，不声称全部检查全绿。

## 接线完成与后续边界

新安装的管理接口要求当前管理员确认《Sub2API 部署与运营合规承诺》v2026.06.10。用户在本轮明确回复“我已阅读并同意，授权你代为确认”，已于 12:20 UTC 通过原 POST /api/v1/admin/compliance/accept 接受，没有写库绕过 gate。此前受阻的历史记录保留。

以下两条命令已经顺序执行并通过，结果分别保存 private-validation.json 与 public-validation.json：

1. python3 /opt/bps2api-twofa-preview/verify_instance.py http://127.0.0.1:18081 --configure
2. python3 /opt/bps2api-twofa-preview/verify_instance.py https://test.aibong.cn

脚本从私有 env 读取 worker token，仅通过应用管理 API 保存配置，核验已鉴权空任务列表、no-store、未登录拒绝、未确认提交拒绝和无新增任务，不创建真实轮换任务。随后 verify_deployment.py 再次核对全部容器、版本、私有 worker、原实例和回切证据，结果通过。

本轮部署验收完成。真实 2FA 端到端仍需用户提供专用账号及恢复方式；当前真实账号操作数为零，不声称自动登录/更换所有账号均已实测成功。

## 证据与回切

部署是独立事务，四角色位于 /opt/bps2api-twofa-preview/evidence/：MODIFIED_FILE、DIFF_FILE、VERIFICATION.txt、可执行 ROLLBACK.sh。本轮已逐一重开、核对归档哈希及脚本与实测版本的字节一致性。完整环境秘密不在四角色归档或 Git 中；env 和数据卷必须保留。

回切命令为 /opt/bps2api-twofa-preview/ROLLBACK.sh，已实际演练。仅把测试域名指回原 XY2API，保留两套服务和数据，不撤销真实账号的任何外部 2FA 操作。

部署记录本身使用独立文档事务 docs-local/twofa-preview-deployment-docs/，相对 a7106a31 保存原文档、补丁与副本回滚验证；不替换原功能三态验证。

## 失败与纠正

最初独立部署审计用 bare sub2api 检查版本，因容器 PATH 不包含 /app 而得到 127；依据实际镜像配置改为 /app/sub2api 后审计通过，未改应用。12:18 UTC 管理接口的 423 属于当时真实待确认状态；12:20 UTC 收到用户明确授权后接受声明，随后接口验收通过。两个阶段分开留证，没有以通过结果覆盖先前错误。

## 2026-09-29：用户真实任务的 409 登录错误诊断

用户在部署验收后自行提交真实账号任务，截图显示“结果不确定，需人工核查”。本轮仅只读检查运行中 worker 的加密任务记录和固定版本源码，未重试登录、未提交轮换、未重启服务、未修改账号状态或数据库。

本轮实测：该任务 mode=change_2fa、status=error、error_kind=technical_error；错误为首次登录的 authorize/continue HTTP 409 invalid_state，login_verified=false、rotated_pending_verify=false、retry_count=0、password_changed=false。根据错误抛出位置与执行顺序，此次任务失败于初始登录，未进入更换 2FA 的步骤。409 的进一步原因尚未确认，不能直接判定为密码错误、封号、代理或特定账号类型。

本轮静态核对：固定上游 mfa_phase.py 使用 chatgpt.com/backend-api，service.rotate 先用旧 TOTP 登录，再执行 TOTP 更换和新密钥登录验证。因此本功能从一开始针对 ChatGPT/OpenAI 的 TOTP，并非邮箱服务或 BPS 管理员自身的 2FA；邮箱只是该订阅账号的登录标识。

worker.py 的 summary 将未进入已保存新密钥待验证状态的通用 technical_error 一律映射为 needs_review，前端显示“结果不确定，需人工核查”。这是为了阻止非原子轮换异常被自动重试，但也将此次明确的前置登录失败归为过于笼统的提示；不代表已成功更换或官方确认账号安全状态不明。

之前的“验收通过”仅指部署、鉴权、worker 通信和离线回归，不包括真实 OpenAI 登录/轮换；本次真实登录失败另记，不覆盖原验收记录。下一开发事项是检查登录会话状态流程，并将前置登录失败与轮换中断后的待核查状态区分；本轮尚未修改应用代码或重试真实账号。脱敏证据及文档事务四角色在 docs-local/twofa-login-409-diagnosis/。

## 2026-09-29：初始化拒绝后的状态流修复（候选）

用户明确要求继续修复。一次受限的真实登录诊断复现了更早的失败：GET auth.openai.com/api/accounts/authorize 返回 403，原纯 HTTP 登录实现未验证状态码，随后仍调用 authorize/continue，得到 409 invalid_state。该诊断仅尝试登录，显式禁止 MFA 设置写入；没有轮换真实账号。CSRF 比较初版未解码 cookie 分隔符，不能据其 false 推断 CSRF 不匹配；已在本地纠正诊断代码并保留原输出。

候选对固定上游文件做原 SHA-256 校验后插入 authorize URL/OAuth state 与响应状态检查；403、429、额外验证或非法来源直接停止，拒绝后不继续提交，不增加指纹或代理轮换。每个服务登录阶段只尝试一次。修复不能将上游拒绝访问变成成功，真实自动登录仍有外部访问门槛。

新增显式轮换阶段适配器：进入上游轮换调用前失败可标记为 login_failed/preflight_failed；一旦进入可能修改 2FA 的调用，仍沿用待核查和仅验证限制。固定版本历史记录的初次登录专属错误作只读映射，不写库改状态；只对确证未轮换的失败允许用户重新确认并提交新任务。后端仅传白名单诊断码；中英文界面显示 ChatGPT 账号、具体错误和未更换状态，不回显原响应、Cookie 或凭据。

隔离测试：worker 29 用例通过；前端类型、改动文件 lint 与 18 个相关用例通过。同一离线 403 输入：基线初始化两次后继续一次并报 invalid_state/needs_review；候选初始化一次后停止、继续请求为零，显示 login_failed。Go 定向回归与部署检查分别留在 docs-local/twofa-login-fix/，按实际完成结果追加。未把真实账号登录或更换记为成功。

候选阶段补充实测：Go 定向 28 个顶层用例及三个相关包通过，前端生产构建通过，worker 镜像完成。一次独立只读挂载真实任务数据的登录诊断，修复版在首次 auth 初始化 403 处返回 login_access_denied，未发送 authorize/continue 或密码验证请求，没有真实 2FA 操作。解码后 CSRF body 与 cookie 匹配；外部访问拒绝仍存在，不能称真实登录已修复成功。

初次镜像构建因既有 .dockerignore 未放行新增模块而失败，补齐精确文件白名单后构建通过；原失败日志保留。本轮源码四角色以 d7df53f3 为基线，在独立副本恢复原字节并复现旧 403→409 行为，再重施补丁复现新行为；完整输出在 docs-local/twofa-login-fix/。

## 2026-09-29 14:44 UTC：修复版上线及当前边界

测试站应用和 worker 已更新到业务提交 f273b2a7cc2c323287a9bcfa2e75559392814e27，版本 0.0.18-pr12.f273b2a7。后续交付记录提交只改文档，不改变部署二进制来源。当前管理员列表把用户原任务显示为 login_failed / login_state_invalid（“ChatGPT 登录失败，未更换 2FA”）；历史原始加密行未改写，也未自动重试。

实际执行 BASELINE → MODIFIED → ROLLBACK.sh → ROLLBACK → FINAL：本机和公网健康、管理员登录、精确版本、任务状态、no-store、未登录 401 与未确认提交 400 均通过；候选的 TokenGuardView 资源与构建字节一致。旧版恢复 needs_review，新版恢复 login_failed，说明行为来自代码而非写库改结果。整个任务表前后哈希和行数完全一致。Caddy 配置、原 XY2API 容器及健康状态保持不变，PG/Redis 未重建。

部署事务四角色位于 /opt/bps2api-twofa-preview/evidence/login-fix-20260929/。其中 ROLLBACK.sh 已实际执行，只恢复本次前的 BPS 应用/worker 镜像并保留数据；/opt/bps2api-twofa-preview/ROLLBACK.sh 仍用于把域名回切到原 XY2API，两种回滚对象不同。应用与 worker 旧镜像均保留。

当前边界：修复了错误地在 403 后推进登录状态和过度笼统的报错，没有解除 OpenAI 的访问拒绝。修复版真实登录诊断在初始化 403 处停止，密码验证请求和 2FA 更换请求均为零；尚未取得登录成功或真实轮换成功结果。已询问用户同一账号在常用浏览器的登录情况，待回答后区分是否需要人工验证或处理登录环境；没有通过轮换代理、指纹或重复提交来规避拒绝。

## 2026-09-29：浏览器正常登录后的访问诊断

用户确认原密码和原 2FA 可在常用浏览器登录。本轮在同一 worker 网络做一次受限初始化诊断：authorize HTTP 403 同时包含 cf-mitigated=challenge、Cloudflare server、HTML challenge-platform、Just a moment 与启用 JavaScript 提示。密码、TOTP 与轮换请求均为零，jobs 表哈希不变。已确认该响应为浏览器验证页，不能据此判断账号或原 2FA 错误。

独立标准 Chromium 无凭据访问 ChatGPT 登录页也停在 challenge，无可见邮箱字段。根据用户建议，核对本仓库 PKCE 配置及官方 openai/codex 源码（18194bfd3534ca567d886eac454028dafaa68b6c），再分别用现有 HTTP 客户端和标准 Chromium 访问 /oauth/authorize；二者均遇到 challenge，未进入密码、MFA、workspace select 或 token exchange。官方设备授权 usercode 在宿主标准客户端得到 530，在 worker 既有客户端得到 200 并签发设备代码；这只证明该接口可达，不能证明令牌能更换 2FA。已向用户发送独立的临时设备授权，仅准备在账号身份匹配后只读查询 mfa_info，禁止轮换与令牌持久化，等待用户完成官方授权。

本轮小修复把明确 challenge 信号的判定提前到通用 403 之前，返回现有白名单 login_interaction_required；普通 403 不变。33 个 worker 离线测试与非 root/只读/无外网镜像 smoke 通过，尚未部署此增量。诊断失败也保留：浏览器镜像未完成时的首次启动失败、只读 home 导致 crashpad 启动失败；改用独立 tmpfs home 后才完成浏览器实测，未更换指纹、代理或操作验证码。证据在 docs-local/twofa-access-diagnosis/；上一轮日志和真实任务保持原样。

## 2026-09-29 15:48 UTC：挑战分类增量部署与上游完整调用链审计

worker 已部署 c09a6233，应用仍为 0.0.18-pr12.f273b2a7。独立 BASELINE→MODIFIED→ROLLBACK→FINAL 验证通过：明确 challenge 从 login_access_denied 变为 login_interaction_required；管理 API、真实任务哈希、Caddy 与应用/PG/Redis/原 XY2API 容器不变。四角色在 /opt/bps2api-twofa-preview/evidence/challenge-worker-20260929/，回滚已实际执行。

设备授权阶段补充：usercode 签发 200 后，轮询 deviceauth/token 返回 cf-mitigated=challenge 的 403，因此诊断立即停止，没有换取令牌或查询 MFA。已通知用户不再使用该代码。不能把代码签发成功等同于设备授权登录成功。用户随后明确要求完全无人操作；后续不再以手工授权作为解决方案。

重新查询上游 main 仍为固定的 9fa8b481a2e5252ce2782833f6d1e0623c68fdce（提交日期 2026-08-22），核心六个源文件与原部署固定源码逐字一致。实际调用链为 server→TwoFAJobManager→TwoFAService._resolve_dependencies→get_session_pure_request→rotate_2fa；默认并未自动使用 Camoufox。HTTP 实现使用 curl_cffi、统一浏览器请求特征、NextAuth、Sentinel、密码与 TOTP 验证，再取得 ChatGPT 会话与 Cookie；真正的更换调用 ChatGPT backend-api 的 mfa_info、disable_in_house、enroll、activate_enrollment，之后以新密钥重新登录。

上游的 HTTP 指纹回退仅覆盖 TLS 错误及 ChatGPT prime/CSRF 的特定 403；不覆盖当前 auth authorize 的挑战。浏览器函数虽存在，但引用的 browser_phase.py 没有发布在该提交中，不能简单切换函数就称默认更换流程已具备浏览器回退。README 中的 Camoufox 安装说明与默认纯 HTTP 调用链必须分开看。

按上游依赖和固定浏览器版本构建独立诊断镜像：camoufox 0.5.4、playwright 1.60.0、official/stable/152.0.4-beta.28。沿原 NextAuth browser helper 初始化，标准 Chromium 仍遇到挑战；Camoufox 无人工操作得到 auth 302→200，并显示正常邮箱输入框。仅为诊断以普通 page.goto 补足缺失的导航函数，没有修改线上 worker 引擎。

一次真实账号浏览器登录诊断已得到 password/verify 200、mfa/issue_challenge 200、mfa/verify 200，各提交一次；没有更换请求，jobs 表哈希不变。但上游等待 ChatGPT session-token Cookie 超时，所以尚未取得可用会话或验证 MFA 设置读取。正在检查登录后的回调/workspace/会话识别，不能把密码和 TOTP 校验成功写成整条登录与更换成功。证据保存在 docs-local/twofa-access-diagnosis/。

随后一次保留浏览器上下文的诊断成功取得分片 NextAuth Cookie 和正确账号的 /api/auth/session，密码/TOTP 各验证一次均 200；用会话令牌与导出的 Cookie 只读 GET mfa_info 同样 200，显示 MFA 已启用，数据库不变，轮换数仍为零。第一轮 Cookie 超时记录保留，尚不能断言该超时的唯一原因。已确认配套 Camoufox 可以在当前网络无人操作地完成登录，下一步是在桥接层补足缺失导航模块、隔离浏览器配置并接入该引擎，而非要求用户手工授权。

## 2026-09-29：无人值守浏览器登录接入候选

新增 browser_login.py；固定上游文件经哈希校验后补足导航依赖与临时配置入口，选择 Camoufox 引擎。每次登录（包含更换后验证）使用独立临时 profile，验证会话邮箱/token/Cookie，异常或取消后清理，不开放控制端口；登录阶段阻止 MFA 设置写入并限制密码/TOTP 重复提交。原 HTTP 模式可显式配置，默认镜像改为 Camoufox。

49 个离线用例通过，覆盖引擎选择、独立 profile、身份匹配、异常/取消清理、挑战停止、登录网络边界、原持久化与阶段限制。相同模拟输入下，旧版本仍走被拒绝的 HTTP 登录，新版自动浏览器登录后模拟轮换并以新密钥重新验证；副本回滚恢复旧行为，重施补丁恢复新行为。模拟轮换不等于真实账号更换。

第一次正式镜像的真实登录探测未成功，离线启动复现缺少 libX11-xcb.so.1；与此前含 Chromium 依赖的诊断镜像环境不同。已增加系统库，并在构建时按浏览器自身库目录检查动态链接依赖。首次 ldd 检查未设置浏览器库路径造成的构建失败保留；修正检查路径后镜像完成。尚未将该浏览器候选部署到测试站，正在复验。源码四角色位于 docs-local/twofa-browser-login/，与前一挑战分类事务分开。

补齐依赖后的正式候选镜像复验通过：相同非 root/只读/noexec tmpfs 限制下离线浏览器启动成功；调用实际 StagedRotationService 所选 get_browser_session 无人工登录成功，身份匹配、MFA 查询 200、原 TOTP 因子解析成功，临时 profile 已清理，数据库哈希不变，更换数零。

## 2026-09-29 16:18 UTC：无人值守浏览器版本上线

业务源码 fe4267f3，worker 镜像 bps-twofa-worker:pr12-fe4267f3（sha256:862d8755e32f0d71aa0a5bfc3ecd5ec70a41bebbf6c7082c2a8ee0a0eeb64374）已上线；应用/前端保持 0.0.18-pr12.f273b2a7。只调整 worker 镜像与所需的内存、PID、共享内存和临时目录；没有添加对外端口。运行实例实际选择 get_browser_session，三个关键模块哈希与仓库一致。

独立 BASELINE→MODIFIED→ROLLBACK.sh→ROLLBACK→FINAL 完成；旧镜像选择 HTTP，新镜像选择 Camoufox，回滚后/最终恢复均符合预期。本机和公网管理 API、no-store、原失败任务状态检查通过，真实任务表前后哈希不变；Caddy、应用、PG/Redis 与旧 XY2API 容器未更换且 healthy。源码与部署四角色分别保留，部署目录 /opt/bps2api-twofa-preview/evidence/browser-worker-20260929/。

已完成的实际能力是：本账号在当前环境中无需人操作即可登录，取得 ChatGPT 会话并读取启用的 MFA 状态和原 TOTP 因子；生产候选镜像已实测，线上部署使用同一镜像和源文件。没有新建真实轮换任务，没有自动重试用户原失败任务，也没有更换原密钥。更换流程与新密钥复验有离线模拟覆盖，但真实更换尚未实测；新环境/账号的邮箱验证、SSO 或交互式挑战仍可能阻止完全自动化，不能承诺所有 Cloudflare 挑战都能自动通过。

## 2026-09-29 16:28 UTC：交付复核与根索引纠正

接续时重新读取本机 Git、正式候选登录证据、部署结果和容器状态：HEAD 为 4921adc1，应用/worker/PG/Redis 四个容器 healthy，worker 仍为 fe4267f3。既有实际登录记录为 login_success=true、mfa_read_status=200、mfa_changes=0；本次仅复核记录与运行状态，没有再次登录或更换密钥。PR 远端状态沿用 16:24 UTC 的独立查询证据，本次未重新查询、推送或合并。

发现根索引“本地 PR 开发”仍保留“真实登录在初始化处 403、待用户确认浏览器”的旧阶段摘要，与后续已完成验收不一致；已按现有证据纠正，历史详细失败记录保留。此补充仅为本地文档更新，不改变已部署源码或已推送 PR 身份；文档同输入回滚与四角色在 docs-local/twofa-browser-delivery-docs-20260929/。真实更换及新密钥真实登录验证仍待执行。

## 2026-09-29：完整凭据复制与默认页面精简（已部署）

用户实际提交后反馈“更换并验证成功”，要求复制改为邮箱----密码----新2FA密钥，同时指出凭证守护页面混入过多无关功能。只读任务列表复核发现 1 个 success/login_verified=true 的任务（16:40:19 UTC 创建）和 3 个旧登录失败任务，无 queued/running；这补充了先前“真实更换尚未验证”的阶段边界，本轮未再次更换账号。

源码新增受限结果接口的原密码字段；前端仅在显式点击“复制更改后的账号凭据”后读取，保持原始密码字符并按四横线拼接三项，不在任务列表或提示中展示明文。既有成功任务从加密数据库恢复，无需重新输入或重新轮换。复制缺少任何字段时拒绝覆盖剪贴板，兼容滚动升级时的旧 worker 响应。

同一 URL 默认进入“自动更换 2FA”，仅展示提交、任务状态和复制结果；维护服务设置折叠。原巡检统计、探活、自动重登、调度恢复、通知移到“令牌守护（高级）”视图，已有配置不变，切换视图不会卸载正在编辑或提交中的 2FA 面板。源码事务和继承文档差异在 docs-local/twofa-credentials-copy/；验证与部署结果后续按实际完成追加。

候选验证补充：49 个 worker 离线用例通过，覆盖完整凭据结果的鉴权/no-store、列表不泄露密码和加密数据库重载后仍可导出；29 个前端用例通过，覆盖特殊字符原样复制、旧 worker 缺失密码时不覆盖剪贴板、默认简洁布局、切换保留输入、独立维护服务保存不改变原守护设置。前端类型检查与改动文件 ESLint 通过；后端两个相关包定向测试通过。同输入复制探针在 BASELINE/ROLLBACK 均只复制密钥，MODIFIED/重建均复制三段完整凭据，字节回滚和链接检查通过。新增页面测试最初因 i18n/groups mock 未保留原模块导出而失败，补全 mock 后通过，失败日志保留。此时生产构建/部署仍在进行。

## 2026-09-29 17:17 UTC：完整凭据复制上线与浏览器验收

业务源码 1bf2a6215c7fa1fdd4bab9ca0d400ee2534c289e，版本 0.0.18-pr12.1bf2a621；app 和 worker 同步更新。源码、前后端构建完成；本机及公网 BASELINE → MODIFIED → ROLLBACK → FINAL 镜像回切全部通过，部署结束于 17:03 UTC。新结果接口的邮箱、原密码、新密钥与已有加密记录逐项匹配，旧版回滚恢复仅导出密钥，新版重施恢复完整凭据；任务表哈希、守护配置、Caddy、原 XY2API 和数据库不变。

真实 Chromium 使用现有管理员登录进入测试站，在默认简洁页面点击“复制更改后的账号凭据”，读取原生剪贴板并与结果 API 的三项按四横线拼接逐字比较；刷新页面后再次复制仍匹配。高级区域默认隐藏、维护设置折叠、两视图切换均通过。期间真实轮换/继续验证 POST 请求数为零，没有删除任务、修改凭据或重复更换。已完成隐去邮箱/任务标识/输入值的全页截图并视觉复核。

浏览器验收早期超时由测试脚本按完整 URL 末尾匹配 /result 导致（前端 GET 自动附带时区参数），并遇到新浏览器的首次引导浮层；中途诊断还出现脚本 urlsplit 局部变量遮蔽。已修正为按 URL path 匹配、通过关闭按钮结束引导并使用标准 Chromium 原生剪贴板复验；这些验收脚本失败保留，不作为应用复制失败或账号更换失败。首次中文截图因诊断镜像缺少 CJK 字体出现方框，最终使用宿主只读字体挂载重新截图，未改应用字体或生产配置。

源码四角色：docs-local/twofa-credentials-copy/；部署四角色：/opt/bps2api-twofa-preview/evidence/credentials-copy-20260929/。两者独立，ROLLBACK.sh 均已实际验证；文档提交不改变部署业务源码。完整 PR 正文及推送结果保存在同一源码证据目录的 pr-body.md、pr-publication.json，PR 不合并或发布正式版。


## 2026-09-29：第二次更换在关闭旧 TOTP 时返回 HTTP 500

用户 17:42 UTC 再次提交任务后显示 needs_review。只读核查真实加密任务记录确认：初始登录和更换前检查已通过，固定引擎在 disable old 2FA 收到 HTTP 500；尚未进入 enroll/activate，没有新密钥 checkpoint。任务输入的密钥和密码与上次成功结果相同；worker 重启次数零、OOM=false，五条任务均终态。不能把本次失败沿用为之前的 Cloudflare 初始化 403，也不能把它解释成“已换好、只差验证”。

在同一部署 worker 内做一次受限只读账号核验：原任务密钥的密码校验和 TOTP 校验均为 200，会话邮箱匹配；mfa_info 为 200、MFA 仍启用、可解析 TOTP 因子。数据库任务哈希不变，临时 profile 已清理，安全设置写入为零。这证明本次核验时旧密钥仍有效，不证明上游历史 HTTP 500 的内部原因或以后不会再次发生。原错误只保留了安全错误文本和状态码，没有请求 ID/响应体，不能进一步断言 500 的服务器内部根因。

候选只修复诊断与界面：严格匹配固定引擎的关闭旧 TOTP 4xx/5xx 错误，输出白名单错误码并通过 Go 管理 API 过滤；前端解释失败阶段，未知错误仍用通用提示。同邮箱较新任务出现后，旧成功行隐藏复制/同步按钮并说明原因，与后端最新任务限制一致。原始任务、needs_review、重复提交/继续验证/导出限制全部保留；没有通过写库解锁或自动重放任务。本次没有修复或绕过上游服务器 500，也未新增账号安全操作。

源码事务：docs-local/twofa-disable-500/，基线 HEAD 为 63b84778418e6c78259b26b349c884c01914a537。59 个 worker 离线用例、29 个前端定向用例已通过；worker 首次测试因未把固定上游加入 PYTHONPATH 有 13 个导入失败，修正测试环境后全部通过，原日志保留。只读探针首次因旧模块名 browser_phase 不存在而未发起网络请求，改为当前 browser_login 导航入口后通过。后端回归、生产构建、部署与 PR 更新按后续实际结果追加。

候选验证完成：后端 service/admin handler 的 TwoFA 定向测试、前端类型检查、改动文件 ESLint、前端生产构建、worker 镜像构建通过。相同模拟 HTTP 500 输入在 BASELINE/ROLLBACK 只显示笼统状态，MODIFIED/重建显示具体关闭旧 2FA 的服务器错误；四态均阻止再次提交。旧成功结果按钮也按同一输入验证前后差异，源码字节回滚/补丁重建、文档链接及四角色重开通过。仍未重试真实账号更换。


## 2026-09-29 18:30 UTC：HTTP 500 原因提示上线

测试站应用/worker 同步更新为 b0bbeae32c3e6439dde9ac5fae366b4350acd48a，版本 0.0.18-pr12.b0bbeae3。真实 BASELINE→MODIFIED→ROLLBACK.sh→ROLLBACK→FINAL 通过，本机/公网任务列表只增加安全错误码，原 needs_review、禁用重试/导出不变。五条加密任务哈希、守护配置、Caddy、原 XY2API 与 PG/Redis 身份保持不变；未修改远端生产。部署四角色在 /opt/bps2api-twofa-preview/evidence/disable-500-20260929/，已重开并实际回滚验证。

首次部署验收在 BASELINE 因预期管理 API 返回 409 而失败，尚未切换任何镜像。源码核对后确认 worker 原始 409 由既有 Go handler 映射成 503；修正验收预期后四阶段通过，没有为验收改接口错误语义，失败日志保留。

公网真实 Chromium 验证具体 5xx 失败说明、旧成功行复制/同步隐藏、页面刷新后保持、默认简洁视图；POST 轮换/继续验证请求数零。脱敏截图已视觉复核。为排除当前 TOTP 因子选择错误，另做一次受限只读检查：密码/TOTP 均 200，MFA 仍启用；factors 列表为空，但 native_default_factor_id 非空，当前选择与默认因子一致。两次只读核验均未更换安全设置、未改任务数据；这不能追溯证明历史响应内部原因或保证重试会成功。

本轮完成范围为事故定位、现有密钥有效性核验、具体原因提示与过期操作修复，不是上游 HTTP 500 修复或失败任务解锁。失败任务仍待受控恢复处理；未写库标成功/删除/解锁，未自动重试。PR 继续保持 open，未合并或发正式版；源码交付记录与发布证据保存在同一事务目录。
