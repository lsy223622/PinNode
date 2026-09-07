English | [简体中文](README.zh-CN.md)

<p align="center">
  <img src="assets/pinnode-nine-ring.svg" alt="PinNode" width="150">
</p>

# PinNode

PinNode is a self-hosted provisioning and lifecycle system for Android devices that run as managed Tailscale nodes. An administrator prepares a node profile on PinNode Server, generates a one-time six-digit PIN, and gives that PIN to the Android device. The app redeems it for a short-lived Tailscale auth key plus the session configuration, joins the tailnet, and then follows the server-managed networking and cleanup policy.

The server keeps the long-lived Tailscale management credential. The Android side receives only the material needed for its own provisioning session.

## System overview

```text
                      ┌──────────────────────────────┐
                      │        Tailscale API         │
                      │ keys / devices / routes     │
                      └──────────────▲───────────────┘
                                     │
                         OAuth or API credential
                                     │
┌────────────────┐        ┌──────────┴───────────┐
│ Admin browser  │───────►│   PinNode Server     │
│ profile + PIN  │        │                      │
└────────────────┘        │ SQLite               │
                          │ encrypted credentials│
                          │ session lifecycle    │
                          └──────────┬───────────┘
                                     │
                           PIN redemption + sync
                                     │
                          one-time auth key
                          session token/config
                                     │
                          ┌──────────▼───────────┐
                          │   PinNode Android    │
                          │                      │
                          │ Tailscale Go backend│
                          │ Android VpnService  │
                          │ network binding     │
                          └──────────┬───────────┘
                                     │
                          WireGuard / DERP / control
                                     │
                          ┌──────────▼───────────┐
                          │       Tailnet        │
                          └──────────────────────┘
```

A normal session moves through this sequence:

```text
Admin saves a node profile
        │
        ▼
Server creates a one-time 6-digit PIN
        │
        ▼
Android redeems the PIN
        │
        ▼
Server creates a short-lived, non-reusable Tailscale auth key
        │
        ▼
Android joins the tailnet with a provisioning hostname
        │
        ▼
Server verifies the new device and binds its stable node ID
        │
        ▼
Configured IP/routes/preferences become active
        │
        ▼
Session sync carries later config revisions
        │
        ▼
Exit policy triggers route withdrawal and device cleanup
```

The exact node ID becomes the control-plane identity for route changes and cleanup after provisioning. This avoids relying on a hostname or tag to identify a device later in its lifecycle.

## Where PinNode is useful

PinNode is built around managed Android nodes rather than interactive per-device Tailscale account setup. A single profile can describe, for example:

- a normal tailnet node;
- a subnet router advertising selected CIDRs;
- an exit node;
- a device that uses a particular exit node;
- a rescue/maintenance node that advertises the current Wi-Fi gateway or subnet;
- a dual-network node that keeps Tailscale control and non-LAN traffic on cellular while using Wi-Fi for selected LAN prefixes.

The quick profiles in the codebase are presets over the same session configuration and cleanup state machine. Operators can also construct a profile directly from the individual fields.

## Components

### PinNode Server

The Go server provides:

- the administrator web interface;
- administrator authentication and session handling;
- encrypted storage for named Tailscale OAuth/API credentials;
- one-time PIN issuance;
- short-lived Tailscale auth-key provisioning;
- device verification and stable node-ID binding;
- Tailscale IP and route management;
- session configuration, revision sync, and cleanup;
- health and administrative status endpoints.

### PinNode Android

The Android app packages the Tailscale Go backend with a PinNode-specific UI and provisioning flow. Its install identity is separate from the official Tailscale app, while the networking core reuses Tailscale backend/LocalAPI behavior.

The app applies the server-provided Tailscale preferences, runs Android `VpnService`, records pending cleanup state, and can bind selected sockets to Wi-Fi or cellular networks according to the session policy.

## Requirements

### Android

Current build settings are:

