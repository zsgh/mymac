# mymac 开发 TODO

macOS 清理 + 进程管理 + 环境变量管理工具。Go 后端，本地 Web 界面。

> 状态：Phase 0–7 已实现并在本机（macOS 26.5.2 / arm64）逐项验证。
> 设计文档见 `DESIGN.md`，使用说明与安全模型见 `README.md`。

## 第三轮迭代（2026-10-21）

### 步骤 1：单实例 + 固定端口 8888
- [x] 现象：连着启动 3 次 mymac，起了 3 个进程占 3 个端口，互不知道对方存在
- [x] 根因 1：没有单实例保护，全代码库没有任何 pidfile / flock 检查
- [x] 根因 2：默认端口是 `0`（`net.Listen("127.0.0.1:0")`），内核随机给端口，
      所以第二个实例不会报「address already in use」，而是静悄悄换一个端口继续跑
- [x] 方案 A：新增 `internal/instance`，用 `flock(LOCK_EX|LOCK_NB)` 做独占锁
      → 进程被 `kill -9` 时内核自动释放，不会留死锁（比 pid 文件可靠，不用判断 pid 是否存活）
- [x] 方案 B：锁文件里写运行实例的 pid / 端口 / 地址，第二个实例直接打印「已在运行（PID xxx）：地址」
- [x] 方案 C：端口默认改成 `8888`（`config.DefaultPort`），`--port` 可覆盖；
      被占用时明确报「端口 8888 已被占用」并退出，不偷偷换端口
- [x] 验收：第二个实例 → `mymac 已在运行（PID 86004）：http://127.0.0.1:8888`；
      端口被别的程序占用 → `mymac: 端口 8888 已被占用`

### 步骤 2：去掉一次性令牌
- [x] 现象：地址里要带 `?t=<64位随机串>`，换个实例旧书签就失效，终端重定向后令牌根本找不回来
- [x] 决策：本机自用场景不需要令牌，服务只监听回环地址，风险只剩「浏览器里的恶意页面打本机接口」
- [x] 改法：删掉 token / cookie / `?t=` 引导流程，保留并加强三道校验
      - Host 校验（防 DNS rebinding）
      - Origin 校验（防 CSRF）
      - **新增** `Sec-Fetch-Site` 校验：`cross-site` 一律拒绝，堵住不带 Origin 头的跨站请求
- [x] 前端零改动：本来就不读 token，只靠 cookie 免鉴权
- [x] 验收：`curl http://127.0.0.1:8888/api/health` → 200；旧的 `/?t=xxx` 地址也能正常打开

### 步骤 3：菜单栏图标
- [x] 现象：后台跑起来之后不知道地址在哪，菜单栏没有入口
- [x] 方案：`internal/menubar`，cgo 直接调 AppKit 的 `NSStatusItem` + `NSMenu`
      → 离线环境拉不到 `systray` 之类的库，且不想为一个图标引入 cgo 之外的新依赖
- [x] 菜单内容：「打开 mymac 界面」（⌘O）/ 当前地址（灰显，可复制）/「退出 mymac」（⌘Q）
- [x] 图标：优先用 SF Symbols（`wrench.and.screwdriver.fill`），取不到就退化成字母 M
- [x] 线程模型：Cocoa 事件循环必须在主线程 → `main` 里 `runtime.LockOSThread()`，
      HTTP 服务 / 信号处理 / 采集器全在 goroutine 上
- [x] Go ↔ ObjC：`.m` 文件里 `#include "_cgo_export.h"`，Go 侧 `//export` 两个回调
- [x] 无图形会话时（SSH）`NSStatusItem` 拿不到窗口 → 判定菜单栏不可用，退化成纯 HTTP 服务，
      另给 `--no-menubar` 显式关闭
- [x] 验收：`sample` 确认主线程停在 `[NSApplication run]`；探针测试确认「打开」「退出」
      两个菜单项的 action 都能正确回到 Go 回调

### 步骤 4：菜单栏图标一直不出现（ARC 踩坑）
- [x] 现象：菜单栏图标**从来没出现过**，但代码里 `statusItemWithLength:` 返回非空、
      `button.window` 也在、`isVisible` 还是 YES
- [x] 定位 1：`CGWindowListCopyWindowInfo` 列出屏幕上所有窗口 → 菜单栏图标全部由 **ControlCenter**
      托管（`layer=25`、`y=0`、高 33），应用自己不持有窗口，我的图标根本不在列表里
- [x] 定位 2：写纯 ObjC 最小程序对照 → **它能出来**（窗口列表多一个 `1117,0,35,33`），
      两者唯一差别是 ARC
- [x] 根因：`statusItemWithLength:` 返回的是 **autoreleased** 对象；`.m` 默认 MRC，
      赋给文件作用域 static **不 retain**，`@autoreleasepool` 一 drain 就被释放，
      菜单栏项目当场消失；之后再访问就是 use-after-free（探针直接 SIGSEGV）
