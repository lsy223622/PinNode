# 发布 PinNode

每个正式 Release 同时提供 Android APK 和 Linux `amd64` 服务端程序。二进制文件名包含
版本号和构建提交短哈希，便于确认来源；服务端以压缩包发布，内含恒定文件名的
`pinnode-server` 和 `LICENSES.md`，便于部署替换并保持许可证随程序分发。APK 以
`assets/LICENSES.md` 的形式内嵌同一份合并许可证。Release 页面另提供一份带版本/提交哈希
文件名的 `LICENSES.md`，不再拆成多个许可证附件。所有发布资产都不包含数据库、
`pinnode.secret`、环境文件或管理凭据。

## 版本与安装身份

`com.lsy223622.pinnode` 是基础 application ID。配置自定义服务器的构建追加
`.custom`，Debug 构建追加 `.debug`；两项同时满足时使用
`com.lsy223622.pinnode.custom.debug`。因此 Debug 构建不会覆盖未配置自定义服务器的正式安装。

应用版本只在仓库根目录的 `version.properties` 中维护。`versionCode` 按以下规则从
`major.minor.patch` 确定性生成：

```text
300,000,000 + major * 1,000,000 + minor * 10,000 + patch * 10 + platform
```

`300,000,000` 是从首发前的时间戳规则迁移所保留的固定基数，确保现有正式签名测试包
仍可原地升级。`platform` 对手机/平板为 `0`，对 Android TV 为 `1`。`0.1.0` 因而对应
`300010000` 和 `300010001`。minor 必须在 0–99，patch 必须在 0–999；已经公开过的
版本号不得复用或降低。

## 固定发布签名

Android 更新必须继续使用同一把签名私钥。发布前应把 keystore、store password、key
password、alias 和证书 SHA-256 指纹分别做至少两份离线备份。不要把 keystore、密码、
Base64 文本或私有服务器配置提交到仓库。

当前工作流使用 alias `pinnode`。先在本机确认现有 keystore 中的 alias 和证书指纹：

```text
keytool -list -v -keystore <path-to-keystore> -alias pinnode
```

同时检查证书有效期是否覆盖应用的预期寿命；Android 建议至少 25 年。若现有 key 的有效期
明显不足，首个公开版本之前是无迁移成本更换它的最后时机。

如果现有 key 的 alias 不是 `pinnode`，应修改工作流中的 `JKS_ALIAS`，不要为匹配 alias
而重新生成签名 key。

### GitHub environment secrets

在 GitHub 仓库的 **Settings → Environments** 中创建或打开 `release` environment，
添加以下 environment secrets：

- `PINNODE_KEYSTORE_BASE64`
- `PINNODE_KEYSTORE_PASSWORD`
- `PINNODE_KEY_PASSWORD`

推荐使用 GitHub CLI 直接从本机写入，避免生成临时 Base64 文件。以下命令应在 keystore
所在的安全目录执行：

```powershell
$repo = 'lsy223622/PinNode'
$keystorePath = Resolve-Path '.\pinnode-release.jks'
$keystoreBase64 = [Convert]::ToBase64String([IO.File]::ReadAllBytes($keystorePath))
$keystoreBase64 | gh secret set PINNODE_KEYSTORE_BASE64 --env release --repo $repo
Remove-Variable keystoreBase64

gh secret set PINNODE_KEYSTORE_PASSWORD --env release --repo $repo
gh secret set PINNODE_KEY_PASSWORD --env release --repo $repo
```

后两个命令会交互读取 secret。Base64 只是编码，不是加密；不要把它留在终端历史、剪贴板
或普通文本文件中。

## 服务器构建参数

规范仓库 `lsy223622/PinNode` 的 release 工作流会无条件忽略 `PINNODE_SERVER_URL`、
`PINNODE_SERVER_NAME` 和 `PINNODE_SERVER_LOCKED`，生成可编辑服务器且不含默认服务器
地址的公开 APK。公开 release environment 也不应保存这些变量。

私有 fork 如需生成固定服务器版本，可在它自己的 `release` environment 中设置：

- `PINNODE_SERVER_URL`
- `PINNODE_SERVER_NAME`
- `PINNODE_SERVER_LOCKED=true`

