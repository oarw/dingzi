# 交接文档

> 环境即将销毁时的工作交接。记录**当前进度**、**已做的技术决策及原因**、**下一步计划**，
> 以及几个如果不知道会踩坑的地方。

最后更新：2026-08-30 · 分支 `main` · 仓库 `oarw/dingzi`（私有）

---

## 1. 当前状态

**已完成并推送**（`go build ./...` 和 `go vet ./...` 均通过）：

| 文件 | 说明 |
| --- | --- |
| `README.md` | 项目定位、对比哪吒 v0 的改进点、架构图、快速开始 |
| `internal/proto/messages.go` | 线协议：`Envelope` 信封、消息类型常量、`Hello`/`Welcome`/`ErrorPayload`、`Encode`/`Decode` |
| `internal/proto/metrics.go` | `Host`（静态信息）、`State`（指标采样）、`Host.Uptime()` |
| `go.mod` / `go.sum` | 模块定义，依赖已解析锁定 |
| `.gitignore` / `.gitattributes` / `LICENSE` | 忽略产物与运行时数据；强制 LF 换行；MIT |

**完成度：约 10%。** 只有协议层，两个二进制（agent / server）都还没开始写。
当前代码能编译是因为 `internal/proto` 不依赖任何第三方库。

---

## 2. 技术决策（附原因，不要轻易推翻）

这几条是花了时间验证过的，直接沿用可以省掉重复调研。

### 2.1 传输层用 WebSocket + JSON，不用 gRPC

哪吒 v0 最大的坑就是 gRPC：过 Cloudflare 要开特殊开关且经常失效，nginx 反代要额外配
`grpc_pass`，`--tls` / `--insecure` 语义容易配反，而且要额外占一个端口。

WebSocket over HTTPS 和普通网站流量无区别，CDN / 反代零特殊配置，和 Web UI 共用一个端口，
出问题能直接用浏览器 devtools 看帧内容。代价是 JSON 比 protobuf 大，但按 1 秒 1 个采样点
× 50 台机器算也只有几 KB/s，完全不是瓶颈。**同时也不需要 protoc**（当前环境也确实没装）。

### 2.2 `go 1.25.0` 是被依赖逼出来的，不是随便写的

最初想把下限压在 1.24（本机工具链是 go1.24.13），实测**做不到**——以下依赖的 `go.mod`
自身就要求 `go 1.25.0`：

```
golang.org/x/crypto v0.55.0      golang.org/x/net v0.57.0
golang.org/x/sync  v0.21.0       golang.org/x/sys v0.47.0
modernc.org/libc   v1.74.4       modernc.org/sqlite v1.57.0
github.com/prometheus-community/pro-bing v0.9.1
```

`modernc.org/sqlite` 最后一个支持 1.24 的版本是 **v1.44.3**（v1.46.2 起全部要 1.25）。
但即使把 sqlite 降到 v1.44.3，`golang.org/x/sys` 和 `x/crypto` 仍然要 1.25——而 `x/sys`
是 gopsutil 的硬依赖，降级会影响新平台的指标采集，不值得。

**结论：保持 `go 1.25.0`。** Go 的 toolchain 自动下载机制会让低版本用户透明地拉到正确工具链
（本机 1.24.13 就是这样自动下载 1.25.0 成功编译的）。CI 里直接指定 Go 1.25 以免每次构建都下载。

### 2.3 SQLite 用 `modernc.org/sqlite`（纯 Go），不用 `mattn/go-sqlite3`

后者要 CGO，交叉编译要准备各架构的 C 工具链，这是哪吒发布流程里的一个痛点。纯 Go 实现
`GOOS=linux GOARCH=arm64 go build` 直接出货。

### 2.4 计数器上报绝对值，不报增量

`State` 里 `NetInTransfer` / `TCPConns` 这类都是绝对值。原因写在 `metrics.go` 的注释里：
agent 重启或丢帧时，增量模型会把速率算错并且污染后续数据，绝对值最多影响它发生的那一个区间。

### 2.5 时间戳双份，服务端为准

`State.AgentTimeMS` 是 agent 自己的时钟，服务端收到时盖自己的时间戳。两者之差用来暴露
**时钟偏移**告警。哪吒 v0 直接信 agent 时间，机器时钟不准时图表会错乱。

### 2.6 `Hello.Name` 只在首次注册时生效

见 `messages.go` 注释：否则在面板上改了机器名，agent 一重连就被覆盖回去。

---

## 2A. 产品与设计决策（2026-08-30 与作者确认）

