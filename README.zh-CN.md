[English](README.md) | 简体中文

<p align="center">
  <img src="assets/pinnode-nine-ring.svg" alt="PinNode" width="150">
</p>

# PinNode

PinNode 是一套自托管的 Android Tailscale 节点供应和生命周期管理系统。管理员先在 PinNode Server 中准备节点配置，再生成一次性的六位 PIN；Android 端输入 PIN 后，会领取本次会话使用的短期 Tailscale auth key 和节点配置，完成入网，并持续按照服务端管理的网络策略和退出策略运行。

长期 Tailscale 管理凭据保留在服务器上。手机只拿到当前 provisioning session 真正需要的材料。

## 系统结构

```text
                      ┌──────────────────────────────┐
                      │        Tailscale API         │
                      │ key / device / route 管理    │
                      └──────────────▲───────────────┘
                                     │
                              OAuth / API 凭据
                                     │
┌────────────────┐        ┌──────────┴───────────┐
│ 管理员浏览器    │───────►│   PinNode Server     │
│ 配置 + PIN      │        │                      │
└────────────────┘        │ SQLite               │
                          │ 加密凭据              │
                          │ session 生命周期      │
                          └──────────┬───────────┘
                                     │
                              PIN 兑换 / 同步
                                     │
                            一次性 auth key
                            session token/config
                                     │
                          ┌──────────▼───────────┐
                          │   PinNode Android    │
                          │                      │
                          │ Tailscale Go backend│
                          │ Android VpnService  │
                          │ 网络绑定             │
                          └──────────┬───────────┘
                                     │
                          WireGuard / DERP / control
                                     │
                          ┌──────────▼───────────┐
                          │       Tailnet        │
                          └──────────────────────┘
```

一次正常的节点供应过程是：

```text
管理员保存节点配置
        │
        ▼
Server 生成一次性 6 位 PIN
        │
        ▼
Android 兑换 PIN
        │
        ▼
Server 创建短期、不可复用的 Tailscale auth key
        │
        ▼
Android 使用 provisioning hostname 加入 tailnet
        │
        ▼
Server 核验新设备并绑定 Stable node ID
        │
        ▼
按配置应用 IP / 路由 / prefs
        │
        ▼
后续配置通过 revision sync 更新
        │
        ▼
退出策略触发路由撤销和节点清理
```

节点完成 provisioning 后，Stable node ID 会成为后续控制面的唯一设备标识。这个 Stable node ID 此后直接标识受管设备，路由操作和清理都围绕它进行。

## 适用场景

PinNode 的核心场景是由管理员预先定义 Android 节点的运行方式，再通过一次 PIN 配对完成设备供应。例如可以把一台 Android 设备配置成：

- 普通 tailnet 节点；
- 广告指定 CIDR 的子网路由器；
- exit node；
- 使用某个指定 exit node 的节点；
- 只发布当前 Wi-Fi 网关或整个子网的救援/维护节点；
- Tailscale control 和非 LAN 流量固定走蜂窝网络、指定 LAN 前缀走 Wi-Fi 的双网络节点。

代码中的快速模板只是对同一套 session config 的预设组合，后面的 provisioning、同步和 cleanup 仍然走同一个状态机。

## 组件

### PinNode Server

Go 服务端负责：

- 管理员 Web 界面；
- 管理员登录和浏览器 session；
- 命名的 Tailscale OAuth/API 凭据加密存储；
- 一次性 PIN；
- 短期 Tailscale auth key 供应；
- 新设备核验与 Stable node ID 绑定；
- Tailscale IP 和路由管理；
- session 配置、revision sync 和 cleanup；
- health 和管理状态接口。

### PinNode Android

Android App 把 Tailscale Go backend 与 PinNode 自己的 UI、provisioning 流程组合在一起。它使用独立的 application ID 和 Android 数据目录，同时复用 Tailscale backend / LocalAPI 的网络数据面。

App 会应用服务器下发的 Tailscale prefs，运行 Android `VpnService`，持久化待清理状态，并根据 session policy 把指定 socket 绑定到 Wi-Fi 或蜂窝网络。

## 系统要求

### Android

当前构建参数：

| 项目 | 当前值 |
| --- | --- |
| 最低 Android API | 33（Android 13） |
| Compile / target API | 36 |
| NDK | `23.1.7779620` |
| Java/Kotlin target | 17 |
| Android Gradle Plugin | `8.13.0` |
| Kotlin | `1.9.22` |

