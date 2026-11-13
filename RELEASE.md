# 发布流程备忘

本仓库的「在线构建 / 发布」与「应用内检查更新」由
[MyGo](https://github.com/egoist/mygo) 框架 + GitHub Actions 配合实现，代码与配置
已基本接好。本文件只讲**手动操作步骤**与注意事项，CI 的细节见
`.github/workflows/release.yml`。

## 机制概览（已经就绪，无需再写）

- `mygo.json` 的 `updates.github` 指向本仓库，`publicKey` 为更新包验签公钥。
- `CHANGELOG.md` 的 `## <version>` 小节会被 `mygo build` 写进每个平台的更新清单，
  应用内的更新窗口据此展示发布说明。
- `.github/workflows/release.yml`：推送 tag 即触发 → 建草稿 Release → 多平台构建
  （win / mac / linux）并**签名更新包** → 上传到草稿 → 全部成功后自动发布。
- `main.go` 已 `mygo.Use(updatenative.Plugin)`；设置窗口的「检查更新」按钮调用
  `updater.CheckForUpdates()`，且应用每天后台自动检查一次 GitHub Release。

## 一次性准备

### 1. 配置 CI 签名密钥（不做，工作流会直接报错退出）

更新包用 Ed25519 签名，构建时由 CI 用**私钥**签名、已装应用用 `mygo.json` 里的
**公钥**验签。私钥不在仓库里（已被 gitignore）。

- 本地私钥位置：`.gotmp/update-keys/mygo-update.key`（其内容须与
  `mygo.json` 的 `publicKey` 配对；当前已核对一致）。
- 到仓库 `Settings → Secrets and variables → Actions → New repository secret`：
  - Name: `MYGO_UPDATER_PRIVATE_KEY`
  - Secret: 上面那个文件的**全部内容**（含换行）。
- `release.yml` 会在构建前检查该 secret，缺失即 `exit 1`。

> ⚠️ **不要重新 `mygo keygen`**，除非你同时把新公钥写回 `mygo.json` 的
> `publicKey`。换密钥会让所有已安装的应用不信任后续更新（老用户无法再升级）。

### 2. 仓库设为 Public

内置更新器通过公开 API
`https://api.github.com/repos/vhxubo/t2-desktop/releases/latest` 拉更新，**不带
token**。私有仓库会返回 404，普通用户点「检查更新」永远显示「已是最新」。
若必须保持私有，需另行给更新器配置访问令牌（MyGo 配置层面处理）。

### 3. 备份私钥（重要）

私钥只存在于本地 `.gotmp/` 且被 gitignore。**本机损坏即永久丢失**，届时所有已装
应用的后续更新都会失效。请将其存入密码管理器或异地备份。

## 每次发布步骤

1. 改 `mygo.json` 的 `"version"`，例如 `"0.1.0"` → `"0.2.0"`。
2. 在 `CHANGELOG.md` 顶部新增 `## 0.2.0` 小节（这同时是 GitHub Release 说明与应用
   内更新窗口的展示内容）。
3. 提交并推送到 `main`。**提交信息必须用英文**（见 `AGENTS.md`）：
   `chore: release v0.2.0` 之类。
4. 打 tag，必须严格等于 `v` + version：`git tag v0.2.0`。
5. 推送 tag：`git push origin v0.2.0`。

推送 tag 后工作流自动运行：校验 tag 与 `mygo.json` 版本一致 → 各平台构建并签名 →
上传到草稿 Release → 全部成功后自动发布为 `latest`。

## 预发布（灰度）

带后缀的 tag（如 `v0.2.0-beta.1`）会被发布为 **prerelease**，不占用 `latest`，
普通用户不会被提示升级。适合先小范围验证下载 / 安装 / 更新链路后再发正式版。

## 注意事项

- **macOS 签名**：当前为 ad hoc 签名（未配 Developer ID / 公证），其他 Mac 首次
  打开会被 Gatekeeper 拦截。在 `mygo.json` 设置 `macos.signingIdentity` /
  `macos.notarize` 后由 `mygo build` 自动签名并公证。
- **Windows / Linux** 均在 Ubuntu runner 交叉编译，NSIS 从 apt 安装，无需额外配置。
- 本地空跑构建（不上传，确认能编译）：`go tool mygo build -platform windows/amd64`，
  产物写入 `build/`（已被 gitignore）。
- 本地环境自检：`go tool mygo doctor`。

## 排错

- **工作流在 build 阶段失败，提示缺少 `MYGO_UPDATER_PRIVATE_KEY`**：按上文第 1 步
  把私钥内容填进仓库 Actions secret。
- **`tag vX.Y.Z 与 mygo.json 的 version 不一致`**：tag 必须是 `v` 紧跟
  `mygo.json` 里的 version 字符串，二者需完全相等（含预发布后缀）。
- **已发版但应用检查更新看不到**：确认仓库为 Public；确认发布后该 Release 是
  `latest`（prerelease 不会成为 latest）；确认 `mygo.json` 的 `updates.github`
  与仓库路径一致。
- **换机器 / 重装后无法再签名**：私钥丢失，需恢复备份的 `MYGO_UPDATER_PRIVATE_KEY`；
  若彻底丢失，只能重新 `mygo keygen` 并同步更新 `mygo.json` 公钥（会作废所有旧版本
  已装应用的更新信任）。
