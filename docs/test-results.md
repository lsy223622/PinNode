# PinNode 测试结果

更新时间：2026-08-25

## 最终构建与静态验证

| 范围 | 结果 |
| --- | --- |
| 服务端 | Go 1.26.6 下普通/Debug `go test -count=1 ./...`、`go vet` 均 PASS；实例根密钥并发创建与 OAuth token 缓存测试重复 50 次 PASS。最终 Windows `-race` 重跑在测试执行前被本机 MinGW64 `collect2` 静默链接失败阻断；此前基础版本曾通过，不能据此宣称本次新增代码已由 race detector 验证 |
| 管理页面 | 内嵌 JavaScript 语法检查：PASS；本地真实浏览器渲染首次注册页和登录后管理面板，OAuth/API 类型切换、DOM、深色布局和控制台错误检查：PASS；CSP 使用逐响应 nonce 且没有 `unsafe-inline` |
| Android | `ktfmtCheck`、`assembleDebug --no-daemon`：PASS；APK 为 `android/build/outputs/apk/debug/android-debug.apk`，minSdk 33、targetSdk 36、含四个 ABI |
| 安装 | 最新 Debug APK 已通过有线 ADB 覆盖安装；此前 OAuth 版本服务端曾经 HTTPS 反向代理完成供应、绑定、心跳和超时清理闭环。当前命名 OAuth/API 凭据版本已使用现有真实 OAuth client 完成 `client_credentials` 交换、必要 scope 校验、`auth_keys` 权限探测和加密入库；尚未重跑手机供应、绑定与清理全链路 |
| Android JUnit | PASS：同一源码在纯 ASCII 临时路径执行 `testDebugUnitTest` 通过；原中文绝对路径仍会令 Gradle test worker 错误地产生 3 个 `ClassNotFoundException` |
| Android lint | `lintDebug` 可完整执行，但因项目设置 `warningsAsErrors`，现有 390 项上游/兼容代码告警会使任务失败；未创建 baseline 或关闭质量门 |
| Tailscale core/AAR | Go 1.26.6 下 `wgengine/netstack`：PASS；重新生成的 AAR 含四 ABI，最终 APK arm64 `libgojni.so` 的二进制漏洞扫描为 0 个可达漏洞 |

## 真机与隔离测试环境

- 手机：Android 16 / API 36 参考真机，有线 ADB；Wi-Fi 地址、网关、运营商和设备标识
  已脱敏，蜂窝网络同时可用。
- 配置服务器：隔离测试服务器（地址和端口已脱敏；当前 Debug 默认为
  `6634`），仅 Debug APK 使用 LAN HTTP；固定名称
  显示为“PinNode 本地测试服务器”。
- 远端节点：独立 Linux 测试节点，节点名、路径和身份均已脱敏，使用短期测试身份；
  宿主机上的其他 Tailscale 登录未参与测试。
- 手机上的官方 `com.tailscale.ipn` 始终保持安装且未被启动、清数据或改包。
- 测试按用户要求没有重启手机。

## 四个快速模板

| 模板 | 真机结果 |
| --- | --- |
| 普通节点 | PASS：无广告路由，接受 DNS/route 配置；强制停止 PinNode 进程后重开，原加密会话和 VPN 恢复，不需重新输入 PIN |
| 子网路由 | PASS：自动发现并发布测试子网（具体 CIDR 已脱敏）；远端路由表获得该路由，Tailscale peer 和 Wi-Fi 网关均可达 |
| 救援连接 | PASS：只发布脱敏后的网关 `/32`；Tailscale peer 经 DERP relay 而非手机 Wi-Fi endpoint；Wi-Fi 断开时 peer 保持而网关 fail closed，恢复后网关 3/3；移动数据断开时 peer 失联且不回退 Wi-Fi，恢复后同一会话继续 |
| 代理节点 | PASS：远端客户端识别手机；选中后外网连通性检查通过，随后清除远端 Exit Node 选择 |

救援测试最初观察到 peer 通过手机 Wi-Fi endpoint 直连。修复为 cellular 模式下从
Tailscale interface snapshot 过滤当前 Wi-Fi 接口，并触发 netmon/DNS 重绑后，连续
Tailscale ping 只能经 DERP，移动数据关闭即无法通信。这个对照是“不回退 Wi-Fi”
结论的重要证据。

## 生命周期和退出策略