其中双网络模式依赖 Android 的网络绑定能力。实际部署如果依赖 Wi-Fi/蜂窝分流，最好在目标 OEM、ROM 和网络环境上做一次真实验证。

### Server

仓库当前声明 Go `1.26.6`。

服务端运行时还需要：

- 可写入 SQLite 数据库和实例密钥的持久目录；
- 到 `https://api.tailscale.com` 的网络，除非开发/测试环境明确覆盖了 `PINNODE_TAILSCALE_BASE_URL`；
- 目标 tailnet 名称；
- 通过管理员界面保存的一份 Tailscale 管理凭据；
- 远程使用时的 HTTPS 前置入口。

正式 release 会提供 Android APK 和 Linux `amd64` server archive。服务端也可以直接用 Go 工具链从源码编译。

## 快速部署

下面用源码构建 Server，并把环境变量文件交给 shell 加载。

### 1. 构建 Server

```bash
git clone https://github.com/lsy223622/PinNode.git
cd PinNode

(cd server && go build -o ../pinnode-server .)
```

### 2. 准备持久化目录

代码默认把 SQLite 放在：

```text
data/pinnode.db
```

数据库旁边的 `pinnode.secret` 是实例根密钥的权威来源；只要该文件存在，残留的密钥环境变量不会覆盖它。

长期服务更适合使用明确的绝对路径，例如：

```bash
sudo install -d -m 700 -o pinnode -g pinnode /var/lib/pinnode
```

这里的用户和组应替换为实际运行 PinNode 的系统账号。

### 3. 准备进程环境变量

`server/.env.example` 是配置模板。PinNode Server 直接用 `os.Getenv` 读取进程环境；通过 shell 或 service manager 把文件内容注入进程环境即可。

例如：

```bash
cp server/.env.example pinnode.env
$EDITOR pinnode.env

set -a
. ./pinnode.env
set +a
```

至少应设置真实 tailnet 和持久化路径。一个本地例子：

```bash
PINNODE_LISTEN_ADDR=:6633
PINNODE_TAILNET=example.com
PINNODE_DATABASE_PATH=/var/lib/pinnode/pinnode.db
PINNODE_SECRET_PATH=/var/lib/pinnode/pinnode.secret
PINNODE_ALLOW_REMOTE_SETUP=false
```

### 4. 启动 Server

```bash
./pinnode-server
```

仓库的环境变量模板使用 `6633` 端口。可以先从本机检查 health：

```bash
curl http://127.0.0.1:6633/healthz
```

正常响应：

```json
{"status":"ok"}
```

### 5. 创建管理员账号

首次管理员 setup 默认只接受 loopback 来源。服务器在远端时，可以直接用 SSH 本地转发：

```bash
ssh -L 6633:127.0.0.1:6633 user@your-server
```

然后在本机浏览器打开：

```text
http://127.0.0.1:6633/
```

管理员密码至少 15 个字符。服务端使用 Argon2id 保存密码哈希；注册和登录流程还包含短期 PoW、来源/账号限速和持久化递增退避。

### 6. 添加 Tailscale 管理凭据

在管理员界面保存：

- Tailscale OAuth client；或
- Tailscale API access token。

长期运行更适合使用按 tag 限权的 OAuth client。PinNode 进行 OAuth token exchange 时请求：

```text
auth_keys
devices:core
devices:routes
```

对应的 tailnet policy 还需要允许该 OAuth client 使用 PinNode 受管节点的 tag。当前 release build 使用 `tag:pinnode`，debug build 使用 `tag:pinnode-test`；OAuth client 的 tag 限制和 tailnet 的 `tagOwners` 应与实际 build 对齐。

### 7. 安装 Android App 并配对

从 GitHub Releases 安装 APK，或者自己从源码构建。App 里完成：

1. 可编辑服务器构建需要填入 PinNode Server 的 HTTPS 地址；固定服务器构建使用编译进 APK 的 URL，不显示可编辑的 URL 字段；
2. 授予 Android VPN 权限；
3. 输入管理员刚生成的六位 PIN；
4. 等待 provisioning 完成。

Server 绑定新节点的 Stable node ID 后，这台设备就进入受管 session 生命周期。

