# mymac

macOS 清理 + 进程管理 + 环境变量管理工具。Go 后端，本地 Web 界面。

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

- **仪表盘**：CPU / 内存 / 磁盘 / 电池环状图 + 系统概况。
- **应用卸载**：扫描已安装的应用，找出残留文件（配置、缓存、容器、启动项、特权辅助工具），一律移入废纸篓。
- **系统清理**：用户缓存、日志、开发者缓存（Xcode / Go / npm / Homebrew / Gradle…），大文件与重复文件查找。
- **进程管理**：实时进程表（虚拟滚动），进程树、架构与 Rosetta 识别、端口映射、网络速率、footprint、线程栈采样、信号与 renice、阈值告警。
- **端口管理**：监听端口 ↔ 进程映射，规则系统，停止 / 强杀 / 重启（带 PID 复用防护）。
- **启动程序**：LaunchAgent / LaunchDaemon / 旧式登录项的启用、禁用与删除。
- **环境变量**：三层模型（Shell / launchd / 已运行进程），保存即对**新终端**和**新启动的 GUI 应用**生效，登录后自动恢复。
- **菜单栏**：常驻在菜单栏，点开就是界面入口和退出菜单。

单二进制、不联网、只监听 `127.0.0.1`。菜单栏图标用 cgo + AppKit 实现（仅 macOS，无第三方依赖）。

---

## 快速开始

```bash
make build     # 编译到 build/mymac
make run       # 直接运行并自动打开浏览器
make test      # 单元测试
make lint      # go vet + gofmt 检查
make bundle    # 打包成 build/mymac.app
make openapi   # 重新生成 docs/ 里的接口描述
```

启动后会在 `127.0.0.1:8888` 上监听，并自动打开浏览器。地址固定，不需要任何令牌，直接访问 <http://127.0.0.1:8888> 即可。

- **端口固定**：默认 `8888`，被占用时直接报错退出，不会偷偷换端口。可以用 `--port` 换一个。
- **单实例**：`~/.config/mymac/mymac.lock` 上加文件锁，第二个实例会提示已在运行的地址后退出。
- **菜单栏**：启动后右上角出现一个扳手图标，菜单里有「打开 mymac 界面」「退出 mymac」和当前地址。不想显示可以用 `--no-menubar`。

打包成 `.app` 后可以放进「应用程序」：

```bash
make bundle
cp -R build/mymac.app /Applications/
```

自用未签名版本第一次打开可能提示「无法验证开发者」，用下面这条去掉隔离标记：

```bash
xattr -dr com.apple.quarantine /Applications/mymac.app
```

---

## 权限

mymac 需要三项 macOS 隐私权限，**权限页**（左侧导航「权限」）会实时显示每一项的状态和开启方法。

| 权限 | 用途 | 开启路径 |
|---|---|---|
| 完全磁盘访问权限 | 读取 `~/Library/Safari` 等受保护目录；扫描用户缓存、残留文件 | 系统设置 → 隐私与安全性 → 完全磁盘访问权限 → 添加并**勾选**启动 mymac 的程序 |
| 自动化权限 | 驱动 Finder 把文件移入废纸篓 | 系统设置 → 隐私与安全性 → 自动化 → 允许 mymac 控制 Finder |
| 管理员权限 | 删除 `/Library` 下的文件、`renice` 降优先级、`powermetrics` | 操作时弹系统密码框，只在确实需要时出现 |

> **重要**：授权是给「启动 mymac 的那个程序」的。
> - 从终端 `make run` 启动 → 需要给**终端**（Terminal / iTerm / Ghostty）授权。
> - 从 `mymac.app` 启动 → 需要给 **mymac.app** 授权。
>
> 这两者在系统设置里是两条独立记录，换一种方式启动就要重新授权。

---

## 安全模型

mymac 只能操作你自己的机器，但仍然做了几层防护：

1. **只监听回环地址**：绑定 `127.0.0.1`，不监听 `0.0.0.0`，局域网内不可达。
2. **Host 校验**：请求的 `Host` 必须是 `127.0.0.1:<端口>` 或 `localhost:<端口>`。这一条挡住了 DNS rebinding——攻击者把域名解析到 127.0.0.1 时，浏览器发出的 Host 仍然是他的域名。
3. **Origin 校验**：带 `Origin` 头的请求，来源必须同源，挡住跨站请求伪造。
4. **Fetch Metadata 校验**：浏览器发的 `Sec-Fetch-Site` 标记为 `cross-site` 的请求一律拒绝，堵住那些不带 `Origin` 头的跨站请求。
5. **单实例**：文件锁保证同一时间只有一个 mymac 在跑，第二个实例直接退出。

