# Dingzi 钉子

轻量级自托管服务器探针 / 监控面板。哪吒探针 v0 的替代品。

**单二进制 · 单端口 · 无 CGO · WebSocket 传输 · 纯 Go SQLite**

## 为什么造这个

哪吒 v0 用久了会踩到一堆坑，Dingzi 针对每一个都做了设计上的规避：

| 哪吒 v0 的问题 | Dingzi 的做法 |
| --- | --- |
| gRPC 过 Cloudflare / nginx 要特殊配置，经常连不上 | 传输层换成 **WebSocket over HTTPS**，使用常规 WebSocket 反代配置 |
| 面板要开两个端口（gRPC + Web），防火墙容易配错 | **单端口**，Agent 和 Web UI 共用一个 HTTP 服务 |
| Agent 掉线后卡死不重连，要手动重启 | 指数退避 + 抖动的**自动重连**，双向心跳超时强制重建连接 |
| `--tls` / `--insecure` 语义混乱，容易配反 | Agent 只认 `server` URL，`https://` 或 `wss://` 自动启用 TLS，无歧义 |
| SQLite 依赖 CGO，交叉编译困难 | `modernc.org/sqlite` **纯 Go 实现**，`GOOS=linux go build` 直接出货 |
| 服务器多了以后面板内存暴涨 | 内存里只保留**固定长度环形缓冲**，历史落库、查询时聚合、按保留期清理 |
| 配置文件被部分写入，密钥丢失 | 配置**原子写入**（临时文件 + rename）；损坏时明确报错，保留原文件 |
| Agent 机器时钟不准导致图表错乱 | 时间戳**以服务端为准**，同时上报 Agent 时间用于偏移提示 |
| 单条 SQL 写入被锁，高并发下丢数据 | WAL + **批量指标写入**；配置事务提前取得写锁，避免读取后升级写锁失败 |
| 网页终端一开就是全机队可执行命令 | 终端**每台机器单独开关**，agent 不加 `--allow-terminal` 就拒绝，决定权在被开终端的机器上 |

## 架构

```
┌──────────┐   WebSocket (JSON)   ┌────────────────────────────┐
│ dingzi-  │ ───────────────────► │        dingzi-server       │
│  agent   │ ◄─────────────────── │  hub → ring buffer → SQLite│
│          │   任务下发/结果回传   │  REST API + 内嵌 Web UI     │
│          │                      │                            │
│          │   WebSocket (二进制)  │                            │
│   pty    │ ───────────────────► │   终端桥接（仅开启时）        │
└──────────┘   仅在开启终端时建立   └────────────────────────────┘
                                        ▲ 单端口 :8008
                                   浏览器 / API 客户端
```

终端走**独立连接**，不和指标复用：终端输出是突发的，一次 `cat` 大日志就能把心跳挤过读超时，
那样会把一台健康的机器判成离线。监控归监控，终端刷爆只影响自己。

- `cmd/server` 面板：接 Agent、存数据、提供 API 和 UI
- `cmd/agent` 探针：采集指标、执行任务（ping / TCP / HTTP 检查）
- `internal/proto` 双方共用的线协议定义

## 快速开始

### 面板端

```bash
go build -o dingzi-server ./cmd/server
./dingzi-server --listen :8008 --data ./data
```

首次启动会在终端打印随机生成的管理员密码和 Agent 密钥，浏览器打开 `http://<ip>:8008` 登录。

### Agent 端(一键安装)

脚本默认安装**最新正式版**。从本开发分支编译面板时，请使用同一提交构建的 Agent，
或指定配套的预发布版本；`v0.1.1` 及更早版本不能与本分支混用。升级前先读
[升级与运维说明](docs/UPGRADING.md)。

```sh
curl -fsSL https://raw.githubusercontent.com/oarw/dingzi/main/install.sh | sh -s -- \
    --server https://panel.example.com --secret <上一步的密钥>
```