总原则（作者原话）：**尽可能安全、简约、不占内存；前端简约美观，后台清晰明了、符合直觉。**

落到执行上的一条判据：**能替用户决定的就不要给开关。** 每一个配置项都是推给运维的一次决策，
也是一处出错可能。哪吒 v0 让你选主题、配两个端口、挑 TLS 开关、手动添加机器——这些都该删掉
而不是美化。

### 2A.1 主列表用卡片式区块，但配响应式网格

作者选定卡片式（我原本推荐单行表格，密度更高，但尊重选择）。

⚠️ 卡片的固有代价是密度低，单列一屏只有 6~8 台。**必须用响应式网格补回来**：
卡片不占满整行，1440px 下三列并排（每张约 450px），一屏 18~24 台；窄屏降到两列、一列。

在 450px 宽度内，迷你曲线要做成**数字的背景**而不是独立一列，横向空间才够。

必须显示的 10 个字段（作者点名）：CPU、内存、交换、硬盘、网速、流量、系统信息、负载、在线时长、状态。

⚠️ **不要给这 10 个字段同等视觉权重**——这正是哪吒表格难扫的原因。三个层级：

1. 机器名 + 状态点：最重
2. 饱和度数字（CPU/内存/交换/硬盘）：中等，超阈值变色
3. 系统信息 / 未设配额等：灰字

### 2A.2 流量按月度累计，可设配额 —— 唯一需要改数据模型的决策

服务端要新增持久计数表。核心是**回退检测**：

```go
delta := raw_now - raw_last
if raw_now < raw_last {   // agent 重启 / 机器重启 / 网卡重置 / 32 位溢出
    delta = raw_now       // 把新值本身当增量
}
in_total += delta
raw_last = raw_now
```

误差上界是**一个上报间隔**（丢掉最后一次上报到重启之间的流量），可接受，实现时写进注释。

⚠️ **由此要改 proto 语义**：`State.NetInTransfer` / `NetOutTransfer` 从「自 agent 启动以来」
改成**上报操作系统原始累计计数器**。原因：如果 agent 自己减基线，agent 也得做一遍回退检测，
两处各有一份同样的易错逻辑；上报原始值则回退检测只在服务端做一次，而服务端算网速本来就需要增量。

配额有两个**必须可配**的点，否则进度条会说谎：

- `CountMode`：`sum | out | max`。计费口径因服务商而异（Vultr/Linode 只算出站，有的进出相加）。
  默认 `sum`——最保守，预警最早。
- `ResetDay`：1–31。VPS 账期不一定是 1 号。**31 号遇到 2 月要夹到当月最后一天。**

```go
type QuotaConfig struct {
    LimitBytes uint64 // 0 = 不限
    ResetDay   int    // 1-31，月末自动夹取
    CountMode  string // sum | out | max
}
```

配额进度条 80% 变黄、95% 变红。

### 2A.3 主题：跟随系统 + 三态手动覆盖

默认读 `prefers-color-scheme`，另留三态开关（自动/亮/暗）存 localStorage。
实现是 CSS 变量 + `data-theme` 属性。

### 2A.4 不做 exec（远程命令执行）

ping/TCP/HTTP 检查已覆盖监控需求，而 exec 意味着面板一旦被攻破等于**全机队 RCE**，
和「尽可能安全」直接冲突。

作者未明确反对，按不做推进。若日后要加：agent 端 `--allow-exec` 显式开启 + 面板二次确认，
默认关闭。

### 2A.5 安全基线

- **密钥只生成、不让用户选**：首次启动打印随机管理员密码和 agent 密钥。没有默认密码可忘记改，
  也不需要写弱密码校验。
- **agent 密钥按「终将泄露」对待**：它只认证*注册*。泄露的后果限制在「能注册假机器」（可见、可删），
  而不是「能读全机队数据或下发任务」。注册后每个 agent 拿自己的 token，吊销一台不必轮换全部。
- session `HttpOnly` + `Secure` + `SameSite=Lax`；bcrypt 存密码；密钥比较用**常量时间**；
  登录限速；SQL 全部参数化。
- **UI 不引 CDN**，第三方脚本永远碰不到面板——这也是 sparkline 要手写内联 SVG 的原因之一。

### 2A.6 内存：靠结构保证有界，不靠调参

判据：**内存随机器数增长，绝不随运行时长或流量增长。**

环形缓冲已满足（槽位固定，最老的样本被覆盖）。实测量级：一个 `State` 采样约 200 字节，
每机 120 槽约 26KB，50 台约 1.3MB 活跃指标。数据库文件会长，常驻内存不会。

