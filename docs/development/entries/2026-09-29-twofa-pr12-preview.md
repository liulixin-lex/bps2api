# 2026-09-29：PR #12 独立测试部署

[返回开发历程](../../../DEVELOPMENT_HISTORY.md)

> 状态：初始化控制流与提示修复已部署，最后实测 2026-09-29 14:44 UTC；真实自动登录仍受 OpenAI 初始化 403 阻塞，未完成真实轮换。
> 证据：本轮实测；未执行真实账号端到端。

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
