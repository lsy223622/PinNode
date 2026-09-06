[简体中文](README.md) | [English](README.en.md)

# PinNode

PinNode is an Android Tailscale node whose configuration is centrally delivered by the server. It is based on Tailscale Android and the Go backend.

The project currently provides four quick templates:

- Rescue connection: Tailscale control-plane and non-LAN traffic always use mobile data; Wi-Fi remains connected even without Internet access, while only the current Wi-Fi gateway `/32` is published as a subnet route.
- Subnet router: retains Android/Tailscale's default upstream selection and automatically publishes the current Wi-Fi IPv4 subnet.
- Proxy node: retains the default upstream selection and publishes the phone as an Exit Node.
- Normal node: accepts DNS and subnet routes delivered by the tailnet without publishing the LAN where the phone is located.

Administrators can also deliver the Exit Node used by the device, additional CIDRs, Shields Up, Hostname, SNAT/stateful filtering, and other preferences supported by the current Android backend. The phone only accepts a one-time six-digit authorization code, displays connection and route status, and provides an explicit exit action; it cannot locally modify managed configuration.

## Session and exit policy

The default configuration has no automatic exit time: the login identity, server configuration, and encrypted session records persist across app closure, process recreation, and network changes. Closing or reopening the interface does not log the node out; whether the VPN can automatically recover after an unattended phone restart still depends on Android's Always-on VPN and background-start policies.

Administrators can configure the following exit conditions. When multiple time-based conditions are present, the earliest one wins:

- A specified number of seconds after configuration publication or login;
- A fixed RFC3339 timestamp;
- Any network change, loss of Wi-Fi, or loss of mobile data;
- Application closure;
- The phone interface's “Stop temporary session” button.

Normal lifecycle callbacks immediately perform local logout. Some OEMs directly kill the process when the app is swiped away from recent tasks; in that case the VPN disconnects with the process, PinNode does not restore the session on the next launch, and it completes server-side route revocation and device deletion from the encrypted pending-work record.

## Building the Android app

The minimum Android version is Android 13 / API 33.

The current app version is `0.2.0`; the version name and deterministic `versionCode` rules are documented in [`docs/releasing.md`](docs/releasing.md). The base application ID is `com.lsy223622.pinnode`; custom-server builds append `.custom`, Debug builds append `.debug`, and builds with both options use `com.lsy223622.pinnode.custom.debug`.

The upstream Android build entry points remain available:

```text
make androidsdk
make pinnode-debug
```

On Windows, after `android/libs/libtailscale.aar` has been generated, you can also run:

```text
cd android
.\gradlew.bat assembleDebug --no-daemon
```

For a development build, copy `android/local.properties.example` to the Git-ignored `android/local.properties`. Public APKs do not embed a default server and keep the server editable; fixed-server builds set all of the following:

```properties
pinnode.serverUrl=https://pinnode.example.com
pinnode.serverName=My PinNode Server
pinnode.serverLocked=true
```

The fixed-server interface displays only `pinnode.serverName`, not the real URL, and does not allow the phone to modify it. Debug builds may temporarily use an HTTP address on a computer on the same LAN; Release builds require HTTPS.

## GitHub Release assets

When publishing a GitHub Release, `.github/workflows/release-apk.yml` checks out the `v<version>` tag matching `version.properties`. The same signing workflow can also be run manually from Actions to produce a short-lived workflow artifact for acceptance without creating a public Release. It runs formatting checks, Go tests, and Android unit tests, then uses `apksigner` to generate signed APKs and SHA-256 checksum files; ordinary pushes or commits do not freeze a version or trigger a signed release build.

Signing configuration is not stored in the repository. The standard repository's public workflow ignores server variables and neither writes nor locks a server. Release filenames contain the version and the seven-character short hash of the build commit, for example `pinnode-android-v<version>-0123456.apk` and `pinnode-server-v<version>-linux-amd64-0123456.tar.gz`; the corresponding `.sha256` files use the same filenames. APKs can be installed directly (they are not AABs), and include accompanying licenses in `assets/LICENSES.md`; the server archive uses the constant filename `pinnode-server` and also includes `LICENSES.md` so it can be replaced directly during deployment. The Release page also provides a merged license file named with the same version and commit hash. The server program contains no database, instance key, environment file, or credentials. Signing secrets, optional server variables for private forks, and release procedures are documented in [`docs/releasing.md`](docs/releasing.md).

## Running the configuration provisioning service

```text
cd server
go test ./...
go run .
```

The Debug service uses a separate default port and can run alongside the production service:

```text
cd server
go run -tags debug .
```

The production service listens on `:6633` by default, and the Debug service listens on `:6634`; set `PINNODE_LISTEN_ADDR` to override the default port.

The first visit to `/` or `/admin` creates the single administrator account. After that, sign in with the account password and local PoW. Add, name, and select an encrypted Tailscale OAuth client (recommended) or API access token, then choose a quick template or fine-tune settings to generate a one-time PIN. Administrative sessions are protected by an HttpOnly cookie, CSRF validation, rate limiting, and increasing backoff.

The configuration, Console status, and live logs in the administration page are three peer views. Console displays PINs awaiting redemption, active sessions, client status, and a snapshot of Tailscale nodes. Live logs support filtering by server/client source, level, component, session/node, and text, and automatically reconnect through a bounded SSE buffer. When the administrator session expires, the server revokes both event streams; client logs are not rebound across sessions.

The server creates only short-lived, one-time auth keys with `reusable=false` and `preauthorized=true`; Android devices that join are non-ephemeral nodes and can therefore remain logged in long-term by default. Tailscale credentials are stored in the server database only as AES-256-GCM ciphertext; the instance root key is generated automatically on first startup and stored separately. OAuth access tokens are short-lived and automatically renewed by the server. Debug builds use `tag:pinnode-test`, while production builds use `tag:pinnode`; the OAuth client must authorize the tag corresponding to the current build.

## Current validation boundary

A physical Android 16 / API 36 device has been used to verify normal-node persistence and recovery, automatic Wi-Fi subnet routing, mobile-data rescue and fail-closed behavior when disconnected, real Internet forwarding through an Exit Node, timed exit, exit on network changes, non-recovery and compensating cleanup after an OEM swipe-to-kill, and the fixed-server interface. At the user's request, the phone was not rebooted, so the phone-restart path has code and build evidence only, not physical-test evidence from this round.

PinNode reuses the official backend's tailnet data plane and DNS/route capabilities, but it is not a complete reproduction of the official client UI: local account login, configuration editing, and helper pages from the official client are replaced by a managed single-screen UI.

Full fields, security boundaries, and itemized evidence are documented in:

- [`docs/api.md`](docs/api.md)
- [`docs/openapi.yaml`](docs/openapi.yaml)
- [`server/README.md`](server/README.md)
- [`docs/architecture.md`](docs/architecture.md)

This repository is based on Tailscale Android commit `0867f01687a3955f7c0b5c6c62b236b997d68601` and the matching core snapshot `25877455e79d9e3ebd5e99200ca86fd62bcc0ed9`.

## License and security reports

Original PinNode code is licensed under the [GNU General Public License v3.0 (GPLv3)](LICENSE). Licenses, copyrights, third-party dependencies, patent grants, and trademark notices accompanying released binaries are combined in [`LICENSES.md`](LICENSES.md); [NOTICE](NOTICE), [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md), and [PATENTS](PATENTS) in the repository remain the authoritative sources for their respective materials. Please report security issues privately as described in [SECURITY.md](SECURITY.md); do not file a public issue.
