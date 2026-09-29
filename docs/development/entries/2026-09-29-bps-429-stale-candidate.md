# 2026-09-29：BPS 429 重试提前结束与 v0.0.18

## 背景与证据

用户报告 v0.0.17 上线后仍有 429 并停止请求。2026-09-29 09:25:32 UTC 的实际请求记录过一次 pre_output_provider_retry，随后切号并报告 no available accounts。该请求并非完全没有重试；完整请求标识和账号编号留在本机 incident-429 原件。

同一时段的上游错误明确为 tokens-per-minute 限流。09:37 UTC 只读实测显示：数据库和完整 sched:acc 缓存均有 08:39 UTC 的 BPS 403 暂停标记，对应轻量 sched:meta 却没有。仍保留的 v0.0.5–v0.0.12 图片 owner 使用相同元数据键，旧投影不保留该标记；未捕获具体最后写入者，因此不把某个旧容器断言为已确认写入源。

此前 v0.0.17 的回归与公网 canary 确实运行，但候选切换用例直接向已选第二账号转发，没有覆盖混合版本摘要与完整记录不一致。本次补上该测试缺口。

## 起点与根因修复

原始 /bps/bps2api 保留原字节。候选 /bps/worktrees/incident-429-20260929，分支 fix/bps-retry-candidate-validation，起点 534d26485d09d3e2598d64c3eb97ec7dfbe50417；标签 v0.0.18 固定应用提交 2bca8554968852c7f28153a8ddc3d75be0b6a374。

excelBPSReserveAccountSwitch 原先只检查摘要候选，在第二次 SSE 429 时把最后一次发送预留给实际不可用账号。真实调度器加载完整账号后拒绝它，导致提前结束。现在对候选执行完整记录与数据库终检，复用正式模型、分组、权限、能力及 RPM 资格检查；没有真实同模型 BPS 替代账号时，当前账号保留第三次尝试。

excelBPSClassifyProviderFailure 原先漏读 SSE error.headers / response.error.headers 内的 Retry-After / Retry-After-Ms。实测错误同时给出 1 秒 header 和 52ms 文本提示，修复后遵守较长的 1 秒；无效或超过预算的提示仍安全终止。

默认总发送上限仍为 3 次、恢复预算仍为 30 秒。不会自动解除 403 暂停、启用被禁账号、切换模型、回落原生通道或重播已经产生文字/工具输出的请求。RPM 80% 是调度偏好，100% 才是硬上限；真实 legacy、legacy_batch、advanced 调度器在 79/80/99/100% 的边界均已测试。预检不等于并发槽/RPM 原子预留，实际发送仍须准入。

## 同输入三态与扩大回归

同一外部 Go overlay 输入 SHA-256 为 999a4ef55dc42d4e851342d89836832b1c02604c46381800115e8ee7177b2e0e，三个源码副本使用匹配的命令和输入：

| 状态 | 实际行为 | 含子测试节点 / 退出码 |
| --- | --- | --- |
| BASELINE | 前两次 SSE 429 后错误预留切号，第三次成功响应未被请求；嵌套 Retry-After 被漏读 | 20 通过、19 失败；exit 1 |
| MODIFIED | 无健康替代时实际发送第三次并成功；同模型健康替代与已有输出边界保留；采用 1 秒提示 | 39 通过、0 失败；exit 0 |
| ROLLBACK | 单独副本经 ROLLBACK.sh 恢复，重现 BASELINE，完整文件哈希与原始一致 | 20 通过、19 失败；exit 1 |

基线 4,908 个文件，候选 4,910 个文件，共 5 条变更路径。补丁可重建候选；回滚后重施补丁完成。重施时 umask 077 曾使 5 个文件模式变为 0600，内容哈希已经一致；按 Git archive 模式恢复 0644 后整树一致。原失败事件与修正证据均保留。

扩大回归执行 48 个顶层 / 165 个含子测试节点，全部通过；另执行 RPM 边界 2 个顶层 / 20 个含子测试节点，全部通过。两批存在覆盖重叠，不宣称为 185 个不同测试。原始命令、逐字 stdout/stderr、退出状态在 hotfix-tests 和 source-transaction。

## 线上替换与真实恢复

2026-09-29 10:34:32 UTC 发起无感切换，10:34:35 UTC Caddy reload 完成，生产为 sub2api-v018 / 8095。线上本机构建镜像 sha256:c59a7aa02ff8aa55b0173b1c5fc077b12eb2750afa344b16c13ff825796e81f0，源码为上述固定应用提交，版本 0.0.18。旧实例及图片 owner 保留，未变更共享账号配置。

