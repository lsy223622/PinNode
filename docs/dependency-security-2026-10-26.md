# 2026-10-26 依赖安全更新与验证

本次针对现有工作区进行最小依赖安全更新。起始分支为 `main`，HEAD 为 `32aecb2a55e49cf3e8efe05aab5c90f5b836fe80`，开始时已经有 Android、server、工作流和构建配置等未提交修改。本报告的“更新前”指开始时的文件内容，不是 HEAD 内容。初始源码快照与 SHA-256 清单保存在忽略目录 `.cache/security-20261026/before/`、`before-hashes.json`。

## 版本与公告

| 组件 | 更新前 | 更新后 | 官方修复依据 |
| --- | --- | --- | --- |
| 根模块、本地 Tailscale：x/crypto | 0.54.0 | 0.57.0 | [GO-2026-6303](https://pkg.go.dev/vuln/GO-2026-6303)、[GO-2026-6354](https://pkg.go.dev/vuln/GO-2026-6354)、[GO-2026-6355](https://pkg.go.dev/vuln/GO-2026-6355)，最低分别为 0.55.0、0.56.0、0.56.0 |
| server：x/crypto | 0.54.0 | 0.56.0 | 同上；server 实际使用 bcrypt 等包，未导入 SSH/openpgp |
| 根模块、本地 Tailscale：x/net | 0.56.0 | 0.60.0 | [GO-2026-6603](https://pkg.go.dev/vuln/GO-2026-6603), [GO-2026-6610](https://pkg.go.dev/vuln/GO-2026-6610), [GO-2026-6611](https://pkg.go.dev/vuln/GO-2026-6611), [GO-2026-6612](https://pkg.go.dev/vuln/GO-2026-6612), [GO-2026-6617](https://pkg.go.dev/vuln/GO-2026-6617)，均修复于 0.60.0 |
| 根模块、本地 Tailscale：x/text | 0.40.0 | 0.42.0 | [GO-2026-6629](https://pkg.go.dev/vuln/GO-2026-6629)，最低 0.41.0 |
| 根模块、本地 Tailscale：x/mod | 0.37.0 | 0.41.0 | [GO-2026-6179](https://pkg.go.dev/vuln/GO-2026-6179)、[GO-2026-6180](https://pkg.go.dev/vuln/GO-2026-6180)，最低 0.40.0 |
| 根模块、本地 Tailscale：insomniacslk/dhcp | 20240129002554-15c9b8791914 | 20260719225207-c76316d4aa82 | [GO-2026-6237](https://pkg.go.dev/vuln/GO-2026-6237)，采用公告修复快照 |
| 根模块、server 的 go 指令 | 1.27.2 | 1.27.3 | [Go 官方发布记录](https://go.dev/doc/devel/release)，含 [GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809) 的 crypto/x509 修复 |
| 本地 Tailscale 的 go 指令 | 1.26.6 | 1.27.3 | 同上 |

`x/net v0.60.0` 的官方模块依赖要求 `x/crypto v0.57.0`、`x/text v0.42.0`、`x/sys v0.48.0`、`x/term v0.46.0`；其完整 MVS 闭包同时选到 `x/mod v0.41.0`、`x/sync v0.23.0`、`x/tools v0.49.0` 和 `x/telemetry v0.0.0-20260811182544-a038080d80e5`。这些是满足安全升级后的依赖约束，未执行全量 `go get -u`。

## 工具链与本地补丁

- `tool/go` 实际通过根 `go.toolchain.rev` 下载 Tailscale Go，Makefile 强制 `GOTOOLCHAIN=local`。根和本地 Tailscale 的固定提交统一到 `7ef7b403648ac3a9481a6e88e8b894eff50b0167`；[该提交的 VERSION](https://github.com/tailscale/go/blob/7ef7b403648ac3a9481a6e88e8b894eff50b0167/VERSION) 为 `go1.27.3`。
- Android CI、发布流程的 `setup-go` 从根 go.mod 读取版本，server CI 从 server/go.mod 读取。Docker 引导 Go 更新到 1.27.3，并更新 Makefile 镜像标签，避免沿用旧工具链构建镜像。
- Android CI 与发布流程的既有 govulncheck 命令补齐 `tailscale_go,ts_omit_cachenetmap` 标签，与实际 gomobile 构建一致；其余原有工作流改动保留。
- 本机实际下载固定提交的 Windows Tailscale Go，编译 gomobile/gobind；AAR 使用 `tailscale_go,ts_omit_cachenetmap`、NDK `30.0.16248370`、API 33、arm64 和现有 16 KB 页对齐链接参数。普通 Go 不提供 `runtime.TailscaleCurrentP`/`net.SockTrace`，因此不能代替带此标签的扫描；成功记录来自固定 Tailscale Go。
- server 的 Linux/amd64 发布构建使用实际 Go 1.27.3，`CGO_ENABLED=0`、`-trimpath -buildvcs=false`。
- `replace tailscale.com => ./third_party/tailscale` 保留，仍为 v1.102.4 本地核心。未覆盖第三方树，未改变 `wgengine/netstack/netstack.go`、Android socket/network hook 或已有补丁测试。版本声明由 `go version -m` 的产物信息复核。

## 依赖图、实际编译与可达性

三个模块分别执行 `go list -m all`、`go mod graph`。Android 按发布标签执行 `GOOS=android GOARCH=arm64 CGO_ENABLED=0 go list -deps -json ./libtailscale`；server 按 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go list -deps -json .`。解析结果与二进制 buildinfo 分别保存，避免把整个模块选择图当成运行时 SBOM。

| 图或扫描 | 更新前 | 更新后 | 范围 |
| --- | --- | --- | --- |
| root 模块清单 / OSV 命中模块版本 | 555 / 21 | 553 / 17 | 全选择图，含上游辅助工具和测试依赖 |
| server 模块清单 / OSV 命中模块版本 | 29 / 4 | 29 / 3 | 全选择图，含上游辅助工具和测试依赖 |
| tailscale 模块清单 / OSV 命中模块版本 | 1011 / 45 | 1010 / 41 | 全选择图，含上游辅助工具和测试依赖 |
| Android 实际依赖 | — | 656 包、40 个非标准库模块（含主模块） | 目标包 libtailscale；另扫描 netstack |
| server 实际依赖 | — | 225 包、10 个非标准库模块（含主模块） | Linux/amd64 发布目标 |

三个清单的 OSV 查询使用 `https://api.osv.dev/v1/querybatch`。`govulncheck v1.9.0` 使用官方 `https://vuln.go.dev`，数据库时间为 `2026-10-21T00:27:29Z`。

Android、server、本地 Tailscale 产品核心包的源码扫描，以及 server 二进制、Gradle 剥离符号前的 AAR ELF 扫描，均只保留 **GO-2026-5932 的模块级 finding**：x/crypto/openpgp 已废弃且没有修复版本。这些源码扫描未报告受影响包或可达漏洞函数。该公告未被忽略或过滤；原始 JSON 中保留 finding。这不等于整个第三方清单零公告。

最终两个 APK 的 ELF 经 Gradle 进一步剥离符号后，二进制扫描对同一 GO-2026-5932 产生 15 条 finding（模块、7 个包、7 个通配符符号），Release 文本扫描退出码为 **3**，不能记为无告警通过。[govulncheck v1.9.0 的实现](https://github.com/golang/vuln/blob/v1.9.0/internal/vulncheck/binary.go#L107) 在没有包符号信息时使用 `allKnownVulnerableSymbols`，精度退回 go.mod 层面；其中 `openpgp/*` 是生成的通配符占位，并非从 ELF 找到的真实函数名。完整源码包图没有 OpenPGP；同次构建保留符号的 ELF 也没有对应包/符号 finding。另对两个 APK 与 AAR ELF 的全部 PT_LOAD 段映射和段内容做 SHA-256 比较，除 ELF 头中的可变节表元数据外完全一致，确认 Gradle 只改变非加载符号/节表信息。原始保守告警与退出码均保留，不修改剥离流程或降低扫描等级。

server 选择图仍间接选择 x/net 0.57.0 和 x/mod 0.37.0，但这两个模块不在 server 的发布包图、二进制依赖或符号中；未为不构建的模块清单额外加入直接依赖。下表逐项保留剩余清单命中及构建依据。

### Go 清单剩余命中

下表合并最终三个模块清单；“未进入”依据是完整 Android/server 发布 `go list -deps` 模块集合，不是人工排除扫描器结果。容器、Git、gRPC、Kubernetes/Helm 等上游工具没有作为 PinNode 发布目标构建或打包；不保证独立运行这些上游工具时安全。

| 模块与版本 | 出现的模块图 | 产品构建判定 | 公告 |
| --- | --- | --- | --- |
| `github.com/aws/aws-sdk-go` v1.50.0 | tailscale | 未进入两个产品发布包图 | [GO-2022-0635](https://pkg.go.dev/vuln/GO-2022-0635), [GO-2022-0646](https://pkg.go.dev/vuln/GO-2022-0646) |
| `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream` v1.6.8 | root, tailscale | 未进入两个产品发布包图 | [GO-2026-5764](https://pkg.go.dev/vuln/GO-2026-5764) |
| `github.com/aws/aws-sdk-go-v2/service/s3` v1.75.3 | root, tailscale | 未进入两个产品发布包图 | [GO-2026-5764](https://pkg.go.dev/vuln/GO-2026-5764) |
| `github.com/cilium/ebpf` v0.16.0 | root, tailscale | 未进入两个产品发布包图 | [GO-2026-6238](https://pkg.go.dev/vuln/GO-2026-6238) |
| `github.com/containerd/containerd` v1.7.29 | root, tailscale | 未进入两个产品发布包图 | [GO-2026-5064](https://pkg.go.dev/vuln/GO-2026-5064), [GO-2026-5338](https://pkg.go.dev/vuln/GO-2026-5338), [GO-2026-5378](https://pkg.go.dev/vuln/GO-2026-5378), [GO-2026-5475](https://pkg.go.dev/vuln/GO-2026-5475), [GO-2026-5622](https://pkg.go.dev/vuln/GO-2026-5622), [GO-2026-5758](https://pkg.go.dev/vuln/GO-2026-5758), [GO-2026-6444](https://pkg.go.dev/vuln/GO-2026-6444), [GO-2026-6597](https://pkg.go.dev/vuln/GO-2026-6597), [GO-2026-6791](https://pkg.go.dev/vuln/GO-2026-6791), [GO-2026-6796](https://pkg.go.dev/vuln/GO-2026-6796) |
| `github.com/containerd/containerd/v2` v2.0.4 | tailscale | 未进入两个产品发布包图 | [GO-2025-3701](https://pkg.go.dev/vuln/GO-2025-3701), [GO-2025-4100](https://pkg.go.dev/vuln/GO-2025-4100), [GO-2025-4108](https://pkg.go.dev/vuln/GO-2025-4108), [GO-2026-5378](https://pkg.go.dev/vuln/GO-2026-5378), [GO-2026-5475](https://pkg.go.dev/vuln/GO-2026-5475), [GO-2026-5758](https://pkg.go.dev/vuln/GO-2026-5758), [GO-2026-6444](https://pkg.go.dev/vuln/GO-2026-6444), [GO-2026-6597](https://pkg.go.dev/vuln/GO-2026-6597), [GO-2026-6796](https://pkg.go.dev/vuln/GO-2026-6796) |
| `github.com/distribution/distribution/v3` v3.0.0 | tailscale | 未进入两个产品发布包图 | [GO-2026-4942](https://pkg.go.dev/vuln/GO-2026-4942), [GO-2026-5094](https://pkg.go.dev/vuln/GO-2026-5094), [GO-2026-5185](https://pkg.go.dev/vuln/GO-2026-5185) |
| `github.com/docker/distribution` v2.8.3+incompatible | tailscale | 未进入两个产品发布包图 | [GHSA-685w-q87j-wqw3](https://github.com/advisories/GHSA-685w-q87j-wqw3) |
| `github.com/docker/docker` v27.5.1+incompatible | tailscale | 未进入两个产品发布包图 | [GO-2026-4883](https://pkg.go.dev/vuln/GO-2026-4883), [GO-2026-4887](https://pkg.go.dev/vuln/GO-2026-4887), [GO-2026-5617](https://pkg.go.dev/vuln/GO-2026-5617), [GO-2026-5668](https://pkg.go.dev/vuln/GO-2026-5668), [GO-2026-5746](https://pkg.go.dev/vuln/GO-2026-5746) |
| `github.com/go-git/go-billy/v5` v5.8.0 | root, tailscale | 未进入两个产品发布包图 | [GO-2026-5490](https://pkg.go.dev/vuln/GO-2026-5490), [GO-2026-5597](https://pkg.go.dev/vuln/GO-2026-5597) |
| `github.com/go-git/go-git/v5` v5.17.1 | root, tailscale | 未进入两个产品发布包图 | [GO-2026-5074](https://pkg.go.dev/vuln/GO-2026-5074), [GO-2026-5105](https://pkg.go.dev/vuln/GO-2026-5105), [GO-2026-5336](https://pkg.go.dev/vuln/GO-2026-5336), [GO-2026-5496](https://pkg.go.dev/vuln/GO-2026-5496), [GO-2026-5693](https://pkg.go.dev/vuln/GO-2026-5693), [GO-2026-6213](https://pkg.go.dev/vuln/GO-2026-6213), [GO-2026-6214](https://pkg.go.dev/vuln/GO-2026-6214) |
| `github.com/go-jose/go-jose/v3` v3.0.4 | tailscale | 未进入两个产品发布包图 | [GO-2026-4945](https://pkg.go.dev/vuln/GO-2026-4945) |
| `github.com/go-jose/go-jose/v4` v4.1.3 | tailscale | 未进入两个产品发布包图 | [GO-2026-4945](https://pkg.go.dev/vuln/GO-2026-4945) |
| `github.com/google/cel-go` v0.26.0 | tailscale | 未进入两个产品发布包图 | [GO-2026-6094](https://pkg.go.dev/vuln/GO-2026-6094) |
| `github.com/gorilla/csrf` v1.7.3 | root, tailscale | 未进入两个产品发布包图 | [GO-2025-3884](https://pkg.go.dev/vuln/GO-2025-3884) |
| `github.com/in-toto/in-toto-golang` v0.5.0 | tailscale | 未进入两个产品发布包图 | [GO-2026-5547](https://pkg.go.dev/vuln/GO-2026-5547) |
| `github.com/jackc/pgx/v5` v5.4.3 | tailscale | 未进入两个产品发布包图 | [GO-2024-2567](https://pkg.go.dev/vuln/GO-2024-2567), [GO-2024-2606](https://pkg.go.dev/vuln/GO-2024-2606), [GO-2026-4771](https://pkg.go.dev/vuln/GO-2026-4771), [GO-2026-4772](https://pkg.go.dev/vuln/GO-2026-4772), [GO-2026-5004](https://pkg.go.dev/vuln/GO-2026-5004) |
| `github.com/moby/buildkit` v0.20.2 | root, tailscale | 未进入两个产品发布包图 | [GO-2026-4858](https://pkg.go.dev/vuln/GO-2026-4858), [GO-2026-4859](https://pkg.go.dev/vuln/GO-2026-4859), [GO-2026-6255](https://pkg.go.dev/vuln/GO-2026-6255), [GO-2026-6256](https://pkg.go.dev/vuln/GO-2026-6256) |
| `github.com/moby/sys/user` v0.3.0 | tailscale | 未进入两个产品发布包图 | [GO-2026-6686](https://pkg.go.dev/vuln/GO-2026-6686) |
| `github.com/opencontainers/selinux` v1.11.1 | tailscale | 未进入两个产品发布包图 | [GO-2025-4098](https://pkg.go.dev/vuln/GO-2025-4098) |
| `github.com/prometheus/prometheus` v0.49.2-0.20240125131847-c3b8ef1694ff | root, tailscale | 未进入两个产品发布包图 | [GO-2026-5264](https://pkg.go.dev/vuln/GO-2026-5264), [GO-2026-5381](https://pkg.go.dev/vuln/GO-2026-5381), [GO-2026-5662](https://pkg.go.dev/vuln/GO-2026-5662), [GO-2026-5710](https://pkg.go.dev/vuln/GO-2026-5710) |
| `github.com/vektah/gqlparser` v1.3.1 | tailscale | 未进入两个产品发布包图 | [GO-2024-2920](https://pkg.go.dev/vuln/GO-2024-2920) |
| `github.com/yuin/goldmark` v1.4.13 | root, tailscale | 未进入两个产品发布包图 | [GO-2026-5320](https://pkg.go.dev/vuln/GO-2026-5320) |
| `go.etcd.io/etcd/client/pkg/v3` v3.6.4 | tailscale | 未进入两个产品发布包图 | [GO-2026-6107](https://pkg.go.dev/vuln/GO-2026-6107) |
| `go.etcd.io/etcd/server/v3` v3.6.4 | tailscale | 未进入两个产品发布包图 | [GO-2026-5736](https://pkg.go.dev/vuln/GO-2026-5736), [GO-2026-6114](https://pkg.go.dev/vuln/GO-2026-6114) |
| `go.mongodb.org/mongo-driver` v1.13.1 | tailscale | 未进入两个产品发布包图 | [GO-2026-5327](https://pkg.go.dev/vuln/GO-2026-5327) |
| `go.opentelemetry.io/otel` v1.39.0 | root, tailscale | 未进入两个产品发布包图 | [GO-2026-5506](https://pkg.go.dev/vuln/GO-2026-5506) |
| `go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc` v0.8.0 | tailscale | 未进入两个产品发布包图 | [GO-2026-6508](https://pkg.go.dev/vuln/GO-2026-6508) |
| `go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp` v0.8.0 | tailscale | 未进入两个产品发布包图 | [GO-2026-4985](https://pkg.go.dev/vuln/GO-2026-4985) |
| `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp` v1.32.0 | tailscale | 未进入两个产品发布包图 | [GO-2026-4985](https://pkg.go.dev/vuln/GO-2026-4985) |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace` v1.34.0 | tailscale | 未进入两个产品发布包图 | [GO-2026-6505](https://pkg.go.dev/vuln/GO-2026-6505) |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc` v1.34.0 | tailscale | 未进入两个产品发布包图 | [GO-2026-6505](https://pkg.go.dev/vuln/GO-2026-6505) |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp` v1.32.0 | tailscale | 未进入两个产品发布包图 | [GO-2026-4985](https://pkg.go.dev/vuln/GO-2026-4985), [GO-2026-6505](https://pkg.go.dev/vuln/GO-2026-6505) |
| `go.opentelemetry.io/otel/sdk` v1.39.0 | tailscale | 未进入两个产品发布包图 | [GO-2026-4394](https://pkg.go.dev/vuln/GO-2026-4394), [GO-2026-5426](https://pkg.go.dev/vuln/GO-2026-5426), [GO-2026-6505](https://pkg.go.dev/vuln/GO-2026-6505) |
| `go.opentelemetry.io/otel/sdk/log` v0.8.0 | tailscale | 未进入两个产品发布包图 | [GO-2026-6615](https://pkg.go.dev/vuln/GO-2026-6615) |
| `golang.org/x/crypto` v0.56.0 | server | server 包含模块；仅 openpgp 公告，受影响包未导入 | [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932) |
| `golang.org/x/crypto` v0.57.0 | root, tailscale | Android 包含模块；仅 openpgp 公告，受影响包未导入 | [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932) |
| `golang.org/x/image` v0.41.0 | root, tailscale | 未进入两个产品发布包图 | [GO-2026-4961](https://pkg.go.dev/vuln/GO-2026-4961), [GO-2026-5061](https://pkg.go.dev/vuln/GO-2026-5061), [GO-2026-5062](https://pkg.go.dev/vuln/GO-2026-5062), [GO-2026-5066](https://pkg.go.dev/vuln/GO-2026-5066), [GO-2026-6222](https://pkg.go.dev/vuln/GO-2026-6222) |
| `golang.org/x/mod` v0.37.0 | server | 未进入两个产品发布包图 | [GO-2026-6179](https://pkg.go.dev/vuln/GO-2026-6179), [GO-2026-6180](https://pkg.go.dev/vuln/GO-2026-6180) |
| `golang.org/x/net` v0.57.0 | server | 未进入两个产品发布包图 | [GO-2026-6603](https://pkg.go.dev/vuln/GO-2026-6603), [GO-2026-6610](https://pkg.go.dev/vuln/GO-2026-6610), [GO-2026-6611](https://pkg.go.dev/vuln/GO-2026-6611), [GO-2026-6612](https://pkg.go.dev/vuln/GO-2026-6612), [GO-2026-6617](https://pkg.go.dev/vuln/GO-2026-6617) |
| `google.golang.org/grpc` v1.78.0 | root, tailscale | 未进入两个产品发布包图 | [GO-2026-4762](https://pkg.go.dev/vuln/GO-2026-4762), [GO-2026-6061](https://pkg.go.dev/vuln/GO-2026-6061), [GO-2026-6348](https://pkg.go.dev/vuln/GO-2026-6348), [GO-2026-6441](https://pkg.go.dev/vuln/GO-2026-6441), [GO-2026-6443](https://pkg.go.dev/vuln/GO-2026-6443) |
| `gopkg.in/square/go-jose.v2` v2.6.0 | root, tailscale | 未进入两个产品发布包图 | [GO-2024-2631](https://pkg.go.dev/vuln/GO-2024-2631) |
| `helm.sh/helm/v3` v3.19.0 | root, tailscale | 未进入两个产品发布包图 | [GO-2026-5435](https://pkg.go.dev/vuln/GO-2026-5435) |
| `oras.land/oras-go/v2` v2.6.0 | root, tailscale | 未进入两个产品发布包图 | [GO-2026-5879](https://pkg.go.dev/vuln/GO-2026-5879), [GO-2026-5880](https://pkg.go.dev/vuln/GO-2026-5880), [GO-2026-5882](https://pkg.go.dev/vuln/GO-2026-5882), [GO-2026-5884](https://pkg.go.dev/vuln/GO-2026-5884), [GO-2026-5885](https://pkg.go.dev/vuln/GO-2026-5885), [GO-2026-6497](https://pkg.go.dev/vuln/GO-2026-6497), [GO-2026-6499](https://pkg.go.dev/vuln/GO-2026-6499) |

## Web 资源与 Yarn 锁文件

- Android 使用 `third_party/tailscale/ipn/ipnlocal/web_client_stub.go` 的 `android || ts_omit_webclient` 条件；完整发布包图中没有 `tailscale.com/client/web`、`github.com/tailscale/web-client-prebuilt` 或 `cmd/tsconnect`。
- 上游 `client/web/assets.go` 的非开发路径使用 Go 模块 `github.com/tailscale/web-client-prebuilt`，不是从本地 Yarn 锁文件重建。它仍出现在根模块选择图，但不进入 Android 产物。没有把“上游预构建资源”误称为已经用本地锁文件修复。
- PinNode server 的 `admin_page.go` 只嵌入自有 `web/admin.html` 与 `web/mark.svg`；页面使用内联脚本，没有 npm/Yarn 打包步骤。当前根 Makefile/工作流也不运行这两个上游 Yarn 项目。
- 因此本次保留两个锁文件，未重建或发布这些上游 Web 工具。若未来启用上游 Web/tsconnect 构建，需要先处理下表公告并执行其既有 lint/test/build。

按每个锁文件的实际包版本对 OSV 做批量查询，结果如下；包版本数按去重后的 npm 包名与版本计数。

### client/web

742 个包版本，27 个包版本命中；均不进入上述产品构建。

| 包 | 锁定版本 | 公告 |
| --- | --- | --- |
| `@babel/core` | 7.23.3 | [GHSA-4x5r-pxfx-6jf8](https://github.com/advisories/GHSA-4x5r-pxfx-6jf8) |
| `@babel/helpers` | 7.23.4 | [GHSA-968p-4wvh-cqc8](https://github.com/advisories/GHSA-968p-4wvh-cqc8) |
| `@babel/plugin-transform-modules-systemjs` | 7.23.3 | [GHSA-fv7c-fp4j-7gwp](https://github.com/advisories/GHSA-fv7c-fp4j-7gwp) |
| `ajv` | 6.12.6 | [GHSA-2g4f-4pwh-qvx6](https://github.com/advisories/GHSA-2g4f-4pwh-qvx6) |
| `brace-expansion` | 1.1.11 | [GHSA-3jxr-9vmj-r5cp](https://github.com/advisories/GHSA-3jxr-9vmj-r5cp), [GHSA-6j4f-fj2g-mc7p](https://github.com/advisories/GHSA-6j4f-fj2g-mc7p), [GHSA-f886-m6hf-6m8v](https://github.com/advisories/GHSA-f886-m6hf-6m8v), [GHSA-mh99-v99m-4gvg](https://github.com/advisories/GHSA-mh99-v99m-4gvg), [GHSA-q2hr-2g5m-vwhr](https://github.com/advisories/GHSA-q2hr-2g5m-vwhr), [GHSA-qhr7-859c-m2p7](https://github.com/advisories/GHSA-qhr7-859c-m2p7), [GHSA-rgw5-rvv9-x895](https://github.com/advisories/GHSA-rgw5-rvv9-x895), [GHSA-v6h2-p8h4-qcjw](https://github.com/advisories/GHSA-v6h2-p8h4-qcjw) |
| `brace-expansion` | 2.0.1 | [GHSA-3jxr-9vmj-r5cp](https://github.com/advisories/GHSA-3jxr-9vmj-r5cp), [GHSA-6j4f-fj2g-mc7p](https://github.com/advisories/GHSA-6j4f-fj2g-mc7p), [GHSA-f886-m6hf-6m8v](https://github.com/advisories/GHSA-f886-m6hf-6m8v), [GHSA-mh99-v99m-4gvg](https://github.com/advisories/GHSA-mh99-v99m-4gvg), [GHSA-q2hr-2g5m-vwhr](https://github.com/advisories/GHSA-q2hr-2g5m-vwhr), [GHSA-qhr7-859c-m2p7](https://github.com/advisories/GHSA-qhr7-859c-m2p7), [GHSA-rgw5-rvv9-x895](https://github.com/advisories/GHSA-rgw5-rvv9-x895), [GHSA-v6h2-p8h4-qcjw](https://github.com/advisories/GHSA-v6h2-p8h4-qcjw) |
| `browserslist` | 4.22.1 | [GHSA-73wf-gq98-2v4g](https://github.com/advisories/GHSA-73wf-gq98-2v4g), [GHSA-c83g-rgw3-j3cx](https://github.com/advisories/GHSA-c83g-rgw3-j3cx) |
| `esbuild` | 0.21.5 | [GHSA-67mh-4wv8-2f99](https://github.com/advisories/GHSA-67mh-4wv8-2f99) |
| `flatted` | 3.2.9 | [GHSA-25h7-pfq9-p65f](https://github.com/advisories/GHSA-25h7-pfq9-p65f), [GHSA-rf6f-7fwh-wjgh](https://github.com/advisories/GHSA-rf6f-7fwh-wjgh) |
| `form-data` | 4.0.4 | [GHSA-hmw2-7cc7-3qxx](https://github.com/advisories/GHSA-hmw2-7cc7-3qxx) |
| `js-yaml` | 4.1.1 | [GHSA-2883-xcg3-v3hh](https://github.com/advisories/GHSA-2883-xcg3-v3hh), [GHSA-52cp-r559-cp3m](https://github.com/advisories/GHSA-52cp-r559-cp3m), [GHSA-5p4m-2wfm-xmqj](https://github.com/advisories/GHSA-5p4m-2wfm-xmqj), [GHSA-h67p-54hq-rp68](https://github.com/advisories/GHSA-h67p-54hq-rp68) |
| `lodash` | 4.17.23 | [GHSA-f23m-r3pf-42rh](https://github.com/advisories/GHSA-f23m-r3pf-42rh), [GHSA-r5fr-rjxr-66jc](https://github.com/advisories/GHSA-r5fr-rjxr-66jc) |
| `micromatch` | 4.0.5 | [GHSA-952p-6rrq-rcjv](https://github.com/advisories/GHSA-952p-6rrq-rcjv) |
| `minimatch` | 3.1.2 | [GHSA-23c5-xmqv-rm74](https://github.com/advisories/GHSA-23c5-xmqv-rm74), [GHSA-3ppc-4f35-3m26](https://github.com/advisories/GHSA-3ppc-4f35-3m26), [GHSA-7r86-cg39-jmmj](https://github.com/advisories/GHSA-7r86-cg39-jmmj) |
| `minimatch` | 9.0.3 | [GHSA-23c5-xmqv-rm74](https://github.com/advisories/GHSA-23c5-xmqv-rm74), [GHSA-3ppc-4f35-3m26](https://github.com/advisories/GHSA-3ppc-4f35-3m26), [GHSA-7r86-cg39-jmmj](https://github.com/advisories/GHSA-7r86-cg39-jmmj) |
| `nanoid` | 3.3.11 | [GHSA-28wg-ghj8-5hjv](https://github.com/advisories/GHSA-28wg-ghj8-5hjv), [GHSA-2v37-7h3g-55p8](https://github.com/advisories/GHSA-2v37-7h3g-55p8), [GHSA-xwg4-73v4-xw9w](https://github.com/advisories/GHSA-xwg4-73v4-xw9w) |
| `picomatch` | 2.3.1 | [GHSA-3v7f-55p6-f55p](https://github.com/advisories/GHSA-3v7f-55p6-f55p), [GHSA-c2c7-rcm5-vvqj](https://github.com/advisories/GHSA-c2c7-rcm5-vvqj) |
| `postcss` | 8.5.6 | [GHSA-6g55-p6wh-862q](https://github.com/advisories/GHSA-6g55-p6wh-862q), [GHSA-fxqj-rqcc-2cmp](https://github.com/advisories/GHSA-fxqj-rqcc-2cmp), [GHSA-qx2v-qp2m-jg93](https://github.com/advisories/GHSA-qx2v-qp2m-jg93), [GHSA-r28c-9q8g-f849](https://github.com/advisories/GHSA-r28c-9q8g-f849) |
| `postcss-selector-parser` | 6.0.13 | [GHSA-rj75-hqrm-r3gf](https://github.com/advisories/GHSA-rj75-hqrm-r3gf) |
| `rollup` | 4.52.5 | [GHSA-mw96-cpmx-2vgc](https://github.com/advisories/GHSA-mw96-cpmx-2vgc) |
| `source-map-js` | 1.2.1 | [GHSA-68fv-2mgg-jv7q](https://github.com/advisories/GHSA-68fv-2mgg-jv7q) |
| `tinypool` | 0.8.4 | [GHSA-5gmw-xhrv-c9v3](https://github.com/advisories/GHSA-5gmw-xhrv-c9v3), [GHSA-85c8-ppgw-ccpr](https://github.com/advisories/GHSA-85c8-ppgw-ccpr) |
| `vite` | 5.4.21 | [GHSA-4w7w-66w2-5vf9](https://github.com/advisories/GHSA-4w7w-66w2-5vf9), [GHSA-587h-7wmj-h5pm](https://github.com/advisories/GHSA-587h-7wmj-h5pm), [GHSA-698v-4r3x-3vcv](https://github.com/advisories/GHSA-698v-4r3x-3vcv), [GHSA-9jrq-w75r-8gcw](https://github.com/advisories/GHSA-9jrq-w75r-8gcw), [GHSA-fx2h-pf6j-xcff](https://github.com/advisories/GHSA-fx2h-pf6j-xcff), [GHSA-r825-w8mm-5pxw](https://github.com/advisories/GHSA-r825-w8mm-5pxw), [GHSA-rq7h-c2jc-7f22](https://github.com/advisories/GHSA-rq7h-c2jc-7f22), [GHSA-v6wh-96g9-6wx3](https://github.com/advisories/GHSA-v6wh-96g9-6wx3) |
| `vitest` | 1.6.1 | [GHSA-5xrq-8626-4rwp](https://github.com/advisories/GHSA-5xrq-8626-4rwp) |
| `ws` | 8.17.1 | [GHSA-58qx-3vcg-4xpx](https://github.com/advisories/GHSA-58qx-3vcg-4xpx), [GHSA-96hv-2xvq-fx4p](https://github.com/advisories/GHSA-96hv-2xvq-fx4p) |
| `yaml` | 1.10.2 | [GHSA-48c2-rrv3-qjmp](https://github.com/advisories/GHSA-48c2-rrv3-qjmp) |
| `yaml` | 2.3.1 | [GHSA-48c2-rrv3-qjmp](https://github.com/advisories/GHSA-48c2-rrv3-qjmp) |
### cmd/tsconnect

108 个包版本，6 个包版本命中；均不进入上述产品构建。

| 包 | 锁定版本 | 公告 |
| --- | --- | --- |
| `nanoid` | 3.3.11 | [GHSA-28wg-ghj8-5hjv](https://github.com/advisories/GHSA-28wg-ghj8-5hjv), [GHSA-2v37-7h3g-55p8](https://github.com/advisories/GHSA-2v37-7h3g-55p8), [GHSA-xwg4-73v4-xw9w](https://github.com/advisories/GHSA-xwg4-73v4-xw9w) |
| `picomatch` | 2.3.1 | [GHSA-3v7f-55p6-f55p](https://github.com/advisories/GHSA-3v7f-55p6-f55p), [GHSA-c2c7-rcm5-vvqj](https://github.com/advisories/GHSA-c2c7-rcm5-vvqj) |
| `postcss` | 8.5.6 | [GHSA-6g55-p6wh-862q](https://github.com/advisories/GHSA-6g55-p6wh-862q), [GHSA-fxqj-rqcc-2cmp](https://github.com/advisories/GHSA-fxqj-rqcc-2cmp), [GHSA-qx2v-qp2m-jg93](https://github.com/advisories/GHSA-qx2v-qp2m-jg93), [GHSA-r28c-9q8g-f849](https://github.com/advisories/GHSA-r28c-9q8g-f849) |
| `postcss-selector-parser` | 6.0.10 | [GHSA-rj75-hqrm-r3gf](https://github.com/advisories/GHSA-rj75-hqrm-r3gf) |
| `source-map-js` | 1.2.1 | [GHSA-68fv-2mgg-jv7q](https://github.com/advisories/GHSA-68fv-2mgg-jv7q) |
| `yaml` | 1.10.2 | [GHSA-48c2-rrv3-qjmp](https://github.com/advisories/GHSA-48c2-rrv3-qjmp) |

## 验证记录

详细命令输出与 JSON 位于 `.cache/security-20261026/`。以下条目仅将实际退出成功的检查记为通过。

| 检查 | 命令 / 环境 | 结果 |
| --- | --- | --- |
| root/server 模块一致性 | `go mod tidy`、`go mod verify` | 通过；根/server 校验和同步 |
| 根模块现有测试与 vet | `make go-test` 等效包范围，排除需 NDK 的 libtailscale；`go test ... -count=1`、`go vet ...` | 通过；Android VM 设备测试仍按 Windows 条件跳过 |
| 本地 netstack 补丁 | 根图 `go test tailscale.com/wgengine/netstack -count=1` 与 vet | 通过 |
| 本地 Tailscale 独立模块 | 固定 TS Go，`go test -tags tailscale_go ./wgengine/netstack ./syncs ./net/sockstats -count=1` 与 vet | 通过；sockstats 无测试文件 |
| Android bridge vet | 固定 TS Go，android/arm64、`tailscale_go,ts_omit_cachenetmap` | 通过 |
| server 完整测试 | Go 1.27.3，普通和 `-tags debug` 的 `go test ./... -count=1`、vet | 通过 |
| Linux race | 本机已有 Go 1.27.3/gcc 镜像，源码与模块缓存只读、`--network none`；server 普通/debug `go test -race ./... -count=1`、vet；netstack `go test -race ... -count=1` | 通过 |
| Android/server 源码 govulncheck | 正确目标 OS/arch；Android 包含发布标签 | 通过，无受影响包/可达函数 finding；保留 openpgp 模块级 finding |
| 保留符号 ELF/server binary govulncheck | 同次构建 AAR arm64 ELF、Linux/amd64 二进制 | 完成；仅 openpgp 模块级 finding |
| 最终 APK binary govulncheck | Debug/未签名 Release 包内实际 ELF | 完成但保留 GO-2026-5932 保守告警；Release 文本命令退出 3，符号剥离精度限制见上文 |
| 本地 Tailscale 独立模块 govulncheck | 固定 TS Go，Android 条件，`./wgengine/netstack ./ipn/ipnlocal` | 通过，同上；未把整套上游辅助工具作为产品扫描目标 |
| Android AAR | 固定 TS Go 1.27.3、NDK 30、API 33、arm64、16 KB max-page-size；按项目流程剥离 debug 段 | 通过；原本地 AAR 保存在 audit 缓存中 |
| Linux server 构建与烟测 | 发布等效交叉构建；临时 Alpine 容器 `--network none --read-only --user 65532:65532`、tmpfs 数据目录；healthz 与一致性备份 | 通过，无外部服务/真实凭据 |
| Logo 与许可证 | `sync_pinnode_logo.py --check`、`build_license_bundle.py --check` | 通过；更新 Go 版本链接并补充 server 实际依赖清单 |

本地 Tailscale 全量 `go mod tidy -diff` 会下载未用于产品的整个上游工具/内核/Kubernetes 图，本次中止了这项检查；`-diff` 没有写入模块文件。该模块通过独立图解析、目标测试、vet、govulncheck 和实际编译验证；不将未完成的全量 tidy 或全上游平台测试记为通过。

### Android 与最终产物复核

- `testDebugUnitTest --rerun testReleaseUnitTest --rerun` 实际重跑成功，Debug 与 Release 各 6 套件、34 条测试，均为 0 failure/error/skipped。
- Debug APK 已成功打包。Release 单独联网补齐缺失的官方 `lint-gradle:31.13.0` 后，`assembleRelease` 成功，`lintVitalAnalyzeRelease`、`lintVitalReportRelease`、`lintVitalRelease` 均执行通过。没有禁用 lint。初次离线解析失败的日志一并保留；OkHttp 本地缓存 JAR 与 Maven Central SHA-1 一致。Gradle 使用既有本机 Maven 缓存 init script，没有修改项目 Java/JVM 依赖版本。
- 两个 APK 均重新抽取实际 `lib/arm64-v8a/libgojni.so`，`go version -m` 确认 Go 1.27.3、五项目标依赖修复版本、原地 PinNode/Tailscale replacement、发布构建标签和 16 KB 链接参数。
- ELF 全部 LOAD 段对齐为 `0x4000`；两个 APK 均通过 `zipalign -c -P 16 4`。符号剥离前后加载段一致性另有 JSON 记录。
- 两个 APK 的 `assets/LICENSES.md` 与当前源码包逐字节一致；Linux tar.gz 中的二进制和许可证包也逐字节一致，服务端执行权限为 0755。验证覆盖版本清单和打包一致性，不构成所有第三方完整许可文本/版权告示的法律合规审计。
- APK 沿用未改动的本机 custom 配置；Debug applicationId 为 `com.lsy223622.pinnode.custom.debug`，Release 未签名。本地原生验证构建没有注入发布流程的版本 stamp，不是正式签名发布候选。
- 最终初始文件哈希比较只涉及 18 个预先存在的文件；业务实现、Android/network 本地补丁和既有未提交测试保持初始内容。对两个既有工作流仅补充扫描标签。最终 `git diff --check` 与全部 workflow YAML 解析通过，独立审查未发现依赖修改的正确性或安全回归。

本机验证产物位于 `.artifacts/security-20261026/`，校验和如下：

| 文件 | SHA-256 |
| --- | --- |
| pinnode-security-debug-arm64.apk | `f55f66f0319a999a6b5820b68087a5f8eb30e90914751bfc26b26a1502cfe6a3` |
| pinnode-security-release-unsigned-arm64.apk | `06ff5f66d9250a58a7f9fefb3a1b1d48df98a775c5c5b1ddef00360f7307b0df` |
| pinnode-server-linux-amd64.tar.gz | `5e847a5b1d43c9703686c70dcdd1356ef2be0f93eff3b21171b06a2872e1f637` |
| LICENSES.md | `480130b68c39eafee7e923f1184c5e6493bffed95a04f672388cc14d21aa8db4` |


## 边界

未推送、合并、签名发布、部署或修改线上服务；未连接/修改真机、执行刷机或设备测试。当前产物来自含原有未提交工作在内的本机工作区。仅验证 Android arm64 和 Linux amd64；其他 Android ABI、其他桌面/服务端平台、远程 CI、真机网络矩阵和升级安装不在已通过范围内。

固定 Windows Tailscale Go 下载包 SHA-256：`27c507c7f8e4dbafdaee1e21b22ba2ac6b10bbd96b7712a78a86812e508b0761`。
