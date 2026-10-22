package api

import (
	"mymac/internal/cleaner"
	"mymac/internal/envs"
	"mymac/internal/loginitems"
	"mymac/internal/ports"
	"mymac/internal/rules"
	"mymac/internal/sysinfo"
	"mymac/internal/trash"
)

// endpoints is the table of every route the server exposes.
//
// The table is the single source of truth: the server registers its routes
// from it and the OpenAPI document is built from it, so a route and its
// documentation cannot drift apart.
var endpoints = []Endpoint{
	// ------------------------------------------------------------ system
	{
		Method: "GET", Path: "/api/health", Tag: "system",
		Summary:     "运行状态",
		Description: "返回版本、运行时长、进程号和监听端口。",
		OperationID: "getHealth",
		Responses: []ResponseSpec{
			{Status: "200", Description: "当前状态", Type: HealthResponse{}},
		},
		Probe: true,
	},
	{
		Method: "GET", Path: "/api/perm/status", Tag: "system",
		Summary:     "隐私权限状态",
		Description: "探测完全磁盘访问、自动化与管理员权限的状态。",
		OperationID: "getPermissionStatus",
		Responses: []ResponseSpec{
			{Status: "200", Description: "每一项权限的状态", Type: PermStatusResponse{}},
		},
		Probe: true,
	},
	{
		Method: "POST", Path: "/api/perm/open", Tag: "system",
		Summary:     "打开系统设置",
		Description: "打开对应隐私权限的系统设置面板，由用户手动授权。",
		OperationID: "openPermissionSettings",
		Request: &RequestSpec{
			Description: "要打开的权限面板",
			Type:        PermOpenRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "面板已打开", Type: OKResponse{}},
		},
	},
	{
		Method: "GET", Path: "/api/audit", Tag: "system",
		Summary:     "审计日志",
		Description: "按时间倒序返回最近的写操作记录。",
		OperationID: "getAuditLog",
		Params: []Param{
			{Name: "limit", In: "query", Description: "返回的条数上限", Type: 0, Example: 200},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "审计条目", Type: AuditResponse{}},
		},
		Probe: true,
	},

	// --------------------------------------------------------------- env
	{
		Method: "GET", Path: "/api/env/vars", Tag: "env",
		Summary:     "环境变量总览",
		Description: "返回受管变量、launchd 层、shell 文件与当前进程环境的合并视图。",
		OperationID: "listEnvVars",
		Responses: []ResponseSpec{
			{Status: "200", Description: "多层面板", Type: envs.Snapshot{}},
		},
		Probe: true,
	},
	{
		Method: "POST", Path: "/api/env/vars", Tag: "env",
		Summary:     "新增环境变量",
		Description: "把一个变量写入受管清单。",
		OperationID: "createEnvVar",
		Request: &RequestSpec{
			Description: "新变量",
			Type:        EnvVarCreateRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "写入后的变量", Type: envs.ManagedVar{}},
		},
	},
	{
		Method: "PATCH", Path: "/api/env/vars/{name}", Tag: "env",
		Summary:     "修改环境变量",
		Description: "只改动请求体里出现的字段，未出现的字段保持原样。",
		OperationID: "updateEnvVar",
		Params: []Param{
			{Name: "name", In: "path", Description: "变量名", Type: "", Required: true, Example: "EDITOR"},
		},
		Request: &RequestSpec{
			Description: "要改动的字段",
			Type:        EnvVarUpdateRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "改动后的变量", Type: envs.ManagedVar{}},
		},
	},
	{
		Method: "DELETE", Path: "/api/env/vars/{name}", Tag: "env",
		Summary:     "删除环境变量",
		Description: "从所有层里移除一个受管变量。",
		OperationID: "deleteEnvVar",
		Params: []Param{
			{Name: "name", In: "path", Description: "变量名", Type: "", Required: true, Example: "EDITOR"},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "已删除", Type: EnvVarDeleteResponse{}},
		},
	},
	{
		Method: "POST", Path: "/api/env/vars/{name}/apply", Tag: "env",
		Summary:     "重新下发环境变量",
		Description: "把一个变量重新推送到 launchd 层并回读校验。",
		OperationID: "applyEnvVar",
		Params: []Param{
			{Name: "name", In: "path", Description: "变量名", Type: "", Required: true, Example: "EDITOR"},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "下发后的变量", Type: envs.ManagedVar{}},
		},
	},
	{
		Method: "GET", Path: "/api/env/path", Tag: "env",
		Summary:     "读取 PATH 配置",
		Description: "返回 PATH 的前置、后置片段与合并结果。",
		OperationID: "getEnvPath",
		Responses: []ResponseSpec{
			{Status: "200", Description: "PATH 视图", Type: envs.PathView{}},
		},
		Probe: true,
	},
	{
		Method: "POST", Path: "/api/env/path", Tag: "env",
		Summary:     "保存 PATH 配置",
		Description: "整体写入 PATH 的前置与后置片段。",
		OperationID: "setEnvPath",
		Request: &RequestSpec{
			Description: "PATH 片段",
			Type:        EnvPathSetRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "保存后的 PATH 视图", Type: envs.PathView{}},
		},
	},
	{
		Method: "GET", Path: "/api/env/export", Tag: "env",
		Summary:     "导出环境变量",
		Description: "导出受管变量。默认是 JSON 文档，format=env 时返回 .env 文本。",
		OperationID: "exportEnvVars",
		Params: []Param{
			{Name: "format", In: "query", Description: "env 返回 .env 文本，默认返回 JSON", Type: "", Example: "env"},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "导出的文档", Type: map[string]any{}},
			{Status: "200", Description: "导出的 .env 文本", ContentType: "text/plain", Type: ""},
		},
	},
	{
		Method: "POST", Path: "/api/env/import/preview", Tag: "env",
		Summary:     "预览导入结果",
		Description: "只计算导入会新增和更新哪些变量，不落盘。",
		OperationID: "previewEnvImport",
		Request: &RequestSpec{
			Description: "要导入的文本",
			Type:        EnvImportRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "将会发生的改动", Type: EnvImportPreviewResponse{}},
		},
	},
	{
		Method: "POST", Path: "/api/env/import", Tag: "env",
		Summary:     "导入环境变量",
		Description: "把导入的变量写进受管清单。",
		OperationID: "importEnvVars",
		Request: &RequestSpec{
			Description: "要导入的文本",
			Type:        EnvImportRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "已应用的改动", Type: EnvImportResponse{}},
		},
	},
	{
		Method: "GET", Path: "/api/env/backups", Tag: "env",
		Summary:     "列出备份",
		Description: "列出 ~/.config/mymac/backup 下的环境变量备份。",
		OperationID: "listEnvBackups",
		Responses: []ResponseSpec{
			{Status: "200", Description: "备份列表", Type: EnvBackupsResponse{}},
		},
		Probe: true,
	},
	{
		Method: "POST", Path: "/api/env/restore", Tag: "env",
		Summary:     "恢复备份",
		Description: "用一份备份覆盖当前的环境变量文件。",
		OperationID: "restoreEnvBackup",
		Request: &RequestSpec{
			Description: "备份文件名",
			Type:        EnvRestoreRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "恢复后的文件", Type: EnvRestoreResponse{}},
		},
	},
	{
		Method: "GET", Path: "/api/env/status", Tag: "env",
		Summary:     "环境层健康状况",
		Description: "汇总标记块、登录代理、受管数量和警告。",
		OperationID: "getEnvStatus",
		Responses: []ResponseSpec{
			{Status: "200", Description: "健康状况", Type: EnvStatusResponse{}},
		},
		Probe: true,
	},
	{
		Method: "POST", Path: "/api/env/marker/repair", Tag: "env",
		Summary:     "修复标记块",
		Description: "重写 ~/.zshenv 里的受管标记块。",
		OperationID: "repairEnvMarker",
		Responses: []ResponseSpec{
			{Status: "200", Description: "修复后的标记块报告", Type: envs.MarkerReport{}},
		},
	},
	{
		Method: "POST", Path: "/api/env/marker/remove", Tag: "env",
		Summary:     "移除标记块",
		Description: "从 ~/.zshenv 里删掉受管标记块。",
		OperationID: "removeEnvMarker",
		Responses: []ResponseSpec{
			{Status: "200", Description: "已移除", Type: OKResponse{}},
		},
	},
	{
		Method: "POST", Path: "/api/env/agent/install", Tag: "env",
		Summary:     "安装登录代理",
		Description: "创建登录启动项，让环境变量在登录后依然生效。",
		OperationID: "installEnvAgent",
		Responses: []ResponseSpec{
			{Status: "200", Description: "已安装", Type: OKResponse{}},
		},
	},
	{
		Method: "POST", Path: "/api/env/agent/uninstall", Tag: "env",
		Summary:     "卸载登录代理",
		Description: "移除登录启动项。",
		OperationID: "uninstallEnvAgent",
		Responses: []ResponseSpec{
			{Status: "200", Description: "已卸载", Type: OKResponse{}},
		},
	},

	// ------------------------------------------------------------- procs
	{
		Method: "GET", Path: "/api/procs", Tag: "procs",
		Summary:     "进程列表",
		Description: "返回缓存的进程表。采集在后台进行，这个请求只读缓存。",
		OperationID: "listProcesses",
		Responses: []ResponseSpec{
			{Status: "200", Description: "进程表", Type: ProcListResponse{}},
		},
		Probe: true,
	},
	{
		Method: "GET", Path: "/api/procs/stream", Tag: "procs",
		Summary:     "进程表流",
		Description: "以 Server-Sent Events 推送进程表与系统概览。",
		OperationID: "streamProcesses",
		Params: []Param{
			{Name: "interval", In: "query", Description: "推送间隔毫秒数，可选 1000、2000、5000", Type: 0, Example: 2000},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "事件流", ContentType: "text/event-stream", Type: ProcStreamResponse{}},
		},
	},
	{
		Method: "GET", Path: "/api/procs/{pid}", Tag: "procs",
		Summary:     "进程详情",
		Description: "返回进程的扩展信息，包括打开的文件和环境变量。",
		OperationID: "getProcess",
		Params: []Param{
			{Name: "pid", In: "path", Description: "进程号", Type: 0, Required: true, Example: 1234},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "进程详情", Type: ProcDetailResponse{}},
			{Status: "404", Description: "进程不存在", Type: ErrorResponse{}},
		},
	},
	{
		Method: "GET", Path: "/api/procs/{pid}/sample", Tag: "procs",
		Summary:     "进程采样",
		Description: "抓取一次线程栈采样。",
		OperationID: "sampleProcess",
		Params: []Param{
			{Name: "pid", In: "path", Description: "进程号", Type: 0, Required: true, Example: 1234},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "采样输出", Type: ProcSampleResponse{}},
		},
	},
	{
		Method: "GET", Path: "/api/procs/{pid}/files", Tag: "procs",
		Summary:     "进程打开的文件",
		Description: "列出进程当前打开的文件描述符。",
		OperationID: "listProcessFiles",
		Params: []Param{
			{Name: "pid", In: "path", Description: "进程号", Type: 0, Required: true, Example: 1234},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "文件路径", Type: ProcFilesResponse{}},
		},
	},
	{
		Method: "POST", Path: "/api/procs/{pid}/signal", Tag: "procs",
		Summary:     "发送信号",
		Description: "给进程发信号。杀受系统保护的进程需要 confirmed 为真。",
		OperationID: "signalProcess",
		Params: []Param{
			{Name: "pid", In: "path", Description: "进程号", Type: 0, Required: true, Example: 1234},
		},
		Request: &RequestSpec{
			Description: "信号名，默认为 TERM",
			Type:        ProcSignalRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "已发送", Type: ProcSignalResponse{}},
			{Status: "409", Description: "受保护的进程需要确认", Type: ErrorResponse{}},
		},
	},
	{
		Method: "POST", Path: "/api/procs/{pid}/renice", Tag: "procs",
		Summary:     "调整优先级",
		Description: "修改进程的调度优先级，取值 -20 到 20。",
		OperationID: "reniceProcess",
		Params: []Param{
			{Name: "pid", In: "path", Description: "进程号", Type: 0, Required: true, Example: 1234},
		},
		Request: &RequestSpec{
			Description: "新的优先级",
			Type:        ProcReniceRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "已调整", Type: ProcReniceResponse{}},
		},
	},
	{
		Method: "GET", Path: "/api/system/overview", Tag: "procs",
		Summary:     "系统概览",
		Description: "返回 CPU、内存、磁盘、网络与电池的汇总。",
		OperationID: "getSystemOverview",
		Responses: []ResponseSpec{
			{Status: "200", Description: "系统概览", Type: sysinfo.Overview{}},
		},
		Probe: true,
	},
	{
		Method: "GET", Path: "/api/procs/alerts", Tag: "procs",
		Summary:     "告警阈值",
		Description: "返回 CPU 与内存的告警阈值。",
		OperationID: "getAlertRules",
		Responses: []ResponseSpec{
			{Status: "200", Description: "阈值列表", Type: AlertRulesResponse{}},
		},
		Probe: true,
	},
	{
		Method: "POST", Path: "/api/procs/alerts", Tag: "procs",
		Summary:     "保存告警阈值",
		Description: "整体替换告警阈值列表。",
		OperationID: "updateAlertRules",
		Request: &RequestSpec{
			Description: "新的阈值列表",
			Type:        AlertRulesRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "保存后的阈值列表", Type: AlertRulesResponse{}},
		},
	},

	// ------------------------------------------------------------- ports
	{
		Method: "GET", Path: "/api/ports/snapshot", Tag: "ports",
		Summary:     "端口占用",
		Description: "返回监听端口与占用它们的进程。系统进程默认隐藏，但计数仍然给出。",
		OperationID: "getPortsSnapshot",
		Params: []Param{
			{Name: "include_system", In: "query", Description: "为 1 时连系统进程一起返回", Type: 0, Example: 0},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "端口快照", Type: ports.Snapshot{}},
		},
		Probe: true,
	},
	{
		Method: "POST", Path: "/api/ports/process/{pid}/{action}", Tag: "ports",
		Summary:     "处理端口进程",
		Description: "对占用端口的进程执行 stop、kill 或 restart。",
		OperationID: "handlePortProcess",
		Params: []Param{
			{Name: "pid", In: "path", Description: "进程号", Type: 0, Required: true, Example: 1234},
			{Name: "action", In: "path", Description: "stop、kill 或 restart", Type: "", Required: true, Example: "restart"},
		},
		Request: &RequestSpec{
			Description: "进程的创建时间，用来防止 pid 被复用",
			Type:        PortActionRequest{},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "处理结果", Type: ports.ActionResult{}},
			{Status: "default", Description: "处理失败时的答案", Type: PortActionErrorResponse{}},
		},
	},
	{
		Method: "GET", Path: "/api/ports/rules", Tag: "ports",
		Summary:     "端口规则",
		Description: "返回内置规则与用户规则的汇总。",
		OperationID: "getPortRules",
		Responses: []ResponseSpec{
			{Status: "200", Description: "规则汇总", Type: rules.Summary{}},
		},
		Probe: true,
	},
	{
		Method: "POST", Path: "/api/ports/rules/reload", Tag: "ports",
		Summary:     "重载端口规则",
		Description: "重新读取用户规则文件。",
		OperationID: "reloadPortRules",
		Responses: []ResponseSpec{
			{Status: "200", Description: "重载后的规则汇总", Type: rules.Summary{}},
		},
	},
	{
		Method: "POST", Path: "/api/ports/rules/override", Tag: "ports",
		Summary:     "覆盖端口规则",
		Description: "新增或删除一条用户规则。",
		OperationID: "overridePortRules",
		Request: &RequestSpec{
			Description: "规则",
			Type:        PortRulesOverrideRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "改动后的规则汇总", Type: rules.Summary{}},
		},
	},

	// ------------------------------------------------------- login items
	{
		Method: "GET", Path: "/api/loginitems", Tag: "loginitems",
		Summary:     "启动项列表",
		Description: "返回登录启动项与后台服务。",
		OperationID: "listLoginItems",
		Responses: []ResponseSpec{
			{Status: "200", Description: "启动项", Type: loginitems.List{}},
		},
		Probe: true,
	},
	{
		Method: "POST", Path: "/api/loginitems/state", Tag: "loginitems",
		Summary:     "启用或禁用启动项",
		Description: "打开或关闭一个启动项。",
		OperationID: "setLoginItemState",
		Request: &RequestSpec{
			Description: "启动项与目标状态",
			Type:        LoginItemStateRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "新的状态", Type: LoginItemStateResponse{}},
		},
	},
	{
		Method: "POST", Path: "/api/loginitems/remove", Tag: "loginitems",
		Summary:     "删除启动项",
		Description: "把一个 LaunchAgent 定义移入废纸篓。",
		OperationID: "removeLoginItem",
		Request: &RequestSpec{
			Description: "plist 文件路径",
			Type:        LoginItemRemoveRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "已删除", Type: OKResponse{}},
		},
	},
	{
		Method: "POST", Path: "/api/loginitems/group/state", Tag: "loginitems",
		Summary:     "启用或禁用整个程序",
		Description: "一次开关同一个程序的所有启动项。分组键由列表接口给出。",
		OperationID: "setLoginItemGroupState",
		Request: &RequestSpec{
			Description: "分组键与目标状态",
			Type:        LoginItemGroupStateRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "处理结果", Type: LoginItemGroupStateResponse{}},
		},
	},

	// -------------------------------------------------------------- apps
	{
		Method: "GET", Path: "/api/apps", Tag: "apps",
		Summary:     "已安装应用",
		Description: "返回已安装的应用。体积较小的应用需要后台测量，pending 是还没量完的数量。",
		OperationID: "listApps",
		Params: []Param{
			{Name: "rescan", In: "query", Description: "为 1 时重新扫描", Type: 0, Example: 0},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "应用列表", Type: AppListResponse{}},
		},
	},
	{
		Method: "GET", Path: "/api/apps/icon", Tag: "apps",
		Summary:     "应用图标",
		Description: "按 bundle id 返回应用图标。只有扫描到的应用才会被返回。",
		OperationID: "getAppIcon",
		Params: []Param{
			{Name: "id", In: "query", Description: "应用的 bundle id", Type: "", Required: true, Example: "com.apple.Safari"},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "PNG 图标", ContentType: "image/png"},
		},
	},
	{
		Method: "GET", Path: "/api/apps/{id}/residual", Tag: "apps",
		Summary:     "应用残留",
		Description: "返回单个应用留下的文件与目录。",
		OperationID: "getAppResiduals",
		Params: []Param{
			{Name: "id", In: "path", Description: "应用的 bundle id", Type: "", Required: true, Example: "com.apple.Safari"},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "残留文件", Type: AppResidualResponse{}},
			{Status: "404", Description: "找不到应用", Type: ErrorResponse{}},
		},
	},
	{
		Method: "GET", Path: "/api/apps/residuals", Tag: "apps",
		Summary:     "批量应用残留",
		Description: "一次返回多个应用的残留文件，按应用分组。",
		OperationID: "getAppResidualsGrouped",
		Params: []Param{
			{Name: "ids", In: "query", Description: "逗号分隔的 bundle id 列表", Type: "", Example: "com.apple.Safari"},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "按应用分组的残留文件", Type: AppResidualsResponse{}},
		},
	},
	{
		Method: "POST", Path: "/api/apps/uninstall", Tag: "apps",
		Summary:     "卸载应用残留",
		Description: "把选中的残留文件移入废纸篓。dryRun 为真时只给出计划，不动文件。",
		OperationID: "uninstallApp",
		Request: &RequestSpec{
			Description: "要删除的路径与所属应用",
			Type:        AppUninstallRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "移入废纸篓的结果，试运行时返回计划", OneOf: []any{trash.Result{}, DryRunResponse{}}},
		},
	},
	{
		Method: "GET", Path: "/api/trash/status", Tag: "apps",
		Summary:     "废纸篓可用性",
		Description: "探测 Finder 自动化是否可用。不可用时只能退回 rm。",
		OperationID: "getTrashStatus",
		Responses: []ResponseSpec{
			{Status: "200", Description: "废纸篓状态", Type: trash.Status{}},
		},
	},

	// ------------------------------------------------------------- clean
	{
		Method: "GET", Path: "/api/clean/scan", Tag: "clean",
		Summary:     "可清理项",
		Description: "返回缓存与日志等可清理的内容。",
		OperationID: "scanCleanable",
		Params: []Param{
			{Name: "rescan", In: "query", Description: "为 1 时重新扫描", Type: 0, Example: 0},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "可清理项", Type: cleaner.ScanResult{}},
		},
	},
	{
		Method: "GET", Path: "/api/clean/large", Tag: "clean",
		Summary:     "大文件扫描",
		Description: "启动一次大文件扫描，扫描在后台跑，用 /api/clean/job 查进度。",
		OperationID: "startLargeFileScan",
		Params: []Param{
			{Name: "minSize", In: "query", Description: "最小文件大小，单位字节", Type: 0, Example: 524288000},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "扫描任务", Type: cleaner.Job{}},
		},
	},
	{
		Method: "GET", Path: "/api/clean/duplicates", Tag: "clean",
		Summary:     "重复文件扫描",
		Description: "启动一次重复文件扫描，扫描在后台跑，用 /api/clean/job 查进度。",
		OperationID: "startDuplicateScan",
		Params: []Param{
			{Name: "minSize", In: "query", Description: "最小文件大小，单位字节", Type: 0, Example: 1048576},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "扫描任务", Type: cleaner.Job{}},
		},
	},
	{
		Method: "GET", Path: "/api/clean/job", Tag: "clean",
		Summary:     "扫描任务",
		Description: "按 id 查询扫描任务，缺省时返回该类型最近的一次任务。",
		OperationID: "getCleanJob",
		Params: []Param{
			{Name: "id", In: "query", Description: "任务 id", Type: "", Example: "12"},
			{Name: "kind", In: "query", Description: "任务类型，large-files 或 duplicates", Type: "", Example: "large-files"},
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "扫描任务", Type: cleaner.Job{}},
			{Status: "404", Description: "没有这个任务", Type: ErrorResponse{}},
		},
	},
	{
		Method: "POST", Path: "/api/clean/execute", Tag: "clean",
		Summary:     "执行清理",
		Description: "把选中的项目移入废纸篓。dryRun 为真时只给出计划，不动文件。",
		OperationID: "executeClean",
		Request: &RequestSpec{
			Description: "要清理的路径",
			Type:        CleanExecuteRequest{},
			Required:    true,
		},
		Responses: []ResponseSpec{
			{Status: "200", Description: "移入废纸篓的结果，试运行时返回计划", OneOf: []any{trash.Result{}, DryRunResponse{}}},
		},
	},

	// -------------------------------------------------------------- docs
	{
		Method: "GET", Path: "/api/openapi.json", Tag: "docs",
		Summary:     "接口描述",
		Description: "返回本 API 的 OpenAPI 3.0 描述。",
		OperationID: "getOpenAPI",
		Responses: []ResponseSpec{
			{Status: "200", Description: "OpenAPI 文档", Type: OpenAPIResponse{}},
		},
		Probe: true,
	},
	{
		Method: "GET", Path: "/api/docs", Tag: "docs",
		Summary:     "接口文档页面",
		Description: "返回一个可以浏览并试用接口的页面。",
		OperationID: "getAPIDocs",
		Responses: []ResponseSpec{
			{Status: "200", Description: "文档页面", ContentType: "text/html"},
		},
		Probe: true,
	},
}