跨切换 SSE 在旧实例开始，切换后继续到正常 stop 与唯一 DONE，标记完整。成功切换过程中 39 次健康检查、之后 30 次健康检查均无失败；公网 7 类协议 canary 最终通过。这是限定窗口的实测，不代表全量请求始终成功。

10:29 UTC 的首次切换探针在首输出前遇到旧实例 503，门禁正确阻止切换。10:40 UTC 的 namespace 第二轮请求也遇到路由 503；原结果保留，容量窗口后仅重跑该失败样本并通过，未把两次结果改写成首次全过。

v018 容器完成日志与 Ops 恢复记录已关联：10:39:13、10:39:19、10:39:59、10:40:16 UTC 的 4 个实际请求均从上游 429 恢复为最终 HTTP 200。证据为 live-recovery-confirmed.json，不只依赖模拟上游。

## 推送与发布产物

按先上线再发布执行：10:46 UTC 原子推送 main 与 v0.0.18 标签；GitHub Release run 36557658398 completed/success，10:53:43 UTC 正式发布。下载产物验收于 10:54:25 UTC 完成，18/18 实际命令 exit 0。

发布资产为五个平台包和 checksums.txt。五包 SHA-256 与档案完整性通过；Linux amd64 下载 binary 及 GHCR amd64 镜像均实际执行 --version，返回 0.0.18 / 2bca8554968852c7f28153a8ddc3d75be0b6a374。GHCR amd64、arm64 按不可变 digest 拉取，平台与 version/revision labels 一致。其余平台包未在对应原生系统执行；arm64 镜像未执行运行时行为。

发布构建时间 10:47:03 UTC 与线上先行本机构建 10:15:41 UTC 不同，来源提交相同，镜像字节不等同。没有为验收发布产物而再次切换生产。文档结果以后续独立提交推进 main，不移动应用标签，也不冒充已编入该标签的源码。

## 独立 CI 与运行边界

10:58 UTC 已核对完成：固定应用提交的两个 CI run（36557658221 / 36557658396）结论均为 failure；完整 unit、frontend、shell、release-helpers 已通过。从两个 lint 原始日志提取的 5 条诊断均与 v17 基线一致；两个 integration 都仅失败 TestAccountRepoSuite/TestBulkUpdate_ExcelBPSModelScope，在 account_repo_integration_test.go:1778 断言 expected true / actual false，与基线相同。新增 lint、失败测试、断言和异常失败差异均为空；不宣称 CI 全绿。timing run 36557658125 已 completed/success，含 race、受影响包编译、前端回归/构建及固定生产 binary 构建。逐项差异与原始日志路径见 ci-review/report.json。

当时可用 BPS 账号配置硬上限为 50 RPM，另两个账号因实际 403 被暂停；未修改这些配置。10:40 UTC 有真实分钟预算耗尽，10:41 UTC 有无健康 BPS session proxy。代码修复不会消除上游 TPM、账号 RPM、403 暂停或代理不可用；预算耗尽和已经产生语义输出的请求仍会停止，不能无限重试。上述账号状态只是该观察窗口，不作永久能力描述。

## 原始证据与回滚

本机证据目录：/bps/artifacts/selected-integration-20260929/rollout-release/incident-429。source-transaction/acceptance.json、deployment-acceptance.json、live-recovery-confirmed.json、published-assets/report.json 分别说明源码三态、生产切换、真实恢复和发布产物。

沿用 /bps/artifacts/development-history-20260928 下 MODIFIED_FILE.tar.gz、DIFF_FILE.patch、VERIFICATION.txt、ROLLBACK.sh 四角色。主源码归档/补丁/回滚绑定应用标签提交；后续文档提交以补充记录延伸主账本。旧事务已存入 history，不覆盖历史失败。

生产回滚入口为 python3 /opt/sub2api/deploy-v018.py rollback；备份在 /opt/sub2api/backups/v018-20260929/transaction，旧生产 v017 / 8094 保留。源码回滚已在单独副本实际运行；生产配置三个阶段的验证已执行，未为证明源码回滚而撤销当前线上修复。

当前：源码修复、线上替换、远端应用提交/标签、正式发布及产物验收完成；独立 CI 已完成且确认仍有上述原有失败。结果文档随单独提交推进 main，应用标签固定。原始源码目录保持原字节。
