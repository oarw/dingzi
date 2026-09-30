# 贡献指南

**简体中文** | [English](CONTRIBUTING.en.md)

Dingzi 是 Go 编写的轻量服务器监控面板与 Agent。网页资源内嵌，无前端构建步骤。
贡献应让使用和维护更简单；项目边界与设计约定见 [维护指南](MAINTAINING.md)。

## 提交问题与改动

问题报告请附版本、系统/架构、部署方式、复现步骤、预期与实际结果、脱敏日志。
不要上传运行配置、数据库、密码、Agent 凭证或 OAuth Secret。
新依赖、协议/存储变更或跨模块功能先在 Issue/PR 中简述问题、方案和兼容性；小修复可直接提 PR。

从 `main` 建分支，向 `main` 提 PR。一个 PR 解决一个可独立验证的问题，带上相关测试与必要文档。
大范围格式化、文件搬移与行为修改分开提交，让评审者能分别核对。
PR 说明写清用户可见变化、验证命令/环境、未验证项；涉及数据或部署时说明升级和回退方式。

## 开发与验证

使用 CI 指定的 Go 工具链（当前 1.27.1，语言最低版本见 `go.mod`）、Node.js 22+、Python 3.10+ 和 Git。
仅在允许构建的环境执行以下命令；环境限制本地测试时，提交到允许的远程 CI 验证并记录结果。
下面以 `python3` 为例；Windows 可用 `python`。所有命令从仓库根目录运行。

```sh
go build ./...
go vet ./...
go test -count=1 -timeout 10m ./...
go test -race -count=1 -timeout 10m ./...
gofmt -l .
node --test e2e/board_test.mjs
python3 scripts/check-structure.py
python3 -B -m unittest discover -s e2e -p structure_test.py
sh -n install.sh
sh -n install-server.sh
sh -n e2e/service_install.sh
```

`gofmt -l .` 应无输出；有输出时仅格式化改动涉及的 Go 文件。JS 改动另用 `node --check <文件>` 检查语法。
race 检测需要 C 编译器；发布程序仍以 `CGO_ENABLED=0` 构建。Windows 没有 POSIX `sh` 时，安装脚本测试会跳过；
真实 PTY 测试只在 Linux/macOS 运行。不要把跳过写成通过。

按改动选择验证，CI 最终执行完整矩阵：

| 改动 | 需要的证据 |
| --- | --- |
| Go 逻辑 | 相关包测试；并发改动补 race，性能优化附可复现 benchmark/profile |
| 会话、权限、凭证 | 成功、拒绝、到期/吊销路径的行为回归 |
| 协议、配额、存储 | 兼容性、边界值、重启/持久化；失败不得伪装成成功 |
| UI | Node 状态回归；交互/布局改变运行浏览器验收，检查桌面、手机、空态与错误态 |
| 安装器 | Go 安装回归及 CI 的真实 systemd/OpenRC 安装、升级、卸载保留数据 |
| 容器 | 镜像构建和 Compose smoke；CI 在 amd64、arm64 原生主机执行 |
| 指南/格式 | 链接、示例与实际工作流一致；无需为纯文字改动新增测试 |

CI 包含 Linux、Windows、macOS 的 build/vet/test/race，Node 状态回归、格式/结构检查、
真实 systemd/OpenRC、9 目标交叉编译和两架构容器检查。浏览器验收目前需手动运行。

### 浏览器验收

测试使用临时面板、真实本机 Agent、本地通知接收端，不需要生产凭据或真实 OAuth App。
在允许构建的环境准备二进制、Playwright 和浏览器（以下为 POSIX shell）：

```sh
mkdir -p e2e/run/tools
go build -o e2e/run/dingzi-server ./cmd/server
go build -o e2e/run/dingzi-agent ./cmd/agent
npm install --prefix e2e/run/tools --no-save --package-lock=false playwright@1.56.1
node e2e/run/tools/node_modules/playwright/cli.js install chromium
export DINGZI_BIN="$PWD/e2e/run"
export DINGZI_TOOLS="$PWD/e2e/run/tools"
export DINGZI_REVIEW="$PWD/e2e/run/review"
node e2e/browser.mjs
node e2e/github-browser.mjs
```

Windows 二进制加 `.exe`，通过 PowerShell `$env:变量名` 设置同名环境变量；默认使用系统 Chrome，
其他安装位置设置 `DINGZI_CHROME`。截图与依赖保留在已忽略的 `e2e/run`，不提交测试产物。

### 容器与服务验收

```sh
docker build --build-arg VERSION=ci -t dingzi-ci:local .
python3 e2e/docker_smoke.py
```

需要 Linux 容器引擎和 Docker Compose v2。smoke 测试使用独立项目名，验证健康、登录、非 root/只读权限、
数据持久化和正常退出，并清理自身资源。
`e2e/service_install.sh` 会修改系统服务和系统目录，只能在一次性 Linux 环境以 root 并显式设置
`DINGZI_SERVICE_TEST=1` 运行；日常开发使用 Go 安装测试，真实服务验收交给 CI。

## 代码与测试约定

- 按职责组织文件；新增功能不要继续塞进入口、路由总表或已有大文件。先看维护指南的结构上限与现有拆分计划。
- Go 用 `gofmt`，错误尽早返回，名称表达职责；优先具体类型与标准库，不预建框架、工厂或仅为 mock 的接口。
- JS/CSS 保持可读格式，控制流展开，不用手工压缩源码追求短文件；只格式化相关范围。
- 外部输入在入口解析、校验并规范化；内部使用已确认的类型和约束。不要逐层重复 nil/范围检查，也不要用空值吞掉异常。
- 测试可观察行为与真实故障边界，不绑定私有实现细节，不靠极短 sleep 或文件时间精度判断先后。
  并发测试用事件/同步或有截止时间的等待，时序夹具明确设置时间。
- 注释说明原因、协议约束或资源所有权；不要复述代码，也不要留下无归属、无后续条件的 TODO。

## 提交信息与自动发版

使用 Conventional Commits：`<类型>(可选范围): <说明>`。推荐 squash merge，PR 标题应能直接作为提交标题。
`main` 上的 Release workflow 检查自上一个正式版本以来的提交，完整 CI 通过后决定是否发版。

| 类型 | 版本变化 |
| --- | --- |
| `feat` | minor，例如 0.1.0 → 0.2.0 |
| `fix`、`perf`、`refactor`、`revert` | patch |
| `docs`、`chore`、`ci`、`test`、`style`、`build` | 本身不触发发版 |
| 类型后 `!` 或正文 `BREAKING CHANGE:` | major，包括 0.x 阶段 |

需要交付给用户的依赖安全修复应使用准确的 `fix` 提交，不能因写成 `build` 而漏发版。
同仓 PR 通过检查后可获得滚动预发布；fork PR 只有 Actions 构建产物，没有写入 Release/镜像的权限。
试用预发布时，面板、Agent、安装脚本选择同一版本。已经发布的正式标签保持不变。