容器里需要网页终端,追加 `--allow-terminal`。

脚本会自己判断平台(Linux / macOS / FreeBSD,amd64 / arm64 / armv6-7 / 386 /
riscv64)、注册服务(systemd 或 OpenRC)、**强制校验 SHA256**。没有
`--skip-checksum` 这个开关:脚本以 root 执行刚下载的二进制,一个跳过校验的开关
迟早会有人在生产上用。

用 POSIX `sh` 写的,不是 bash —— Alpine 和各种 slim 镜像没有 bash,而那些正是
最需要一键安装的机器。

卸载:`install.sh --uninstall`（保留配置中的 UUID 和单机 token，重装可续用）。
安装脚本会先暂存二进制并校验配置，成功后才替换现有程序；配置失败时保留原二进制。

### Agent 端(手动)

```bash
go build -o dingzi-agent ./cmd/agent
./dingzi-agent --config ./agent.yaml --server wss://panel.example.com --secret <上一步的密钥>
```

Agent 会在连接前生成并保存 UUID 和单机 token，再使用注册密钥接入面板，无需手动添加机器。
保留 `agent.yaml`，避免重启或重装后丢失身份；不要在不同机器之间复制 UUID/token。

## 管理与监控

机器看板和历史查询可公开读取；登录后可管理机器、服务监控、告警规则和通知渠道。

- **机器管理**：改名、删除、设置月度配额、UTC 归零日及 `sum` / `out` / `max` 计费口径。
  修改归零日会清空当前周期累计量；删除机器会同时删除历史、关联监控和规则，并吊销该身份。
- **历史图表**：点击机器名称，查看 CPU、内存、交换、磁盘、负载和网络速率，支持 1 小时至
  30 天范围。数据在查询时聚合；升级前缺少容量或速率字段的历史点显示为空值。
- **服务监控**：每条检查选择一台执行探针，支持 HTTP/HTTPS、TCP、ICMP Ping。检查间隔
  10–86400 秒，超时 100–60000 毫秒且不超过检查间隔；最多 200 条监控、16 个并发检查。
  探针离线、缺少 ICMP 权限或无法取得结果记为“未知”，不计入服务可用率的分母。
- **告警通知**：支持 CPU、内存、交换、磁盘、负载、配额、机器离线和服务故障规则。
  每 5 秒评估一次，达到阈值并持续指定时间后触发，恢复时另记一次事件；未知数据不会触发恢复。
  Webhook 和 Telegram 支持测试通知，自动通知失败后以 30 秒、60 秒间隔重试，总计最多 3 次。

看板每 2 秒刷新一次；监控、规则、渠道和事件页面可通过刷新按钮更新，历史图表在打开或切换范围时查询。
原始指标、检查结果和告警事件默认保留 30 天，面板每小时清理一次；可用 `--retention-days` 调整。
Web UI 继续使用内嵌 HTML/CSS/JavaScript，无前端构建步骤或运行时 CDN。

## 网页终端

给没有 SSH 的容器留的一条进去干活的路。**默认关闭**，要用得在那台机器上显式打开：

```bash
./dingzi-agent --config ./agent.yaml --server wss://panel.example.com --secret <密钥> --allow-terminal
```

开了之后面板上那台机器的卡片右上角会出现 `›_` 按钮。找不到 shell 的镜像（distroless、
scratch）会明确告诉你镜像里没有 `/bin/sh`，而不是给你一个空白窗口。

shell 查找顺序：`$SHELL` → `bash` → `sh` → `ash` → `dash` → `busybox sh`。
Alpine 这类只有 busybox 的镜像可以正常用（已在 busybox v1.37 上验证）。

### 请诚实地理解它的风险

**网页终端就是远程执行命令。** pty 可以被程序化写入，所以「shell 会话」和「执行命令」
的区别只在仪式上，不在后果上。这也是本项目没有 `exec` 任务类型的原因，终端没有把它从后门放回来。

