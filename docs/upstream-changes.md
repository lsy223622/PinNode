# 上游改动记录

## 基线

- Android 上游：0867f01687a3955f7c0b5c6c62b236b997d68601
- Tailscale 核心：25877455e79d9e3ebd5e99200ca86fd62bcc0ed9

## PinNode 改动

### third_party/tailscale/wgengine/netstack/netstack.go

新增：

- ForwardSocketBinder：带 fd 和目标 netip.AddrPort 的平台回调。
- Impl.SetForwardSocketBinder：原子替换/清除回调。
- TCP subnet forwarding 的 destination-aware net.Dialer.Control。
- UDP subnet forwarding 的 SyscallConn FD 绑定。

回调只覆盖非本机、非 loopback forwarding destination；测试注入的
forwardDialFunc 优先级保持不变。回调错误会关闭 forwarding，默认无回调时仍
使用上游行为。

### Android/Go 边界

- AppContext 新增救援 socket 绑定和救援模式查询。
- IPNService 在救援转发 socket 上先 VpnService.protect，再调用
  RescueNetworkController。
- NetworkChangeCallback 在救援模式为 Tailscale 控制 socket 选择蜂窝。
- RescueNetworkController 以服务端返回的 `wifiRoutes` 多前缀选择 Wi-Fi/蜂窝；
  Tailscale 广告的 Exit Node 默认路由保留在蜂窝侧。
- libtailscale/backend.go 在 backend 创建前就安装 Android control/DERP 的网络
  绑定 hook，使救援登录早于 VPN service 启动时也能固定蜂窝；VPN service 启动
  时再叠加 protect hook，断开时保留绑定 hook。普通模式的 Android netns
  best-effort 兼容逻辑保留。

## 重放上游改动的方法

1. 固定新的 Android 与 tailscale.com 版本，先运行源码研究和现有测试。
2. 把 netstack.go 的 destination-aware binder 改动作为最小 patch 重新应用。
3. 检查 netstack 的 TCP/UDP forwarding 函数签名和 Android AppContext gomobile
   接口是否变化。
4. 运行 go test ./wgengine/netstack、Android/arm64 CGO 编译和真机网络矩阵。
5. 只有所有证据更新后，才修改本文件的基线哈希。

当前没有向 Tailscale 上游提交 PR；该改动是 PinNode 的本地 fork 方案，必须随本
文件一起审阅和维护。