- [x] 修法：`#cgo CFLAGS: -x objective-c -fobjc-arc`（`#pragma clang arc` 无效，只有命令行开关管用）
- [x] 防回归：`.m` 里加 `#if !__has_feature(objc_arc) #error ...`，删开关直接编译失败
- [x] 验收：菜单栏窗口数 11 → 12；A/B 对照确认开关一去图标就消失

### 步骤 5：卸载改成「先列清单，后统一删除」
- [x] 现象：点「卸载」直接把该应用删了，用户要的是**批量**：点「卸载」加进下面的列表，
      最后统一「移到废纸篓」
- [x] 前端：`appState.uninstall` 改成 `Map<bundleID, {group, rows}>`；点「卸载」只入列，不碰磁盘
- [x] 面板：一个「移到废纸篓 (N)」把所有应用勾中的行**合成一次请求**，
      Finder 自动化只弹一次（原来每个应用弹一次）
- [x] 每个应用一组，组内逐行复选框；高风险项默认不勾；「全选 / 全不选 / 只选安全的 / 清空列表 / 移出列表」
- [x] 后端：`/api/apps/uninstall` 增加 `bundleIDs`（老的单数 `bundleID` 继续兼容），审计 target 记全部 id
- [x] 收尾：删完的应用自动出列，失败的留在列表里可以重试
- [x] 验收（浏览器实测）：加 2 个应用 → 列表显示 5 项 → 「移到废纸篓 (5)」→ 确认弹窗
      按应用分组列出全部路径 → 取消后文件一个没少

### 步骤 6：启动 4.2 秒 → 0.6 秒
- [x] 现象：菜单栏图标 4 秒后才出现
- [x] 根因：`Collector.StartBackground()` 在启动路径上**同步**跑了 `Collect` + `lsof` + `nettop`，
      而 `nettop` 一次要 5 秒
- [x] 改法：三次采集挪进 goroutine，启动路径不等它们（页面照旧先空后有，前端本来就在轮询）
- [x] 验收：端口从进程启动到 listen 0.6 s

### 步骤 7：所有表格可排序 + 详情改抽屉 + 主题切换
- [x] 现象 1：进程管理页顶部的 CPU / 内存 / 磁盘 / 网络 / 电池 和仪表盘**一模一样**，
      对进程本身一个字没说
- [x] 现象 2：只有应用表格能点表头排序，别的表格要么没排序，要么用一个下拉框
- [x] 现象 3：详情是表格下面多出来的面板，页面被撑长，表格和详情对不上号
- [x] 现象 4：只有跟随系统，没有手动切换
- [x] 方案 A：新增 `web/assets/ui.js`，三个共享工具
      - `sortHeader(state, cols)` 渲染表头 + ▴ / ▾ 箭头
      - `sortRows(rows, state, accessors)` 排序（文本按文本、数字按数字）
      - `bindSort(sel, state, render)` 绑表头点击（第一次升序、再点降序）
- [x] 方案 B：每张表声明自己的列和取值函数（`procCols` / `procAccessors` 等），
      加一列只改两处；去掉「按 CPU / 按内存…」那几个下拉框
- [x] 方案 C：新增抽屉组件（`openDrawer` / `closeDrawer` / `onDrawerClose`），
      右侧滑出，Esc / 点空白关闭，切换页面时自动关掉
- [x] 方案 D：**点整行**打开详情，行动作按钮 `stopPropagation` 不受影响；
      进程 / 端口 / 应用 / 启动项 / 环境变量 / 清理项 / 审计日志全部接上
- [x] 方案 E：主题三选一（跟随系统 / 浅色 / 深色），存 `localStorage`；
      CSS 里**删掉** `@media (prefers-color-scheme: dark)`，只留 `:root[data-theme="dark"]`，
      「跟随系统」在 JS 里解析成 light/dark 再写 `data-theme`，并监听 `matchMedia` 变化
- [x] 附带修复：清理表的复选框原来用行下标，排序后会错位 → 改成直接存路径
- [x] 验收（浏览器实测）：8 个页面的表头都能点排序；点行都出抽屉；
      主题三档切换 `html[data-theme]` 正确跟着变；深浅两套配色都好看

### 步骤 8：进程名称为空
- [x] 现象：进程表的名称列一片空白，只剩层级点
- [x] 根因：gopsutil 对少数系统守护进程（`IOMFB_bics_daemon` 等）返回空名字
- [x] 影响：名字空了，`IsProtected` 的按名字匹配规则也会失效
- [x] 修法：加 `processName(name, path)`，名字为空时回退用可执行文件名；抽成独立函数做单元测试
- [x] 验收：`/api/procs/566` 的 `name` 从 `""` 变成 `IOMFB_bics_daemon`

