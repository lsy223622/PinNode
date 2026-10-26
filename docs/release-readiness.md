# 1.0 发布准备与既有规划核对

核对日期：2026-10-19。代码基线：`32aecb2`（v0.3.0），下文注明本轮工作区修复。本表区分“存在实现”“有自动化证明”和“当前候选包的实际场景已验收”；三者不能互相替代。

## 规划来源与结论

核对了仓库内的三份历史目标：

- `.artifacts/research/pinnode-long-term-roadmap/roadmap-advice.md`：外部长期建议，开头明确声明不代表用户已经批准或承诺实施；其中阶段编号也不是当前发行版本的完成承诺。
- `.artifacts/research/pinnode-long-term-roadmap/milestone-0.1x-execution-goal.md`：M0.1-P0 清理恢复、生命周期、安全门槛的具体实施目标。
- `.artifacts/admin-console-session-management-goal.md`：Console 编辑、复制、结束会话的具体实施目标。

**两个具体目标的主体功能已实现；长期建议仍有大量未实现项；现有使用场景尚未完全验收。** 不能继续沿用旧调研稿中“管理端只读”“PATCH 预留未实现”“没有能力门槛”的结论，也不能因版本已经到 0.3.0 就把长期建议的 0.1–0.3 阶段算作全部完成。

## 具体目标核对

| 目标 | 当前实现与代码证据 | 尚缺的证明或边界 |
| --- | --- | --- |
| 崩溃后恢复 `cleaning` | `server/store.go` 有清理 lease/generation、过期重领和带 generation 的完成更新；`server/http.go` reaper 处理遗留清理；Store/API 有重开数据库、旧 worker fencing、404 和部分失败重试测试。 | fake 控制面测试不能证明真实 Tailscale 对象最终消失；候选部署仍需重启演练。 |
| cleaning 不冒充 stopped | stop 区分 `cleaning`、`stopped`、`already-stopped`；Android 对清理中的响应保留待办。 | 真实响应丢失、进程被杀和再次启动的组合场景仍需实测。 |
| Android lifecycle 耐久 handoff | 本轮加入 generation 检查；等待 attach 或 Prefs 的旧 start 不能重新提交，sync 的迟到写入先清理再允许新 start，旧同步的过期响应也不能误停新会话。恢复时优先处理同 ID 的 durable cleanup；回归还覆盖 commit 失败和遗留 active 记录。 | JVM 使用 mock SharedPreferences、LocalAPI 和 VPN；仍需验证实际存储故障、进程重建和 OEM 回调。 |
| applied revision 单调 | Store 使用单调更新；`TestAppliedConfigRevisionIsMonotonic` 覆盖乱序。 | ACK 表示客户端报告应用，不能证明路由批准、分发或目标可达。 |
| policy/capability 门槛 | `CompileSessionPolicy` 生成路由、风险、required/optional 能力；start 在创建 auth key 前拒绝能力不足；admin PATCH 同样检查能力。 | 高级偏好仍是较粗的 `advanced-prefs-v1` 能力，实际平台效果未逐项验证；完整 readiness 还没有实现。 |
| 管理员编辑、复制、结束 | `server/session_admin.go` 和 `server/web/admin.html` 实现 PATCH CAS、冲突草稿保留、精确 session 操作、独立停止确认和安全复制；停止保留历史。 | 服务端测试已覆盖主要 API；浏览器中两个标签页竞争、窄屏、键盘和真实 Android 接收新 revision 的完整交互仍需候选包验收。 |
| 活动路由与配置一致性 | PATCH 先标记持久化 route reconciliation，再更新精确设备路由，成功后 CAS 提交；失败尝试恢复，reaper 可重放数据库期望。 | 当前恢复测试使用 fake API 和预置标记；仍缺进程在真实 API 成功、DB 提交前退出的演练。 |
| provisioning 编辑不丢配置 | 本轮修复 attach 仅更新 Prefs 的缺陷：绑定和 sync 共用完整配置应用流程，更新 Wi-Fi routes、网络模式和 VPN 开关，持久化成功后才发布 active/revision；覆盖 VPN 双向切换和同 revision 的完整 snapshot。 | JVM 回归已通过；仍需实际设备在配对中编辑后验证数据面和批准路由。 |
| 复制不复制身份 | `copySafeConfig` 仅从 config 取值，并清空 Tailscale IP、hostname 和绝对退出时间；新 PIN 仍需显式生成。 | 复制后生成 PIN、由另一客户端兑换的浏览器与设备联合流程尚待复测。 |