> 本机自用不再需要令牌：服务只监听回环地址，浏览器里的恶意页面够不着本机的 `127.0.0.1` 接口——上面三条校验就是为此准备的。

6. **永不直接删除**：所有删除都走 Finder 移入废纸篓，误删可以恢复。
7. **保护路径**：`/`、`/System`、`/usr`、`/bin`、`/sbin`、`/etc`、`/var`、`/opt`、`/private/var/db` 以及 `~/.config/mymac/` 一律拒绝删除。
8. **审计日志**：所有删除、杀进程、改环境变量的操作都写入 `~/.config/mymac/audit.log`（JSON Lines，0600）。敏感变量的值在日志里被脱敏。

---

## 环境变量：三层模型

macOS 上的「环境变量」分三层，mymac 明确区分它们，不含糊其辞：

| 层 | 影响范围 | 生效方式 | mymac 的做法 |
|---|---|---|---|
| **Shell 层** | 之后新开的终端 | 写配置文件 | 生成 `~/.config/mymac/env.sh`，在 `~/.zshenv` 的标记块里 source 它 |
| **launchd 层** | 之后启动的 GUI 应用（从 Dock 启动的 Xcode、IDEA、VS Code 等） | `launchctl setenv` 立即生效 | 保存后立刻执行 `launchctl setenv`，并用 `launchctl getenv` **回读校验** |
| **已运行进程** | 无法从外部修改 | 只能重启或手动 `source` | 界面明确列出「N 个应用需重启」 |

### 「立即生效」到底意味着什么

保存一个变量之后：

- ✅ **新开的终端**立刻能读到（`~/.zshenv` 在 zsh 启动时读取，非交互 shell、IDE 内置终端、脚本同样生效）
- ✅ **新启动的 GUI 应用**立刻能读到（`launchctl setenv` 写入了 launchd 的 GUI domain）
- ❌ **已经打开的终端**不会变 → 执行 `source ~/.config/mymac/env.sh`
- ❌ **已经运行的应用**不会变 → 需要重启该应用

界面上的三列状态直接对应上面三层：

| Shell | launchd | 含义 |
|---|---|---|
| 已写入 | 已设置 | 一切正常 |
| 已写入 | 未设置 | 只对新终端生效，GUI 应用还读不到 |
| 未写入 | 已设置 | 只对新应用生效 |

### 登录后还能保持吗

可以。`launchctl setenv` 在重启后会丢失，所以 mymac 会创建一个登录项：

```
~/Library/LaunchAgents/com.mymac.env.plist
```

它在每次登录时运行 `~/.config/mymac/env-apply.sh`，把受管的变量重新写进 launchd。安装/卸载在「环境变量 → 登录代理」里。

### 文件布局

```
~/.config/mymac/
├── env.json            # 唯一数据源：变量名、值、备注、启用状态、创建/更新时间
├── env.sh              # 由 env.json 生成，只含 export 语句
├── env-apply.sh        # 登录时重放 launchctl setenv
├── audit.log           # 审计日志（JSON Lines）
├── backup/             # 改动前的备份，保留最近 10 份
└── icons/              # icns → png 转换缓存
```

`~/.zshenv` 里只有这一段，标记块之外的内容 mymac 永远不碰：

```sh
# >>> mymac env >>>
[ -f "$HOME/.config/mymac/env.sh" ] && . "$HOME/.config/mymac/env.sh"
# <<< mymac env <<<
```

### 值的转义

值用单引号包裹，单引号本身转义成 `'\''`。单引号内 shell 不做任何展开，所以空格、`$`、反引号、反斜杠、换行、中文都能原样保留。测试里有一条用例会真的用 `/bin/sh` source 生成的 `env.sh`，再逐字节比对读回来的值。

### PATH 是特例

PATH 不做整体覆盖，只管理**片段**：

```sh
export PATH='/opt/homebrew/bin':"$PATH"   # 前置
export PATH="$PATH":'/custom/bin'          # 后置
```