## PIN、凭据与节点绑定

### Tailscale 管理凭据

管理员保存的 OAuth/API credential 只存在服务端。入库前会先通过 Tailscale API 验证，再用当前 PinNode 实例的密钥体系加密保存。

OAuth 模式下，Server 用 client ID/secret 换取短期 access token，并在接近过期时重新获取。长期 OAuth/API 凭据始终留在 PinNode Server；Android 只接收当前 session 的 provisioning 材料。

### 六位 PIN

一个 pairing code 会绑定：

- 所选的 Tailscale credential ID；
- 规范化后的 session config；
- 创建和过期时间。

用户看到的是六位十进制数字。默认有效期 5 分钟，只能成功消费一次；服务端持久化的是带 pepper/HMAC 处理后的表示，同时在生成和兑换入口做限速。

### Provisioning auth key

PIN 兑换成功后，Server 创建的 Tailscale auth key 当前参数是：

```text
reusable=false
preauthorized=true
ephemeral=false
expiry = PINNODE_PROVISIONING_TTL（默认 10m）
tag = PinNode managed-device tag
```

auth key 明文只出现在 provisioning response；长期 Session Store 保留 key ID，用于后续控制面处理。

### Stable node ID 绑定

每个 session 会生成不可预测的 provisioning hostname。新节点出现后，Server 会核对创建时间、provisioning hostname、预期 tag 和 persistent-node 状态，再用数据库唯一约束绑定 Stable node ID。

从此以后，路由启用、撤销和设备删除都直接使用这个 node ID。

## Session 配置

管理员的配置会映射到 Android/Tailscale 行为：

| 配置 | 实际行为 |
| --- | --- |
| `networkMode=default` | 在 eligible 网络中优先非计费网络，再使用其他具备 Internet/DNS 的网络。 |
| `networkMode=cellular` | Tailscale control、DERP、endpoint 和非 LAN 转发绑定到移动数据；指定 LAN 前缀走 Wi-Fi。 |
| `acceptRoutes` | 对应 Tailscale `RouteAll`。 |
| `acceptDNS` | 对应 Tailscale `CorpDNS`。 |
| `tailscaleIp` | 节点加入后由 Server 设置 Tailscale IPv4。 |
| `useExitNode` | 按 ID/IP 或自动选择规则设置 exit node。 |
| `subnetRouter` + CIDR | Android advertise routes，Server 再启用对应 device routes。 |
| `autoGatewayRoute` | 加入当前 Wi-Fi IPv4 gateway 的 `/32`。 |
| `autoWiFiSubnetRoute` | 加入当前 Wi-Fi IPv4 接口所在的规范子网。 |
| `advertiseExitNode` | 加入 `0.0.0.0/0` 和 `::/0`。 |
| Shields/hostname/SSH/Web 等字段 | 应用对应的 Tailscale backend prefs。 |
| `exitPolicy` | 定义时间、网络或 App 生命周期触发的 session 退出条件。 |

机器可读的完整 contract 在 [`docs/openapi.yaml`](docs/openapi.yaml)，组件和状态机说明在 [`docs/architecture.md`](docs/architecture.md)。

## 网络模式

### `default`

默认模式接近上游的网络选择逻辑：eligible 的非计费网络优先，其次使用其他具备 Internet/DNS 能力的网络。

普通节点通常直接使用这个模式，让 Android 和 Tailscale backend 处理 Wi-Fi/蜂窝网络选择。

### `cellular`

cellular 模式把两类流量分开：

```text
Tailscale control / DERP / peer endpoint / 非 LAN 流量
                          │
                          ▼
                        移动网络

管理员指定的 LAN 前缀
                          │
                          ▼
                         Wi-Fi
```

转发 socket 会先经过 `VpnService.protect` 排除 VPN 回环，再根据策略绑定到指定的 Android `Network`。策略要求某张物理网络时，如果这张网络已经消失，会直接返回错误，避免受约束的连接悄悄切到另一张接口。

仓库当前的 threat-model 记录了 Android 16 参考设备上的真实测试证据。OEM 网络栈、调制解调器和 IPv6 拓扑差异较大，实际生产设备仍值得单独验证。

## 子网路由与 Exit Node

### 手工 CIDR