当 locked 为 `true` 时，工作流会要求 URL 和名称都存在；公开仓库无需保留任何私有值。

## 资产命名与许可证布局

工作流从 `version.properties` 读取版本，并从实际构建提交生成 12 位短哈希 `commit`。正式
Release 的资产名称为：

```text
pinnode-android-v<version>-<commit>.apk
pinnode-android-v<version>-<commit>.apk.sha256
pinnode-server-v<version>-linux-amd64-<commit>.tar.gz
pinnode-server-v<version>-linux-amd64-<commit>.tar.gz.sha256
pinnode-v<version>-<commit>-LICENSES.md
```

服务端压缩包解开后只有恒定部署文件名 `pinnode-server` 和 `LICENSES.md`；APK 内的
`assets/LICENSES.md` 与 Release 上的合并文件来自同一提交。`scripts/build_license_bundle.py`
从仓库根目录的 GPL、NOTICE、PATENTS、第三方声明以及锁定版本的 Tailscale 依赖清单生成
`LICENSES.md`，构建前会用 `--check` 防止合并文件过期。仓库中的单独许可证文件仍然保留，
用于源码浏览和作为合并文件的权威来源。

## 发布步骤

1. 更新 `version.properties`，完成测试并确认工作区干净。
2. 执行 `make tag_release`，把 `v<version>` tag 推送到 GitHub。
3. 正式发布时基于该 tag 创建 GitHub Release；发布后工作流才会开始构建。发布前可在
   Actions 手动运行 `Release APK`，输入 `v<version>`，先验证签名 secrets、Android 构建链路
   和 Linux `amd64` 服务端构建；手动运行只上传短期 workflow artifact，不会写入 GitHub
   Release。
4. 确认 APK 签名证书 SHA-256 与固定证书一致，并核对带有相同 `<version>-<commit>` 的
   APK、服务端压缩包及各自 `.sha256` 文件；同时确认 APK 包含 `assets/LICENSES.md`，
   服务端压缩包包含 `pinnode-server` 和 `LICENSES.md`。校验文件只记录附件文件名，
   下载到同一目录后可直接执行 `sha256sum -c <asset>.sha256`。
5. 服务端部署时从 Release 下载 `pinnode-server-v<version>-linux-amd64-<commit>.tar.gz`，
   先核对 SHA-256，解压后使用其中的 `pinnode-server`，再按 `server/README.md` 的
   systemd/HTTPS 反代流程部署；不要把数据库、实例密钥、环境文件或管理凭据放入 Release。

## 发布前验收矩阵

正式打 tag 前至少保留以下证据；构建成功本身不等于链路验收完成：

- 服务端：`go test ./...`、`go vet ./...`，再用 Debug 构建在本地 `6634` 启动；若通过
  反代验收，可信代理配置只包含实际反代来源的精确 CIDR，并分别检查 origin 和 HTTPS
  域名的 `/healthz`、管理登录、静态资源缓存头和 SSE。
- 管理页：创建、自然过期和兑换 PIN；Console 的 pending/active 转移、六种健康状态、
  多于历史查询上限的活动会话、节点离线/控制面故障降级；Logs 的来源/级别/组件/会话
  筛选、暂停、继续、自动滚动、断线恢复、`reset`、无重复和 session 失效收回。
- Android：在专用 AVD 上安装当前 Debug APK，完成会话建立、状态同步、日志上传、短暂
  网络故障后的有限补发、正常停止、超时清理和清理失败重试；记录固定 ADB serial，不能
  把 instrumentation APK 构建成功当作 connected test 成功。真机重启、OEM 特定强杀和
  不同路由器/调制解调器拓扑若未执行，保留为单独证据缺口。
- 发布包：清空 AVD 应用数据后安装 Release APK，核对 application ID、版本、签名证书
  指纹、服务端变体和内嵌许可证；运行 `apksigner verify --verbose --print-certs`，再
  用同一 `<version>-<commit>` 核对 APK、服务端压缩包和 SHA-256 文件。签名密码只从当前
  进程环境传入，keystore 不进入仓库、构建产物、临时记录或命令输出。