| Item | Value |
| --- | --- |
| Minimum Android API | 33 (Android 13) |
| Compile/target API | 36 |
| NDK | `23.1.7779620` |
| Java/Kotlin target | 17 |
| Android Gradle Plugin | `8.13.0` |
| Kotlin | `1.9.22` |

The more specialized dual-network behavior depends on Android network-binding APIs and is worth validating on the actual device/OEM/network combination you plan to deploy.

### Server

The repository currently declares Go `1.26.6`.

A running server also needs:

- writable storage for the SQLite database and instance secret;
- reachability to `https://api.tailscale.com` unless `PINNODE_TAILSCALE_BASE_URL` is intentionally overridden for development/testing;
- the target tailnet name;
- a Tailscale management credential added through the administrator UI;
- HTTPS in front of the server for normal remote use.

Formal releases include an Android APK and a Linux `amd64` server archive. The server can also be built from source on a suitable Go toolchain.

## Quick start

This path builds the server from source and uses a local environment file as shell input.

### 1. Build the server

```bash
git clone https://github.com/lsy223622/PinNode.git
cd PinNode

(cd server && go build -o ../pinnode-server .)
```

### 2. Create a runtime directory

The code defaults the SQLite path to:

```text
data/pinnode.db
```

The instance root key is placed next to that database as `pinnode.secret` unless `PINNODE_SECRET_PATH` or `PINNODE_INSTANCE_KEY` supplies another source.

For a service deployment, an absolute state directory is easier to back up and permission explicitly:

```bash
sudo install -d -m 700 -o pinnode -g pinnode /var/lib/pinnode
```

Use the actual system account that will run the service.

### 3. Prepare process environment

`server/.env.example` is a template. PinNode Server reads process environment variables with `os.Getenv`; load the file through your shell or service manager rather than expecting the binary to parse `.env` itself.

For example:

```bash
cp server/.env.example pinnode.env
$EDITOR pinnode.env

set -a
. ./pinnode.env
set +a
```

At minimum, set the real tailnet and persistent paths. A typical local configuration might include:

```bash
PINNODE_LISTEN_ADDR=:6633
PINNODE_TAILNET=example.com
PINNODE_DATABASE_PATH=/var/lib/pinnode/pinnode.db
PINNODE_SECRET_PATH=/var/lib/pinnode/pinnode.secret
PINNODE_ALLOW_REMOTE_SETUP=false
```

### 4. Start the server

```bash
./pinnode-server
```

The environment template uses port `6633`. Check the health route locally:

```bash
curl http://127.0.0.1:6633/healthz
```

A healthy server returns:

```json
{"status":"ok"}
```

### 5. Create the administrator account

First-time administrator setup is restricted to loopback by default. On a remote host, SSH local forwarding provides a straightforward setup path:

```bash
ssh -L 6633:127.0.0.1:6633 user@your-server
```

Then open:

```text
http://127.0.0.1:6633/
```

The administrator password must be at least 15 characters. The server hashes it with Argon2id and also applies a short-lived proof-of-work challenge, source/account rate limiting, and persistent backoff to the login flow.

### 6. Add a Tailscale management credential

Open the administrator interface and save either:

- a Tailscale OAuth client; or
- a Tailscale API access token.

For long-running deployments, a tag-scoped OAuth client gives the server a narrower authority surface. PinNode's OAuth exchange requests these scopes:

```text
auth_keys
devices:core
devices:routes
```

The associated tailnet policy must allow the client to use the tag assigned to PinNode-managed devices. Current release builds use `tag:pinnode`; debug builds use `tag:pinnode-test`. Match the OAuth client constraints and tailnet `tagOwners` policy to the build you are deploying.

### 7. Install the Android app and pair a device

Install the APK from the project's GitHub Releases, or build it from source. In the app:

1. for an editable-server build, enter the PinNode Server HTTPS URL; a fixed-server build uses the URL compiled into the APK and does not show an editable URL field;
2. grant Android VPN permission;
3. enter a fresh six-digit PIN from the administrator interface;
4. let the provisioning session finish.

