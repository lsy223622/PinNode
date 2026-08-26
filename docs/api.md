# PinNode HTTP API v1

本文档是当前服务端、Android 客户端和管理页面共同遵循的 v1 契约。机器可读版本见 [OpenAPI 3.1](openapi.yaml)。

## 1. 设计边界

- API 领域对象统一称为 `Session`、`SessionConfig` 和 pairing code。
- v1 当前采用干净契约，不保留旧字段、旧路径或兼容别名。
- 会话创建是可重试的幂等操作；PIN 只在会话及其重放记录成功落库时消费。
- 所有活动会话都通过 revisioned `sync` 同步状态和配置。`exitPolicy.onAppClose=true` 时，同一个同步请求还会续期服务端清理租约。
- 客户端日志上传和会话中配置修改不塞进 `sync` 请求体；它们使用独立资源，并通过能力标识协商。

## 2. 通用约定

### 2.1 传输和 JSON

- 除本机调试外使用 HTTPS。
- 有 JSON 请求体的请求必须发送 `Content-Type: application/json`。
- JSON 请求体上限为 16 KiB；服务端拒绝未知字段、畸形 JSON、尾随第二个 JSON 值和不正确的媒体类型。
- 时间使用 UTC RFC 3339 字符串。可选时间没有值时为 JSON `null`，不使用空字符串。
- 列表没有成员时为 `[]`，不使用 `null`。
- 服务端响应可以增加字段；客户端必须忽略未知响应字段。

### 2.2 请求追踪和错误

每个 HTTP 响应都带 `X-Request-ID`。错误响应统一为：

```json
{
  "error": {
    "code": "pairing_code_invalid",
    "message": "code 无效、已使用或已过期",
    "retryable": false
  },
  "requestId": "PZr_xxxxxxxxxxxx"
}
```

客户端逻辑以稳定的 `error.code` 和 `retryable` 为准；`message` 用于展示或诊断，不作为分支条件。限流响应还带 `Retry-After`。

### 2.3 版本和能力

`GET /v1/meta` 是客户端建立会话前的能力发现入口。当前协议版本为 `1`，当前能力为：

- `idempotent-session-start-v1`
- `revisioned-session-config-v1`
- `session-sync-v1`
- `structured-errors-v1`

v1 冻结后，兼容变更只能增加可选响应字段、能力标识或新资源；不能改变已有字段含义、把可选字段改为必填、复用错误码表达另一种错误，或改变现有认证方式。

## 3. 认证模型

| 认证名 | 方式 | 使用范围 |
| --- | --- | --- |
| Public | 无 | `/healthz`、`/v1/meta`、登录前认证接口、创建会话 |
| Admin session | `pinnode_admin_session` HttpOnly、SameSite=Strict cookie | 管理接口读取 |
| Admin write | Admin session + `X-CSRF-Token` + 同源检查 | 管理接口写入、退出 |
| Session bearer | `Authorization: Bearer <sessionToken>` | 单个会话的读取、绑定、同步和停止 |
| Idempotency | `Idempotency-Key: <16..128 visible ASCII without whitespace>` | `POST /v1/sessions` 必填 |

首次管理员注册默认只允许服务端本机发起；远程管理登录和凭据保存要求 HTTPS。登录和注册还需要先完成 `/v1/auth/pow` 返回的工作量证明。

## 4. 接口总表

| 方法 | 路径 | 调用者 | 认证 | 用途 |
| --- | --- | --- | --- | --- |
| `GET` | `/healthz` | 运维 | Public | 存活检查 |
| `GET` | `/v1/meta` | Android/工具 | Public | 协议和能力发现 |
| `GET` | `/v1/auth/state` | 管理页 | Public/Admin | 管理员初始化和登录状态 |
| `GET` | `/v1/auth/pow` | 管理页 | Public | 获取登录工作量证明 |
| `POST` | `/v1/auth/setup` | 管理页 | Public + PoW | 首次创建管理员 |
| `POST` | `/v1/auth/login` | 管理页 | Public + PoW | 管理员登录 |
| `POST` | `/v1/auth/logout` | 管理页 | Admin write | 管理员退出 |
| `GET` | `/v1/tailscale-credentials` | 管理页 | Admin session | 列出凭据元数据 |
| `POST` | `/v1/tailscale-credentials` | 管理页 | Admin write | 验证并加密保存凭据 |
| `POST` | `/v1/pairing-codes` | 管理页 | Admin write | 创建一次性六位 PIN |
| `POST` | `/v1/sessions` | Android | Public + Idempotency | 兑换 PIN 并创建会话 |
| `GET` | `/v1/sessions` | 管理页 | Admin session | 查询会话历史 |
| `GET` | `/v1/sessions/{sessionId}` | Android/工具 | Session bearer | 读取当前会话 |
| `POST` | `/v1/sessions/{sessionId}/device` | Android | Session bearer | 绑定精确 Tailscale 节点并启用路由 |
| `POST` | `/v1/sessions/{sessionId}/sync` | Android | Session bearer | 上报已应用 revision、续期并获取配置 |
| `POST` | `/v1/sessions/{sessionId}/stop` | Android | Session bearer | 撤销路由并删除节点 |

