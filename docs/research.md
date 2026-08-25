# PinNode 研究结论

更新时间：2026-08-24

## 研究边界

本次基线固定为：

- Tailscale Android：`0867f01687a3955f7c0b5c6c62b236b997d68601`，2026-08-12 的 `main` 快照。
- Tailscale 核心模块：`25877455e79d9e3ebd5e99200ca86fd62bcc0ed9`，版本
  `v1.103.0-pre.0.20260810100007-25877455e79d`。
- 上游 Android 工程位于 `android/`、`libtailscale/`；匹配的核心源码副本位于
  `third_party/tailscale/`。

研究只把源码和官方文档作为“实现依据”，不把官方支持某个路由功能等同于
“Android 双网络已经验证可用”。

## 目标网络模型

救援模式下需要两个独立的 Android `Network`：

1. 蜂窝 Network：Tailscale control、DERP、直连 WireGuard 以及非局域网目的地。
2. Wi-Fi Network：只承载选定家庭 LAN 网关的 TCP/UDP 转发；该 Wi-Fi 可以没有 WAN。

Android 官方 API 的 `Network.bindSocket` 是逐 socket 绑定，且要求在连接前完成；
`Network.getSocketFactory` 也提供同一 Network 的逐 socket 创建方式。官方对
`ConnectivityManager.bindProcessToNetwork` 的语义则是影响进程之后创建的 socket，
不适合本项目的两个并行出口。因此实现不能使用进程级绑定。