| 场景 | 真机结果 |
| --- | --- |
| 默认网络变化 | PASS：Wi-Fi 或移动数据丢失不自动注销；选定路径恢复后同一 node/route 继续 |
| 登录后定时退出 | PASS：配置 `afterLoginSeconds=25`，手机先上线，约 25 秒后界面回到未启动，远端 peer 同时消失 |
| Wi-Fi 丢失即退出 | PASS（本地）：配置 `networkChange=wifi-lost`，关闭 Wi-Fi 后界面回到未启动；Wi-Fi 已恢复，远端待办删除未在本轮单独二次观测 |
| 正常手动停止 | PASS：清空本地广告、logout，服务端撤销 enabled routes 并删除精确设备 |
| 应用关闭策略 | PASS（含 OEM 限制）：最近任务强杀没有投递 Activity/Service 回调，VPN service 当场消失；最终实现禁止该会话在下次进程启动时恢复，并补做本地与远端清理；重开界面保持未启动，远端 peer 消失 |
| 应用关闭心跳兜底 | PASS：`onAppClose` 会话约每 40 秒更新租约；ADB `force-stop` 后没有错误地立即清理，超过两分钟租约后 reaper 撤销路由、删除精确设备并把历史状态记为 `heartbeat_timeout`；重开应用不恢复旧会话 |
| 普通进程重建 | PASS：无 `onAppClose` 策略时会话、配置、路由和 VPN 恢复 |
| 手机重启 | NOT TESTED：用户明确要求不重启，因为解锁前 ADB 不可用 |

## UI 与固定服务器

- PASS：应用名和居中标题均为 PinNode，状态栏/标题区与页面同色。
- PASS：未登录只显示 Wi-Fi、移动数据和启动表单；登录后才显示 Tailscale 路线、
  Subnet Router/网段、使用 Exit Node 和发布为 Exit Node。
- PASS：路由配置行距统一，内容在单屏卡片结构中展示。
- PASS：locked APK 只显示服务器名称，字段禁用，真实 URL 不显示且无法编辑。
- PASS：成功开始会话后旧 PIN 清空，避免误用已消费的 code。

## 安全与控制面

- 服务端生成的 auth key 为 `reusable=false`、`preauthorized=true`、十分钟有效；
  Android 加入后为非 ephemeral 持久节点。设备 attach 要求本次 provisioning hostname、
  创建时间和目标 tag 同时匹配，并以数据库唯一约束绑定 node ID。
- SQLite 重启恢复、历史会话、唯一设备绑定、可信代理地址解析、限流窗口，以及仅
  `onAppClose` 会话因心跳超时进入 reaper，均通过服务端测试；历史记录在服务端重启后
  仍可由管理 API 查询。
- `govulncheck` 对服务端源码未发现漏洞；对最终 APK 的 arm64 Go 原生库未发现可达漏洞
  （依赖模块中有 4 个未被本二进制调用的已知漏洞）。
- 管理员密码使用 Argon2id；首次启动原子生成实例根密钥，HKDF 派生独立的 PIN HMAC 和
  AES-256-GCM 子密钥。OAuth client secret/API token 的页面列表与接口不回显明文；
  auth key、会话 token、管理员 Cookie 和 CSRF token 未写入源码、APK UI 或测试记录。
- provisioning hostname 绑定、一次性 auth key、精确设备 routes/删除 API 和心跳租约
  已在此前 OAuth 版本完成真实 tailnet 闭环；当前凭据选择和加密版本通过模拟 API 测试，
  并使用现有真实 OAuth client 验证了在线交换、scope、权限探测和加密保存。
  Debug 两端日志只记录阶段、耗时、状态和脱敏路由，
  不记录 PIN、请求体、Authorization、auth key、会话令牌或完整会话 ID。

## 仍不能宣称

- 手机重启、解锁前、Boot/Always-on VPN 无人值守恢复未测试。
- 没有路由器侧抓包，也没有独立的“Wi-Fi 可达 LAN 但完全无 WAN”物理拓扑。
- 未验证本机选择另一个 Exit Node、真实 IPv6、直连 WireGuard UDP、重叠本地路由、
  高级 SSH/Web/App Connector 等每一个偏好。
- 当前反向代理到本机服务端的 Tailscale 入站链路不稳定：自定义 DERP 不可达且 NAT
  直连映射过期后，公网请求会在到达应用前超时；本机主动 Tailscale ping 代理节点后
  可暂时恢复。该部署基础设施问题在修复前不能宣称公网服务可持续可用。
- PinNode 复用官方数据面，但受管 UI 不等于官方客户端全部界面/本地设置能力。
- 当前新管理登录与命名 OAuth/API 凭据流程尚未使用当前版本重跑手机供应、绑定和清理；
  本轮真实验证止于 OAuth 在线交换、scope/权限探测、加密入库和本地浏览器登录页。
