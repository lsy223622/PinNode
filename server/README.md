# PinNode Provisioning Server

该模块负责一次性 PIN、受限 Tailscale auth key、受管配置、会话令牌、精确路由启用
和设备清理。管理员账号、登录会话和命名后的 Tailscale OAuth client/API access token
保存在 SQLite；credential 使用独立的 AES-256-GCM 子密钥加密，不会回显到浏览器。

## 本地启动

复制 `.env.example` 为受保护的 `.env`，再由进程管理器、容器的 `--env-file`，或本地
启动脚本把它导入服务端进程环境后执行：

```text
go test ./...
go run .
```

服务端只读取进程环境，不会自行解析 `.env`；直接执行 `go run .` 前必须先导入这些变量。
现有系统环境的值应优先于本地文件，生产部署不要依赖仓库目录中的 `.env`。
首次启动会在 `PINNODE_SECRET_PATH`（默认 `data/pinnode.secret`）创建 32 字节实例根密钥，
后续启动复用同一文件。服务端用 HKDF-SHA-256 从它分别派生凭据加密和 PIN HMAC 子密钥。
容器或正式密钥管理系统可改用 base64 编码的 `PINNODE_INSTANCE_KEY`，此时不会创建文件。
根密钥丢失或更换会使数据库中已有的 Tailscale 凭据无法解密；必须与 SQLite 分开备份。
旧部署的 `PINNODE_CREDENTIAL_KEY` 和 `PINNODE_CODE_PEPPER` 仍作为对应子密钥覆盖项读取，
两项均存在时不会额外创建实例密钥文件。

正式构建默认监听 `:6633`；Debug 构建使用 `go run -tags debug .` 启动，默认监听
`:6634`，因此两者可以同时运行。设置 `PINNODE_LISTEN_ADDR` 可覆盖任一默认值。
生产部署必须放在 HTTPS 反向代理或其他 TLS 终止层后，
并对管理员入口增加外层访问控制、限流和审计。

默认数据库为 `data/pinnode.db`。相对路径按服务端进程的当前工作目录解析，因此应从
`server` 目录启动，或在服务部署中使用绝对路径。`PINNODE_DATABASE_PATH` 可修改位置；SQLite 会保存
配对记录、全部历史会话、auth key ID、唯一设备绑定、同步租约和清理失败状态。管理员
可用 `GET /v1/sessions?limit=100` 查询不含令牌摘要的历史记录。

反向代理应追加 `X-Forwarded-For`，并把代理自身地址或网段写入
`PINNODE_TRUSTED_PROXY_CIDRS`。未显式信任 TCP 直连代理时，服务端会忽略该 header。

Debug 构建输出脱敏访问日志，包括方法、规范化路由、连接端地址、可信代理解析后的
客户端地址、状态码和耗时；Release 构建不输出该访问日志。日志不会包含 PIN、令牌、
密钥、请求体或完整会话 ID。

## 管理页面与快速模板

首次打开 `/` 或 `/admin` 时在登录页创建唯一管理员账号。默认仅允许从服务端本机完成
首次注册；确需从远端初始化时必须显式设置 `PINNODE_ALLOW_REMOTE_SETUP=true`，并通过
HTTPS 访问。密码至少 15 个字符，使用 Argon2id 加盐哈希；登录还执行本地 PoW、来源与
账号限速、递增退避。登录成功后使用 HttpOnly、SameSite=Strict 会话 Cookie 和 CSRF
token 保护管理接口。

管理员可以添加并命名 Tailscale OAuth client 或以 `tskey-api-` 开头的 API access token。
OAuth client 是长期运行的推荐方式：需要 `auth_keys`、`devices:core`、`devices:routes`
scope；Debug 构建授权 `tag:pinnode-test`，正式构建授权 `tag:pinnode`。服务端用
client ID/secret 换取约一小时的短期 access
token，在到期前自动重新获取；client secret 和普通 API token 都只以密文保存。普通 API
token 最长 90 天，适合快速配置。以后登录只需从列表选择，不再输入凭据。生成一次性 PIN 时，
所选凭据 ID 会与 PIN 和会话一起持久化，以便后续设备绑定、路由撤销和清理使用同一凭据。
升级旧数据库时，保存的第一个凭据会原子回填到尚无凭据 ID 的既有 PIN 和会话。
页面提供：

