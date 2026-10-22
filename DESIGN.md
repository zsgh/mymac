# mymac 设计文档

> 状态：待确认 → Phase 0 起按此实现
> 目标：macOS 26.5.2 (25F84) / arm64，纯 Go，无 cgo，单二进制 + 内嵌 Web UI

---

## 0. 本机实测基线（已在本机跑过，解析器按真实输出写）

| 命令 | 实测结论 |
|---|---|
| `launchctl setenv K V` | exit 0，值原样保存（`$`、引号、空格无需转义） |
| `launchctl getenv K` | exit 0；**变量不存在时也 exit 0 且输出为空** → 必须判空，不能判 exit code |
| `launchctl unsetenv K` | exit 0，立即生效 |
| `launchctl print gui/$UID` | `environment = { KEY => value ... }` 区块，含 launchd 全局环境 |
| `launchctl bootstrap gui/501 <plist>` | exit 0，LaunchAgent 立即 `state = running`，`RunAtLoad` 已执行 |
| `launchctl bootout gui/501/<label>` | exit 0；**但不会清掉它 setenv 过的变量** → 卸载时必须额外 `unsetenv` |
| `launchctl setenv` 在 LaunchAgent 内调用 | 可行（Agent 属 gui domain），登录后重放验证通过 |
| `nettop -P -L 1` | CSV；行格式 `进程名.pid,bytes_in,bytes_out,`；**累计值**，速率需两次采样求差；进程名含空格/中文；无 root 只能看到自己的 |
| `lsof -nP -i` | `COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME`；NAME 形如 `TCP 127.0.0.1:62814->127.0.0.1:49374 (ESTABLISHED)` |
| `footprint -p <pid>` | 文本表 + `Footprint: 1568 KB`；**逐进程调用极慢**，只在详情页按需调用 |
| `powermetrics` | 非 root 直接拒绝：`powermetrics must be invoked as the superuser` → 仅在提权后可用，否则显示"需管理员" |
| `plutil -convert json -o - <plist>` | 成功输出 JSON（binary/xml plist 通用）→ **plist 解析走 plutil + encoding/json，零第三方依赖** |
| `sips -s format png x.icns` | 成功，输出 PNG（256×256）→ 图标转换可行 |
| `codesign -dv --verbose=2 <app>` | `Authority=Developer ID Application: ...`、`TeamIdentifier=`、`Format=app bundle with Mach-O thin (arm64)` |
| `vm_stat` | page size 16384，输出 `Pages free/active/inactive/wired down/...` |
| `ioreg -rn AppleSmartBattery` | `CurrentCapacity` / `MaxCapacity` / `IsCharging` / `ExternalConnected` / `CycleCount` |
| `sysctl -n sysctl.proc_translated` | 只对自身有效（0=原生）；他进程用 `lipo -archs` 判定 |
| 沙箱 | `~/Library/Safari`、`~/Library/Mail` 可读 → **本机终端已有 FDA**，权限页需能区分"已授权/未授权" |

**依赖现状**：无外网。`gopsutil/v4 v4.26.9` 及其全部依赖已在 `~/go/pkg/mod` 缓存中 → 用 `GOFLAGS=-mod=mod GOPROXY=off` 可离线构建。

---

## 1. 总体架构

```
cmd/mymac/main.go          入口：解析参数 → 单实例锁 → 初始化 config/audit → 启动 server → 菜单栏 → 等待退出
internal/
  config/     路径常量、~/.config/mymac 目录管理
  audit/      JSONL 审计日志（append + fsync）
  instance/   单实例文件锁（flock）
  menubar/    macOS 菜单栏图标（cgo + AppKit，无第三方依赖）
  perm/       FDA 探测、自动化权限探测、管理员提权封装
  server/     HTTP 路由、Host/Origin/Fetch-Metadata 校验、SSE
  envs/       Phase 2 环境变量（env.json / env.sh / .zshenv 标记块 / launchctl / LaunchAgent）
  procs/      Phase 3 进程管理（gopsutil 采集、进程树、lsof/nettop 采集、信号、renice）
  apps/       Phase 4 应用卸载（扫描、Info.plist 解析、残留匹配、废纸篓）
  cleaner/    Phase 5 系统清理（缓存/开发者缓存扫描、大文件、重复文件）
  sysinfo/    Phase 3/5 系统总览（CPU/内存/磁盘/网络/电池）
  web/        go:embed 包装
web/          index.html + app.js + style.css + favicon
```

- **后端**：`net/http` 标准库，REST JSON + SSE。无 Web 框架依赖。
- **前端**：原生 JS + CSS，**无构建步骤**（无外网，也避免 npm 依赖）。单页应用 + 左侧导航。
- **进程模型**：后台 collector goroutine 周期采集并写入缓存；HTTP 请求只读缓存（Phase 6 要求）。