用这种动态写法，PATH 本身后续被别的程序改动也不会被写死。launchd 的 PATH 永远不会被 mymac 改写。

### 保护名单

`HOME`、`USER`、`LOGNAME`、`SHELL`、`PATH`、`TMPDIR`、`PWD`、`OLDPWD`、`TERM`、`LANG`、`LC_*`、`HOSTNAME`、`UID`、`GID`、`EDITOR`、`VISUAL`、`PS1`、`SSH_AUTH_SOCK` 禁止删除，修改需要强确认。`/etc/paths` 和 `/etc/paths.d` 完全不碰。

---

## 目录结构

```
cmd/mymac/          入口
internal/
  api/              接口清单、请求/响应类型、OpenAPI 描述生成
  apps/             应用扫描、Info.plist 解析、残留匹配
  audit/            审计日志
  cleaner/          缓存、日志、开发者缓存、大文件、重复文件、后台扫描任务
  config/           路径常量
  envs/             环境变量三层模型
  errs/             错误分类
  fsutil/           文件系统辅助（APFS 实际占用大小）
  instance/         单实例文件锁
  loginitems/       启动程序（LaunchAgent / LaunchDaemon / 登录项）
  menubar/          macOS 菜单栏图标（cgo + AppKit）
  perm/             权限探测与提权封装
  ports/            端口采集与进程操作（stop / kill / restart）
  procs/            进程采集与控制
  rules/            系统服务规则（端口 / 进程 / 路径 / 用户）
  server/           HTTP 路由、Host/Origin 校验、SSE
  sysinfo/          系统总览
  trash/            移入废纸篓
web/                前端资源（go:embed 打进二进制）
web/assets/ui.js    排序、详情抽屉、主题切换
tools/genicon/      图标生成器
tools/genopenapi/   生成 docs/ 里的接口描述
```

### 命令行参数

| 参数 | 说明 |
|---|---|
| `--port <n>` | 监听端口，默认 `8888`；被占用时报错退出 |
| `--no-browser` | 启动时不打开浏览器 |
| `--no-menubar` | 不显示菜单栏图标（无图形会话时用） |
| `--version` | 打印版本后退出 |

---

## 端口管理

端口视图把「监听端口 → 进程 → 资源占用」放在一张表里，同一 PID 的多个端口合并成一行。

- **规则系统**：内置规则列出 macOS 自己的端口（88 / 631 / 5000 / 5353 / 7000）和进程
  （`launchd`、`WindowServer`…），系统服务默认隐藏。用户白名单 > 用户黑名单 > 内置规则。
  规则文件在 `~/.config/mymac/port_rules.json`。
- **白名单只影响显示**，不会解除进程保护：被内置规则命中的进程依然不能被停止。
- **PID 复用防护**：所有操作都必须携带列表里的进程创建时间。发信号前会重新读取并校验，
  PID 被复用时返回 409 而不是误杀。
- **重启**会保留原来的命令行、工作目录和环境变量；读取不到任何一项就取消重启，
  不会把进程留成死的。
- **系统保护**：PID 1、内核线程、mymac 自身，以及命中规则的系统进程都拒绝操作。

---

## 启动程序

列出并管理随系统启动的后台项，**按程序分成一棵树**：一个应用常常带三四个启动项
（主程序、helper、Sparkle 更新器），点程序名左边的箭头可以折叠，
分支上的「**全部禁用**」一次关掉整组。

- 来源：`launchctl print-disabled`（启用/禁用状态）、`launchctl print`（已加载的服务）、
  `~/Library/LaunchAgents`、`/Library/LaunchAgents`、`/Library/LaunchDaemons`、
  以及 System Events 的旧式登录项。
- **细分控制还在**：每一行都有自己的「禁用 / 启用」，只想关掉其中一个也行。
- **禁用 / 启用**通过 `launchctl enable` / `disable` 立即生效：
  **正在运行的进程不会停**，但登录后不再启动。这个标记写在 launchd 的数据库里
  （`/var/db/com.apple.xpc.launchd/disabled.<uid>.plist`），**重启后依然有效**，
  除非程序自己在安装时重新启用。
- **删除** LaunchAgent 会把 plist 移入废纸篓，可随时恢复。
- 旧式登录项（System Events）不是 launchd 服务，`launchctl` 会照单全收却什么也不做，
  所以它们的「禁用」按钮是灰的，只能去「系统设置 → 通用 → 登录项」里移除。