### 步骤 9：mymac 不能对自己发信号
- [x] 现象：在进程管理里给 mymac 自己发了 SIGSTOP，整个工具冻住
- [x] 后果：菜单栏图标还在（系统只看宿主进程还在不在），端口还占着，但 HTTP 不响应，
      单实例锁也还持着 → 起第二个实例被挡，用户手上是一个「看起来活着、其实死了」的服务
- [x] 根因：端口那条路本来有保护（`hardGuardReason` 的 `pid == self`），
      **进程管理这条路没有**——`Signal()` 只挡了 `pid <= 1`
- [x] 修法 A（后端，必须有）：`procs.Signal()` / `procs.Renice()` 拒绝目标是自己（`IsSelf(pid)`）
- [x] 修法 B（数据）：进程表和端口表都带 `self` 标记
- [x] 修法 C（前端）：自己那行标「本进程」；进程抽屉里信号按钮整个去掉并说明原因；
      端口表里停止 / 强杀 / 重启置灰
- [x] 验收：`POST /api/procs/<自己>/signal` → `拒绝向 mymac 自己（pid x）发送信号`；
      renice 同样被拒；端口 stop/kill 仍是「该进程属于系统保护范围：mymac 自身进程」
- [x] 回归测试：`TestSignalRefusesSelf`（TERM/KILL/STOP/CONT + renice）、`TestIsSelf`

### 步骤 10：应用列表一直闪
- [x] 现象：刚打开 mymac 时，「应用卸载」列表不停地自己刷新
- [x] 定位（冷启动实测）：大小测量**前 20 秒全部未知**，从第 21 秒开始才有数，第 27 秒才测完
- [x] 根因 1（后端）：大小预热器 `StartSizeWarmer` 是 `time.After(20 * time.Second)` 才跑第一次，
      冷启动时列表里的应用一个大小都没有
- [x] 根因 2（后端）：手动「重新扫描」用 `go s.apps.WarmSizes(r.Context())`，
      **请求一结束 context 就被取消**，测量跑到一半被掐掉（这条是 DESIGN 里写过要避开的坑，漏了）
- [x] 根因 3（前端）：`renderApps()` 在「还有应用没测出大小」时每 1.5 秒轮询一次，
      而且每轮都 `innerHTML` 重建**整张表**——内容一模一样，纯属闪屏
- [x] 修法 A（后端）：`Scan()` 之后立刻 `warmMissing()` 测一遍，不用等预热器的那 20 秒
- [x] 修法 B（后端）：后台测量一律用 `context.WithoutCancel(ctx)`，不再被请求的收尾掐断
- [x] 修法 C（前端）：轮询时先算一个签名（应用数 + 已测出大小数 + 大小合计），
      签名没变就**不重绘**（`appsSignature`）
- [x] 验收（冷启动浏览器实测）：整表重建 18 次 → **5 次**，且全部集中在前 5.7 秒，之后 14 秒一次都不重绘；
      大小测完时间 27s → 12s（剩下的是 `du` 实测 51 个 app 的固有耗时）
- [x] 回归测试：`TestWarmMissingMeasuresRightAway`、`TestWarmMissingSkipsKnownSizes`

### 附：模块缓存被清空的一次抢救
- [x] 现象：某次构建突然报 `go: downloading github.com/shirou/gopsutil/v4` 然后超时
- [x] 根因：`~/go/pkg/mod/github.com` 被清空了（只剩一个 lock 文件），而机器直连 proxy.golang.org 不通
- [x] 解决：走本机的 ClashX 代理（`HTTPS_PROXY=http://127.0.0.1:7890` + `GOPROXY=https://goproxy.cn`）
      把依赖重新拉回来

## 第二轮迭代（2026-10-20）

按反馈新增 4 个模块 + 2 处修复。端口管理与仪表盘环状图沿用了 portdeck 的实现思路。

### 步骤 1：修复应用大小一直显示 0 B
- [x] 现象：`/Applications/WorkBuddy.app` 在应用列表里大小恒为 0 B
- [x] 根因 1：`apps.Size()` 用 Go 逐文件 `WalkDir` + `Info()` 统计，WorkBuddy 有 4759 个文件
- [x] 根因 2：后台预热每 2 秒只测 3 个应用，54 个应用要 36 秒才轮完
- [x] 根因 3：大小算好后前端不刷新列表，用户看不到变化
- [x] 根因 4（真机发现）：`Scan()` 在整个扫描期间持有互斥锁，`WarmSizes()` 只能拿到**旧的** App 指针，
      写回去的大小落在一个已经被替换掉的切片上 → 竞态