节点配置可以直接填写需要 advertise 的 CIDR。Server 会规范化前缀，当前最多接受 16 条路由。

### 当前 Wi-Fi 网关

`autoGatewayRoute` 只添加当前 Wi-Fi IPv4 gateway 的 `/32`。如果目标只是访问路由器或单个局域网设备，这种范围比公布整个子网更小。

### 当前 Wi-Fi 子网

`autoWiFiSubnetRoute` 根据当前 Wi-Fi 接口计算规范 IPv4 subnet。自动 gateway 和自动 whole-subnet 两种模式在 session model 中互斥。

### Advertise Exit Node

开启 `advertiseExitNode` 会加入：

```text
0.0.0.0/0
::/0
```

策略把 `routes` 与 `wifiRoutes` 分开保存；`wifiRoutes` 只装需要绑定 Wi-Fi 的 LAN prefix，exit-node 默认路由留在普通 `routes` 中。

### Tailscale route enable

Android backend 负责 advertise routes；PinNode Server 再通过 Tailscale API，针对已经绑定的 node ID 把对应 routes 设置为 enabled。

清理时沿用同一个 node ID，先撤掉 enabled routes，再删除受管 device。

## 退出策略与清理

Session 可以根据这些条件结束：

- 从配置时间开始计算的时长；
- 从登录时间开始计算的时长；
- 固定 RFC3339 时间；
- 网络变化；
- Wi-Fi 丢失；
- 蜂窝网络丢失；
- App close。

Android 会先持久化 cleanup 工作，再执行本地和服务端退出流程。Server 同时在 SQLite 里保存完整 session 状态，并且每 30 秒运行一次 reaper，继续处理 provisioning timeout、适用的 heartbeat/lease timeout、固定过期和失败后的 cleanup retry。

典型的控制面清理顺序：

```text
session 退出
    │
    ├─ enabled routes 设为空列表
    │
    ├─ 删除绑定的 Tailscale node
    │
    └─ 更新 session cleanup 状态
```

### App-close session

启用 `onAppClose` 的 active session 会续一个短 lease。`PINNODE_SYNC_LEASE_TTL` 默认 5 分钟。

操作系统/OEM 强杀 Android 进程时，正常生命周期回调可能被跳过。VPN 会随进程退出，控制面中的离线 device 记录可能暂时保留；下次 App 启动以及 Server 侧的 lease/reaper 会继续处理已经持久化的 cleanup 状态。

## Server 环境变量

PinNode Server 直接读取进程环境变量。

| 变量 | 代码 fallback / 仓库示例 | 作用 |
| --- | --- | --- |
| `PINNODE_LISTEN_ADDR` | `.env.example` 使用 `:6633` | HTTP 监听地址。 |
| `PINNODE_TAILSCALE_BASE_URL` | `https://api.tailscale.com` | Tailscale API base URL。 |
| `PINNODE_TAILNET` | 代码 fallback 为 `-`；`.env.example` 使用 `example.com` | 目标 tailnet；真实部署应显式设置。 |
| `PINNODE_DATABASE_PATH` | `data/pinnode.db` | SQLite 路径。 |
| `PINNODE_SECRET_PATH` | 默认位于数据库旁的 `pinnode.secret` | 仅在数据库旁的密钥文件尚不存在时指定实例根密钥文件。 |
| `PINNODE_INSTANCE_KEY` | 未设置 | 仅在数据库旁的密钥文件尚不存在时注入 base64 编码的 32-byte instance root key。 |
| `PINNODE_CODE_TTL` | `5m` | PIN 有效期。 |
| `PINNODE_PROVISIONING_TTL` | `10m` | provisioning/auth-key 时间窗口；最小 1 分钟。 |
| `PINNODE_SYNC_LEASE_TTL` | `5m` | session sync/cleanup lease；最小 2 分钟。 |
| `PINNODE_ADMIN_SESSION_TTL` | `12h` | 管理员浏览器 session；允许范围 15 分钟到 7 天。 |
| `PINNODE_POW_DIFFICULTY` | `18` | 登录 PoW 难度；允许范围 16–24。 |
| `PINNODE_ALLOW_REMOTE_SETUP` | `false` | 是否允许非 loopback 来源完成首次 setup。 |
| `PINNODE_TRUSTED_PROXY_CIDRS` | 代码 fallback 为空；`.env.example` 建议 `127.0.0.1/32,::1/128` | 允许提供可信 forwarded client/scheme 信息的代理来源。 |

