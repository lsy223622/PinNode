# 已知限制

## 已验证范围

- Android 16 参考真机已验证 Wi-Fi + 蜂窝网络并行、普通节点进程恢复、
  自动 Wi-Fi `/24` 子网路由、网关 `/32` 救援、两条物理网络分别断开/恢复、Exit Node
  真实互联网转发、时间/网络/应用关闭退出策略和固定服务器 UI。
- 独立 Linux 测试节点验证了 peer、路由表、网关回包和 Exit Node 客户端选择；
  测试环境中的其他 Tailscale 节点未参与测试。
- Gradle Kotlin/Java 编译和 Debug APK 打包通过；服务端 Go 测试通过。Android JUnit
  在相同源码的纯 ASCII 临时路径执行通过；当前 Windows 中文绝对路径会导致 Gradle
  test worker 的 classpath 失真并报 `ClassNotFoundException`。
- `onAppClose` 会话已完成真机心跳、ADB 强制停止、完整租约等待和服务端远端清理闭环；
  普通长期会话不会建立心跳租约。

## 尚未完成

- 按用户要求，本轮没有重启手机。加密会话和进程恢复路径已验证，但手机重启、解锁前
  行为、Boot/Always-on VPN 自动拉起均没有物理证据。
- 没有路由器侧抓包，也没有“Wi-Fi 可达 LAN 但完全无 WAN”的独立物理拓扑；当前
  fail-closed 证据来自真实 Wi-Fi 断开、接口绑定、路由表和端到端回包。
- 本机选择并使用另一个 Exit Node、真实 IPv6 转发、本地 `/32` 路由重叠、直连
  WireGuard UDP 路径和持续既有流的精确终止时延尚未验证。
- SSH/Web client、App Connector、posture、RemoteConfig 和 Netfilter 已接到
  LocalAPI，但效果取决于 Android build feature，未逐项做真机验证。管理页/服务端的
  AutoUpdate 字段尚未映射到 Android，当前不能依赖它改变手机更新行为。
- AAR 使用 NDK 29 生成，尚未用上游 Makefile 指定的 NDK 23.1 做发布兼容验证。
- Android lint 当前会把 390 项上游/兼容代码 warning 提升为 error；本轮没有用 baseline
  或关闭 `warningsAsErrors` 隐藏这些问题。

## 原型限制

- 服务端使用 SQLite，适合单实例部署；多实例需要共享事务数据库及共享限流。
- `onAppClose` 无法让应用在 OEM 直接强杀时执行代码。当前保证是 VPN 当场随进程
  断开、下次启动不恢复并补做服务端清理；强杀到再次启动之间可能暂留离线设备记录。
- forwarding binder 对新 socket fail closed，但没有统一跟踪并立即关闭所有既有
  TCP/UDP 流。
- 自动 Wi-Fi 路由只发现 IPv4 网关和接口前缀；多 Wi-Fi、IPv6 自动网关和复杂策略
  路由未实现。
- PinNode 使用官方 backend 的 peer/DNS/subnet 数据面，但不是官方客户端完整 UI。
  本地登录、多 profile 和本地配置编辑被有意移除，因此不能声称“界面和能力完全
  一模一样”。

## 部署限制

- 六位 PIN 不是强身份凭据；生产部署必须使用 HTTPS、管理员认证、外层限流、最小
  OAuth 权限和精确 ACL/route approval。
- 服务端本身不终止 TLS，需置于安全反向代理或服务网关后。
- 当前测试部署的反向代理入站依赖 Tailscale 直连，但自定义 DERP 不可达；NAT 映射过期
  后请求会在到达应用前超时，本机主动 ping 只能暂时恢复。发布前必须修复 DERP 或把
  服务端迁移到具有稳定入站路径的节点。
- 固定服务器 URL 可从 APK 中提取；locked UI 只用于防止普通用户查看/修改，不是
  秘密保护机制。