---

## 2. 目录与配置

| 路径 | 权限 | 用途 |
|---|---|---|
| `~/.config/mymac/` | 0700 | 配置根目录（白名单，清理器永不删） |
| `~/.config/mymac/env.json` | 0600 | 环境变量单一数据源 |
| `~/.config/mymac/env.sh` | 0600 | 由 env.json 生成的 `export` 文件 |
| `~/.config/mymac/env-apply.sh` | 0700 | 登录时重放 `launchctl setenv` |
| `~/.config/mymac/backup/` | 0700 | `.zshenv` 等被改文件的备份，保留最近 10 份 |
| `~/.config/mymac/audit.log` | 0600 | JSONL 审计日志 |
| `~/.config/mymac/mymac.lock` | 0600 | 单实例文件锁，内含运行实例的 pid 与地址 |
| `~/.config/mymac/icons/` | 0700 | icns→png 转换缓存 |
| `~/Library/LaunchAgents/com.mymac.env.plist` | 0644 | 登录时重放 launchd 环境 |

---

## 3. HTTP API 设计

Base：`http://127.0.0.1:8888`（端口固定，`--port` 可改）

### 3.1 鉴权与安全

服务只监听回环地址，本机自用不再需要令牌，防护靠三道请求校验：

- **Host 校验**：必须等于 `127.0.0.1:<port>` 或 `localhost:<port>`（防 DNS rebinding）。
- **Origin 校验**：存在时必须等于 `http://127.0.0.1:<port>` 或 `http://localhost:<port>`（防 CSRF）。
- **Fetch Metadata 校验**：`Sec-Fetch-Site` 为 `cross-site` 的请求一律拒绝（防不带 Origin 的跨站请求）。
- 监听仅 `127.0.0.1`（不监听 `0.0.0.0`）。
- **单实例**：`~/.config/mymac/mymac.lock` 加 flock 独占锁，第二个实例打印已有实例的地址后退出；
  进程被杀时内核自动释放锁，不会留下死锁。

### 3.2 端点

```
GET    /                       页面
GET    /static/*               静态资源（无鉴权，无敏感数据）
GET    /api/health             {version, uptime, pid, port}

# 权限
GET    /api/perm/status        每项权限状态 + 开启指引
POST   /api/perm/open          {target:"fda"|"automation"|"admin"} 打开系统设置
POST   /api/perm/test          {target} 触发一次探测

# 环境变量
GET    /api/env/vars           按来源分组返回全部变量
POST   /api/env/vars           创建
PATCH  /api/env/vars/{name}    改值/备注/启用状态
DELETE /api/env/vars/{name}    删除（连带清理 launchd）
POST   /api/env/vars/{name}/apply   重新 apply（launchctl setenv + 回读校验）
GET    /api/env/path           PATH 专用编辑器数据
POST   /api/env/path           保存 PATH 片段
GET    /api/env/export?format=json|env
POST   /api/env/import/preview 预览（不落盘）
POST   /api/env/import         应用
GET    /api/env/backups        备份列表
POST   /api/env/restore        恢复某个备份
GET    /api/env/status         标记块健康检查（是否被手动破坏）

# 进程
GET    /api/procs              列表（读缓存）
GET    /api/procs/stream       SSE 实时流 ?interval=1000|2000|5000
GET    /api/procs/{pid}        详情（含 footprint、lsof、启动时环境）
POST   /api/procs/{pid}/signal {sig:TERM|KILL|STOP|CONT}
POST   /api/procs/{pid}/renice {nice}
GET    /api/procs/{pid}/sample  线程栈采样（sample，可选）
GET    /api/procs/snapshot     导出 JSON
GET    /api/system/overview     CPU/内存/磁盘/网络/电池

# 应用卸载
GET    /api/apps               已安装应用列表
GET    /api/apps/{id}/residual 残留项（带大小/路径/依据/风险）
POST   /api/apps/uninstall     {bundleID, items[], dryRun}
GET    /api/apps/icon?id=...   图标（png，路径白名单校验）

# 清理
GET    /api/clean/scan          扫描结果
POST   /api/clean/execute       {items[], dryRun}

# 审计
GET    /api/audit?limit=200     最近审计记录
```

### 3.3 统一错误格式

```json
{ "error": { "kind": "permission_denied|sip_protected|process_gone|file_busy|not_found|invalid_request|internal",
             "message": "人类可读", "detail": "原始错误" } }
```

### 3.4 接口描述

接口清单是**一份表**（`internal/api/ops.go`），路由从这张表注册，OpenAPI 描述也从这张表生成：