另外还有 `PINNODE_CREDENTIAL_KEY` 和 `PINNODE_CODE_PEPPER` 两个旧版底层 secret override；它们仅在数据库旁的实例密钥文件不存在时用于兼容旧部署。普通部署应直接使用实例根密钥，由 HKDF 分别派生 credential encryption 和 PIN HMAC 的子密钥。

## 数据与实例密钥

### SQLite

数据库保存管理员/session 状态、加密后的命名 Tailscale credential、pairing/session metadata、config revision、绑定的 node ID 和 cleanup 状态。

### 实例根密钥

Server 优先从普通数据库路径旁的 `pinnode.secret` 加载 32-byte root key。该文件一旦存在即固定为实例密钥来源；如果文件损坏，Server 会拒绝启动而不会回退到环境变量。只有首次部署且该文件不存在时，才会按 `PINNODE_INSTANCE_KEY`、`PINNODE_SECRET_PATH` 或默认路径选择密钥来源，并在需要时生成文件。

Server 使用 HKDF-SHA-256 派生不同用途的子密钥：

- AES-256-GCM credential encryption；
- pairing-code HMAC / pepper。

实例密钥所在目录按 `0700` 创建；首次生成还使用同目录临时文件和原子发布，避免多个进程并发初始化出不同的实例密钥。

### 备份

需要可恢复的 Server 备份时，SQLite 数据库和 instance root key 应作为同一组敏感资产保存。数据库里是加密后的 credential，恢复时仍需要对应 root key 才能解密。

这组备份具有 PinNode 控制面的敏感度，适合放在有访问控制的备份存储中。

## 管理员安全

### 密码和 Session

管理员密码使用 Argon2id 加随机盐保存。当前 threat model 记录的参数是 19 MiB memory、2 次迭代、parallelism 1。

登录成功后生成 32-byte 随机 session token，SQLite 只保存 SHA-256 digest。浏览器 cookie 使用 `HttpOnly`、`SameSite=Strict`，HTTPS 请求下还会加入 `Secure`；管理写接口另外校验 origin 和 CSRF。

### PoW 和限速

setup/login 确认前需要完成短期、按来源绑定、一次性消费的 SHA-256 PoW challenge。来源/账号限速和持久化递增退避继续限制密码尝试频率。

### Credential at rest

Tailscale OAuth client/API credential 在进入 SQLite 前使用 AES-256-GCM 加密。管理员页面后续只展示 credential metadata；已保存的长期 secret 留在服务端密文存储中。

完整安全模型见 [`docs/threat-model.md`](docs/threat-model.md)。

## 反向代理与 HTTPS

PinNode Server 本身提供 HTTP application server。远程部署通常是：

```text
Android / browser
        │
        │ HTTPS
        ▼
TLS reverse proxy
        │
        │ 受控 backend 网络上的 HTTP
        ▼
PinNode Server
```

`PINNODE_TRUSTED_PROXY_CIDRS` 决定哪些来源地址可以提供 Server 会用于安全判断的 forwarded client/scheme 信息。程序 fallback 是空列表；仓库的 `.env.example` 给出了同机 proxy 常用的 loopback CIDR。

如果 Nginx/Caddy/Traefik 在另一个容器 network namespace 或另一台机器上，应填写真实的代理来源网络，并把 trusted proxy 范围限制在这些受控地址。

请求被识别为 HTTPS 时，PinNode 会设置 HSTS，并给管理员 cookie 加 `Secure`。

## Health、管理控制台与日志

### Health

```text
GET /healthz
```

返回：

```json
{"status":"ok"}
```

### API metadata

```text
GET /v1/meta
```

会返回 API/protocol version、server features 和请求大小限制，客户端可据此进行 capability discovery。

### 管理控制台

管理员 Web UI 可以读取当前 session 状态和 Tailscale 状态，并通过 server-sent events 订阅 state/log 更新。

内置 recent-log buffer 用于当前运行态排查。需要长期审计或历史检索时，日志应由进程管理器或外部日志系统持久化。

## HTTP API

PinNode 的公共协议位于 `/v1`。

对应文档：

