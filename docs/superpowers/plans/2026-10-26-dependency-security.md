# Dependency security update implementation plan

**Goal:** Resolve the confirmed product dependency advisories and verify the resulting Android and server artifacts without touching devices or deployed services.

**Architecture:** Keep `tailscale.com => ./third_party/tailscale` and its networking patches. Update only security dependencies and the Go toolchain pins; classify upstream auxiliary tooling separately using actual release dependency graphs.

**Tech stack:** Go, Tailscale Go, gomobile, Android Gradle, OSV, govulncheck.

**Spec:** User request dated 2026-10-26 in this chat.

## Constraints and review focus

- Preserve all pre-existing work and local Tailscale source patches.
- Resolve root, server, local Tailscale, Android and Linux server dependency graphs independently.
- Confirm official advisory version floors, including the actual downloaded Android toolchain.
- Distinguish module inventory, imported packages and reachable vulnerable symbols.
- Inspect prebuilt Web assets separately from unused upstream Yarn build tooling.
- Rebuild artifacts when feasible and inspect embedded Go versions and bundled licenses.
- Use existing tests; do not connect devices, push, publish or deploy.

## Execution

- [x] Snapshot current tracked/untracked source hashes and dependency graphs under ignored `.cache/security-20261026`.
- [x] Fetch official advisories and audit both Yarn lockfiles; document release inclusion evidence.
- [x] Update all three Go module manifests/checksums to the necessary security floors and Go 1.27.3; update the fixed Tailscale Go revision and build bootstrap pins.
- [x] Run module consistency checks, existing root/server/netstack tests, vet and govulncheck using the updated toolchain and relevant release build tags.
- [x] Rebuild Android native library/APK and Linux server where supported; verify embedded dependencies and licenses, recording environmental blockers precisely.
- [x] Update versioned license inventories and the aggregate bundle; review the security diff against the initial working state and write the evidence report.

Execution is inline in the current dirty checkout because the user explicitly requested direct completion while preserving existing work. No commits are needed. Validation results and any deviations will be recorded in the security report.