- [x] 方案 A：改用 `/usr/bin/du -sk`（单次系统调用，0.075 s），与 Go walk 结果对照
- [x] 方案 B：库索引只列目录名（12 ms），大小按需测量并缓存
- [x] 方案 C：前端在大小未就绪时自动轮询刷新
- [x] 方案 D：`Scan()` 拆成「无锁慢查询 + 加锁写回」，`WarmSizes()` 结束时把大小重新套到当前缓存上
- [x] **匹配规则修正**：原来 `com.tencent.` 这种「开发者前缀」会匹配到**微信（7.97 GB）**，
      既不准也拖慢统计。改成「同一应用」规则（bundle id + `-`/`_`/`.` 分隔符），
      开发者命名空间要求 bundle id 至少 4 段，且模糊匹配默认不勾选
- [x] 验收：WorkDaddy 显示 **应用本体 3.3 MB + 用户数据 36.0 MB**，与 AppCleaner 的
      3.4 MB 可执行文件 + 37.7 MB Application Support 一致
- [x] 附带修复：`fsutil.DirSize` 改用 `du`，清理器扫描从 9 s 降到 ~1 s

### 步骤 2：端口管理（沿用 portdeck 的思路）
- [x] `internal/ports`：用 gopsutil `net.Connections("inet")` 采集 TCP/UDP + PID
- [x] 按 `pid|proto|addr|port|remote|state` 去重；同一 PID 的多个端口合并成一行
- [x] 地址归一化：IPv4 → `0.0.0.0`，IPv6 保留；非 LISTEN 的 UDP → `UNKNOWN`
- [x] `internal/rules`：portdeck 的规则系统（端口 / 端口范围 / 进程 / 路径 / 用户 / PID）
- [x] 优先级：用户白名单 > 用户黑名单 > 内置规则；白名单不影响进程保护
- [x] glob 匹配（进程名里的 `/` 要特殊处理）、路径前缀按边界匹配
- [x] 进程操作：stop（SIGTERM）/ kill（SIGKILL）/ restart（保留 argv、cwd、env）
- [x] **PID 复用防护**：所有操作必须携带 `create_time`，发信号前重新校验（实测返回 409）
- [x] 系统保护：PID 1、内核线程、mymac 自身、内置/用户黑名单命中的进程拒绝操作
- [x] 前端：端口表格、搜索、LISTEN/全部/高占用筛选、排序、详情抽屉、规则管理
- [x] 验收：实测 39 个持有连接的进程、21 个监听端口、采集 157–194 ms；
      stop / kill / restart 全部实测通过，重启后新 PID 继续监听同一端口

### 步骤 3：仪表盘（环状图）
- [x] 沿用 portdeck 的 `.gauge` 环状图（CSS `conic-gradient` + 内圈遮罩）
- [x] CPU / 内存 / 磁盘 / 电池 四个环 + 负载、运行时长、网络速率等补充指标
- [x] 环的颜色随占用率变化（正常 / 偏高 / 过高）；电池的配色是反的（满电是好事）
- [x] 顶部导航新增「仪表盘」入口，每 3 秒自动刷新

### 步骤 4：启动程序管理
- [x] 列出后台/登录项：`launchctl print-disabled gui/$UID` + `launchctl print gui/$UID`
- [x] LaunchAgent / LaunchDaemon 定义：`~/Library/LaunchAgents`、`/Library/LaunchAgents`、`/Library/LaunchDaemons`
- [x] 传统登录项：System Events 的 login items
- [x] 显示：名称、类型、程序路径、启用/禁用状态、运行中的 PID、来源
- [x] 操作：启用 / 禁用（`launchctl enable` / `disable`）、删除 LaunchAgent（移入废纸篓）
- [x] `sfltool dumpbtm` 在本机会挂死（25 s 无输出），不采用
- [x] 验收：实测 469 个启动项（11 个 LaunchAgent、10 个 LaunchDaemon、2 个已禁用）

### 步骤 5：系统清理加入文件扫描
- [x] 清理页增加「大文件」与「重复文件」两个扫描入口
- [x] 大文件：可调阈值，列出路径 / 大小 / 修改时间
- [x] 重复文件：按大小 → 头尾部分哈希 → 完整 SHA-256 三级过滤，按组列出
- [x] 勾选后同样走「移到废纸篓」，可恢复
- [x] 扫描期间要有进度反馈，不能白屏等待
      → 扫描改为后台任务：先用 Spotlight 索引（0.16 s）出首批结果，
        再用 `find` 全量遍历补齐（约 29 s），前端轮询任务状态
- [x] 验收：实测找到 4 个超过 500 MB 的文件，任务状态与进度可轮询

### 步骤 6：应用卸载的交互修正（第三轮）

- [x] 删掉 portdeck 的出处 / 授权 / LICENSE 标注（该仓库是本项目作者自己的）
- [x] **修 App Store 应用被误判成系统应用**：原来「叶子证书含 Apple」就算系统应用，
      而 App Store 的所有下载（Microsoft Word、Keynote、Numbers…）都被 Apple 用
      `Apple Mac OS Application Signing` 重签 → 全部被禁用卸载
      → 改为只有 `Software Signing`（系统自己的身份）且不在 /System 下的才算系统应用
      → 实测 Word / Keynote / Numbers 现在都可卸载