- [`docs/api.md`](docs/api.md)：接口行为说明；
- [`docs/openapi.yaml`](docs/openapi.yaml)：OpenAPI 3.1 contract；
- [`docs/architecture.md`](docs/architecture.md)：组件与生命周期；
- [`docs/threat-model.md`](docs/threat-model.md)：安全模型和当前验证边界。

Server 对 JSON body 做大小限制，拒绝格式错误和 trailing input，返回结构化错误，并为请求生成 `X-Request-ID`。写集成时以 OpenAPI schema 作为 request/response contract。

## Android 构建

### Debug APK

根目录 Makefile 提供：

```bash
make pinnode-debug
```

这个 target 会构建 `libtailscale`、运行 Android unit tests、执行 `assembleDebug`，最后输出：

```text
pinnode-debug.apk
```

### Android SDK / NDK

Makefile 可以安装仓库要求的 Android SDK package：

```bash
make androidsdk
```

当前构建使用 API 36、Android Build Tools `36.0.0` 和 NDK `23.1.7779620`。

### 固定 Server 的 APK

先复制：

```bash
cp android/local.properties.example android/local.properties
```

设置：

```properties
pinnode.serverUrl=https://pinnode.example.com
pinnode.serverName=My PinNode
pinnode.serverLocked=true
```

locked build 在 UI 中只显示配置的名称，并移除普通的 server URL 编辑入口。URL 仍然作为 build config 存在 APK 内部，真正的访问边界仍由 Server authentication 和 HTTPS 提供。

配置 custom server 的 build 使用 `.custom` application-ID suffix；debug build 还会再加 `.debug`。

## 开发与测试

### Go tests

```bash
make go-test
```

这个 target 会执行仓库 Go tests，并排除必须经过 Android NDK 构建链的 `libtailscale` package。

单独开发 Server 时也可以：

```bash
cd server
go test ./...
go vet ./...
```

### Android unit tests

```bash
make test
```

### 格式化

```bash
make fmt
make fmt-check
```

### Android integration tests

仓库带有 Docker/KVM Android emulator 流程：

```bash
make android-integration-test
```

它会构建 debug APK，再在准备好的 Android integration image 中运行 adb-backed 测试。

涉及网络绑定、`VpnService`、subnet routing、exit node、进程死亡和 cleanup 的修改，除了 unit/emulator tests 之外也很适合补目标设备实测。

## 当前验证边界

仓库的设计文档和 threat model 对现有证据范围有明确记录：

- `cellular` 双网络路径已有 Android 16 参考设备实测，其他 OEM、modem 和 IPv6 组合仍需要按目标环境验证；
- 普通进程/session 恢复已有实现路径，物理开机后的完整 boot-persistence 属于另一项独立验证；
- OEM 强杀可能让 `onAppClose` 的控制面清理延后到 App 下次运行，或者由 Server lease/reaper 接管；
- Tailscale API 故障会让 route revoke 或远端 device deletion 延迟，即使本地 VPN 已经停止；
- 高级 Tailscale prefs 的真实 tailnet 测试覆盖程度并不完全相同，因此“backend 有这个字段”和“所有生产组合都经过验证”需要分开看。

如果某个 profile 会承担关键网络职责，这些边界正好可以作为上线前实机测试清单。

## Release

正式 release 流程会生成：

- 签名 Android APK；
- Linux `amd64` PinNode Server archive；
- SHA-256 checksum material。

Server archive 内包含 `pinnode-server` 和 `LICENSES.md`。运行时产生的 SQLite、`pinnode.secret`、env 文件和 credential 留在部署环境中；release asset 只包含发布产物和对应许可证材料。

Release 机制详见 [`docs/releasing.md`](docs/releasing.md)。

## 安全问题报告

安全问题使用 GitHub 私有漏洞报告 / Security Advisory。Tailscale credential、auth key、instance root key、管理员/session token 和数据库备份等敏感内容应只提交到私有报告。

当前报告策略见 [`SECURITY.md`](SECURITY.md)。

## 许可证与上游代码

PinNode 项目本身使用 [GNU General Public License v3.0](LICENSE)。

仓库同时包含或派生自 Tailscale 及其他第三方项目的代码。重新分发时需要保留相应的上游声明和许可证材料：

- [`LICENSES.md`](LICENSES.md)
- [`NOTICE`](NOTICE)
- [`PATENTS`](PATENTS)