## 长期建议的当前覆盖

下表用于决定范围，不把未批准的建议自动升级为 1.0 必交功能。

| 规划方向 | 状态 | 现有能力与剩余工作 |
| --- | --- | --- |
| 可靠性与安全基础 | 部分完成 | 清理恢复、能力门槛、revision 已有；真机/OEM、凭据异常、备份恢复和升级矩阵仍需验收。 |
| 救援任务控制面 | 部分完成 | Console、SSE、Observed State、单会话编辑/复制/结束/失败重试已有；持久事件时间线、分阶段有效性和最终可达证明未形成闭环。 |
| 连接前 preflight / Tailnet readiness | 部分完成 | TLS、协议、网络要求、凭据和 auth-key 检查散布在启动链路；没有统一的 pass/fail/unknown 结果、scope/tag/route approval 检查和连接前授权摘要。 |
| 管理端热更新 | 部分完成 | CAS、能力、风险确认、路由恢复、attach/sync 已有；历史版本、策略差异预览、自动超时回滚和最终有效性探测未实现。 |
| Remote Diagnostics | 部分完成 | 状态、脱敏日志、有界缓冲和 SSE 已有；主动 control/DERP/direct、LAN IP:port、DNS、captive portal、NAT64 等结构化探测未实现。 |
| 版本化 Profile / Policy | 未实现 | 当前为固定模板和 SessionConfig 快照，没有命名 profile、版本、来源、历史与回滚模型。 |
| QR / Deep Link | 未实现 | 当前入口为服务器 URL + 一次性六位 PIN。 |
| 部署与恢复 | 部分完成 | 本轮增加同 ID 凭据轮换、密码恢复、不迁移源 schema 的一致性快照、systemd/HTTPS 示例和恢复说明；真实部署升级/恢复与凭据过期通知仍需验收或补齐。 |
| 条件策略 | 部分完成 | 已有关闭应用、网络变化和时间退出；没有通用条件/动作、dry-run、优先级、回放和通知。 |
| 服务级访问 | 未实现 | CIDR 路由与 SSH/Web 偏好不能替代按目标 IP、端口、协议的权限模型。 |
| 设备库存 | 部分完成 | 会话上报设备/版本/last seen；没有独立的长期 managed-device、分组和更新管理。 |
| 团队治理 | 未实现 | 单管理员；没有 RBAC、审批、持久审计与通知链。 |
| IPv6 / 复杂现场网络 | 部分完成 | 路由与底层支持部分 IPv6；自动网关/子网主要依赖 IPv4，NAT64、多接口、双栈现场矩阵未闭环。 |
| Headscale provider | 未实现 | 没有控制面 provider 抽象和对应管理实现。 |
| PinDrop | 未实现 | 不属于现有救援会话闭环。 |
| PostgreSQL / 多实例 / HA | 未实现 | 当前是单实例 SQLite；需真实规模触发独立设计。 |

## 使用场景与当前证据

历史实机记录 `.artifacts/local/RELEASE_DEVICE_TEST_PLAN.md` 最后明确确认的是 **v0.1.0 修复包的普通节点启动、连续同步、主动停止**，并明确要求路由、救援、Exit Node、网络变化和重启重新测试。`docs/threat-model.md` 的参考设备结果也不能外推到当前 APK 或其他 OEM。

