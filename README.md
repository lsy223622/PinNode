# PinNode

PinNode 是一个由服务端统一下发配置的 Android Tailscale 节点。它基于 Tailscale
Android 与 Go backend。

项目当前提供四个快速模板：

- 救援连接：Tailscale 控制面和非 LAN 流量固定使用移动数据，Wi-Fi 即使没有
  Internet 也保持连接，只把当前 Wi-Fi 网关 `/32` 发布为子网路由。
- 子网路由：保留 Android/Tailscale 的默认上游选择，自动发布当前 Wi-Fi IPv4
  子网。
- 代理节点：保留默认上游选择，将手机发布为 Exit Node。
- 普通节点：接受 tailnet 下发的 DNS 和子网路由，不发布手机所在 LAN。

管理员还可以下发本机使用的 Exit Node、额外 CIDR、Shields Up、Hostname、
SNAT/有状态过滤和当前 Android backend 支持的其他偏好。手机端只负责输入一次性
六位 PIN、展示连接/路由状态和明确退出，不允许本地修改受管配置。

## 会话与退出策略

默认配置没有自动退出时间：登录身份、服务端配置和加密会话记录会跨应用关闭、
进程重建和网络变化保留。关闭或重开界面本身不会注销节点；VPN 是否能在无人操作的
手机重启后自动恢复，仍取决于 Android 的 Always-on VPN/后台启动策略。

管理员可以配置以下退出条件，多个时间条件并存时取最早者：

- 从配置发布或登录开始经过指定秒数；
- 固定 RFC3339 时间；
- 任意网络变化、Wi-Fi 丢失或移动数据丢失；
- 应用关闭；
- 手机界面的“停止临时会话”按钮。

正常生命周期回调会立即执行本地注销。部分 OEM 会在最近任务划卡时直接强杀进程；
此时 VPN 随进程断开，PinNode 在下一次启动时不会恢复该会话，并会根据加密待办补做
服务端路由撤销和设备删除。

## 构建 Android 应用

Android 最低版本为 Android 13 / API 33。

上游 Android 构建入口仍可使用：

```text
make androidsdk
make tailscale-debug
```

Windows 下，在已生成 `android/libs/libtailscale.aar` 后也可以执行：

```text
cd android
.\gradlew.bat assembleDebug --no-daemon
```

开发构建可复制 `android/local.properties.example` 为被 Git 忽略的
`android/local.properties`。可编辑服务器构建只设置初始 URL；固定服务器构建同时
设置：

```properties
pinnode.serverUrl=https://pinnode.example.com
pinnode.serverName=My PinNode Server
pinnode.serverLocked=true
```

固定构建的界面只显示 `pinnode.serverName`，不会显示真实 URL，也不允许手机端修改。
Debug 构建可以临时使用同一 LAN 内电脑的 HTTP 地址；Release 构建要求 HTTPS。

## 运行配置下发服务

```text
cd server
go test ./...
go run .
```

Debug 服务使用独立默认端口，和正式服务同时运行：

```text
cd server
go run -tags debug .
```

正式服务默认监听 `:6633`，Debug 服务默认监听 `:6634`；设置
`PINNODE_LISTEN_ADDR` 可覆盖默认端口。

首次打开 `/` 或 `/admin` 时创建唯一管理员账号；后续使用账号密码和本地 PoW 登录。
登录后添加、命名并选择加密保存的 Tailscale OAuth client（推荐）或 API access token，再选择快速模板或微调
设置生成一次性 PIN。管理会话使用 HttpOnly Cookie、CSRF 校验、限速和递增退避保护。

服务端只创建 `reusable=false`、`preauthorized=true` 的短期一次性 auth key；加入后的
Android 设备是非 ephemeral 节点，因此默认能够长期保持登录。Tailscale credential
只以 AES-256-GCM 密文保存在服务端数据库中；实例根密钥首次启动时自动生成并独立保存。
OAuth access token 有效期短且由服务端自动续取。Debug 构建使用
`tag:pinnode-test`，正式构建使用 `tag:pinnode`；OAuth client 必须授权当前构建对应的标签。

## 当前验证边界

Android 16 / API 36 真机已验证普通节点持久恢复、自动 Wi-Fi 子网路由、
移动数据救援与断网 fail-closed、Exit Node 真实互联网转发、定时退出、网络变化退出、
OEM 划卡强杀后的不恢复与补偿清理，以及固定服务器 UI。按用户要求没有重启手机，
因此手机重启路径只有代码/构建证据，没有本轮物理测试证据。

PinNode 复用官方 backend 的 tailnet 数据面和 DNS/路由能力，但它不是官方客户端 UI
的完整复刻：本地账号登录、配置编辑和官方客户端的辅助页面被替换为受管单屏 UI。

完整字段、安全边界和逐项证据见：

- [`server/README.md`](server/README.md)
- [`docs/architecture.md`](docs/architecture.md)
- [`docs/known-limitations.md`](docs/known-limitations.md)
- [`docs/test-results.md`](docs/test-results.md)

本仓库基于 Tailscale Android commit
`0867f01687a3955f7c0b5c6c62b236b997d68601` 和匹配的 core snapshot
`25877455e79d9e3ebd5e99200ca86fd62bcc0ed9`。