## 5. 核心模型

### 5.1 `SessionConfig`

pairing code 请求中的 `config` 是对安全默认值的部分覆盖；服务端校验和规范化后，总是返回完整快照。默认配置仅发布当前 Wi-Fi 网关 `/32`。

| 字段 | 类型 | 默认值 |
| --- | --- | --- |
| `networkMode` | `"default" \| "cellular"` | `"cellular"` |
| `vpnEnabled` | boolean | `true` |
| `acceptRoutes` | boolean | `true` |
| `acceptDNS` | boolean | `true` |
| `useExitNode` | boolean | `false` |
| `exitNodeId` / `exitNodeIp` / `autoExitNode` | string | `""` |
| `exitNodeAllowLanAccess` | boolean | `false` |
| `subnetRouter` | boolean | `true` |
| `autoGatewayRoute` | boolean | `true` |
| `autoWiFiSubnetRoute` | boolean | `false` |
| `advertiseRoutes` | string[] | `[]` |
| `advertiseExitNode` | boolean | `false` |
| `disableSNAT` | boolean | `false` |
| `noStatefulFiltering` | boolean | `false` |
| `shieldsUp` | boolean | `false` |
| `runSSHServer` | boolean | `false` |
| `runWebClient` | boolean | `false` |
| `postureChecking` | boolean | `false` |
| `remoteConfig` | boolean | `false` |
| `hostname` | string | `""` |
| `netfilterMode` | `"" \| "on" \| "off" \| "nodivert"` | `""` |
| `appConnector` | boolean | `false` |
| `exitPolicy` | `ExitPolicy` | 全部关闭 |

`useExitNode=true` 时必须且只能指定 `exitNodeId`、`exitNodeIp`、`autoExitNode` 之一；当前 `autoExitNode` 只支持 `auto:any`。`autoGatewayRoute` 与 `autoWiFiSubnetRoute` 不能同时启用。`advertiseRoutes` 最多 16 条，必须是规范化网络 CIDR；默认路由只在 `advertiseExitNode=true` 时允许。

`ExitPolicy`：

```json
{
  "onAppClose": false,
  "networkChange": "",
  "afterConfigSeconds": 0,
  "afterLoginSeconds": 0,
  "at": ""
}
```

`networkChange` 可为 `""`、`"any-change"`、`"wifi-lost"`、`"cellular-lost"`。多个时间策略同时存在时，最早时间生效。

### 5.2 `Session`

会话有 `provisioning`、`active`、`cleaning`、`stopped`、`cleanup_failed` 五种状态。`configRevision` 是服务器期望配置版本，`appliedConfigRevision` 是客户端最近通过 `sync` 确认的版本。

## 6. 接口详情

### 6.1 健康和能力

`GET /healthz`：

```json
{"status":"ok"}
```

`GET /v1/meta`：

```json
{
  "apiVersion": "v1",
  "protocolVersion": 1,
  "features": [
    "idempotent-session-start-v1",
    "revisioned-session-config-v1",
    "session-sync-v1",
    "structured-errors-v1"
  ],
  "limits": {"jsonBodyBytes": 16384}
}
```

### 6.2 管理员认证

`GET /v1/auth/state` 返回 `setupRequired`、`setupAllowed`、`authenticated`；已登录时还返回 `username`、`csrfToken`、`expiresAt`。

`GET /v1/auth/pow` 返回 `id`、`challenge`、`difficulty`、`expiresAt`。客户端寻找十进制 `powNonce`，使 `SHA-256(challenge + ":" + powNonce)` 至少有 `difficulty` 个前导零位。

`POST /v1/auth/setup` 与 `POST /v1/auth/login` 请求相同：

```json
{
  "username": "admin",
  "password": "a long password",
  "powId": "...",
  "powNonce": "12345"
}
```

成功后设置管理员 cookie，并返回 `authenticated`、`username`、`csrfToken`、`expiresAt`。setup 成功为 `201`，login 成功为 `200`。`POST /v1/auth/logout` 无请求体，成功为 `204 No Content`。

### 6.3 Tailscale 管理凭据

