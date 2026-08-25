# PinNode 威胁模型

## 资产与信任边界

| 资产 | 位置 | 保护要求 |
| --- | --- | --- |
| Tailscale OAuth client/API token | 服务端 SQLite 密文 | AES-256-GCM、实例根密钥与数据库分离、不回显浏览器 |
| 管理员密码与会话 | 服务端 SQLite / 浏览器 Cookie | Argon2id、令牌摘要、HttpOnly/SameSite/CSRF、限速与退避 |
| 一次性 auth key | 单次供应响应 | 十分钟、不可复用、预授权、tag 限制 |
| 六位 PIN | 管理员与手机 | 五分钟、单次消费、来源限速 |
| 会话令牌 | 手机加密存储/服务端摘要 | 高熵、只用于绑定会话的操作 |
| 受管配置和路由 | 服务端、Android、控制面 | 规范 CIDR、精确 node ID、退出时撤销 |
| 家庭 LAN | Wi-Fi 后端 | 只向管理员明确配置的范围转发 |

## 主要控制

### 管理员登录、PIN 和 Tailscale 凭据

- 首次管理员注册默认只允许 loopback；远程初始化必须显式启用并使用 HTTPS。数据库以
  单例约束和原子插入阻止并发抢注。
- 单因素密码至少 15 个字符，使用 Argon2id（19 MiB、2 次迭代、单并行度）和独立随机盐。
  每次确认注册或登录前都要完成与来源绑定、两分钟过期、一次性消费的 SHA-256 PoW；
  PoW 只增加自动化成本，不替代来源/账号限速和持久化递增退避。
- 管理员会话令牌为 32 字节随机值，数据库只存 SHA-256 摘要；浏览器使用 HttpOnly、
  SameSite=Strict Cookie，HTTPS 下加 Secure，管理写接口同时验证同源和 CSRF token。

- PIN 使用 CSPRNG 生成，服务端只保存带 pepper 的摘要；原子消费保证并发只有一次成功。
- PIN 熵有限，必须与 HTTPS、管理员认证、应用层和外层限速共同使用。
- 首次启动生成独立实例根密钥，HKDF 派生互不复用的 PIN HMAC 与 AES-256-GCM 子密钥；
  根密钥文件不进入 SQLite。Tailscale OAuth client/API token 保存前在线验证并加密入库，
  不进入 APK、页面响应或普通日志；auth key 明文也不进入 Session Store，只有 key ID 入库。
- auth key 为 `reusable=false`、`preauthorized=true`，加入设备为
  `ephemeral=false`。持久节点是默认保持登录语义所必需，因此不能再依赖 ephemeral
  自动过期兜底。

### 设备绑定与删除

- 每个会话生成不可预测的 provisioning hostname。Android 用它完成首次注册；服务端
  同时校验创建时间、hostname、目标 tag 和 `isEphemeral=false`，再用数据库唯一约束
  绑定 Stable node ID。绑定成功后才允许恢复管理员配置的 hostname。
- 路由启用、撤销和删除只使用该会话绑定的 ID，不按 hostname 或 tag 模糊选择。
- 普通 `tskey-api-` access token 具有完整 API 权限且最长 90 天，应定期轮换。长期部署
  推荐 OAuth client，仅授予 `auth_keys`、`devices:core`、`devices:routes` 并限制到
  当前构建标签（Debug 为 `tag:pinnode-test`，正式版为 `tag:pinnode`）；短期 access token
  由服务端缓存并在临近过期时重新获取。

### 路由范围

- 最多接受 16 条规范 CIDR；非网络地址被拒绝，默认路由只有显式发布 Exit Node 时
  才允许。
- 自动网关 `/32` 与自动 Wi-Fi 子网互斥；救援模板默认只发布网关 `/32`。
- `routes` 与 `wifiRoutes` 分离，Exit Node 默认路由不会被错误绑定到无 WAN Wi-Fi。
- enabled routes 使用替换语义，退出时设置空列表后删除设备。

### 双网络错绑与 VPN 回环

- cellular 模式下控制面和非 LAN 只选移动数据，LAN 前缀只选 Wi-Fi。
- forwarding socket 先由 `VpnService.protect` 排除 VPN 回环，再逐 FD
  `Network.bindSocket`。
- 选定 Network 缺失时返回错误，不能静默回退到另一接口。
- 实机端到端结果支持当前 Android 16 参考设备行为，但没有路由器侧抓包，不能外推到
  所有 ROM、调制解调器和 IPv6 拓扑。

### 持久会话与退出失败

- Android 在登录前保存 provisioning session，并用队列加密保存 pending cleanup 及原
  服务器 URL；明确停止、时间策略和网络策略均先落盘清理任务，再执行本地和服务端清理。
- OEM 划卡强杀可能完全不投递生命周期回调。VPN 会随进程断开；`onAppClose` 会话在
  下次启动时禁止恢复并转为清理待办。强杀到下次启动之间，控制面可能暂时保留一个
  离线设备记录。
- 服务端 SQLite 保存全部会话状态；仅启用 `onAppClose` 的 active 会话通过心跳续租，
  重启后的 reaper 会继续清理 provisioning 超时、对应的心跳超时、固定过期和失败重试。
- 手机重启路径本轮没有物理测试。不能把普通进程恢复证据表述为已验证的开机自启。

### 固定服务器 APK

locked 构建只在 UI 隐藏 URL 和禁止编辑，不把 URL 当作秘密。攻击者仍可从 APK 中
提取它；服务端必须继续验证管理员会话/PIN、使用 TLS 并执行最小权限策略。

### 日志

生产环境需检查反向代理日志、Android logcat、崩溃转储和监控系统，避免记录
管理员 Cookie/CSRF token、session token、PIN、auth key、OAuth secret 或 API access token。

## 残余风险

- 六位 PIN 的在线猜测风险仍取决于限速和发码频率；应用层限制来源、PIN/令牌指纹和
  全局窗口，可信反向代理还应执行外层限流。
- PoW 可被专用硬件或分布式来源加速，且不能阻止攻击者针对已知账号进行拒绝服务；
  因此反向代理访问控制、TLS、共享限流与审计仍是公网部署的必要外层措施。
- `pinnode.secret`/`PINNODE_INSTANCE_KEY` 与数据库同时泄露时，保存的 Tailscale 凭据可以
  被还原；两者必须分开备份和保护。实例根密钥丢失也会使既有凭据及其会话清理不可用。
- 控制面/API 故障可能留下离线持久节点或已批准路由，必须持久化清理队列并定期审计
  专用 tag。
- Wi-Fi 丢失能阻止新转发 socket；已经建立的流没有统一注册表保证瞬时终止。
- 本机使用 Exit Node、高级 SSH/Web/App Connector 等偏好只完成字段下发或构建验证，
  未全部在本轮真实 tailnet 逐项验证。