### AI 风险分析

点工具栏的「**AI 风险分析**」，AI 会按「该不该被信任」给每个启动项评一个风险，
结果落在「风险」列（高 / 中 / 低，鼠标悬停看理由），高风险的项另有一张卡片提醒。

- 走的是本机的 **claude CLI**（Claude Code）：mymac 自己没有密钥也不联网，
  启动项的标签和路径不出这台机器。
- 只评**第三方**的启动项：系统项天生低风险，而且有几百个。
- 分析在后台跑（45 个项约两分钟），按钮会显示「分析中…」，结果缓存在内存里。
- 没装 claude CLI 时按钮会直接报错，不会假装分析过。
- 想换模型用 `MYMAC_AI_MODEL=sonnet mymac`。

> `sfltool dumpbtm` 在本机（macOS 26.5.2）会挂死，25 秒无任何输出，所以没有采用。

---

## 系统清理的文件扫描

大文件和重复文件的扫描会遍历整个主目录，耗时可达几十秒，所以它是**后台任务**：

1. 先用 Spotlight 索引（`mdfind`）秒出首批结果；
2. 再用 `find` 全量遍历补齐（Spotlight 不索引缓存目录，结果不完整）；
3. 前端轮询任务状态，显示进度，不白屏等待。

重复文件按三级过滤：文件大小 → 头尾 4 KiB 的部分哈希 → 完整 SHA-256。
部分哈希**故意不覆盖文件中部**，它是廉价过滤器，最终判定靠全量哈希。

---

## 自己不能对自己动手

mymac **不会**给自己发信号、改优先级，也不会在端口页对自己做停止 / 强杀 / 重启。
自己那一行在进程表和端口表里都标着「本进程」，按钮是灰的。

原因很实在：被 SIGSTOP 冻住的 mymac 会变成一个「看起来活着、其实已经死了」的服务——
菜单栏图标还在（系统只看宿主进程还在不在），端口还占着，但 HTTP 不响应了，
单实例锁也还持着，起第二个实例也会被挡。要恢复只能 `kill -CONT <pid>`。

---

## 界面

- **主题**：左下角可以选「跟随系统 / 浅色 / 深色」，选择会记住。默认跟随系统。
- **表格排序**：所有表格的表头都能点，第一次点升序，再点降序，表头会显示 ▴ / ▾。
- **详情抽屉**：点任意一行，详情从右侧滑出来（Esc 或点空白处关闭）。表格不会被推走，
  所以可以一边看详情一边对照表格。进程、端口、应用、启动项、环境变量、清理项、审计日志都有详情。

---

## 接口文档

HTTP 接口有一份 **OpenAPI 3.0** 描述，描述由代码生成，不会和实现对不上。

| 位置 | 说明 |
|---|---|
| <http://127.0.0.1:8888/api/docs> | 浏览器里的接口文档，可以直接发请求（界面左下角也有「接口文档」按钮） |
| <http://127.0.0.1:8888/api/openapi.json> | 机器可读的描述，Postman / 代码生成器 / 校验工具都能吃 |
| `docs/openapi.json`、`docs/openapi.yaml` | 提交进仓库的同一份描述，不启动 mymac 也能给别的工具用 |

```bash
make openapi    # 接口变了之后重新生成 docs/ 里的文件
```

**为什么是生成的**：接口清单写在 `internal/api/ops.go` 一张表里，路由从这张表注册，
描述也从这张表生成。表里少一个处理器，服务启动时会直接报错；多一个没写进表的路由，
测试会失败。所以「文档和代码对不上」这件事在结构上就不成立。

- `internal/api/types.go` —— 请求体和响应体的结构，处理器和描述用的是同一份类型。
- `internal/api/schema.go` —— 从 Go 类型生成 JSON Schema，命名类型变成 `components/schemas`。
- `internal/api/validate.go` —— 校验器，测试用它检查真实返回和描述是否一致。

测试里会真的去调那些只读的接口，把返回的 JSON 和描述里的 schema 逐字段比对；
所以「改了实现忘了改描述」会被 CI 拦下来。

---

## 应用卸载

