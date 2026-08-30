# PinNode 架构

## 组件关系

```text
管理员浏览器
    │ 管理员会话 Cookie + CSRF + 受管配置
    ▼
PinNode server ── 加密保存并按 PIN 选择的 OAuth/API 凭据 ── Tailscale API
    │                                      │
    │ 一次性 auth key + 会话令牌           │ 验证设备、启用/撤销路由、删除设备
    ▼                                      ▼
Android App ── Tailscale Go backend ── WireGuard / DERP / control
    │                    │
    │                    └─ netstack TCP/UDP forwarding binder
    │
    ├─ default：使用上游默认网络选择
    └─ cellular：控制面/非 LAN 走移动数据，指定 LAN 前缀走 Wi-Fi
```

PinNode 与官方应用使用不同包名和 Android 数据目录。它复用 Tailscale backend 和
LocalAPI 数据面，但用服务端受管单屏 UI 取代本地账号登录和配置页面。

## 管理配置

`POST /v1/pairing-codes` 把规范化配置、选定的 Tailscale 凭据 ID 与一次性六位 PIN
绑定。PIN 记录不包含 access token 明文或 auth key。主要映射如下：

| 配置 | Android/Go 行为 |
| --- | --- |
| `networkMode=default` | eligible 网络中优先非计费网络，随后使用其他有 DNS/Internet 的网络 |
| `networkMode=cellular` | Tailscale control、DERP、endpoint 和非 LAN 转发固定选移动数据 |
| `acceptRoutes` / `acceptDNS` | `RouteAll` / `CorpDNS` |
| `tailscaleIp` | 节点加入后由服务端通过 Tailscale API 设置 IPv4 |
| `useExitNode`、ID/IP/`auto:any` | `ExitNodeID`、`ExitNodeIP`、`AutoExitNode` |
| `subnetRouter` + 自动/显式 CIDR | `AdvertiseRoutes` 和服务端 enabled routes |
| `autoGatewayRoute` | 当前 Wi-Fi IPv4 网关 `/32` |
| `autoWiFiSubnetRoute` | 当前 Wi-Fi IPv4 接口的规范网络前缀 |
| `advertiseExitNode` | 增加 `0.0.0.0/0` 和 `::/0` |
| Shields、Hostname、SSH/Web 等 | 对应当前 Android backend 的 LocalAPI prefs |
| `exitPolicy` | 时间、网络变化和应用关闭触发的退出条件 |

`routes` 是广告并由服务端启用的完整列表；`wifiRoutes` 只包含需要绑定到 Wi-Fi 的
LAN 前缀。Exit Node 默认路由不进入 `wifiRoutes`。自动网关和自动整个 Wi-Fi 子网
互斥，避免无意同时扩大范围。

快速模板只是这些字段的预设：救援、子网路由、代理节点和普通节点都走同一供应与
清理状态机。

## 登录与持久状态

服务端首次启动时只允许创建一个管理员账号；默认注册来源必须是 loopback。密码以
Argon2id 加盐哈希保存，登录前必须完成短时、按来源绑定且一次性消费的 SHA-256 PoW，
同时还有来源/账号限速和持久化递增退避。登录成功后只在 HttpOnly、Secure（HTTPS）、
SameSite=Strict Cookie 中保存随机会话令牌，数据库只保存摘要；写接口另行校验 CSRF token。

首次启动生成 32 字节实例根密钥并保存在独立 `pinnode.secret` 文件；也可由
`PINNODE_INSTANCE_KEY` 注入。HKDF-SHA-256 为 AES-256-GCM 凭据加密和 PIN HMAC 派生
不同子密钥。命名后的 Tailscale OAuth client 或 API access token 以密文保存在 SQLite，
浏览器只能看到凭据 ID、类型、名称和使用时间。OAuth client ID/secret 用来自动获取并
缓存短期 access token；临近过期时重新交换。生成 PIN 时选定的凭据 ID 会继续写入
Session，保证供应、设备校验、路由操作和清理使用同一凭据。

```text
PIN issued → validated → one-time key issued → PIN/session/replay committed atomically
    → persistent node joined → creation/name/tag verified → unique node binding
    → optional Tailscale IPv4 assignment → exact routes enabled → revisioned sync
    → optional onAppClose lease active
    → explicit/policy/optional sync timeout → routes withdrawn → device deleted
```