| 场景 | 实现覆盖 | 发布前还要取得的证据 |
| --- | --- | --- |
| 普通临时节点 | 模板、PIN、start/attach/sync/stop、历史状态均有 | 当前 Release APK 从 PIN 到实际 peer 通信，再到设备/key 清理的完整链路。 |
| 无 WAN Wi-Fi + 蜂窝救援网关 | 默认发布网关 /32；控制/非 LAN 走蜂窝、LAN 目标走 Wi-Fi；缺指定 Network 时拒绝 socket | 实际无互联网 Wi-Fi、目标网关访问、蜂窝控制连接；双网络抓包或等价路径证明。 |
| 整个 Wi-Fi 子网 / 自定义 CIDR | 自动 IPv4 子网与显式 CIDR、`routes`/`wifiRoutes` 分离 | 从另一 tailnet 节点访问实际 LAN；route approval 与实际可达分别记录。 |
| 提供 Exit Node | 发布 IPv4/IPv6 默认路由，默认路由不绑定 LAN Wi-Fi | 远端 peer 出口地址/路径、控制面批准、DNS/IPv6 及蜂窝断开后的行为。 |
| 使用 Exit Node | ID/IP/自动选择、LAN access 与状态观察 | 指定/自动 Exit Node、批准/离线/切换、DNS 和 LAN 可达的实际结果。 |
| 时间与网络退出 | after-config/after-login/绝对时间、Wi-Fi/蜂窝丢失、任意变化、关闭应用 | 锁屏/Doze、系统时间变化、切网、强杀、撤销 VPN、重启，以及离线清理最终收敛。 |
| 管理员远程更新/结束 | 服务端控制面更新、下次 sync 应用、本地 cleanup | 配对中编辑、会话中编辑、两标签页冲突、远程 stop、API 失败与恢复的联合流程。 |
| 服务端离线/重启 | pending cleanup、reaper、lease 和 route reconciliation 有实现 | 实例重启、远端成功但响应丢失、凭据过期/吊销、root key/SQLite 恢复。 |
| 旧版本升级 | schema migration 与协议协商已有 | v0.3.0 数据/活动会话升级、同签名 APK 覆盖、故障后恢复；迁移后能否直接降级需要明确。 |

现有 `integration/androidvmtest` 通过测试入口直接注入 control URL/auth key，主要验证上游登录与 VPN 数据面；它没有经过 PinNode 发 PIN、兑换、绑定、管理热更新和清理的完整业务链。该测试当前要求 Linux，不能把 Windows 上 skip 或单纯 APK 构建当成通过。

## 需要在 1.0 范围决定前解决的差距

1. **风险策略入口不一致。** 新建 PIN 仅执行 Normalize，兑换时执行 policy/capability；admin PATCH 要求 `confirmHighRisk`，新建没有相同的显式确认。`remoteConfig` 仅归入高风险，没有长期建议要求的部署级 opt-in 和持久审计。需要统一创建/变更的授权语义，再决定正式支持哪些高级偏好。
2. **“字段可下发”不能当成“Android 功能完整”。** SSH、Web Client、App Connector、SNAT/Netfilter、posture 和 RemoteConfig 已映射到 Prefs，但缺少本轮逐项实际效果证明；应逐项明确可验证的功能承诺。
3. **生命周期并发需要设备级验收。** 本轮阻止已停止 start 重新发布，sync 迟到写入在同一 Mutex 内完成本地清理后才允许新 start，旧 404/409/410 响应执行 stop 前核对会话与 generation；对应延迟回归已通过。这还不能证明实际 LocalAPI、系统 VPN Service、ROM 回调及进程被杀的所有组合都能收敛。
4. **已有连接的断网终止语义需实测。** 当前 socket 选择能阻止新连接错绑；已有流没有统一注册表保证 Wi-Fi 丢失即终止。退出策略不能仅凭新 socket 被拒绝判定完成。
5. **日志授权与可追溯性仍不完整。** 会话日志会自动开始上传，没有独立的本次诊断授权流程；日志和事件环在重启后消失，不能作为持久操作审计。
6. **配置原子应用仍有边界。** 本轮将 socket 路由/模式变更移到 LocalAPI 成功之后，并检查同步 commit，持久化失败不会发布新 active/revision。后端 Prefs、socket 绑定、VPN Service 和磁盘之间没有跨组件事务；落盘失败或进程中断仍可能出现部分已应用，VPN 启动函数也会自行捕获失败。ACK 不能独立证明实际服务启动或最终可达，需要针对故障终态继续实测。

## 本轮可在本机补齐的运维能力