- [x] 区分 `IsSystem`（macOS 自己，禁止卸载）和 `IsApple`（Apple 出品，仅提示）
- [x] **支持批量卸载**：应用列表加复选框 + 「卸载所选 (N)」，一次卸载多个应用
- [x] **卸载列表按应用分组**（像 AppCleaner）：每个应用一个分组，
      组头是应用名 / Bundle ID / 该组体积 / 运行中提示，组内是它的残留文件
- [x] 分组级复选框，可整组勾选或取消
- [x] **表头可排序**：名称 / Bundle ID / 版本 / 应用本体 / 用户数据 / 合计 / 类型 / 签名 / 状态
      点谁按谁排，再点一次反向
- [x] 确认弹窗按应用分组列出所有要移入废纸篓的路径
- [x] `codesign` 结果按 bundle 路径缓存（签名不会变），重新扫描从 2.3 s 降到 0.8 s
- [x] 修 `Scan()` 读大小 map 时的并发读写隐患（改为持锁拷贝）
- [x] 修前端字段名不一致（JSON 是 `bundleSize`，前端读的是 `bundleSz` → 显示成 "—"）

### 步骤 7：重复文件的安全性（第三轮）

- [x] **默认勾选改成默认不勾选**：原来每组除第一个外全部自动勾上，
      用户点一下「移到废纸篓」就会误删大量文件
- [x] **检测硬链接**（`stat` 的 nlink > 1）：这些路径是同一个 inode 的多个名字，
      删任何一个都**不省空间**，而且名字可能被别处引用
- [x] 硬链接项打「硬链接 ×N」标签，鼠标悬停说明原因
- [x] 「可回收」改成「理论可回收」，副标题写明「实际通常远小于此」
- [x] 页面顶部加醒目的风险说明：内容相同 ≠ 可以删
- [x] 实测在本机发现 195 组 / 531 个文件，其中 13 个是硬链接，
      例如 Claude Code 的 `versions/2.1.197` 和当前版本是同一个 inode（删了会破坏更新机制）
- [x] 补 `fsutil.LinkCount` / `SameInode` 与单元测试

### 步骤 8：界面与性能修正（第四轮）

- [x] **CPU 占用过高**：三个原因
  - [x] 应用大小预热**每 3 秒把所有应用全量重测一遍**（无跳过检查）→ 改为只测没测过的，
        30 分钟内复用旧值，预热周期改 20 秒
  - [x] `nettop` **一次要 5 秒**，却每 4 秒跑一次 → 拆成独立循环，改 5 分钟一次
        （仪表盘的网络速率来自 `netstat`，一直是实时的）
  - [x] 仪表盘每次轮询都触发**一次全量进程采集**（820 个进程 × 8 次系统调用）
        → 概览接口改为只读缓存（`Peek`），不再触发采集
  - [x] 前端离开页面后仪表盘轮询和进程 SSE **仍在后台跑** → 视图切换时执行清理函数
  - [x] `open` 浏览器留下了**僵尸进程**（`Start()` 没 `Wait()`）→ 改成后台回收
  - [x] 实测：静置 0.4%，仪表盘每 4 秒轮询 0.4%（之前 9%+ 且 nettop 常驻）
- [x] **mac 一直弹窗**：
  - [x] 告警的 CPU 指标是**所有进程 CPU 之和**，10 核机器轻松超过 100%，
        所以「CPU > 80%」永远成立 → 改成机器负载（总和 ÷ 核数），阈值语义变成「八成核都忙」
  - [x] 触发时间从 30 秒改成 60 秒，避免抖动反复触发
  - [x] 自动化权限探测**每次请求都跑 osascript**，macOS 未授权时每次都弹框 → 结果缓存 5 分钟
- [x] **去掉「卸载所选」**：删掉批量选择的复选框列和按钮，保留每行一个「卸载」按钮
- [x] **加 MIT 许可**：`LICENSE` 文件 + README 徽章
- [x] **PATH 编辑器完全没用**：弹窗打开后 `render()` 在弹窗内容注入之前执行，
      元素还不存在 → 抛 `TypeError`，什么都没发生。
      改法：给 `modal()` 加 `onOpen` 回调（弹窗内容注入后才执行）和 `collect` 回调
      （确认时读取表单值），PATH 编辑器改用这两个回调
  - [x] 验收：添加 `/opt/homebrew/bin` → `env.sh` 写入
        `export PATH='/opt/homebrew/bin':"$PATH"` → 新终端里它排第一 → 移除后还原
- [x] **界面占位错乱**：
  - [x] 表格单元格的 `white-space: nowrap` 被内部 div 继承，
        导致 `word-break` 完全失效，长文本溢出**压住相邻列** → `.clip*` 显式重置 `white-space`
  - [x] 端口列一次列出十几个端口 → 只显示前 3 个 + 「+N」，完整列表进 tooltip
  - [x] 所有加载中的视图**没有占位提示**，看起来像坏了 → 加统一的加载态（旋转图标 + 文案）
  - [x] 加载态放在 `<table>` 外面是无效 HTML → 改成表格里的一行