真正撑住这件事的是 **agent 默认拒绝**：开关在被开终端的那台机器上，而不是在那个可能被攻破的面板上。
面板被攻破，也只能碰到机主主动开过终端的机器。

其余措施只是缩小影响面，不是阻止：

- 一次性会话 token，30 秒失效，用掉即删（防重放）
- 并发上限：单机 4 个，全局 16 个
- 15 分钟无**输入**自动关闭（只看输入不看输出，否则一个被遗忘的 `tail -f` 能挂到进程结束）
- 每次会话记审计日志：请求 / 打开 / 关闭，带来源 IP、时长、用的哪个 shell

**残余风险，明说：**

1. **拿到面板有效 session 的人可以开 shell。** 没有做二次验密码——拿着 session 本来就能删机器、
   改配额，再输一次密码挡不住已经进来的人，所以不假装它有用。请把面板密码当作 SSH 私钥同等对待。
2. **shell 以 agent 进程的用户身份运行。** 监控 agent 通常以 root 跑（要读所有挂载点和传感器），
   那这就是一个 root shell。agent 启动时会打一条 WARN 提醒。
3. 整个面板可以用 `--terminal=false` 一刀关掉，这是给「机队里谁都不许开」的场景准备的。

目前只支持 Linux / Unix。Windows 上会明确返回「此平台暂不支持」，不会静默失败。

## 配置

Agent 配置优先级为「命令行参数 > 环境变量 > 配置文件」，面板则从数据目录读取密钥配置，
监听地址、保留期等由命令行控制。参考
[`config.example.yaml`](config.example.yaml) 和 [`agent.example.yaml`](agent.example.yaml)。

注册后，面板只保存单机 token 的 SHA-256 哈希；已绑定身份不再依赖注册密钥。
密码重置、身份恢复、版本配套和反向代理配置见 [升级与运维说明](docs/UPGRADING.md)。

## 开发

> 🚧 开发中。已实现探针采集、实时看板、机器管理、历史图表、服务监控调度、告警通知和网页终端。
> 本轮审查针对 `feat/complete-monitoring`，详情及验证记录见 [代码复审](docs/REVIEW_2026-09-13.md)。
>
> 接手前请先读 [`HANDOFF.md`](HANDOFF.md)，里面有当前进度、每个技术决策的原因，
> 以及踩过的坑（省得再调研一遍）。

需要 Go 1.25+（原因见 HANDOFF.md 第 2.2 节）。

```bash
go build ./...        # 编译
go test ./...         # 测试
go test -race ./...   # 并发检查（需 C 编译器，仅检测器需要）
go vet ./...          # 静态检查
gofmt -l .            # 格式检查，提交前跑一下
```

`go test ./...` 也包含安装脚本回归测试：用本地下载桩和临时目录验证升级失败保护，
不访问外部下载地址、不安装系统服务。没有 POSIX `sh` 时该组测试会跳过。
管理界面的真实 Agent/浏览器验证方法见 [CONTRIBUTING.md](CONTRIBUTING.md)。

终端相关的集成测试带 `unix` build tag，会真的起一个 pty 和真的 shell，所以只在
Linux / macOS 上运行。在 Windows 上开发时，可以用 WSL 里的 Alpine 验证：

```bash
GOOS=linux go test -c -o e2e/wsl/server.test ./internal/server   # 交叉编译测试二进制
wsl -d <distro> -- /bin/sh e2e/wsl/run-tests.sh                  # 在 Linux 里跑
```

交叉编译（无需 CGO 工具链）：

```bash
GOOS=linux GOARCH=amd64 go build -o dingzi-agent-linux-amd64 ./cmd/agent
GOOS=linux GOARCH=arm64 go build -o dingzi-agent-linux-arm64 ./cmd/agent
```

## 许可

GNU Affero General Public License v3.0 or later (AGPL-3.0-or-later)