- 表里的一条记录 = 一个方法 + 一个路径 + 参数 + 请求体类型 + 响应体类型 + 说明文字。
- `internal/api/types.go` 里的请求/响应类型同时被处理器和描述使用，字段名不会对不上。
- `internal/api/schema.go` 用反射把 Go 类型变成 JSON Schema：命名类型变成 `components/schemas`，
  内嵌结构体按 `encoding/json` 的规则摊平，闭集（`errs.Kind` 等）写成 `enum`。
- 描述用 OpenAPI 3.0.3，同时输出 JSON 和 YAML；JSON 由标准库写出，YAML 由一个只覆盖本项目
  用到的结构的写出器生成（`internal/api/encoding.go`），不引第三方依赖。
- 端点在 `GET /api/openapi.json`，可读的文档页在 `GET /api/docs`（自带页面，不联网）。
  同一份描述也提交在 `docs/openapi.json` 与 `docs/openapi.yaml`，`make openapi` 重新生成。

**为什么不用代码注解生成**：注解式生成器（swaggo 之类）要么引依赖，要么在编译期跑
外部工具；本项目要求单二进制、零第三方依赖，接口也就四十来个，一张表就够。
表驱动还带来一个额外好处：路由和描述不可能对不上，测试会直接失败而不是等到有人写文档。

---

## 4. 核心数据结构

```go
// internal/envs
type Store struct {
    Version   int       `json:"version"`
    UpdatedAt time.Time `json:"updatedAt"`
    Vars      []Var     `json:"vars"`
    PathCfg   PathCfg   `json:"path"`
}

type Var struct {
    Name        string    `json:"name"`
    Value       string    `json:"value"`
    Note        string    `json:"note,omitempty"`
    Enabled     bool      `json:"enabled"`
    Scope       Scope     `json:"scope"`        // ScopeUser | ScopePath
    Sensitive   bool      `json:"sensitive"`    // 名称含 KEY/TOKEN/SECRET/PASSWORD...
    CreatedAt   time.Time `json:"createdAt"`
    UpdatedAt   time.Time `json:"updatedAt"`
    LastApplied time.Time `json:"lastApplied,omitempty"`
}

type PathCfg struct {
    Enabled bool     `json:"enabled"`
    Prepend []string `json:"prepend"`
    Append  []string `json:"append"`
}

// 列表响应（按来源分组）
type Snapshot struct {
    Managed   []ManagedVar   `json:"managed"`     // mymac 管理的
    Launchd   []LaunchdVar   `json:"launchd"`     // launchctl getenv，只读
    ShellFile []ShellExport  `json:"shellFiles"`  // ~/.zshenv 等解析，只读
    Self      []SelfVar      `json:"self"`        // 当前进程环境，只读
    Warnings  []string       `json:"warnings"`
}

type ManagedVar struct {
    Var
    ShellApplied bool   `json:"shellApplied"`  // env.sh 已写入
    LaunchdValue string `json:"launchdValue"`  // launchctl getenv 回读
    LaunchdSet   bool   `json:"launchdSet"`    // 回读值 == 期望值
    Conflicts    []Conflict `json:"conflicts"`
}

type Conflict struct {
    Source string `json:"source"` // ".zshrc:12" / "launchd" / "shellFile"
    Value  string `json:"value"`
    Wins   bool   `json:"wins"`   // true = 该来源会覆盖 mymac
}

// internal/procs
type Process struct {
    PID       int      `json:"pid"`
    PPID      int      `json:"ppid"`
    Name      string   `json:"name"`
    Path      string   `json:"path"`
    Args      []string `json:"args,omitempty"`
    User      string   `json:"user"`
    CPU       float64  `json:"cpu"`
    Mem       uint64   `json:"mem"`         // RSS
    Footprint uint64   `json:"footprint,omitempty"`
    Threads   int32    `json:"threads"`
    FDs       int32    `json:"fds"`
    Status    string   `json:"status"`
    StartTime int64    `json:"startTime"`
    Arch      string   `json:"arch"`        // arm64 | x86_64 | universal
    Rosetta   bool     `json:"rosetta"`
    AppID     string   `json:"appId,omitempty"`  // bundle id（按 .app 分组）
    AppName   string   `json:"appName,omitempty"`
    Protected bool     `json:"protected"`
    Signer    string   `json:"signer,omitempty"`
    Ports     []Port   `json:"ports,omitempty"`
    NetIn     uint64   `json:"netIn"`       // 本采样周期增量
    NetOut    uint64   `json:"netOut"`
    State     string   `json:"state"`       // running/sleeping/stopped
}

// internal/apps
type App struct {
    BundleID   string `json:"bundleId"`
    Name       string `json:"name"`
    Version    string `json:"version"`
    Path       string `json:"path"`
    Icon       string `json:"icon,omitempty"`
    Size       uint64 `json:"size"`
    IsMAS      bool   `json:"isMAS"`
    IsIOS      bool   `json:"isIOS"`
    IsApple    bool   `json:"isApple"`
    IsSystem   bool   `json:"isSystem"`
    Developer  string `json:"developer,omitempty"`
    TeamID     string `json:"teamId,omitempty"`
    Running    bool   `json:"running"`
}

type Residual struct {
    Path       string `json:"path"`
    Kind       string `json:"kind"`      // appSupport|cache|prefs|logs|container|groupContainer|launchAgent|helper|...
    Size       uint64 `json:"size"`
    MatchKind  string `json:"matchKind"` // exactBundleId|exactName|fuzzyVendor
    MatchedBy  string `json:"matchedBy"`
    Risk       string `json:"risk"`      // low|medium|high
    Selected   bool   `json:"selected"`
}

// internal/audit
type Entry struct {
    TS      time.Time `json:"ts"`
    Action  string    `json:"action"`  // env.set / env.delete / proc.kill / file.trash / ...
    Target  string    `json:"target"`
    Detail  string    `json:"detail,omitempty"`
    Result  string    `json:"result"`  // ok|denied|failed
    PID     int       `json:"pid"`
    Elapsed int64     `json:"elapsedMs,omitempty"`
}
```