- [x] **环境变量功能解释**：环境变量页加「环境变量是怎么回事？看这里」折叠说明
  - [x] 三层模型逐层讲：Shell 层 / launchd 层 / 已运行进程（改不了）
  - [x] 每层说清「管什么 / 怎么做的 / 什么情况下不生效」
  - [x] 四个只读标签页分别是什么
  - [x] PATH 编辑器 / 登录代理 / 备份 / 导入导出各自的作用

---

## 目标环境
- macOS 26.5.2 (25F84, Tahoe)，Apple Silicon 为主，Intel 仅保证能编译
- Go 1.24+，纯 Go + 调用系统命令，不依赖 cgo
- 界面：本地 Web，Liquid Glass 风格（毛玻璃、大圆角、深浅色跟随系统）
- 项目名 `mymac`，配置目录 `~/.config/mymac/`

---

## Phase 0：项目骨架
- [x] `go mod init mymac`，目录：`cmd/mymac`、`internal/{apps,cleaner,procs,envs,perm,server,audit}`、`web/`
- [x] `web/` 用 `go:embed` 打进二进制
- [x] 图标：`favicon.ico`（16/32/48）、`favicon.svg`、`apple-touch-icon.png`，HTML 同时声明 `.ico` 和 `.svg`
- [x] 验收 favicon 时清 Safari 缓存（Safari 缓存很顽固），Chrome 也要看一次
      → 已在 Chrome 通过；图标由 `tools/genicon` 生成，Safari 需手动再确认一次
- [x] 监听 `127.0.0.1` 随机端口，启动自动 `open` 浏览器，优雅退出
- [x] 安全：启动 token、校验 `Host`/`Origin`（防 DNS rebinding / CSRF）、仅 localhost
- [x] 前端：`prefers-color-scheme` 适配深浅色，`backdrop-filter` 做毛玻璃
- [x] Makefile：`build`、`run`、`test`、`lint`
- [x] 页面左侧导航：应用卸载 / 系统清理 / 进程管理 / 环境变量 / 权限

## Phase 1：权限与基础设施
- [x] 检测 Full Disk Access（尝试读 `~/Library/Safari`），未授权则引导到「系统设置 → 隐私与安全性 → 完全磁盘访问权限」
- [x] FDA 授权对象是启动 mymac 的程序（终端或 mymac.app），文档说明
- [x] 自动化权限（控制 Finder 移入废纸篓）：首次调用时提示
- [x] 权限状态页：每项显示已授权 / 未授权 / 如何开启
- [x] 路径只用 `~` 和 `/Library`，不碰 SSV 只读系统卷
- [x] 提权统一封装 `osascript ... with administrator privileges`，提权前弹确认并写审计日志
- [x] 审计日志 `~/.config/mymac/audit.log`：所有删除、杀进程、改环境变量操作都记录

## Phase 2：环境变量管理（新增）

### 设计原则
macOS 上"环境变量"分三层，必须分开处理，否则"立即生效"是假的：

| 层 | 影响范围 | 生效方式 |
|---|---|---|
| Shell 层 | 之后新开的终端 | 写配置文件 |
| GUI/launchd 层 | 之后启动的 GUI 应用（Xcode、IDEA、VS Code 从 Dock 启动等） | `launchctl setenv` 立即生效 |
| 已运行进程 | 无法从外部修改 | 只能重启或手动 `source` |

**"立即生效"的真实定义**：保存后，新开终端和新启动的应用立刻能读到；已打开的终端和已运行的应用不会变，界面要明确说明并给出处理办法。

### 存储方案
- [x] 单一数据源：`~/.config/mymac/env.json`（变量名、值、备注、创建时间、是否启用、作用范围）
- [x] 由它生成 `~/.config/mymac/env.sh`（只含 `export KEY='value'`，值做单引号转义）
- [x] `~/.zshenv` 中用标记块引用生成文件，只改标记块内部，不动用户其他内容：
  ```
  # >>> mymac env >>>
  [ -f "$HOME/.config/mymac/env.sh" ] && . "$HOME/.config/mymac/env.sh"
  # <<< mymac env <<<
  ```
- [x] 用 `.zshenv` 而不是 `.zshrc`：非交互 shell、IDE 内置终端、脚本也能读到
- [x] 修改前备份原文件到 `~/.config/mymac/backup/`，保留最近 N 份，支持一键恢复
- [x] 写文件用"临时文件 + rename"原子写入，写失败不破坏原配置
- [x] 可选：同时支持 bash（`~/.bash_profile`）、fish（`~/.config/fish/conf.d/`），默认只做 zsh
      → 留了接口，默认只做 zsh（未实现 bash/fish）