⚠️ 真正会漏的是两处**看起来有界其实无界**的地方：

- pending 任务表：必须做超时清理，否则 agent 掉线后条目永久泄漏
- 浏览器 WebSocket 客户端：发送队列必须有上界，慢客户端要**丢弃**而不是无限缓冲

50 台机器的面板目标常驻内存 < 50MB。实现后实测，不预先承诺。

---

## 3. 下一步计划

建议按顺序做，每步都能独立编译验证。

### 第一优先：打通端到端最小链路

1. **`internal/proto/task.go`** —— 补 `Task` / `TaskResult` 结构体。
   ⚠️ 当前 `messages.go` 的文档注释里已经引用了 `[Task]` 和 `[TaskResult]`，但类型还没定义，
   godoc 链接是断的。任务类型至少要有：`ping`(ICMP) / `tcp` / `http` / `exec`。
2. **`internal/agent/`**
   - `config.go` 配置加载，优先级：命令行 > 环境变量 > 配置文件；**原子写入**（临时文件 + rename）
   - `collect.go` 用 gopsutil 采集，填充 `proto.Host` 和 `proto.State`
   - `client.go` WebSocket 客户端：指数退避 + 抖动重连、双向心跳、收到 `Welcome` 后按
     `Interval` 上报。UUID 首次生成并持久化。
3. **`internal/server/`**
   - `hub.go` 连接管理 + 每台机器**固定长度环形缓冲**存活跃指标（内存有上界）
   - `store.go` SQLite：WAL 模式，**写入串行化到单个 writer goroutine**（避免锁竞争丢数据）
   - `api.go` REST 接口，用标准库 `net/http` 的 `ServeMux`（Go 1.22+ 支持 `GET /x/{id}` 路由，
     不需要引第三方框架）
   - `auth.go` bcrypt 密码 + session cookie
4. **`cmd/server/main.go`** / **`cmd/agent/main.go`** 组装并加参数解析
5. **Web UI** `internal/server/web/`，用 `embed` 内嵌进二进制。
   不要引 CDN 依赖，sparkline 图自己写内联 SVG（约 100 行）即可，保证离线可用。

### 第二优先

6. 服务监控（HTTP/TCP/ICMP 定时检查 + 可用率统计）
7. 告警规则 + 通知（webhook / Telegram）
8. 在线终端（WebSocket 转发 pty）
9. `.github/workflows/` CI：build + vet + test，**Go 版本钉 1.25**；交叉编译 release 产物
10. 安装脚本 + systemd unit

---

## 4. 坑位提醒

### ⚠️ 现在不要跑 `go mod tidy`

`go.mod` 里的依赖是**预先解析好但还没被任何代码 import** 的（目前只有 `internal/proto`，
它不依赖第三方库）。现在跑 `go mod tidy` 会把这些依赖全部删掉，白费前面的版本调研：

```
github.com/shirou/gopsutil/v4          指标采集
github.com/gorilla/websocket           传输层
modernc.org/sqlite                     存储（纯 Go）
gopkg.in/yaml.v3                       配置文件
golang.org/x/crypto/bcrypt             密码哈希
github.com/prometheus-community/pro-bing  ICMP ping
github.com/google/uuid                 agent 身份
```

**等写完 import 这些包的代码之后再 tidy。** 届时它还会把 `// indirect` 标记修正成直接依赖
（现在全被标成 indirect 是因为确实没有代码引用）。

### 其他

- 本机 Go 是 1.24.13，但 `go.mod` 要求 1.25.0，所以**每次构建都会自动下载 1.25 工具链**
  （能正常工作，只是第一次慢）。CI 里装 Go 1.25 就没这个问题。
- 环境没有 `protoc`。按 2.1 的决策本来也不需要。
- 提交时 Git 会警告 `LF will be replaced by CRLF`，这是 Windows 下的正常现象，
  `.gitattributes` 已保证仓库里存的是 LF。
- `gh` 已用 `oarw` 账号登录（scope 含 `repo`），git 凭据走 `gh auth git-credential` 助手，
  `git push` 可直接用，无需再配 token。

---

## 5. 环境信息

```
工作目录  C:\Users\runneradmin\Desktop\pro\dingzi
平台      Windows Server 2025 (GitHub Actions runner)
Go        1.24.13（go.mod 要求 1.25.0，自动下载）
Node      v22.23.2（当前用不上，UI 不走构建步骤）
gh        2.98.0，已登录 oarw
protoc    未安装
```