- 救援连接：`networkMode=cellular`，自动发布当前 Wi-Fi 网关 `/32`；
- 子网路由：`networkMode=default`，自动发布当前 Wi-Fi IPv4 子网；
- 代理节点：默认线路，发布 IPv4/IPv6 Exit Node 默认路由；
- 普通节点：默认线路，接受 tailnet DNS/路由，不发布 LAN。

快速模板只填写核心字段，管理员仍可继续配置客户端 Tailscale IPv4、本机使用的 Exit Node、额外 CIDR、
Allow LAN、SNAT/过滤、Hostname、SSH/Web client、posture、RemoteConfig、
App Connector 和 Netfilter 等偏好。

`routes` 是交给 Tailscale 广告并由服务端启用的完整列表；`wifiRoutes` 只包含 Android
应绑定到 Wi-Fi 的 LAN 前缀。Exit Node 的 `0.0.0.0/0` 和 `::/0` 不会进入
`wifiRoutes`。

## 登录与退出语义

服务端创建的 auth key 有效期为十分钟、不可复用、预授权，但设备本身是
`ephemeral=false`。每次供应还生成唯一 provisioning hostname；服务端只接受在本次
供应后创建、hostname 匹配、带目标 tag 且非 ephemeral 的设备，并把 node ID 以数据库
唯一约束绑定到会话，按配置设置 Tailscale IPv4，再启用精确路由。绑定成功后 Android
恢复管理员要求的 hostname。

未配置退出策略时，会话 `expiresAt` 为 `null`，也不建立清理租约。所有活动会话都按
服务端下发的间隔调用 revisioned `sync`，以确认已应用配置并接收后续完整配置快照。
只有启用 `exitPolicy.onAppClose` 时，同一个同步请求才续期租约；超过
`PINNODE_SYNC_LEASE_TTL`（默认五分钟）后，服务端撤销路由并删除设备，用于兜底进程
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
Tailscale API access token 最长 90 天并可提前撤销；长期服务应使用可撤销、按 scope 和
tag 限制的 OAuth client。数据库备份和 `pinnode.secret`/`PINNODE_INSTANCE_KEY` 应分开
保护，任何一方单独泄露都不应足以还原凭据。POSIX 首次创建使用 `0600`；Windows 部署
还应确保服务账号对密钥目录拥有独占 ACL。

## 生产部署（systemd + HTTPS 反向代理）

正式服务可以运行在自己的 Linux 服务器上。推荐使用独立服务账号和 systemd：

1. 在 `server` 目录执行 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o pinnode-server .`，把生成的二进制部署到服务账号可读的目录。
2. 通过 systemd 的 `WorkingDirectory` 和 `EnvironmentFile` 指定运行目录与进程环境，设置
   `PINNODE_LISTEN_ADDR=127.0.0.1:6633`；首次管理员注册完成后保持
   `PINNODE_ALLOW_REMOTE_SETUP=false`。
3. 由 OpenResty、Nginx 或其他 HTTPS 终止层监听公网，把请求反代到
   `127.0.0.1:6633`，追加 `X-Forwarded-For`、`X-Forwarded-Proto` 等 header，并配置
   `PINNODE_TRUSTED_PROXY_CIDRS` 只信任该反代来源。
4. 部署或升级后从外部 HTTPS 地址检查 `/healthz`，同时分别备份 SQLite 数据库和实例根密钥；
   替换二进制前保留上一版以便回滚。

接口契约见 `../docs/api.md` 与 `../docs/openapi.yaml`，安全边界见
`../docs/architecture.md` 与 `../docs/threat-model.md`。