---

## 5. 各 Phase 关键实现点

### Phase 1 权限
- **FDA 探测**：读 `~/Library/Safari`（列目录）→ `EPERM` 即未授权。同时探测 `~/Library/Mail` 作为第二路径。
- **自动化权限**：执行 `osascript -e 'tell application "System Events" to return 1'` 探测；首次失败提示去「系统设置 → 隐私与安全性 → 自动化」。
- **提权封装**（统一入口 `perm.Elevate`）：
  ```
  osascript -e 'on run argv' -e 'do shell script (item 1 of argv) with administrator privileges' -e 'end run' -- "<cmd>"
  ```
  用 argv 传参避免引号地狱；提权前弹确认框 + 写审计日志。
- **审计日志**：JSONL，append + `Sync()`，0600。

### Phase 2 环境变量
- **标记块读写**：`# >>> mymac env >>>` / `# <<< mymac env <<<`，只替换块内内容，块外一字不动。
- **原子写**：同目录 `.<name>.tmp.<pid>` → `fsync` → `os.Rename` → `fsync(dir)`。
- **env.sh 生成**：只含已启用的变量，`export KEY='<value>'`，单引号转义 `'` → `'\''`，换行保留在引号内。
- **zshenv 注入**：
  ```
  # >>> mymac env >>>
  [ -f "$HOME/.config/mymac/env.sh" ] && . "$HOME/.config/mymac/env.sh"
  # <<< mymac env <<<
  ```
- **立即生效**：
  - 新增/改值 → 写 env.sh → `launchctl setenv KEY VALUE` → `launchctl getenv KEY` 回读校验。
  - 删除 → `launchctl unsetenv KEY` + 从 env.json / env.sh 移除。
  - 登录持久化 → 生成 `~/Library/LaunchAgents/com.mymac.env.plist`（RunAtLoad 调 `env-apply.sh`）→ `launchctl bootstrap gui/$UID`；卸载用 `bootout gui/$UID/com.mymac.env`。
- **状态三连**：Shell 文件已写入 / launchd 已设置（回读一致）/ 当前生效值。
- **PATH 专项**：单独 `PathCfg`（prepend/append 片段列表），应用时基于当前 PATH 计算去重后的完整值写入 env.sh，不整体覆盖。
- **敏感变量**：名称匹配 `KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH` → 默认遮罩；env.json 0600。
- **保护名单**：`HOME USER LOGNAME SHELL PATH TMPDIR PWD OLDPWD TERM LANG LC_* HOSTNAME UID GID EDITOR VISUAL` 禁止删除，修改需强确认。
- **不碰** `/etc/paths`、`/etc/paths.d`。
- **导入导出**：JSON / `.env` 两种格式；导入先走 preview 返回 diff，确认后才应用。

### Phase 3 进程
- **采集**：`gopsutil/v4`（CPU/RSS/线程/fd/状态/启动时间/用户），进程树按 PPID 构建。
- **架构**：`lipo -archs <exec path>`（按路径缓存）；仅 x86_64 → 标记 Rosetta。
- **端口**：`lsof -nP -i` 解析，按 PID 聚合。
- **网络速率**：`nettop -P -L 1` 两次采样求差（累计值差）。
- **footprint**：`footprint -p <pid>` 仅详情页按需调用。
- **能效核/性能核**：`powermetrics` 需 root → 无提权时显示"需管理员权限"。
- **系统总览**：`vm_stat` + `memory_pressure` + `statfs` + `netstat -ib` + `ioreg AppleSmartBattery`。
- **SSE**：`/api/procs/stream`，间隔 1/2/5s 可调，可暂停；页面不可见（`document.hidden`）自动降频到 5s。
- **信号**：`syscall.Kill`；renice 用 `syscall.Setpriority`（提权值走 osascript）。
- **黑名单**：`launchd kernel_task WindowServer loginwindow ...`，二次确认；SIP 保护显示「受保护」不报错。
- **阈值告警**：`osascript -e 'display notification ...'`。

