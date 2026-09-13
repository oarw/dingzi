# 参与开发

## 提交信息格式会决定版本号

发版是全自动的:合并进 `main` 之后,版本号**从 commit 信息里推出来**。所以格式不是
风格偏好,是功能。

```
<类型>: <说明>
<类型>(<范围>): <说明>
<类型>!: <说明>          ← 破坏性变更
```

| 类型 | 版本变化 | 用在什么时候 |
| --- | --- | --- |
| `feat` | minor (0.1.0 → 0.2.0) | 新功能 |
| `fix` | patch (0.1.0 → 0.1.1) | 修 bug |
| `perf` | patch | 性能优化 |
| `refactor` | patch | 重构,行为不变 |
| `revert` | patch | 回滚 |
| `docs` `chore` `ci` `test` `style` `build` | **不发版** | 文档、依赖、CI、测试、格式 |
| 任意类型加 `!`,或正文里 `BREAKING CHANGE:` | major (0.1.0 → 1.0.0) | 配置格式变了、协议不兼容了 |

跳过文档等提交的作用:改个错别字不会产出一个新版本。没有它,「合并就发版」会让 release
页面被文档提交淹掉。

例:

```
feat(agent): 支持 OpenVZ 的内存上报
fix: 交换分区百分比在 swap 为 0 时除零
feat(proto)!: 移除 v0 的 task 消息类型
docs: 补充网页终端的风险说明
```

## 分支和 PR

所有改动通过 PR 合并进 `main`:

```sh
git switch -c fix/swap-divide-by-zero
# ... 改代码 ...
git commit -m "fix: 交换分区百分比在 swap 为 0 时除零"
git push -u origin fix/swap-divide-by-zero
gh pr create
```

PR 开着的时候每次 push 都会:

1. 跑完整 CI(Linux + Windows 测试、race detector、gofmt、9 个交叉编译目标)
2. 全绿之后发一个**滚动预发布** `vX.Y.Z-prNN`,可以直接装来试

```sh
curl -fsSL https://raw.githubusercontent.com/oarw/dingzi/v0.2.0-pr42/install.sh | sh -s -- \
    --version v0.2.0-pr42 --server https://panel.example.com --secret <密钥>
```

预发布是滚动的 —— 同一个 PR 再推一次会替换掉上一个,不会在 release 页面堆一串。
上面的标签仅为示例，实际使用时替换为当前 PR 的发布标签。面板、Agent 和安装脚本应来自
同一轮发布；测试开发分支时不要默认安装最新正式版。认证协议和恢复步骤见
[升级与运维说明](docs/UPGRADING.md)。

**从 fork 提的 PR 只有构建产物,没有预发布。** 那种 PR 拿到的 `GITHUB_TOKEN` 是
只读的,这是安全属性:否则任何人 fork 一下就能往 release 页面推东西。产物在 Actions
run 页面的 Artifacts 里,登录后可下载。

## 合并方式:squash

PR 用 squash merge,**PR 标题会成为那条 commit 信息**,所以标题也要符合上面的格式。
这样 release notes 里一个 PR 就是一行,而不是把分支上十几条 WIP commit 全倒出来。

## 本地验证

先遵守所在环境的限制：维护者的 `Desktop/pro` 是允许安装依赖、测试和构建的远程环境；
`code` 目录是工作电脑，只阅读、编辑和静态检查，编译测试交给 CI。

```sh
go build ./...
go test -count=1 -timeout 10m ./...
go test -race -count=1 -timeout 10m ./...
go vet ./...
gofmt -l .          # 有输出就是 CI 会红的地方
node --check internal/server/web/app.js
node --check e2e/browser.mjs
sh -n install.sh
```

race 检测需要 C 编译器；正式程序仍支持 `CGO_ENABLED=0`。`go test ./...` 包含安装脚本回归：
本地下载桩、临时二进制和配置目录，不访问下载站或注册系统服务；没有 POSIX `sh` 时跳过该组测试。
CI 与发布矩阵的 Linux ARM 包显式使用 `GOARM=6`，兼容 ARMv6/ARMv7。