`GET /v1/tailscale-credentials` 只返回元数据，绝不返回 token 或 client secret：

```json
{
  "credentials": [{
    "id": "credential-id",
    "name": "家庭 Tailnet",
    "type": "oauth_client",
    "createdAt": "2026-08-26T08:00:00Z",
    "lastUsedAt": null
  }]
}
```

`POST /v1/tailscale-credentials` 支持：

```json
{"name":"临时 API token","type":"api_token","token":"tskey-api-..."}
```

```json
{"name":"长期 OAuth","type":"oauth_client","clientId":"...","clientSecret":"..."}
```

OAuth 交换会显式请求 `auth_keys devices:core devices:routes` scope 和当前构建的受管设备 tag；服务端检查 token 响应的 scope，并用列出 auth keys 做无副作用的凭据/tailnet 探测。成功为 `201`，响应是单个凭据元数据对象。

### 6.4 创建 pairing code

`POST /v1/pairing-codes`：

```json
{
  "credentialId": "credential-id",
  "config": {
    "networkMode": "default",
    "exitPolicy": {"onAppClose": true}
  }
}
```

`config` 可省略；若提供则必须是对象。它是部分覆盖，不是完整替换。成功为 `201`，返回六位 `code`、`expiresAt` 和规范化后的完整 `config`。

### 6.5 创建会话

`POST /v1/sessions` 必须带 `Idempotency-Key`：

```json
{
  "code": "123456",
  "gatewayRoute": "192.168.1.1/32",
  "wifiSubnetRoute": "192.168.1.0/24"
}
```

成功为 `201`：

```json
{
  "protocolVersion": 1,
  "serverFeatures": ["idempotent-session-start-v1", "revisioned-session-config-v1", "session-sync-v1", "structured-errors-v1"],
  "sessionId": "...",
  "sessionToken": "...",
  "authKey": "tskey-auth-...",
  "provisioningHostname": "pinnode-...",
  "configRevision": 1,
  "syncIntervalSeconds": 60,
  "gatewayRoute": "192.168.1.1/32",
  "routes": ["192.168.1.1/32"],
  "wifiRoutes": ["192.168.1.1/32"],
  "config": {"...":"完整 SessionConfig"},
  "expiresAt": null
}
```

同一幂等键和同一规范请求会返回完全相同的敏感响应，并带 `Idempotent-Replayed: true`；同一键用于不同请求返回 `409 idempotency_key_conflict`。重放密文只保留到供应期限，设备成功绑定后删除。客户端必须在未知结果的网络失败中持久保存并复用同一键。

Tailscale auth key 创建成功后，服务端在一个 SQLite 事务中消费 PIN、写入会话并写入加密重放响应。校验失败、凭据失败或 Tailscale 上游失败不会消费 PIN。

### 6.6 查询会话

`GET /v1/sessions?limit=100` 返回管理端历史记录，`limit` 为 1..1000。每项包含公开会话字段，以及 `authKeyId`、`provisioningHostname`、`provisioningDeadline`、`lastSeenAt`、`syncDeadline`、`stoppedAt`、`stopReason`、`cleanupError`。

`GET /v1/sessions/{sessionId}` 使用 session bearer，返回路由、完整配置、`configRevision`、`appliedConfigRevision`、设备、状态、创建时间和可空的 `expiresAt`。

### 6.7 绑定设备

`POST /v1/sessions/{sessionId}/device`：

```json
{"nodeId":"node-id"}
```

服务端验证节点 tag、非 ephemeral 属性、供应 hostname 和创建时间，再只为该精确节点启用本会话路由。重复绑定同一节点是幂等成功，并返回 `nodeId`、`gatewayRoute`、`routes`、`wifiRoutes` 和 `enabled:true`。

### 6.8 同步会话

`POST /v1/sessions/{sessionId}/sync` 对所有 `active` 会话可用：

```json
{
  "protocolVersion": 1,
  "appliedConfigRevision": 1,
  "clientVersion": "0.1.0",
  "clientCapabilities": ["session-sync-v1"]
}
```

响应：

```json
{
  "protocolVersion": 1,
  "serverFeatures": ["idempotent-session-start-v1", "revisioned-session-config-v1", "session-sync-v1", "structured-errors-v1"],
  "status": "active",
  "serverTime": "2026-08-26T08:01:00Z",
  "nextSyncAfterSeconds": 60,
  "syncDeadline": null,
  "desiredConfig": null
}
```