### Phase 4 应用卸载
- 扫描 `/Applications`、`~/Applications`、`/Applications/Utilities`。
- `plutil -convert json -o -` 解析 Info.plist → 取 bundle id / 名称 / 版本 / 图标。
- MAS：`Contents/_MASReceipt` 存在；iOS：`CFBundleSupportedPlatforms` 含 `iPhoneOS` 或有 `UIDeviceFamily`。
- 残留按 bundle id 精确 + 名称模糊 + 开发者前缀，来源覆盖 `~/Library/{Application Support,Caches,Preferences,Logs,Saved Application State,WebKit,HTTPStorages,Cookies,Application Scripts}`、`~/Library/Containers`、`~/Library/Group Containers`（默认不勾选）、`~/Library/LaunchAgents`、`/Library/LaunchAgents`、`/Library/LaunchDaemons`、`/Library/PrivilegedHelperTools`。
- **删除一律走 Finder 废纸篓**：
  ```
  osascript -e 'tell application "Finder" to delete POSIX file "<path>"'
  ```
  永不 `rm`。
- 禁止卸载系统应用 / Apple 签名应用（`codesign` Authority 含 Apple）。

### Phase 5 系统清理
- `~/Library/Caches`、`~/Library/Logs`（读不到 → 提示，不静默跳过）。
- 开发者缓存：Xcode DerivedData/Archives/DeviceSupport、CoreSimulator、Go build/mod、npm/pnpm/yarn、Homebrew、pip、Gradle/Maven、Docker（只提示不删）。
- Time Machine 本地快照、系统更新残留：只显示。
- 大文件：walk + `mdfind` 辅助；重复文件：size → 头尾 4KB 部分哈希 → 全量 sha256。
- 白名单：`~/.config/mymac/` 及其子路径永不可删。
- **APFS**：用 allocated size（`stat.Blocks * 512`），不用 `st_size`，避免 clone/稀疏文件虚高。

### Phase 6/7
- 错误分类统一到 `kind` 字段。
- 1000+ 进程采集 < 500ms：collector goroutine 周期采集 + 缓存，API 只读缓存；前端虚拟滚动。
- 单元测试：plist 解析、残留匹配、大小计算、白/黑名单、**env 转义与标记块读写**。
- 集成测试：临时 HOME + 假 `.app` + **mock launchctl**（接口注入 + 假二进制），绝不碰真实数据。

---

## 6. 前端结构

- 左侧导航：应用卸载 / 系统清理 / 进程管理 / 环境变量 / 权限。
- 样式：`prefers-color-scheme` 深浅色 + CSS 变量；毛玻璃 `backdrop-filter: blur(20px) saturate(180%)`；大圆角 16–22px。
- 进程表格：虚拟滚动（自研，~80 行代码）；列可配置；SSE 增量更新。
- 环境变量：按来源分组的 tab；敏感值遮罩 + 「显示」按钮；三色状态徽章（Shell ✓ / launchd ✓ / 当前值）。

---

## 7. 里程碑

| Phase | 交付 | 验收 |
|---|---|---|
| 0 | 骨架 + 防护 + 毛玻璃 UI + 导航 | 浏览器打开无控制台错误；伪造 Host / Origin / Sec-Fetch-Site 的请求被拒 |
| 1 | 权限页 + 提权封装 + 审计 | 权限页正确显示本机状态；提权/删除产生 audit 记录 |
| 2 | 环境变量全量 | 验收标准见 todo.md Phase 2 |
| 3 | 进程管理 | 1000+ 进程 < 500ms；SSE 正常；信号生效 |
| 4 | 应用卸载 | 扫描/残留/废纸篓删除全通 |
| 5 | 系统清理 | 扫描准、删除走废纸篓、空间估算不虚高 |
| 6 | 性能与错误分类 | 压测达标 |
| 7 | 测试 + 打包 | `go test ./...` 全绿；mymac.app 可运行 |

---

## 8. 实现记录（与初稿的偏差及原因）

这一节记录**实现过程中发现的、与上文初稿不同的地方**，以及改动的原因。

### 8.1 PATH 改成动态形式

初稿写的是「把合并后的完整 PATH 字面量写进 `env.sh`」。实现时改为：

```sh
export PATH='/opt/homebrew/bin':"$PATH"
export PATH="$PATH":'/custom/bin'
```

