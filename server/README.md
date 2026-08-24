# PinNode Provisioning Server

该模块负责一次性 PIN、受限 Tailscale auth key、受管配置、会话令牌、精确路由启用
和设备清理。OAuth client secret 只允许出现在服务端环境变量中。

## 本地启动

复制 `.env.example` 为受保护的 `.env`，再由进程管理器、容器的 `--env-file`，或本地
启动脚本把它导入服务端进程环境后执行：

```text
go test ./...
go run .
```

服务端只读取进程环境，不会自行解析 `.env`；直接执行 `go run .` 前必须先导入这些变量。
现有系统环境的值应优先于本地文件，生产部署不要依赖仓库目录中的 `.env`。

正式构建默认监听 `:6633`；Debug 构建使用 `go run -tags debug .` 启动，默认监听
`:6634`，因此两者可以同时运行。设置 `PINNODE_LISTEN_ADDR` 可覆盖任一默认值。
生产部署必须放在 HTTPS 反向代理或其他 TLS 终止层后，
并对管理员入口增加外层访问控制、限流和审计。

默认数据库为 `data/pinnode.db`。相对路径按服务端进程的当前工作目录解析，因此应从
`server` 目录启动，或在服务部署中使用绝对路径。`PINNODE_DATABASE_PATH` 可修改位置；SQLite 会保存
配对记录、全部历史会话、auth key ID、唯一设备绑定、心跳租约和清理失败状态。管理员
可用 `GET /v1/sessions?limit=100` 查询不含令牌摘要的历史记录。

反向代理应追加 `X-Forwarded-For`，并把代理自身地址或网段写入
`PINNODE_TRUSTED_PROXY_CIDRS`。未显式信任 TCP 直连代理时，服务端会忽略该 header。

Debug 构建输出脱敏访问日志，包括方法、规范化路由、连接端地址、可信代理解析后的
客户端地址、状态码和耗时；Release 构建不输出该访问日志。日志不会包含 PIN、令牌、
密钥、请求体或完整会话 ID。

## 管理页面与快速模板

打开 `/` 或 `/admin`，输入 `PINNODE_ADMIN_TOKEN` 后调用
`POST /v1/pairing-codes`。令牌只放在 Bearer header 中，不写入 URL 或
localStorage。页面提供：

- 救援连接：`networkMode=cellular`，自动发布当前 Wi-Fi 网关 `/32`；
- 子网路由：`networkMode=default`，自动发布当前 Wi-Fi IPv4 子网；
- 代理节点：默认线路，发布 IPv4/IPv6 Exit Node 默认路由；
- 普通节点：默认线路，接受 tailnet DNS/路由，不发布 LAN。

快速模板只填写核心字段，管理员仍可继续配置本机使用的 Exit Node、额外 CIDR、
Allow LAN、SNAT/过滤、Hostname、SSH/Web client、posture、RemoteConfig、
App Connector 和 Netfilter 等偏好。

`routes` 是交给 Tailscale 广告并由服务端启用的完整列表；`wifiRoutes` 只包含 Android
应绑定到 Wi-Fi 的 LAN 前缀。Exit Node 的 `0.0.0.0/0` 和 `::/0` 不会进入
`wifiRoutes`。

## 登录与退出语义

服务端创建的 auth key 有效期为十分钟、不可复用、预授权，但设备本身是
`ephemeral=false`。每次供应还生成唯一 provisioning hostname；服务端只接受在本次
供应后创建、hostname 匹配、带目标 tag 且非 ephemeral 的设备，并把 node ID 以数据库
唯一约束绑定到会话，再启用精确路由。绑定成功后 Android 恢复管理员要求的 hostname。

未配置退出策略时，会话 `expiresAt` 为空，也不建立心跳租约。只有启用
`exitPolicy.onAppClose` 时，Android 才按服务端下发的间隔发送心跳；超过
`PINNODE_HEARTBEAT_TTL`（默认五分钟）后，服务端撤销路由并删除设备，用于兜底进程
被直接终止、无法送达关闭回调的场景。
可配置：

- `exitPolicy.afterConfigSeconds`
- `exitPolicy.afterLoginSeconds`
- `exitPolicy.at`
- `exitPolicy.networkChange`：`any-change`、`wifi-lost`、`cellular-lost`
- `exitPolicy.onAppClose`

前三个时间条件由服务端和 Android 共同执行，取最早时间。网络和应用生命周期条件
由 Android 触发；清理失败会在手机的加密待办中保留，并在网络恢复或下次启动重试。

## 部署边界

SQLite 面向单个 PinNode 服务实例。多实例部署需要改用共享事务数据库，并把应用层
限流迁移到共享存储或由可信反向代理统一执行。数据库会保留全部会话历史，不自动删除。

接口和安全边界见 `../docs/architecture.md` 与 `../docs/threat-model.md`。