Once the server binds the new stable node ID, the device moves into the managed session lifecycle.

## Provisioning and credential flow

### Tailscale management credentials

Named OAuth/API credentials are stored on the server. Before storage, the credential is validated against the Tailscale API and encrypted for the local instance.

The normal OAuth path exchanges client ID/secret for a short-lived access token and caches that token until it approaches expiry. Long-lived OAuth/API credentials stay on PinNode Server; Android receives the session-scoped provisioning material.

### Pairing PIN

A pairing code binds:

- the selected Tailscale credential ID;
- a normalized session configuration;
- creation and expiry time.

The user-visible code is six decimal digits. The default lifetime is five minutes, and redemption is single-use. The server stores a peppered/HMAC-derived representation and uses rate limits around code creation and redemption.

### Provisioning auth key

After a PIN is accepted, the server creates a Tailscale auth key with:

```text
reusable=false
preauthorized=true
ephemeral=false
expiry = PINNODE_PROVISIONING_TTL (10m by default)
tag = PinNode managed-device tag
```

The plaintext auth key is returned in the provisioning response and is absent from the long-term Session Store; the key ID is retained for control-plane cleanup.

### Stable node binding

Each session receives an unpredictable provisioning hostname. The server verifies the newly created Tailscale device against its creation time, provisioning name, expected tag, and persistent-node state, then records the stable Tailscale node ID under a database uniqueness constraint.

After that binding, route changes and deletion target that exact node ID.

## Session configuration

The session model maps administrator choices to Android/Tailscale behavior.

| Configuration | Effect |
| --- | --- |
| `networkMode=default` | Uses the normal eligible-network selection; unmetered networks are preferred before other Internet/DNS-capable networks. |
| `networkMode=cellular` | Binds Tailscale control, DERP, endpoint, and non-LAN forwarding to mobile data; selected LAN prefixes use Wi-Fi. |
| `acceptRoutes` | Maps to Tailscale `RouteAll`. |
| `acceptDNS` | Maps to Tailscale `CorpDNS`. |
| `tailscaleIp` | Server sets the node's Tailscale IPv4 after join. |
| `useExitNode` | Selects an exit node by ID/IP or auto-selection mode. |
| `subnetRouter` + CIDRs | Advertises routes and asks the server to enable the corresponding device routes. |
| `autoGatewayRoute` | Adds the current Wi-Fi IPv4 gateway as a `/32`. |
| `autoWiFiSubnetRoute` | Adds the normalized current Wi-Fi IPv4 subnet. |
| `advertiseExitNode` | Adds `0.0.0.0/0` and `::/0` to advertised routes. |
| Shields/hostname/SSH/Web fields | Apply the corresponding Tailscale backend preferences. |
| `exitPolicy` | Defines time-, network-, or app-lifecycle conditions that end the session. |

The machine-readable contract lives in [`docs/openapi.yaml`](docs/openapi.yaml), and the implementation model is described in [`docs/architecture.md`](docs/architecture.md).

## Network modes

### `default`

The default mode leaves network selection close to upstream behavior. Eligible unmetered networks are preferred, followed by other networks that provide Internet/DNS capability.

This mode fits ordinary devices whose Wi-Fi/cellular choice can follow the operating system and Tailscale backend.

### `cellular`

Cellular mode separates traffic classes:

```text
Tailscale control / DERP / peer endpoint / non-LAN traffic
                          │
                          ▼
                    mobile network

configured LAN prefixes
                          │
                          ▼
                         Wi-Fi
```

Forwarding sockets are first protected from VPN recursion with `VpnService.protect`, then bound to the selected Android `Network` as needed. The policy treats a missing required physical network as an error instead of silently moving constrained traffic to another interface.

The repository's threat-model evidence currently includes Android 16 real-device behavior for this path. OEM network stacks, modem behavior, and IPv6 topologies vary, so production use should include testing on the actual hardware/network combination.