参考：[Android Network API](https://developer.android.com/reference/android/net/Network.html)、
[ConnectivityManager](https://developer.android.com/reference/android/net/ConnectivityManager)、
[NetworkCapabilities](https://developer.android.com/reference/android/net/NetworkCapabilities)、
[LinkProperties](https://developer.android.com/reference/android/net/LinkProperties)、
[RouteInfo](https://developer.android.com/reference/android/net/RouteInfo)。

## 上游 Android 行为

源码检查结果：

- `android/src/main/java/com/tailscale/ipn/NetworkChangeCallback.kt` 收集非 VPN
  网络，并把一个“默认” Network 缓存在 `cachedDefaultNetwork`。它按能力、DNS、
  非计费等条件选取，并不表达“控制面必须是蜂窝、LAN 目的地必须是 Wi-Fi”的
  目的地策略。
- `android/src/main/java/com/tailscale/ipn/App.kt` 的
  `bindSocketToNetwork` 只绑定 `cachedDefaultNetwork`。
- `libtailscale/backend.go` 把该回调接入 `tailscale.com/net/netns`。因此原有
  Tailscale netns socket 可以使用当前默认 Network，但这是全局当前网络选择。
- `third_party/tailscale/wgengine/netstack/netstack.go` 的 subnet TCP 转发在没有
  测试用 `forwardDialFunc` 时使用零值 `net.Dialer`；该 dialer 没有 Android
  `netns` 的 `Control`。UDP 转发使用 `net.ListenUDP`，同样没有目的地感知的
  Android Network 绑定。

结论：仅修改 Android 默认 Network 选择，不能证明 netstack 的 LAN 转发一定走
Wi-Fi；仅依赖上游 netns hook 也不能覆盖 subnet TCP/UDP forwarding socket。

## 最小改动结论

PinNode 在 `third_party/tailscale/wgengine/netstack/netstack.go` 增加
`ForwardSocketBinder` 与 `SetForwardSocketBinder`：

- TCP：在 `net.Dialer.Control` 中取得真实 FD，先交给 Android 服务保护，再按
  目的地绑定 Network，最后才 connect。
- UDP：`net.ListenUDP` 后通过 `SyscallConn` 取得 FD，在开始复制数据前按目的地
  绑定 Network。
- 只对非本机、非 loopback 的转发目的地调用该 binder；本机 Tailscale 服务保持
  原有路径。
- binder 返回错误即关闭转发，不允许从 Wi-Fi 丢失回退到蜂窝，也不允许从蜂窝
  回退到 Wi-Fi。

Android 侧新增 `RescueNetworkController`：

- 通过只排除 VPN 的 `NetworkRequest` 同时观察 Wi-Fi 和蜂窝；不要求 Wi-Fi
  `VALIDATED`，以便识别“连接家庭 LAN 但没有 WAN”的 Wi-Fi。
- `RescueNetworkController` 当前支持多个 IPv4/IPv6 `IpPrefix`。目标命中服务端
  返回的 `wifiRoutes` 选 Wi-Fi，其余控制/出站目的地选蜂窝；当前 Wi-Fi 网关自动
  发现仍只取带默认路由的 IPv4 gateway，并生成 `/32`。
- 救援模式下 `NetworkChangeCallback.controlNetwork()` 固定返回蜂窝，普通模式
  保持上游默认网络选择。
- `libtailscale/backend.go` 在 backend 创建前就安装 control/DERP 的 Android
  bind hook，所以 auth-key 登录早于 VPN service 启动时也能使用蜂窝；VPN service
  启动后再叠加 `VpnService.protect`。非救援模式下 forwarding binder 是 no-op，
  不改变上游普通转发路径。
- 缺少选定 Network 或 `Network.bindSocket` 失败时返回 false，Go 层将该连接
  失败关闭。

## Tailscale 路由与控制面

官方路由注入过程要求：节点广告路由、管理员批准路由、控制面下发路由、客户端
接受路由；ACL/grants 和路由注入是两个独立层次。Android 默认接受子网路由，
但这不替代广告与批准。

PinNode 服务端使用精确的设备路由 API：

```http
POST /api/v2/device/{deviceID}/routes
Content-Type: application/json

{"routes":["192.0.2.1/32"]}
```

清理时发送 `{"routes":[]}`，再删除临时设备。请求体格式由官方
[Tailscale client-go v2 的 SetSubnetRoutes 实现](https://github.com/tailscale/tailscale-client-go-v2/blob/main/devices.go)
和 [Trust credentials 文档](https://tailscale.com/docs/reference/trust-credentials)
共同确认；服务端需要 `devices:routes` 权限。

不采用把 `autoApprovers` 配成 `192.0.2.0/24` 的方案，因为它会把同一范围内
更多子网纳入自动批准，超出“只救援一个网关”的最小授权边界。若未来改用
`autoApprovers`，必须用独立 tailnet policy 评估其精确匹配行为并重新做实测。

## 受管供应链路

服务端 `server/`：

1. 管理员完成账号登录并选择已加密保存的 Tailscale OAuth client 或 API access token，再通过
   `POST /v1/pairing-codes` 创建五分钟有效的六位代码。
2. 代码只保存 HMAC 摘要，并在同一把锁中原子消费；同一个代码只能成功一次。
3. 手机把代码和检测到的 `gateway/32` 发送到 `POST /v1/sessions`。
4. 服务端解密与该 PIN 绑定的凭据；OAuth client 会自动换取短期 access token，再通过 Tailscale API 创建
   带当前构建标签（Debug 为 `tag:pinnode-test`，正式版为 `tag:pinnode`）、`ephemeral=false`、`reusable=false`、
   `preauthorized=true` 的一次性 auth key。短期的是登录 key，不是加入后的节点。
5. 服务端只把一次性 auth key 和高熵会话令牌返回手机；OAuth secret/API token 不进入 APK。
6. 手机加入 tailnet 后报告 node ID；服务端验证设备是带目标 tag 的非 ephemeral
   节点，再启用配置的 `routes`。响应同时包含 `wifiRoutes`，用于 Android 只把
   家庭 LAN 目标绑定到 Wi-Fi；Exit Node 默认路由不会进入该列表。
7. 默认不设置退出时间，手机加密保存会话并跨进程恢复。明确停止或服务端策略触发时
   先清空广告并 logout，再由服务端幂等撤销路由和删除设备；失败保留加密待办。
   `onAppClose` 遇 OEM 强杀时禁止下次恢复并补做清理。

管理网页把官方客户端偏好映射到 LocalAPI：接受 subnet/DNS 对应 `RouteAll`/`CorpDNS`；
选择 Exit Node 对应 `ExitNodeID`/`ExitNodeIP`/`AutoExitNode`；subnet router 与
Exit Node 广告对应 `AdvertiseRoutes`；还覆盖 `WantRunning`、`ShieldsUp`、Hostname、
SSH/Web client、posture、SNAT/过滤和 RemoteConfig。Linux-only 的
`OperatorUser`、`ForceDaemon`、relay server 等没有伪装成 Android 可用选项。

参考：[Tailscale API](https://tailscale.com/docs/reference/tailscale-api)、
[Auth keys](https://tailscale.com/docs/features/access-control/auth-keys)、
[Ephemeral nodes](https://tailscale.com/docs/features/ephemeral-nodes)、
[Route injection](https://tailscale.com/docs/reference/route-injection)。

## 结论与未验证项

Android 16 参考真机已验证 DERP/控制面不回退到 Wi-Fi、LAN 网关转发、
子网路由、Exit Node、网络断开恢复和退出策略。仍未验证直连 WireGuard UDP、独立的
“Wi-Fi 有 LAN 但完全无 WAN”物理拓扑、既有流的精确终止时延、手机重启以及 `/32`
与本地路由重叠行为。