终端相关的集成测试带 `unix` build tag —— 它们会真的起一个 pty 和真的 shell,
所以只在 Linux / macOS 上跑。CI 在 ubuntu 上跑它们。

Windows 上开发的话,可以拿 WSL 里的 Alpine 验证(顺便测了 busybox 那条路径,
而那正是网页终端的主要使用场景):

```sh
GOOS=linux go test -c -o e2e/wsl/server.test ./internal/server
wsl -d <distro> -- /bin/sh e2e/wsl/run-tests.sh
```

### 管理界面浏览器验收

`e2e/browser.mjs` 会启动临时面板和真实本机 Agent，检查登录、机器设置、监控执行、本地 Webhook、
规则创建、历史图表、桌面/手机布局、渠道凭据、计费口径和断连状态。结束时停止自身进程并清理
临时数据，截图保留在 `DINGZI_REVIEW`。它不会使用生产凭据或发送外部通知。

需要 Node.js 22+、Playwright 及 Chrome/Chromium。下面是在允许构建的 Windows 环境中的命令：

```powershell
New-Item -ItemType Directory -Force e2e/run/tools | Out-Null
go build -o e2e/run/dingzi-server.exe ./cmd/server
go build -o e2e/run/dingzi-agent.exe ./cmd/agent
npm install --prefix e2e/run/tools --no-save --package-lock=false playwright@1.56.1
$env:DINGZI_BIN = (Resolve-Path e2e/run).Path
$env:DINGZI_TOOLS = (Resolve-Path e2e/run/tools).Path
$env:DINGZI_REVIEW = Join-Path $env:DINGZI_BIN 'review'
node e2e/browser.mjs
```

Windows 默认使用 `C:/Program Files/Google/Chrome/Application/chrome.exe`，其他位置通过
`DINGZI_CHROME` 指定。Unix 下去掉二进制的 `.exe` 后缀，用 `export` 设置同名环境变量；
可设置 `DINGZI_CHROME` 使用系统浏览器，或通过
`node e2e/run/tools/node_modules/playwright/cli.js install chromium` 安装测试浏览器。
依赖和产物放在已忽略的 `e2e/run`，不提交截图、数据库、日志、凭据或 PID 文件。

Go CI 当前不自动运行这组浏览器测试。每次验收应记录提交/工作区范围、环境、通过的命令和未验证项，
不要用历史 CI 结果替代当前修改的证据。本轮记录见 [代码复审](docs/REVIEW_2026-09-13.md)。

## 一些设计立场

改动碰到这几处之前,先看 [`HANDOFF.md`](HANDOFF.md) 里记的原因,免得重新调研一遍:

- **不做 `exec` 任务类型。** 面板能在每台机器上跑命令,等于面板被攻破一次就是
  全机队 RCE。网页终端是显式例外,并且默认关闭。
- **agent 只上报原始计数器。** 回退检测(重启、网卡重置、32 位溢出)只在服务端
  做一次,不在两边各做一次。
- **内存必须由结构保证有界。** 环形缓冲固定长度,内存只随机器数增长,不随运行
  时长或流量增长。
- **能替用户决定的就不给开关。** 例外是那些真的因服务商而异的东西(流量口径、
  归零日),和那些必须由机主自己承担风险的东西(`--allow-terminal`)。

## 装完之后没上线怎么查

```sh
# systemd
journalctl -u dingzi-agent -f
# OpenRC
tail -f /var/log/dingzi-agent.log
```

最常见的两个原因:面板地址写错(scheme 决定是否加密,`http://` 不会自动升级),
以及密钥粘贴时带了空格。

面板侧看 agent 有没有连上:`journalctl -u dingzi-server -f | grep "agent connected"`。