- 点应用行的「**卸载**」不会删任何东西，只是把这个应用**加进下面的「卸载列表」**。列表可以累积多个应用。
- 列表里每个应用一组，组内是它自己的残留文件（Application Support、偏好设置、容器、脚本……），每行一个复选框。
  高风险项（特权辅助工具、Group Container、系统级后台服务）默认不勾选。
- 顶部的「**移到废纸篓**」是唯一的删除入口：把列表里所有勾选的项目**一次请求**送进废纸篓，
  Finder 自动化也只弹一次。删完的应用自动从列表里消失，失败的留在列表里可以重试。
- 「全选 / 全不选 / 只选安全的 / 清空列表 / 移出列表」只改计划，不动文件。

---

## 实现要点

**采集性能**：1000+ 进程时一次完整采集 **< 100ms**。

- gopsutil 的 `Status()` 在 macOS 上会为每个进程跑一次命令，800 个进程要 2.7 秒。改成一次 `ps -axo pid=,stat=` 拿全表状态。
- `lipo -archs` 判断架构、`plutil` 读 Info.plist 都很慢，这两类都放到后台预热，采集循环里只读缓存。
- `lsof` 和 `nettop` 在后台刷新端口表和网络速率，采集循环不等待。
- CPU 百分比通过两次采样的累计 CPU 时间差计算；网络速率通过两次 `nettop` 累计计数器的差计算。
- 目录大小用 `/usr/bin/du -sk`（一次系统调用）而不是逐文件 `lstat`。
- **后台任务不能继承请求的 context**：HTTP 请求结束时 context 会被取消，
  挂在上面的 goroutine 会跟着死。扫描任务和大小预热都用自己的 context。
- **首屏缓存不能在启动路径上同步填**：`nettop` 一次要 5 秒，放在启动路径上会让端口迟迟 listen 不了。
  采集全部挪进 goroutine，启动耗时从 4.2 s 降到 0.6 s。
- **进程的名称可能为空**：少数系统守护进程在进程表里没有名字，回退用可执行文件名，
  否则名称列是空的，连保护规则（按名字匹配）也会失效。
- **应用大小是后台测的**：51 个应用全部量完要十几秒（每个 app 都要走一遍文件树）。
  测量期间表格会更新几次；数字没变的时候**不会重绘**，所以不会一直闪。

**错误分类**：所有错误都带一个 `kind`，前端据此给出可执行的提示：

`permission_denied` · `sip_protected` · `process_gone` · `file_busy` · `not_found` · `invalid_request` · `unsupported` · `internal`

---

## 已知限制

- **无法读取其它用户进程的环境变量**。macOS 没有公开接口；mymac 用 `ps eww` 读取自己拥有的进程的值，其它用户的会明确提示。
- **未签名的 `.app` 每次重建都要重新授权**隐私权限——签名变了，TCC 里的记录就对不上。稳定使用请固定一个签名。
- **`nettop` 看不到其它用户的进程**，没有 root 时网络速率只覆盖自己的进程。
- **`powermetrics` 需要 root**，能效核/性能核信息在没有提权时不可用。
- **`footprint` 很慢**（每个进程几百毫秒），只在进程详情里按需调用。
- **重复文件检测**按「大小 → 头尾 4KiB 部分哈希 → 完整 SHA-256」三级过滤，大目录会慢。
- **废纸篓优先用 Finder**；没有自动化权限时降级为直接移入 `~/.Trash`，文件仍然可恢复，但不会出现在 Finder 的废纸篓栈里。
- **`.zshenv` 标记块被手动破坏**时（缺少结束标记等），mymac 不会自动改写，而是提示你点「修复标记块」——它只替换标记块内部。
- 只支持 **zsh**（`.zshenv`）。bash / fish 留了接口但默认不启用。

---

## 测试

```bash
make test
```

- 单元测试：plist 解析、残留匹配、大小计算、白名单/黑名单、环境变量转义与标记块读写、`launchctl` 输出解析、`ps eww` 解析、告警阈值。
- 集成测试：全部使用临时 `HOME`、临时目录和假的 `.app`；`launchctl` 用接口注入的假实现，**测试永远不会改动真实的 launchd 环境**。
- 环境变量的转义用真的 `/bin/sh` 验证：source 之后逐字节比对。
- 接口契约：路由和接口描述必须一一对应；只读接口会被真的调一次，返回的 JSON 要和描述里的 schema 逐字段对得上；
  `docs/` 里的描述文件和当前代码对不上时测试也会失败。
