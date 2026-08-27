# Security policy

## Supported versions

Until the first release, security fixes are made on `main`. Afterward, the
latest published release and the current `main` branch are supported. Older
releases may not receive fixes.

## Reporting a vulnerability

Please use [GitHub private vulnerability reporting](https://github.com/lsy223622/PinNode/security/advisories/new).
Do not open a public issue for a suspected vulnerability.

Include the affected version or commit, the affected component (Android app,
server, or release pipeline), reproduction steps or a minimal proof of concept,
and the expected impact. Remove credentials, real user data, server URLs and
other private information from screenshots, logs and attachments.

If a credential may have been exposed, revoke or rotate it with its issuer
immediately. Do not include the credential in the report.

Reports are handled on a best-effort basis. PinNode does not currently operate
a bug-bounty program or promise a fixed response time. Please allow time for a
fix before public disclosure.

Issues that reproduce unchanged in upstream Tailscale code should also be
reported through the upstream project's security process. Please avoid filing
duplicate public reports while the issue is under coordinated disclosure.