原因：字面量会把当时的 PATH 写死，用户之后装的工具就不在 PATH 里了，也违背 todo「不直接整体覆盖，只追加/前置片段」的要求。launchd 的 PATH 则**完全不碰**。合并后的完整值仍然在界面上显示，仅作预览。

### 8.2 `launchctl print` 的多行值

`launchctl print gui/$UID` 会把含换行的值拆成多行打印：

```
	environment = {
		MYMAC_SPECIAL => a b"c'd $HOME `id` 中文
line2
		SSH_AUTH_SOCK => /var/run/...
	}
```

初稿的解析器按行取 `KEY => VALUE`，会把多行值截断。改成：不匹配 `KEY => VALUE` 且不是 `}` 的行视为上一条值的续行。

另外 `launchctl getenv` 会在值后面**追加恰好一个** `\n`，所以只能去掉一个而不是全部 `TrimRight`，否则值本身以换行结尾时会丢字符。

### 8.3 进程采集的性能改造

实测：gopsutil 的 `Status()` 在 macOS 上为**每个进程**跑一次命令，820 个进程要 **2.7 秒**。改成一次 `ps -axo pid=,stat=` 拿全表。

| 优化项 | 之前 | 之后 |
|---|---|---|
| 一次完整采集 | 2793 ms | **82–89 ms**（820+ 进程） |
| `lipo` 架构判定 | 采集循环内同步调用 | 后台预热，采集只读缓存 |
| `plutil` 读 Info.plist | 采集循环内同步调用 | 后台预热，采集只读缓存 |
| `lsof` / `nettop` | 采集循环内同步调用 | 后台刷新，采集只读缓存 |

`lsof` 的列位置会因 NODE 列为空而偏移，解析器改为**找协议字段**（TCP/UDP/UNIX）而不是数列。

### 8.4 废纸篓的授权处理

第一次调用 Finder 时 macOS 会弹自动化授权对话框，调用会一直阻塞到用户点「好」为止。实测无响应会挂 120 秒。

改法：
- `trash.Check()` 用 8 秒超时探测，结果**缓存 2 分钟**。
- 探测失败 → 直接走 `~/.Trash` 降级路径，不去等 Finder 的 25 秒超时。
- 降级路径同样可恢复，界面上明确说明「文件会被移入 ~/.Trash，可随时恢复」。

### 8.5 Apple 签名判定

初稿按 `Authority` 链里是否出现 "Apple" 判断。实测**每个** Developer ID 应用都会链到 `Apple Root CA`，会把 54 个应用里的 50 个误判成 Apple 应用。

改为只看**叶子** Authority：系统应用的叶子是 `Software Signing` 或以 `Apple ` 开头；另外 `com.apple.*` 的 bundle id 也算系统应用。修正后 54 个应用里只有 4 个是 Apple 签名。

### 8.6 补充实现

- 进程详情里的「启动时环境变量」用 `ps eww -p <pid>` 实现（本机实测可用），敏感变量脱敏。
- 阈值告警：规则在后台每 5 秒评估，只有**持续**超过阈值时长才发 `osascript display notification`，避免瞬时尖峰误报。
- 重复文件检测的三级过滤：大小 → 头尾 4 KiB 部分哈希 → 完整 SHA-256。部分哈希**故意不覆盖文件中部**，它是廉价过滤器，最终判定靠全量哈希。

### 8.7 去掉令牌、固定端口、菜单栏

初稿的 `?t=<token>` 一次性令牌在实测中成了负担：地址要带一串随机字符、每换一个实例旧书签就失效、
终端里启动后令牌很容易丢（stdout 重定向到文件时只留最后一次）。本机自用场景下这个令牌没有实际价值，
所以**整体删掉**，换成三道请求校验顶住真正的风险（浏览器里的恶意页面打本机接口）：

| | 初稿 | 现在 |
|---|---|---|
| 访问凭证 | 32 字节随机 token + cookie | 无 |
| 端口 | `net.Listen(":0")` 随机端口 | 固定 `8888`，占用即报错 |
| 多实例 | 静默各起一个，随机端口 | flock 单实例锁，第二个退出并打印已有地址 |
| 入口 | 终端启动后手动找地址 | 菜单栏图标 |

三个关键取舍：

- **端口固定成 8888**：随机端口的代价是「找不到地址」和「第二个实例不报错」。改成固定端口后，
  地址可以背下来；端口被占时明确报 `端口 8888 已被占用` 并退出，而不是换个端口继续跑。
- **单实例用 flock 而不是 pid 文件**：pid 文件在进程被 `kill -9` 后会留下孤儿，需要额外判断 pid 是否存活；
  flock 由内核在进程退出时自动释放，锁文件本身可以长期存在。