## Subnet routing and exit nodes

### Explicit routes

A profile may advertise administrator-supplied CIDRs. The server normalizes the prefixes and currently accepts up to 16 routes.

### Current Wi-Fi gateway

`autoGatewayRoute` adds only the Wi-Fi IPv4 gateway as `/32`, keeping the advertised range to that single address. This fits router or local-appliance access where a full subnet route would be broader than necessary.

### Current Wi-Fi subnet

`autoWiFiSubnetRoute` calculates the normalized IPv4 subnet from the active Wi-Fi interface. Gateway-only and whole-subnet auto modes are mutually exclusive in the session model.

### Exit-node advertisement

`advertiseExitNode` adds the IPv4 and IPv6 default routes:

```text
0.0.0.0/0
::/0
```

The policy keeps `routes` and `wifiRoutes` separate. `wifiRoutes` contains the LAN prefixes that bind to Wi-Fi; exit-node default routes remain in the general route set.

### Enabling routes in Tailscale

The Android node advertises the routes through its backend preferences. PinNode Server then uses the Tailscale API and the bound node ID to set the intended routes as enabled.

Cleanup uses the same node ID to withdraw enabled routes before deleting the managed device record.

## Exit policy and cleanup

A session can be configured to end on conditions such as:

- a duration measured from configuration time;
- a duration measured from login time;
- a fixed RFC3339 timestamp;
- network change;
- Wi-Fi loss;
- cellular loss;
- app close.

Android persists cleanup work before carrying out the local/server shutdown sequence. The server also stores session status in SQLite and runs a reaper every 30 seconds to continue cleanup for provisioning timeouts, applicable heartbeat/lease expiry, fixed expiry, and failed cleanup retries.

A typical control-plane cleanup sequence is:

```text
session exits
    │
    ├─ set enabled routes to an empty list
    │
    ├─ delete the bound Tailscale node
    │
    └─ update session cleanup state
```

### App-close sessions

For sessions using `onAppClose`, active clients renew a short lease. The default `PINNODE_SYNC_LEASE_TTL` is five minutes.

Android process death is an operating-system event rather than a guaranteed lifecycle callback. If an OEM force-kills the app, the VPN stops with the process and the server may temporarily retain the offline device record. The next app start and server-side cleanup machinery continue the lifecycle from persisted state.

## Server environment

PinNode Server reads its configuration from process environment variables.

| Variable | Runtime fallback / behavior | Purpose |
| --- | --- | --- |
| `PINNODE_LISTEN_ADDR` | Repository env template uses `:6633` | HTTP listen address. |
| `PINNODE_TAILSCALE_BASE_URL` | `https://api.tailscale.com` | Tailscale API base URL. |
| `PINNODE_TAILNET` | `-` in code; env template uses `example.com` | Target tailnet identifier. Set this for a real deployment. |
| `PINNODE_DATABASE_PATH` | `data/pinnode.db` | SQLite path. |
| `PINNODE_SECRET_PATH` | defaults beside the database as `pinnode.secret` | File containing the instance root key. |
| `PINNODE_INSTANCE_KEY` | unset | Optional base64-encoded 32-byte instance root key supplied directly by environment. |
| `PINNODE_CODE_TTL` | `5m` | Pairing-code lifetime. |
| `PINNODE_PROVISIONING_TTL` | `10m` | Provisioning/auth-key window. Minimum: 1 minute. |
| `PINNODE_SYNC_LEASE_TTL` | `5m` | Session sync/cleanup lease. Minimum: 2 minutes. |
| `PINNODE_ADMIN_SESSION_TTL` | `12h` | Admin browser session. Allowed range: 15 minutes to 7 days. |
| `PINNODE_POW_DIFFICULTY` | `18` | Login proof-of-work difficulty. Allowed range: 16–24. |
| `PINNODE_ALLOW_REMOTE_SETUP` | `false` | Permits first-time setup from non-loopback clients when enabled. |
| `PINNODE_TRUSTED_PROXY_CIDRS` | empty in code; env template suggests `127.0.0.1/32,::1/128` | Sources whose forwarded client/scheme metadata the server may trust. |