auth key 有效期十分钟、`reusable=false`、`preauthorized=true`；加入后的节点必须是
`ephemeral=false`。服务端把 auth key ID、唯一 provisioning hostname、node ID、令牌
摘要、配置和完整状态保存在 SQLite；auth key 明文不入库。Android 在登录前先把会话和
原服务器 URL 写入加密 SharedPreferences，绑定成功后转为 active。普通进程重建会恢复
网络模式、路由和退出定时器，并在已有 VPN 权限时重新请求 VPN service。

时间退出支持：配置发布后时长、登录后时长和固定 RFC3339 时间，多个条件取最早者。
网络退出支持任意变化、Wi-Fi 丢失或移动数据丢失。所有活动会话按服务端下发间隔同步
配置 revision；仅 `onAppClose` 会话同时续期默认五分钟的清理租约。普通长期会话没有
`syncDeadline`，但仍通过同一接口接收会话中配置更新。停止接口和服务端清理均按数据库
唯一绑定的精确 node ID 操作；hostname 只用于首次供应挑战，不用于后续模糊选择。

应用关闭策略需要兼容两类 Android 行为：

1. 正常投递 Activity/Service 生命周期回调时，PinNode 先持久化退出态，再做本地注销；
2. OEM 直接强杀时没有回调，VPN 随进程立即断开。该策略的加密会话在下一次进程启动
   时绝不恢复，并转换为待清理记录，补做 logout、路由撤销和设备删除。

## 管理员可观测性

管理页面的配置、Console 状态和实时日志是三个同级视图。Console 只查询数据库中尚未
`stopped` 的会话，不受历史会话查询的数量上限影响；读取 Tailscale 节点状态使用带五秒
超时的短 TTL 快照，控制面暂时不可用时降级为 `Unknown`，不会阻塞整个页面响应。健康
统计同时返回所有状态和 `attention`，后者等于活动会话中所有非 `Healthy` 状态。

服务端状态事件和日志事件使用各自最多 2048 项的进程内环形缓冲，并共享递增事件序列；
日志不写入 SQLite。管理员 SSE 在建立后仍会周期性检查管理员 session，session 被登出或
删除时发送 `auth-expired` 并关闭连接。客户端日志在 Android 和服务端各脱敏一次，且每条
待上传日志绑定原 session；session 结束时未上传的条目丢弃，不能改绑给新 session。

## Android 网络选择

`RescueNetworkController` 同时观察非 VPN Wi-Fi 和移动网络。Wi-Fi 只要求存在带网关
的默认路由，不要求 `VALIDATED`，因此无 WAN 的家庭 Wi-Fi 仍可作为 LAN 出口。

cellular 模式下：

- control/DERP/endpoint 只暴露移动数据接口；
- netstack 目标命中 `wifiRoutes` 时逐 socket `protect` 后绑定 Wi-Fi；
- 其他转发 socket 绑定移动数据；
- 移动数据缺失或 LAN 对应 Wi-Fi 缺失时 fail closed，不回退到另一条线路；
- Wi-Fi 丢失默认保持会话和路由，恢复后自动继续，除非退出策略明确要求退出。

default 模式不强制目的地绑定，保留上游默认网络选择。当前选择器对有 DNS 的 eligible
非 VPN 网络优先 `NOT_METERED`，随后回退到其他 Internet 网络。

## 固定服务器构建

Gradle 从被忽略的 `android/local.properties` 读取 `pinnode.serverUrl`、
`pinnode.serverName` 和 `pinnode.serverLocked`。locked 构建要求 URL 和名称同时
存在；运行时只显示名称且禁用编辑。URL 是 APK 内部配置而不是秘密，服务器仍必须
执行认证、TLS 和最小权限控制。

## 路由批准与部署

路由可用需要节点广告、管理员批准/auto-approval、控制面下发和客户端接受四个条件。
服务端使用设备 routes API 启用该会话的精确列表。Debug 构建使用
`tag:pinnode-test`，正式构建使用 `tag:pinnode`。生产 tailnet 应用精确 autoApprovers
或等价审批策略，不能用宽泛私网段授权替代。

SQLite 持久保存全部历史会话和清理重试状态，服务端重启后 reaper 会继续处理过期、
心跳超时和 `cleanup_failed` 会话。当前数据库适用于单服务实例；多实例需要共享事务
数据库及共享限流。

为减少后续同步 Tailscale Android 上游时的冲突，未删除上游入口实现：
`ShareActivity` 和 `QuickToggleService` 在 manifest 中禁用，`IPNReceiver` 设为
`exported=false`、只保留应用内部通知动作。受管界面不会暴露这些入口。