服务端新增同 ID Tailscale 凭据轮换，既有会话继续引用同一凭据；OAuth 缓存随轮换失效，校验失败保留旧 secret。新增本机 `admin-reset-password`（标准输入读取新密码，并撤销已有管理员 session）和 `backup`（使用 SQLite `VACUUM INTO` 生成一致性快照，不覆盖已有文件）。实例根密钥仍需单独备份。

对应回归包括轮换后既有会话可清理、权限/CSRF/传输边界、OAuth 缓存失效、密码恢复不破坏凭据、WAL 数据进入快照、恢复后使用原密钥读取会话和凭据。它们证明本地数据和 fake 控制面路径，不代表真实生产恢复已演练。

管理页已增加 `prefers-reduced-motion` 检测，CSS 动画/过渡和两处平滑滚动都遵循系统偏好。凭据添加与同 ID 轮换已在隔离的本机浏览器和 fake 上游中操作验证，没有使用真实凭据。

## 本轮自动化与本机验证

| 检查 | 当前结果与边界 |
| --- | --- |
| Android Debug / Release 单元测试 | 各 34 条、0 失败、0 跳过，包括本轮 10 条 start/attach/sync/停止/恢复回归；host JVM mock LocalAPI/VPN，不证明真实数据面。 |
| 服务端完整测试与 vet | Go 1.27.2 下普通与 `-tags debug` 的 `go test ./... -count=1`、`go vet ./...` 均通过。 |
| 非 NDK Go 包 | 按 `make go-test` 的包范围排除根 `libtailscale` 后测试通过；`TestAndroidAuthKeyLogin` 在 Windows 按既有条件跳过，不能计为设备链路通过。 |
| 四模板 HTTP 业务链 | 普通、救援、Wi-Fi 子网、Exit Node 均覆盖管理员登录/创建 PIN、兑换幂等重放、绑定、PATCH/sync ACK、数据库重开、精确清理和历史保留；上游为隔离 fake HTTP。 |
| TCP/UDP socket 绑定 | `go test tailscale.com/wgengine/netstack -count=1` 通过；新增回归覆盖普通节点、指定网络不可用时拒绝转发、清空/替换回调和 loopback。 |
| Go 漏洞扫描 | Go 1.27.2 下服务端与 `GOOS=android GOARCH=arm64 CGO_ENABLED=0` 的 `./libtailscale tailscale.com/wgengine/netstack` 均未发现可达漏洞；仍有未被导入/调用的模块级公告。Go/TS Go 固定版本已更新，但既有 AAR/APK 不会因此自动得到修复。 |
| Linux 服务端烟测 | `linux/amd64` 交叉构建通过；在无外网、只读根文件系统、非 root 的临时 Alpine 容器中启动 `/healthz` 并生成备份成功；使用临时数据。 |
| 管理页与接口文档 | 隔离浏览器完成凭据添加/轮换并确认秘密字段清空；内联 JS 语法、OpenAPI 本地引用与全部 workflow YAML 检查通过。Logo 同步、许可证合并检查与两处 Kotlin 文件的 ktfmt 0.47 检查通过。 |
| race / 远程 CI | 本机 Windows 没有可用 CGO 编译器，race 未执行；CI 已增加服务端和 netstack race、vet 与漏洞门槛，修改后的远程工作流尚未运行。 |
| 当前候选构建与设备 | 单元测试使用既有本地 AAR；未用更新后的固定 TS Go/NDK 完整重建并签名发布包，没有当前可用 Android 设备，真机和升级覆盖仍为 `not run`。 |

## 验收记录规则

沿用 [发布前验收矩阵](releasing.md#发布前验收矩阵)。每条结果至少记录 commit、构建变体、测试环境、操作、观察结果和清理终态。`passed` 只用于实际执行成功；未执行写 `not run`，没有足够信息写 `unknown`，环境原因无法执行写 `blocked`。

本次代码核对没有操作生产服务或真实 tailnet，也没有连接可用 Android 设备。当前候选 APK 的真机、OEM、重启、网络和真实路由效果仍为 `not run`。本文件记录代码和验收证据，不能替代发布批准。