`PINNODE_CREDENTIAL_KEY` and `PINNODE_CODE_PEPPER` also exist as lower-level secret overrides. Normal deployments can let the instance root key derive separate credential-encryption and PIN-HMAC subkeys instead.

## Data and instance keys

### SQLite database

The database stores administrator/session state, encrypted named Tailscale credentials, pairing/session metadata, configuration revisions, bound node IDs, and cleanup state.

### Instance root key

When `PINNODE_INSTANCE_KEY` is absent, the server loads or creates a 32-byte root key from `PINNODE_SECRET_PATH`. The default path is `pinnode.secret` beside a normal database file.

The server derives separate subkeys with HKDF-SHA-256 for:

- AES-256-GCM credential encryption;
- pairing-code HMAC/pepper material.

The root-key file is created in a mode-`0700` parent directory and published atomically so concurrent first starts converge on one instance key.

### Backups

For a recoverable server backup, treat the SQLite database and instance root key as one protected set. The database contains encrypted credentials; the root key provides the material needed to decrypt them on restore.

This combination has the same sensitivity as the live PinNode control plane and belongs in access-controlled backup storage.

## Administrator security

### Password and session

Administrator passwords are stored as Argon2id hashes with per-password salt. The current parameters documented in the threat model are 19 MiB memory, two iterations, and parallelism 1.

Successful login creates a 32-byte random session token; SQLite stores only its SHA-256 digest. Browser sessions use `HttpOnly` and `SameSite=Strict` cookies, with `Secure` set for HTTPS requests. State-changing admin API calls also validate origin and CSRF material.

### Proof of work and rate limiting

Setup/login confirmation includes a source-bound, short-lived, single-use SHA-256 proof-of-work challenge. Source/account limits and persistent escalating backoff provide additional controls around password attempts.

### Credential storage

Tailscale OAuth client/API credentials are encrypted with AES-256-GCM before SQLite persistence. The administrator interface later exposes credential metadata only; the stored secret remains server-side.

The full trust model is documented in [`docs/threat-model.md`](docs/threat-model.md).

## Reverse proxy and HTTPS

PinNode Server is an HTTP application server. For remote deployment, the normal layout is:

```text
Android / browser
        │
        │ HTTPS
        ▼
TLS reverse proxy
        │
        │ HTTP on a controlled backend path
        ▼
PinNode Server
```

`PINNODE_TRUSTED_PROXY_CIDRS` defines which proxy source addresses are allowed to supply forwarded client/scheme information used by security decisions. The runtime fallback is an empty list; the repository's environment template suggests loopback CIDRs for a same-host proxy.

If the proxy runs in another container/network namespace or on another host, set this list to the actual trusted proxy source network rather than a broad client-facing network.

When the request is recognized as HTTPS, PinNode adds HSTS and marks the administrator cookie `Secure`.

## Health, admin console, and logs

### Health endpoint

```text
GET /healthz
```

returns:

```json
{"status":"ok"}
```

### API metadata

```text
GET /v1/meta
```

returns protocol/API version information, server feature identifiers, and request-size limits that clients can use for capability discovery.

### Admin console

The administrator web application can read current session state and Tailscale status and can subscribe to server-sent event streams for state/log updates.

The built-in recent-log buffer is an operational convenience rather than a durable audit store. Use the process manager or external logging stack when you need persistent history.

## HTTP API

PinNode's public protocol is versioned under `/v1`.

Useful source documents:

- [`docs/api.md`](docs/api.md) — human-readable API behavior;
- [`docs/openapi.yaml`](docs/openapi.yaml) — OpenAPI 3.1 contract;
- [`docs/architecture.md`](docs/architecture.md) — component and lifecycle model;
- [`docs/threat-model.md`](docs/threat-model.md) — security reasoning and current validation boundaries.