- **菜单栏用 cgo 直接调 AppKit**：没有可用的第三方库（离线环境拉不到 `systray`），所以自己写
  `NSStatusItem` + `NSMenu`。Go 侧通过 `//export` 回调拿「打开界面 / 退出」两个动作。
  Cocoa 事件循环必须在主线程跑，所以 `main` 里 `runtime.LockOSThread()`，HTTP 服务和信号处理都挪到 goroutine。

没有图形会话时（SSH 场景）`NSStatusItem` 拿不到窗口，此时菜单栏判定为不可用，程序退化成
「只跑 HTTP 服务 + 等信号」，可以用 `--no-menubar` 显式关闭。

### 8.8 菜单栏图标不出现：ARC 踩坑

第一次实现菜单栏后，图标**永远不出现**，但 `statusItemWithLength:` 返回的对象非空、
`button.window` 也在、`isVisible` 还是 `YES`——所有「看起来正常」的检查都过了。

定位过程：

1. 用 `CGWindowListCopyWindowInfo` 列出屏幕上所有窗口，发现菜单栏图标全部由 **ControlCenter** 托管
   （`layer=25`，`y=0`，高 33），应用自己并不持有窗口——我的图标根本不在这个列表里。
2. 写了一个纯 ObjC 的最小程序做对照，**它能出来**（窗口列表里多了一个 `1117,0,35,33`）。
   两者唯一的差别是 ARC。
3. 原因：`statusItemWithLength:` 返回的对象是 **autoreleased** 的。`.m` 文件默认是 MRC，
   赋给文件作用域的 static 变量**不会 retain**，`@autoreleasepool` 一 drain 它就被释放，
   菜单栏项目当场消失。之后再访问它就是 use-after-free，探针直接 SIGSEGV。
4. 修法：`.m` 开 ARC（`#cgo CFLAGS: -x objective-c -fobjc-arc`）。
   `#pragma clang arc` 在这里**不生效**，只有命令行开关管用。
   另外加了一条编译期断言 `#if !__has_feature(objc_arc) #error ...`，
   以后谁把开关删了会直接编译失败，而不是运行时图标默默不见。

> 教训：Objective-C 里的「全局变量持有对象」这件事，ARC 与否的行为完全不同。
> 用 static 存 AppKit 对象时，不开 ARC 就必须自己 `retain`。

### 8.9 卸载改成「先列清单，后统一删除」

原来点「卸载」直接弹出该应用的残留面板，里面一个「移到废纸篓」按钮就把它删了。
用户要的是**批量**：点「卸载」把应用加进下面的卸载列表，最后统一「移到废纸篓」。

改法：

- 前端把 `appState.uninstall` 做成 `Map<bundleID, {group, rows}>`，点「卸载」只往里加，不碰磁盘。
- 面板顶部一个「移到废纸篓 (N)」，把所有应用勾中的行**合成一次请求**发出去——
  Finder 自动化也只弹一次，而不是每个应用弹一次。
- 后端 `/api/apps/uninstall` 增加 `bundleIDs` 字段（老的单数 `bundleID` 继续兼容），
  审计日志的 target 记全部 bundle id。
- 删完的应用自动出列，失败的留在列表里可以重试。

### 8.10 启动要 4.2 秒

菜单栏图标 4 秒后才出现。原因是 `Collector.StartBackground()` 在启动路径上**同步**跑了三次采集：
`Collect` + `lsof` + `nettop`，而 `nettop` 一次要 5 秒。

这跟「启动就绪后立刻填满缓存」的初衷冲突：填缓存是为了第一个页面就有数据，
但没必要让服务器等它。改成在 goroutine 里填，启动耗时降到 **0.6 秒**，
页面上照旧是先空后有（前端本来就在轮询）。

### 8.11 界面：排序、详情抽屉、主题

三件事一起做，因为它们共用同一套表格基础设施。

**所有表格都能排序。** 之前只有应用表格能排（点表头），其它表格要么没有排序，
要么用一个下拉框。统一成点表头：新增 `ui.js` 里的三个小工具——

| 函数 | 作用 |
|---|---|
| `sortHeader(state, cols)` | 按列定义渲染表头，带 ▴ / ▾ 箭头 |
| `sortRows(rows, state, accessors)` | 按当前列排序，文本按文本比、数字按数字比 |
| `bindSort(sel, state, render)` | 绑表头点击，第一次升序、再点降序 |

每张表各自声明自己的列和取值函数（`procCols` / `procAccessors` 这样），
表头定义和排序逻辑就分开了：加一列只要改这两处。

**详情改用抽屉。** 原来详情是表格下面多出来的一个面板，页面会被撑长，表格和详情
也对不上号。改成右侧滑出的抽屉（`openDrawer` / `closeDrawer`）：
表格留在原地，可以一边看详情一边对照。

抽屉统一了所有表格的详情入口：**点整行**就打开，行动作按钮不受影响
（按钮的点击会 `stopPropagation`）。进程、端口、应用、启动项、环境变量、
清理项、审计日志都有详情。