当客户端 revision 落后时，`desiredConfig` 是包含 `revision`、完整 `config`、`gatewayRoute`、`routes`、`wifiRoutes`、`expiresAt` 的原子快照。客户端先完整应用快照，再在下一次 `sync` 上报新的 `appliedConfigRevision`。`onAppClose=true` 时 `syncDeadline` 为租约截止时间；其他会话为 `null`，但仍必须同步以接收配置。

### 6.9 停止会话

`POST /v1/sessions/{sessionId}/stop` 无请求体。服务端先向 Tailscale 发送 `{"routes":[]}`，再删除精确节点并撤销尚存的 auth key。成功为 `{"status":"stopped"}`；已经停止时为 `{"status":"already-stopped"}`。清理失败进入 `cleanup_failed`，reaper 会继续重试。

## 7. 稳定错误码

| 类别 | 错误码 |
| --- | --- |
| 通用协议 | `route_not_found`, `method_not_allowed`, `content_type_invalid`, `json_invalid`, `rate_limited`, `limit_invalid` |
| 管理认证 | `pow_unavailable`, `pow_invalid`, `auth_state_failed`, `admin_setup_forbidden`, `admin_username_invalid`, `admin_password_invalid`, `admin_already_initialized`, `admin_setup_failed`, `admin_session_create_failed`, `admin_credentials_invalid`, `admin_login_limited`, `admin_login_failed`, `admin_auth_required`, `admin_auth_failed`, `csrf_invalid`, `origin_invalid`, `secure_transport_required` |
| Tailscale 凭据 | `credential_required`, `credential_unavailable`, `credential_name_invalid`, `credential_type_unsupported`, `credential_list_failed`, `credential_save_failed`, `api_token_invalid`, `oauth_client_invalid`, `oauth_scope_invalid`, `credential_name_conflict`, `tailscale_permission_denied`, `tailscale_rate_limited`, `tailscale_unavailable` |
| pairing code | `pairing_code_format_invalid`, `pairing_code_invalid`, `pairing_code_read_failed`, `pairing_code_create_failed` |
| 会话创建 | `idempotency_key_invalid`, `idempotency_key_conflict`, `session_config_invalid`, `session_config_expired`, `wifi_gateway_required`, `wifi_subnet_required`, `gateway_route_invalid`, `wifi_subnet_route_invalid`, `session_create_failed`, `session_replay_failed` |
| 会话运行 | `session_list_failed`, `session_path_not_found`, `session_operation_not_found`, `session_read_failed`, `session_not_found`, `session_auth_required`, `session_auth_invalid`, `session_state_conflict`, `session_expired`, `protocol_version_unsupported`, `config_revision_invalid`, `client_metadata_invalid`, `node_id_invalid`, `device_not_ready`, `device_identity_mismatch`, `device_not_managed`, `device_already_bound`, `device_attach_failed`, `device_binding_refresh_failed`, `session_sync_failed`, `session_cleanup_start_failed`, `session_cleanup_failed` |

每个操作的成功状态和请求/响应结构以 OpenAPI 文件为准；错误状态使用本节的稳定错误码和统一错误信封。

## 8. Tailscale 出站调用

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `POST` | `/api/v2/oauth/token` | 用 client credentials、显式 scope 和 tag 换 access token |
| `GET` | `/api/v2/tailnet/{tailnet}/keys` | 无副作用凭据探测 |
| `POST` | `/api/v2/tailnet/{tailnet}/keys` | 创建一次性、持久、预授权、带受管 tag 的 auth key |
| `DELETE` | `/api/v2/tailnet/{tailnet}/keys/{keyId}` | 撤销 auth key |
| `GET` | `/api/v2/device/{nodeId}` | 验证待绑定节点 |
| `POST` | `/api/v2/device/{nodeId}/routes` | 启用精确路由或用空数组撤销路由 |
| `DELETE` | `/api/v2/device/{nodeId}` | 删除精确受管节点 |

## 9. 已预留的 v1 扩展

以下名称和资源边界被保留，但当前版本尚未提供对应端点，客户端不得假定它们可用：

- `client-log-upload-v1`：能力启用后使用独立的批量资源 `POST /v1/sessions/{sessionId}/log-batches`。日志具有单独的体积限制、压缩、去重、保留期和隐私策略，不占用 `sync` 的 16 KiB 控制面预算。
- `session-config-update-v1`：能力启用后由管理端使用 `PATCH /v1/sessions/{sessionId}/config` 提交基于当前 revision 的完整或受控 patch；并发写入使用 revision 前置条件。Android 仍只通过现有 `sync.desiredConfig` 接收原子快照。

新增扩展必须先出现在 `/v1/meta.features`，再允许客户端调用。未知能力必须被忽略；缺少客户端必需能力时，客户端应在创建会话前停止并给出明确的版本错误。
