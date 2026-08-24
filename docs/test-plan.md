# PinNode 测试计划

## 测试环境

- Android 真机：至少一部支持蜂窝和 Wi-Fi 并行的 Android 设备。
- Wi-Fi：能连接家庭 LAN，但可关闭其 WAN；记录网关 IPv4、接口名和路由表。
- 蜂窝：可单独访问 Tailscale control/DERP，记录网络类型和 IPv4/IPv6 条件。
- Tailscale：准备隔离测试 tailnet、`tag:rescue-gateway`、精确
  `autoApprovers`/最小 ACL(grant)、devices:routes API 凭据，以及一个可作为 Exit
  Node 的测试节点。
- 观测：Android logcat、Tailscale debug/status、路由器访问日志、控制面设备/路由
  API 快照；不能只用“能 ping”作为全部证据。
- 本地 UI 冒烟：可使用 Android 36 x86_64 AVD；AVD 不能替代蜂窝/Wi-Fi 并行的实体
  网络测试。

## 网络路径矩阵

| ID | 流量 | 期望路径 | 证据 | 状态 |
| --- | --- | --- | --- | --- |
| N1 | control HTTPS | 蜂窝 | socket/抓包 + control 日志 | PASS（真机 bind 日志） |
| N2 | DERP 长连接 | 蜂窝 | DERP region/抓包 | PASS（真机 bind 日志 + DERP peer） |
| N3 | 直连 WireGuard UDP | 蜂窝 | peer endpoint/抓包 | NOT TESTED |
| N4 | LAN TCP 网关端口 | Wi-Fi | 路由器日志 + 绑定 Network | PASS（真机 HTTP 200） |
| N5 | LAN UDP 查询/服务 | Wi-Fi | 路由器日志 + UDP 回包 | PASS（真机 DNS 回包） |
| N6 | 非 LAN Tailscale peer | 蜂窝 | peer 流量证据 | PASS（真机经 DERP，移动数据关闭即失联） |
| N7 | ICMP 到网关 | 记录 netstack 行为 | 不能与 TCP/UDP 结果混写 | PASS（真机网关 3/3） |

## 配置下发矩阵

| ID | 配置 | 期望 | 证据 | 状态 |
| --- | --- | --- | --- | --- |
| C1 | 默认配置 | `routes=网关/32`，`wifiRoutes=网关/32` | server test + AVD UI + Tailscale routes API | PASS（AVD + 真机 LAN） |
| C2 | 自动 Wi-Fi subnet CIDR | 同一 CIDR 出现在 `routes` 和 `wifiRoutes` | server test + peer 路由表 | PASS（真机，具体网段已脱敏） |
| C3 | `subnetRouter=false` | 不发布普通 subnet，Wi-Fi route 列表为空 | server test + control status | PASS（普通节点真机） |
| C4 | `advertiseExitNode=true` | `routes` 增加 IPv4/IPv6 默认路由；默认路由不进 `wifiRoutes` | server test + 远程 peer | PASS（真机互联网转发） |
| C5 | `useExitNode` + ID/IP | Android `ExitNodeID/IP` 与 Allow LAN 被设置 | LocalAPI prefs + peer route | NOT TESTED |
| C6 | `acceptRoutes` / `acceptDNS` / `vpnEnabled` | 对应 `RouteAll` / `CorpDNS` / `WantRunning`；VPN 开启时需系统授权并建立 TUN | LocalAPI prefs + AVD VPN 状态 | PASS（AVD + 真机 VPN/TUN） |
| C7 | 高级官方偏好 | SSH/Web、Shields、SNAT、过滤等字段正确下发 | LocalAPI prefs/status | NOT TESTED；AutoUpdate 尚未映射 Android |

## 生命周期与安全矩阵

| ID | 场景 | 期望 |
| --- | --- | --- |
| L1 | 六位 code 正确兑换 | 一次成功、返回受管会话和一次性 key |
| L2 | code 并发兑换 | 只有一个 2xx，其余 401 |
| L3 | code 过期/重放 | 401，不创建 node |
| L4 | 错误 code 高频尝试 | 429 后继续拒绝 |
| L5 | Wi-Fi 丢失（无退出策略） | 保持会话、节点和广告路由；新 LAN 流 fail closed；Wi-Fi 恢复后同一会话自动继续 |
| L6 | 蜂窝丢失 | control/DERP 断开并重连；不能改走 Wi-Fi |
| L7 | App stop | 本地广告清空并 logout 当前受管身份，服务端清空 enabled route 并删除 node |
| L8 | 配置的时间/网络退出 | 与 L7 相同；时间条件取最早者 |
| L9 | 默认进程重启 | 恢复加密会话和 VPN，不重新输入 PIN |
| L10 | `onAppClose` + OEM 强杀 | VPN 断开；下次启动不恢复旧会话并补做远端清理 |
| L11 | 手机重启 | 保留登录/配置；自动 VPN 恢复取决于 Android Always-on/后台策略，需真机验证 |
| L12 | 服务端不可用 | 已运行会话按本地策略收敛，不生成新 key；保留清理待办 |
| L13 | Wi-Fi 与蜂窝都存在但 Wi-Fi 无 WAN | control 仍蜂窝，网关 TCP/UDP 仍 Wi-Fi |
| L14 | 本地网段重叠 | 记录 /32 最长前缀实际行为，不推断 |
| L15 | `onAppClose` 心跳中断 | 租约内不清理；超时后撤销路由并删除精确设备，历史状态保留为 `heartbeat_timeout` |

## 已执行的自动化测试

- server：code 原子消费、路由校验、供应和清理流程。
- server：配置规范化、默认网关 `/32`、Exit Node 默认路由与 Wi-Fi 绑定路由分离、
  配置响应和内嵌管理网页。
- server：普通/Debug 测试和 vet、MinGW64 CGO race detector；真机完成 `onAppClose`
  心跳中断与超时清理闭环。
- third_party/tailscale/wgengine/netstack：上游 netstack 测试套件，覆盖新代码
  的宿主编译和现有 forwarding 行为。

## 构建与真机命令

具备 Android 工具链后执行（上游完整构建仍可用 Makefile；Windows 当前可直接
执行 Gradle，AAR 需先由 gomobile/Makefile 生成）：

~~~text
make androidsdk
make tailscale-debug
android\\gradlew.bat assembleDebug --no-daemon
adb install -r android/build/outputs/apk/debug/android-debug.apk
adb logcat -s RescueNetworkController
~~~

完成 N1-N12 后，必须把每个结果、设备与 Android 版本（公开记录需脱敏）、
Tailscale commit、时间、抓包/日志路径写入 docs/test-results.md，不能只写“测试通过”。