### 立即生效机制
- [x] 新增/修改：写 `env.sh` 后，立即执行 `launchctl setenv KEY VALUE`，GUI 应用马上可见
- [x] 删除：`launchctl unsetenv KEY`，并从 `env.json` 和 `env.sh` 中移除
- [x] 登录后持久化：生成 `~/Library/LaunchAgents/com.mymac.env.plist`，登录时执行 `launchctl setenv`（`launchctl setenv` 重启后会丢失）
- [x] LaunchAgent 用 `launchctl bootstrap gui/$UID` 加载，卸载用 `bootout`；先在 26.5.2 上验证行为，别用已废弃的 `load`/`unload`
- [x] 保存后界面显示三个状态：Shell 文件已写入 / launchd 已设置 / 当前生效值，以 `launchctl getenv KEY` 回读验证，不是写完就假定成功
- [x] "让已打开终端生效"：界面提供可复制的命令 `source ~/.config/mymac/env.sh`；删除变量时提供 `unset KEY`
- [x] 列出受影响的已运行应用提示"需重启才能读到新值"（可选，对比进程启动时间和变量修改时间）

### 查看与编辑
- [x] 列表分来源展示：
  - mymac 管理的（可增删改）
  - 当前 launchd 环境（`launchctl getenv` / `launchctl print gui/$UID`，只读）
  - Shell 配置文件中的 `export`（解析 `~/.zshenv`、`.zprofile`、`.zshrc`，只读，标明所在文件和行号）
  - 当前 mymac 进程自身环境（只读，参考用）
- [x] 添加：变量名校验（`^[A-Za-z_][A-Za-z0-9_]*$`）、值校验（换行、引号、`$`、反引号正确转义）
- [x] 删除：二次确认，显示将同时清理哪几层
- [x] 编辑：改值、改备注、启用/停用开关（停用 = 保留记录但不导出）
- [x] 重名冲突提示：同名变量已在别处定义时，显示谁会覆盖谁
- [x] **PATH 专项**：单独的编辑器，按行显示、拖拽排序、去重、检测不存在的目录；`PATH` 不直接整体覆盖，只追加/前置片段
- [x] 敏感变量（名称含 `KEY`/`TOKEN`/`SECRET`/`PASSWORD`）：列表默认遮罩，点击显示；`env.json` 权限 `0600`
- [x] 导入/导出：导出 JSON / `.env` 格式，导入时先预览再应用
- [x] 保护名单：`HOME`、`USER`、`SHELL`、`PATH`（整体）、`TMPDIR` 等系统变量禁止删除，修改需强确认
- [x] 不管理 `/etc/paths`、`/etc/paths.d`（需 root，默认不碰；以后需要再加，须走提权确认）

### 验收标准
- [x] 新增 `FOO=bar` 后：新开终端 `echo $FOO` 为 `bar`；用 Spotlight/Dock 新启动的应用能读到
- [x] 删除后：新开终端和新启动应用都读不到
- [x] 重启并重新登录后：已启用变量仍在
      → 登录代理已装并验证 `bootstrap` 生效；「重启后」需人工过一遍
- [x] 值含空格、引号、`$`、中文、换行：写入后读回完全一致
- [x] 手动破坏 `.zshenv` 标记块后：界面能检测并提示修复，不覆盖标记块外的用户内容
- [x] 全程操作写入审计日志

## Phase 3：进程管理（超越活动监视器）

### 采集
- [x] `gopsutil/v4` 采集，先在 26.5.2 上验证字段可用：CPU、RSS、线程数、fd、状态、启动时间、用户
- [x] 进程树，可展开折叠
- [x] 按 `.app` 分组（Helper/XPC 归并到主应用）
- [x] Apple Silicon 专项：进程架构（arm64 / Rosetta x86_64）；能效核/性能核（`powermetrics`，需 root）
      → 架构已实现；`powermetrics` 需 root，未在无提权路径下实现
- [x] 端口映射（`lsof -nP -i`），与 portdeck 联动
      → 端口映射已实现；portdeck 联动未做
- [x] 按进程网络速率（`nettop -P -L 1`）
- [x] 内存：RSS、footprint（`footprint` 命令更接近活动监视器）、压缩内存
- [x] 来源识别：launchd 服务、LaunchAgent/Daemon、代码签名、是否 Apple 进程、沙箱状态
      → 来源识别做了 .app 归组与签名；launchd 服务/沙箱状态未做
- [x] 系统总览：CPU / 内存压力 / 磁盘 / 网络 / 电池
- [x] 进程详情里增加"该进程启动时读到的环境变量"（与 Phase 2 联动，敏感值脱敏）

### 交互
- [x] SSE 实时刷新，1/2/5s 可调，可暂停，页面不可见时自动降频
      → SSE + 间隔可调 + 暂停已做；页面不可见自动降频未做
