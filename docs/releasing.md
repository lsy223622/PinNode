# 发布 PinNode

每个正式 Release 同时提供 Android APK 和 Linux `amd64` 服务端程序。服务端 Release
资产只包含可执行文件及校验文件，不包含数据库、`pinnode.secret`、环境文件或管理凭据。

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

## 发布步骤

1. 更新 `version.properties`，完成测试并确认工作区干净。
2. 执行 `make tag_release`，把 `v<version>` tag 推送到 GitHub。
3. 正式发布时基于该 tag 创建 GitHub Release；发布后工作流才会开始构建。发布前可在
   Actions 手动运行 `Release APK`，输入 `v<version>`，先验证签名 secrets、Android 构建链路
   和 Linux `amd64` 服务端构建；手动运行只上传短期 workflow artifact，不会写入 GitHub
   Release。
4. 确认 APK 签名证书 SHA-256 与固定证书一致，并核对以下四个二进制/校验文件：
   `pinnode-release.apk`、`pinnode-release.apk.sha256`、`pinnode-server-linux-amd64`、
   `pinnode-server-linux-amd64.sha256`。
5. 服务端部署时从 Release 下载 `pinnode-server-linux-amd64`，先核对 SHA-256，再按
   `server/README.md` 的 systemd/HTTPS 反代流程部署；不要把数据库、实例密钥、环境文件
   或管理凭据放入 Release。
6. 正式发布时保留随 Release 上传的 `LICENSE`、`NOTICE`、`THIRD_PARTY_NOTICES.md` 和
   `PATENTS`。
