# 验证与发布门禁

## 提交前

代码改动在仓库根目录完成以下检查（Windows 可用 Git Bash）。

- `CGO_ENABLED=0 go test ./...` 通过。
- `go tool mygo vet .` 无告警。
- `gofmt -l .` 无输出。

仅改文档时，检查链接、命令和 `git diff --check`。

按改动补充回归：

- UI：用 `ui.NewTester` 复现，按需用 `Find`、`Texts` 或 `Image` 断言行为、文案与布局。
- API：用 `httptest` 断言路径、查询参数、请求体和错误分支。
- 新增或改动网络请求：在 `proxy_test.go` 用假代理确认出口遵循配置。升级 mygo 时还要核对更新器仍用 `netproxy.Transport()`，并跑 `TestUpdateCheckUsesConfiguredProxy`。

## 发版前

`scripts/check.sh` 禁用 CGO，检查 Go 格式、未完成标记、构建和静态检查，并运行 `go test -count=1 -shuffle=on -timeout=15m ./...`。PR、`main` 推送和发布 tag 共用这套三平台回归。本地推送钩子的安装见 [RELEASE.md](RELEASE.md#4-启用本地推送前检查每个克隆一次)。

下列项目全部通过后，才能按 [RELEASE.md](RELEASE.md#每次发布步骤) 打 tag、出包或推送更新：

- [ ] `bash scripts/check.sh` 和 `bash scripts/release-metadata.sh v<version>` 均通过。
- [ ] 应用可启动进入主界面，窗口与托盘可用，启动及空闲时无必现崩溃；SSE 重连和代理切换断连仍有效。
- [ ] 功能有完整的执行、错误处理和结果反馈；清理未完成占位、死代码、调试残留和已替换的实现。
- [ ] API 名称与文档一致，UI 文案断言已同步；代理用例覆盖应用请求、检查更新与插件默认客户端。

通过手测或无头测试验证核心路径：

- [ ] 授权：登录、刷新令牌、补授权后重跑。
- [ ] 阅读：主列表、详情、评论、六维属性、徽章墙和佩戴、用户卡片、通知流。
- [ ] 互动：发帖、评论、投票、表态、收藏、关注。
- [ ] 经济：金币、签到、打赏、盲盒、付费墙、上传（含图床适配）。
- [ ] 搜索与节点：搜索、节点级联选择、发帖两列选项。