抽屉的坑：**异步的详情要等 API 回来才打开**，所以不能在点击的同一帧就断言「没打开」。
另外切换页面时要把抽屉关掉（`showView` 里调 `closeDrawer`），否则详情会跟着跑到别的页面。

**主题。** 之前是纯 `prefers-color-scheme`，只能跟随系统。现在左下角可以选
「跟随系统 / 浅色 / 深色」，选择存在 `localStorage`。

实现上有个关键点：CSS 里**不再有** `@media (prefers-color-scheme: dark)`，
只有 `:root[data-theme="dark"]`。「跟随系统」在 JS 里解析成 light / dark 再写到
`data-theme` 上，并且监听 `matchMedia` 的变化。这样深色的变量只写一份，
不会出现「系统是深色但用户选了浅色」时两套变量打架的情况。

### 8.12 进程名称可能为空

改完进程表才发现名称列有一片空白。查下来是 gopsutil 对少数系统守护进程
（`IOMFB_bics_daemon` 这类）返回的名字就是空字符串。

空名字不只是难看：保护规则（`IsProtected`）是按名字匹配的，名字空了规则就失效。
所以加了回退：名字为空时用可执行文件名（`processName(name, path)`），
这个函数单独抽出来做了单元测试。

顺带发现「进程管理」页面顶部的统计卡片是 CPU / 内存 / 磁盘 / 网络 / 电池——
和仪表盘一模一样，对进程本身一个字都没说。换成进程自己的数字：
进程总数（用户 / 系统）、线程总数、文件描述符、僵尸进程、Rosetta 进程。

### 8.13 mymac 不能对自己发信号

用户在进程管理里给 mymac 自己发了 SIGSTOP，结果整个工具冻住了：
菜单栏图标还在（系统只按「宿主进程还在不在」决定图标在不在），
端口还占着（socket 没关），但 HTTP 不响应了，单实例锁也还持着，
再起一个实例会被挡住——变成一个「看起来活着、其实已经死了」的服务。

端口那条路本来就有保护（`hardGuardReason` 里的 `pid == self`），
**进程管理这条路没有**：`Signal()` 只挡了 `pid <= 1`。

补了三层：

| 层 | 做法 |
|---|---|
| 后端 | `procs.Signal()` 和 `procs.Renice()` 拒绝目标是自己（`IsSelf(pid)`） |
| 数据 | 进程表和端口表都带上 `self` 标记（`json:"self"`） |
| 前端 | 自己那一行标「本进程」；进程抽屉里信号按钮整个去掉，端口表里停止 / 强杀 / 重启置灰 |

后端这条是必须的：前端只是提示，真正的闸门在后端。
前端那层是为了让用户**看不出来自己点不了**——直接不显示按钮，
再配一句说明「冻住后端口还占着、图标还在，但服务已经不响应了」。

### 8.14 应用列表一直闪

刚打开 mymac 时，「应用卸载」列表会不停地自己刷新。冷启动实测给出了答案：

| 时间 | 状态 |
|---|---|
| 0 – 19.3 s | 51 个应用，**大小全部未知** |
| 21.7 s | 未知 17（测量开始） |
| 27.4 s | 未知 0（测完） |

三个原因叠在一起：

1. **预热器第一次要等 20 秒**。`StartSizeWarmer` 是 `time.After(20 * time.Second)` 才跑第一次，
   冷启动时列表里一个大小都没有。
2. **前端的轮询每 1.5 秒重建整张表**。`renderApps()` 在「还有应用没测出大小」时会一直轮询，
   而且每轮都用 `innerHTML` 把整张表换掉——可这二十秒里内容一模一样，纯粹是闪屏。
3. **手动重扫的测量被掐断**。`go s.apps.WarmSizes(r.Context())` 用了请求的 context，
   请求一结束 context 就被取消，测量跑到一半就没了。这正是本文件 5.6 节写过的坑，
   实现时漏了。

改法：

- `Scan()` 之后立刻 `warmMissing()` 测一遍，不用等预热器的那 20 秒；
- 后台测量一律用 `context.WithoutCancel(ctx)`，不再被请求的收尾掐断；
- 前端轮询时先算一个签名（应用数 + 已测出大小数 + 大小合计），**签名没变就不重绘**。

效果：整表重建 18 次 → **5 次**，全部集中在前 5.7 秒，之后一次都不重绘；
大小测完 27 s → 12 s（剩下的是 `du` 实测 51 个 app 的固有耗时，省不掉）。

> 教训：轮询本身不是问题，**没有意义地重绘**才是。前端「拿到新数据就整个重画」的习惯，
> 在有实时数据的地方就是持续闪屏。要么让服务端把「有没有变」说清楚，
> 要么前端自己比一下再决定要不要动 DOM。