The server applies bounded JSON parsing, rejects malformed/trailing input, returns structured errors, and emits `X-Request-ID` for requests. Integration code should use the OpenAPI contract for request/response schema rather than copying structures from README examples.

## Android builds

### Standard debug APK

The root Makefile provides:

```bash
make pinnode-debug
```

The target builds `libtailscale`, runs Android unit tests, assembles the debug APK, and copies the result to:

```text
pinnode-debug.apk
```

### Android SDK/NDK bootstrap

The Makefile can install the repository's Android SDK package set:

```bash
make androidsdk
```

It uses API level `36`, Android Build Tools `36.0.0`, and NDK `23.1.7779620` from the repository build configuration.

### Fixed-server build

Copy the local-properties template:

```bash
cp android/local.properties.example android/local.properties
```

Then set:

```properties
pinnode.serverUrl=https://pinnode.example.com
pinnode.serverName=My PinNode
pinnode.serverLocked=true
```

A locked build shows the configured display name and removes normal server-URL editing from the UI. The URL remains build configuration inside the APK, so server authentication and HTTPS remain the security boundary.

A build with a custom server uses the `.custom` application-ID suffix; debug builds add `.debug` as well.

## Development and tests

### Go tests

```bash
make go-test
```

This runs Go tests across the repository while excluding the package that requires the Android NDK build path.

Server-only development can also use:

```bash
cd server
go test ./...
go vet ./...
```

### Android unit tests

```bash
make test
```

### Formatting

```bash
make fmt
make fmt-check
```

### Android integration tests

The repository includes a Docker/KVM-backed Android emulator path:

```bash
make android-integration-test
```

It builds a debug APK and runs the adb-backed integration suite in the prepared Android integration image.

Network binding, `VpnService`, subnet routing, exit-node behavior, process death, and cleanup are the areas where emulator/unit coverage benefits most from real-device validation as well.

## Current validation boundaries

The repository's non-README design and threat-model documents record several areas where evidence is intentionally scoped:

- the dual-network `cellular` path has real-device evidence on an Android 16 reference device; broader OEM/modem/IPv6 combinations require their own validation;
- ordinary process/session recovery has implementation coverage, while full physical boot-persistence behavior is a separate validation problem;
- an OEM force-kill can postpone the server's awareness of `onAppClose` until the device runs again or the server cleanup lease/reaper catches up;
- a Tailscale API outage can delay route revocation or remote device deletion even after the local VPN has stopped;
- advanced Tailscale preferences have different levels of real-tailnet test coverage, so “field exists in the backend” and “every production combination has been exercised” are separate claims.

These boundaries are useful when deciding what to test on target hardware before relying on a profile operationally.

## Releases

The release process publishes:

- a signed Android APK;
- a Linux `amd64` PinNode Server archive;
- SHA-256 checksum material.

The server archive includes the `pinnode-server` binary and `LICENSES.md`. Release packaging intentionally keeps runtime state such as the SQLite database, `pinnode.secret`, environment files, and credentials outside the artifacts.

See [`docs/releasing.md`](docs/releasing.md) for release mechanics.

## Security reports

Use GitHub's private vulnerability-reporting / Security Advisory flow for security issues. Secrets such as Tailscale credentials, auth keys, instance root keys, administrator/session tokens, and database backups belong in the private report rather than a public issue.

See [`SECURITY.md`](SECURITY.md) for the current reporting policy.

## License and upstream code

PinNode's project license is [GNU General Public License v3.0](LICENSE).

The repository also contains or derives code from Tailscale and other third-party projects. Distribution should preserve the applicable upstream notices and license material in:

- [`LICENSES.md`](LICENSES.md)
- [`NOTICE`](NOTICE)
- [`PATENTS`](PATENTS)