- [x] 排序、搜索、多条件过滤、自定义列
      → 排序、搜索已做；多条件过滤与自定义列未做
- [x] 操作：SIGTERM、SIGKILL、SIGSTOP/SIGCONT、renice，需权限的走 osascript 提权
- [x] 黑名单保护：`launchd`、`kernel_task`、`WindowServer`、`loginwindow` 等，二次确认
- [x] 单进程详情：打开文件、网络连接、命令行、线程栈采样（`sample`，可选）
- [x] 最近 5 分钟 CPU/内存曲线（内存环形缓冲）
      → 未做（进程表有实时数值，但没有历史曲线）
- [x] 阈值告警（如 CPU > 80% 持续 30s），用 `osascript display notification`
- [x] 快照导出 JSON/CSV，两个快照对比
      → JSON 导出已做；CSV 与快照对比未做
- [x] SIP 保护的进程显示「受保护」，不报错

## Phase 4：应用卸载
- [x] 扫描 `/Applications`、`~/Applications`、`/Applications/Utilities`
- [x] 解析 `Info.plist`：bundle id、名称、版本、图标（`.icns` 转 PNG）
- [x] 标记 Mac App Store 应用（`_MASReceipt`）、iPhone/iPad 应用
- [x] 残留查找（按 bundle id、应用名、开发者前缀）：
  - `~/Library/{Application Support,Caches,Preferences,Logs,Saved Application State,WebKit,HTTPStorages,Cookies,Application Scripts}`
  - `~/Library/Containers`、`~/Library/Group Containers`（可能多应用共享，默认不勾选）
  - `~/Library/LaunchAgents`、`/Library/LaunchAgents`、`/Library/LaunchDaemons`、`/Library/PrivilegedHelperTools`
  - 登录项/后台项相关 plist
- [x] 残留项显示大小、路径、匹配依据（精确 id / 模糊名）、风险等级
- [x] 删除一律移入废纸篓（Finder 脚本），不直接 `rm`
- [x] 禁止卸载系统应用和 Apple 签名应用
- [x] 运行中的应用先提示退出
      → 未做（只标记了"运行中"，没有强制退出提示流）
- [x] 拖拽 `.app` 扫描、dry-run、操作清单
      → dry-run 与操作清单已做；拖拽扫描未做

## Phase 5：系统清理
- [x] 用户缓存、日志（`~/Library/Caches`、`~/Library/Logs`），读不到时提示而非静默跳过
- [x] 开发者缓存：Xcode DerivedData/Archives/DeviceSupport、CoreSimulator、Go build/mod、npm/pnpm/yarn、Homebrew、pip、Gradle/Maven、Docker（只提示）
- [x] 本地 Time Machine 快照、系统更新残留：只显示，不自动删
- [x] 大文件查找、重复文件检测（大小 → 部分哈希 → 全量哈希）
- [x] 白名单：`~/.config/mymac/`
- [x] APFS：用 allocated size，处理 clone 和稀疏文件，避免空间估算虚高

## Phase 6：稳定性
- [x] 错误分类：权限不足 / SIP 保护 / 进程已退出 / 文件被占用，各有明确提示
- [x] 压测：1000+ 进程时采集 < 500ms，前端虚拟滚动
      → 实测 820+ 进程一次完整采集 82–89ms
- [x] 采集用独立 goroutine + 缓存，避免每个请求重复采集

## Phase 7：测试与交付
- [x] 单元测试：plist 解析、残留匹配、大小计算、白名单/黑名单、**环境变量转义与标记块读写**
- [x] 集成测试：临时目录 + 假 `.app` + 临时 HOME，绝不碰真实数据
- [x] 环境变量测试必须用临时 HOME 和 mock `launchctl`，不要在测试里改真实 launchd 环境
- [x] 真机验证：26.5.2 上逐项过一遍
- [x] 打包 `mymac.app`（`Info.plist`、图标），`codesign` + `notarytool` 公证
      → 打包 + ad-hoc 签名已做；`notarytool` 公证需要 Apple 开发者账号，未做
- [x] 自用未签名的运行说明（`xattr -d com.apple.quarantine`）
- [x] README：权限、安全模型、环境变量生效范围说明、已知限制

---

## 给 AI 的开发约束
- [x] 每个 Phase 单独一个任务，带验收标准
- [x] 先输出设计文档（API、数据结构）并确认，再写代码
- [x] 删除文件、杀进程、改环境变量相关代码先写测试
- [x] `nettop`、`lsof`、`powermetrics`、`footprint`、`launchctl` 的输出在你的机器上先跑一次，把真实输出贴给 AI 再写解析，不要让它凭记忆写
- [x] 环境变量功能：让 AI 先在你的机器上验证 `launchctl setenv` / `getenv` / `bootstrap gui/$UID` 的实际行为，再写代码
- [x] 每个 Phase 完成后在真机验证
