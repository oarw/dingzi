# Dingzi 钉子

轻量级自托管服务器探针 / 监控面板。哪吒探针 v0 的替代品。

**单二进制 · 单端口 · 无 CGO · WebSocket 传输 · 纯 Go SQLite**

## 为什么造这个

哪吒 v0 用久了会踩到一堆坑，Dingzi 针对每一个都做了设计上的规避：

| 哪吒 v0 的问题 | Dingzi 的做法 |
| --- | --- |
| gRPC 过 Cloudflare / nginx 要特殊配置，经常连不上 | 传输层换成 **WebSocket over HTTPS**，和普通网站流量一样，CDN / 反代零特殊配置 |
| 面板要开两个端口（gRPC + Web），防火墙容易配错 | **单端口**，Agent 和 Web UI 共用一个 HTTP 服务 |
| Agent 掉线后卡死不重连，要手动重启 | 指数退避 + 抖动的**自动重连**，双向心跳超时强制重建连接 |
| `--tls` / `--insecure` 语义混乱，容易配反 | Agent 只认 `server` URL，`https://` 或 `wss://` 自动启用 TLS，无歧义 |
| SQLite 依赖 CGO，交叉编译困难 | `modernc.org/sqlite` **纯 Go 实现**，`GOOS=linux go build` 直接出货 |
| 服务器多了以后面板内存暴涨 | 内存里只保留**固定长度环形缓冲**，历史数据落库并自动降采样 |
| 配置文件写坏导致起不来 | 配置**原子写入**（临时文件 + rename），坏了自动回退默认值 |
| Agent 机器时钟不准导致图表错乱 | 时间戳**以服务端为准**，同时上报 Agent 时间用于时钟偏移告警 |
| 单条 SQL 写入被锁，高并发下丢数据 | WAL 模式 + **批量写入**，写入串行化到单 writer goroutine |

## 架构

```
┌──────────┐   WebSocket (JSON)   ┌────────────────────────────┐
│ dingzi-  │ ───────────────────► │        dingzi-server       │
│  agent   │ ◄─────────────────── │  hub → ring buffer → SQLite│
└──────────┘   任务下发/结果回传   │  REST API + 内嵌 Web UI     │
                                  └────────────────────────────┘
                                        ▲ 单端口 :8008
                                   浏览器 / API 客户端
```

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

### Agent 端

```bash
go build -o dingzi-agent ./cmd/agent
./dingzi-agent --server wss://panel.example.com --secret <上一步的密钥>
```

Agent 首次连接会自动生成 UUID 并注册到面板，无需在面板上手动添加机器。

## 配置

两端都支持「命令行参数 > 环境变量 > 配置文件」的优先级。参考
[`config.example.yaml`](config.example.yaml) 和 [`agent.example.yaml`](agent.example.yaml)。

## 开发

> 🚧 项目正在开发中，目前只完成协议层。接手开发前请先读
> [`HANDOFF.md`](HANDOFF.md)，里面有当前进度、技术决策的原因和几个坑位提醒
> （**特别是：现在不要跑 `go mod tidy`**）。

需要 Go 1.25+（原因见 HANDOFF.md 第 2.2 节）。

```bash
go build ./...        # 编译
go test ./...         # 测试
go vet ./...          # 静态检查
```

交叉编译（无需 CGO 工具链）：

```bash
GOOS=linux GOARCH=amd64 go build -o dingzi-agent-linux-amd64 ./cmd/agent
GOOS=linux GOARCH=arm64 go build -o dingzi-agent-linux-arm64 ./cmd/agent
```

## 许可

MIT
