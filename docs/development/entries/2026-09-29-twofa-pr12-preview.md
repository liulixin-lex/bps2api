# 2026-09-29：PR #12 独立测试部署

[返回开发历程](../../../DEVELOPMENT_HISTORY.md)

> 状态：部署、worker 接线、本机/公网接口与回切验收完成。最后实测 2026-09-29 12:21 UTC；真实账号端到端待专项测试。
> 证据：本轮实测；未执行真实账号端到端。

## 背景与目标

用户在 PR #12 创建后追加授权：拉取 BPS2API 最新主分支、叠加 PR，并让 test.aibong.cn 临时使用此版本；保留原 XY2API 测试实例以便回切。远端生产源码与生产应用不属于本次部署范围。

## 起点与版本

- 仓库 liulixin-lex/bps2api；分支 feat/account-twofa-rotation-20260929。
- 已同步并复核的 main：2dfb76d7fb361a42824545cc406f9c2ef92ff8ac。
- 实际部署业务代码：a7106a31aef11ae8cb7328c5449e3d2def1380eb；版本 0.0.18-pr12.a7106a31。此后的记录提交仅改文档，不能混作二进制 revision。
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
