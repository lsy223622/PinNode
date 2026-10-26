# PinNode Linux server dependencies

The Linux/amd64 server built with Go 1.27.3 includes the following Go modules.
The links identify the corresponding upstream license and copyright notices.

| Module | Version | License and notices |
| --- | --- | --- |
| github.com/dustin/go-humanize | v1.0.1 | [MIT](https://github.com/dustin/go-humanize/blob/v1.0.1/LICENSE) |
| github.com/google/uuid | v1.6.0 | [BSD-3-Clause](https://github.com/google/uuid/blob/v1.6.0/LICENSE) |
| github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | [BSD-3-Clause](https://github.com/remyoudompheng/bigfft/blob/24d4a6f8daec/LICENSE) |
| golang.org/x/crypto | v0.56.0 | [BSD-3-Clause](https://cs.opensource.google/go/x/crypto/+/v0.56.0:LICENSE) |
| golang.org/x/sys | v0.47.0 | [BSD-3-Clause](https://cs.opensource.google/go/x/sys/+/v0.47.0:LICENSE) |
| modernc.org/libc | v1.74.4 | [BSD-3-Clause](https://gitlab.com/cznic/libc/-/blob/v1.74.4/LICENSE), [third-party notices](https://gitlab.com/cznic/libc/-/blob/v1.74.4/LICENSE-3RD-PARTY.md) |
| modernc.org/mathutil | v1.7.1 | [BSD-3-Clause](https://gitlab.com/cznic/mathutil/-/blob/v1.7.1/LICENSE) |
| modernc.org/memory | v1.11.0 | [BSD-3-Clause](https://gitlab.com/cznic/memory/-/blob/v1.11.0/LICENSE), [Go](https://gitlab.com/cznic/memory/-/blob/v1.11.0/LICENSE-GO), [mmap-go](https://gitlab.com/cznic/memory/-/blob/v1.11.0/LICENSE-MMAP-GO) |
| modernc.org/sqlite | v1.57.0 | [BSD-3-Clause](https://gitlab.com/cznic/sqlite/-/blob/v1.57.0/LICENSE), [SQLite public-domain notice](https://gitlab.com/cznic/sqlite/-/blob/v1.57.0/LICENSE-SQLITE), [sqlite-vec MIT notice](https://gitlab.com/cznic/sqlite/-/blob/v1.57.0/LICENSE-SQLITE_VEC) |

Go and the Tailscale Go toolchain are distributed under the
[Go BSD license](https://go.googlesource.com/go/+/refs/tags/go1.27.3/LICENSE).
