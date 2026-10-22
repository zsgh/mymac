package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"mymac/internal/ai"
	"mymac/internal/api"
	"mymac/internal/errs"
	"mymac/internal/sysinfo"
)

// The names of the analyses. One per page that has something to judge.
const (
	AnalysisDash       = "dash"
	AnalysisApps       = "apps"
	AnalysisClean      = "clean"
	AnalysisProcs      = "procs"
	AnalysisPorts      = "ports"
	AnalysisLoginItems = "loginitems"
	AnalysisEnv        = "env"
)

// analysis is one named analysis: what to look at and what to ask.
//
// Every page has its own prompt: the same model answers a completely
// different question about a cache folder than about a login item.
type analysis struct {
	// name is the name of the analysis.
	name string
	// items builds the list to judge.
	items func(ctx context.Context, s *Server) ([]ai.Item, error)
	// system is the role of the model.
	system string
	// question builds the question from the items.
	question func(items []ai.Item) string
}

// analyses are all the analyses mymac can run.
var analyses = []analysis{
	{
		name: AnalysisDash,
		items: func(ctx context.Context, s *Server) ([]ai.Item, error) {
			o := s.sysinfo.Collect(ctx)
			return []ai.Item{{
				Key:   "overview",
				Name:  "系统概况",
				Kind:  "系统指标",
				State: "运行正常",
				Extra: map[string]string{
					"CPU":  fmt.Sprintf("%.0f%%", o.CPUPercent),
					"内存":   fmt.Sprintf("%.0f%% / %d GB", o.MemPercent, o.MemTotal>>30),
					"内存压力": o.MemPressure,
					"磁盘":   fmt.Sprintf("%.0f%% 已用", o.DiskPercent),
					"网络":   fmt.Sprintf("收 %.1f MB / 发 %.1f MB", float64(o.NetIn)/1048576, float64(o.NetOut)/1048576),
					"已开机":  o.Uptime,
					"进程数":  fmt.Sprint(o.ProcCount),
					"电池":   batteryText(*o),
				},
			}}, nil
		},
		system: "你是一个 macOS 系统助手。用户想知道自己这台机器有什么问题，" +
			"你根据给出的系统指标给出判断和��法。回答用中文，简短直接，不要客套。",
		question: func(items []ai.Item) string {
			return "下面是一台 macOS 的系统指标：\n\n" + ai.ItemList(items) + `
请判断这台机器现在有没有问题，给出结论和建议。只输出一个 JSON 数组：

[{"key": "overview", "level": "high 或 medium 或 low", "label": "两到六个字的结论", "reason": "一句话说明依据", "advice": "一句话建议"}]

level 的意思：high = 有明显问题需要处理，medium = 有点留意，low = 一切正常。`
		},
	},
	{
		name: AnalysisApps,
		items: func(ctx context.Context, s *Server) ([]ai.Item, error) {
			list, err := s.apps.Scan(ctx, false)
			if err != nil {
				return nil, err
			}
			out := make([]ai.Item, 0, len(list))
			for _, a := range list {
				if a.IsSystem {
					continue
				}
				out = append(out, ai.Item{
					Key:   a.BundleID,
					Name:  a.Name,
					Kind:  "应用",
					Path:  a.Path,
					Size:  int64(a.Size),
					Extra: map[string]string{"版本": a.Version, "开发者": a.Developer},
				})
			}
			return out, nil
		},
		system: "你是一个 macOS 系统助手，帮用户判断哪些应用该留着、哪些可以卸掉。" +
			"你只看得到清单里的事实，不要臆测。回答用中文，简短直接。",
		question: func(items []ai.Item) string {
			return "下面是这台 macOS 上已安装的应用：\n\n" + ai.ItemList(items) + `
请判断每个应用值不值得留着。只输出一个 JSON 数组：

[{"key": "原样的 bundle id", "level": "high 或 medium 或 low", "label": "两到六个字的结论", "reason": "一句话说明依据", "advice": "一句话建议"}]

level 的意思：high = 建议卸载（占地方又没什么用），medium = 看情况（你自己决定用不用），
low = 建议留着（常用的工具）。
不要建议卸载系统自带的东西。`
		},
	},
	{
		name: AnalysisClean,
		items: func(ctx context.Context, s *Server) ([]ai.Item, error) {
			res := s.cleaner.Scan(ctx, false)
			out := make([]ai.Item, 0, len(res.Items))
			for _, it := range res.Items {
				out = append(out, ai.Item{
					Key:   it.Path,
					Name:  it.Name,
					Kind:  it.Category,
					Path:  it.Path,
					Size:  int64(it.Size),
					Extra: map[string]string{"说明": it.Note, "只读": boolText(it.ReadOnly)},
				})
			}
			return out, nil
		},
		system: "你是一个 macOS 系统助手，帮用户判断哪些缓存和日志可以放心删。" +
			"你只看得到清单里的事实，不要臆测。回答用中文，简短直接。",
		question: func(items []ai.Item) string {
			return "下面是这台 macOS 上可以清理的内容：\n\n" + ai.ItemList(items) + `
请判断每一项删掉安不安全。只输出一个 JSON 数组：

[{"key": "原样的路径", "level": "high 或 medium 或 low", "label": "两到六个字的结论", "reason": "一句话说明依据", "advice": "一句话建议"}]

level 的意思：high = 别删（系统要用的、删了会出问题），medium = 可以删但要注意，
low = 放心删（缓存、日志之类的垃圾）。`
		},
	},
	{
		name: AnalysisProcs,
		items: func(ctx context.Context, s *Server) ([]ai.Item, error) {
			coll := s.procs.Snapshot(ctx)
			groups := map[string]*ai.Item{}
			for _, p := range coll.Processes {
				if p.Name == "" {
					continue
				}
				it, ok := groups[p.Name]
				if !ok {
					it = &ai.Item{Key: p.Name, Name: p.Name, Kind: "进程", Path: p.Path}
					groups[p.Name] = it
				}
				it.Extra = extraOf(it.Extra, "个数", fmt.Sprint(countOf(groups, p.Name)))
				if len(p.Args) > 0 {
					it.Extra = extraOf(it.Extra, "参数", strings.Join(p.Args, " "))
				}
			}
			out := make([]ai.Item, 0, len(groups))
			for _, it := range groups {
				out = append(out, *it)
			}
			// The busiest programs come first: they are the ones worth
			// looking at.
			sort.Slice(out, func(i, j int) bool {
				return out[i].Extra["个数"] > out[j].Extra["个数"]
			})
			if len(out) > maxExplainedPrograms {
				out = out[:maxExplainedPrograms]
			}
			return out, nil
		},
		system: "你是一个 macOS 系统助手。用户看不懂进程表里那些进程是干什么的，" +
			"你的任务是看路径和命令行，告诉他每个进程是什么、在替谁干活。" +
			"你看不到的东西不要臆测，回答用中文，简短直接。",
		question: func(items []ai.Item) string {
			return "下面是这台 macOS 上正在跑的进程，按可执行文件名分组：\n\n" + ai.ItemList(items) + `
请解释每一组进程是干什么的。只输出一个 JSON 数组：

[{"key": "原样的名字", "level": "high 或 medium 或 low", "label": "两到六个字的结论", "reason": "一句话说明它是什么", "advice": "一句话说明它在替用户做什么"}]

level 的意思：high = 来路不明或者危险，medium = 需要留意（自动更新器、来路不明的脚本），
low = 正常（系统组件、知名应用的子进程、用户自己跑的开发工具）。`
		},
	},
	{
		name: AnalysisPorts,
		items: func(ctx context.Context, s *Server) ([]ai.Item, error) {
			snap := s.ports.Snapshot(ctx)
			out := make([]ai.Item, 0, len(snap.Processes))
			for _, p := range snap.Processes {
				if p.Decision.IsSystem {
					continue
				}
				ports := []string{}
				for _, c := range p.Connections {
					if c.IsListen {
						ports = append(ports, fmt.Sprintf("%s:%d", c.Address, c.Port))
					}
				}
				out = append(out, ai.Item{
					Key:   fmt.Sprintf("%d", p.PID),
					Name:  p.Name,
					Kind:  "端口占用",
					Path:  p.Exe,
					Extra: map[string]string{"用户": p.Username, "监听": strings.Join(ports, ", ")},
				})
			}
			return out, nil
		},
		system: "你是一个 macOS 系统助手，帮用户判断这些端口占用正不正常。" +
			"你只看得到清单里的事实，不要臆测。回答用中文，简短直接。",
		question: func(items []ai.Item) string {
			return "下面是这台 macOS 上监听端口的进程：\n\n" + ai.ItemList(items) + `
请判断每一个占用正不正常。只输出一个 JSON 数组：

[{"key": "原样的 PID", "level": "high 或 medium 或 low", "label": "两到六个字的结论", "reason": "一句话说明依据", "advice": "一句话建议"}]

level 的意思：high = 可疑（陌生的后台在监听、藏起来的路径），
medium = 需要留意（自动更新器、自己开的开发服务），low = 正常（系统服务、常用应用）。`
		},
	},
	{
		name: AnalysisLoginItems,
		items: func(ctx context.Context, s *Server) ([]ai.Item, error) {
			list := s.loginItems.Collect(ctx)
			out := make([]ai.Item, 0, len(list.Items))
			for _, it := range list.Items {
				if it.IsSystem {
					continue
				}
				out = append(out, ai.Item{
					Key:   it.Label,
					Name:  it.Label,
					Kind:  string(it.Kind),
					Path:  it.Program,
					State: it.Source,
					Extra: map[string]string{"运行中": boolText(it.Loaded)},
				})
			}
			return out, nil
		},
		system: "你是一个 macOS 系统安全助手。你的任务是评估「开机自启项」的风险。" +
			"你只看得到给到你的事实（标签、类型、程序路径、来源目录、是否在运行），" +
			"看不到的内容不要臆测。回答用中文，简短直接。",
		question: func(items []ai.Item) string {
			return "下面是一台 macOS 上的启动项清单：\n\n" + ai.ItemList(items) + `
请评估每个启动项「该不该被信任」。只输出一个 JSON 数组：

[{"key": "原样的标签", "level": "high 或 medium 或 low", "label": "两到六个字的结论", "reason": "一句话说明依据", "advice": "一句话建议"}]

level 的意思：high = 来源可疑或行为危险（以 root 运行的可疑程序、开发者与路径对不上、
藏在用户目录或临时目录里的可执行文件、名字伪装成系统组件的服务），
medium = 需要留意（自动更新器、装在 ~/Library 下的 helper、没有 plist 的服务），
low = 可信（macOS 自带、装在 /Applications 下的应用自己的组件）。`
		},
	},
	{
		name: AnalysisEnv,
		items: func(ctx context.Context, s *Server) ([]ai.Item, error) {
			snap, err := s.env.Snapshot(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]ai.Item, 0, len(snap.Managed))
			for _, v := range snap.Managed {
				value := v.Value
				if v.Sensitive {
					value = "（敏感值已隐藏）"
				}
				out = append(out, ai.Item{
					Key:   v.Name,
					Name:  v.Name,
					Kind:  "环境变量",
					Path:  string(v.Scope),
					State: value,
					Extra: map[string]string{"备注": v.Note, "已写入 shell": boolText(v.ShellApplied)},
				})
			}
			return out, nil
		},
		system: "你是一个 macOS 系统助手，帮用户检查环境变量有没有问题。" +
			"你只看得到清单里的事实，不要臆测。回答用中文，简短直接。",
		question: func(items []ai.Item) string {
			return "下面是这台 macOS 上受管理的环境变量：\n\n" + ai.ItemList(items) + `
请检查每一个变量有没有问题。只输出一个 JSON 数组：

[{"key": "原样的变量名", "level": "high 或 medium 或 low", "label": "两到六个字的结论", "reason": "一句话说明依据", "advice": "一句话建议"}]

level 的意思：high = 有问题（指向可疑目录、会被别的程序覆盖、明显重复了），
medium = 有点留意（命名不规范、可能和别的工具冲突），low = 正常。`
		},
	},
}

// maxExplainedPrograms is how many groups of processes are explained.
const maxExplainedPrograms = 40

// analysisByName finds an analysis.
func analysisByName(name string) (analysis, bool) {
	for _, a := range analyses {
		if a.name == name {
			return a, true
		}
	}
	return analysis{}, false
}

// AnalysisNames returns the names of all the analyses.
func AnalysisNames() []string {
	out := make([]string, 0, len(analyses))
	for _, a := range analyses {
		out = append(out, a.name)
	}
	sort.Strings(out)
	return out
}

// analysisRunner runs the analyses in the background.
type analysisRunner struct {
	mu sync.Mutex
	// running marks the analyses that are in flight.
	running map[string]bool
	// errors holds why an analysis failed.
	errors map[string]string
	// started is when an analysis was started.
	started map[string]time.Time
	// unmatched counts the verdicts that could not be placed on an item.
	unmatched map[string]int
}

// start runs an analysis in the background.
//
// The mode decides what the model is asked about: "full" asks about
// everything, "cache" only about the items that have no verdict yet.
func (s *Server) startAnalysis(ctx context.Context, name, mode string) error {
	a, ok := analysisByName(name)
	if !ok {
		return errs.New(errs.KindInvalid, "没有这个分析：%s", name)
	}
	client := s.aiClient()
	if client == nil {
		return errs.New(errs.KindInvalid, "还没有配置大模型")
	}
	s.runner.mu.Lock()
	if s.runner.running[name] {
		s.runner.mu.Unlock()
		return nil
	}
	s.runner.running[name] = true
	s.runner.errors[name] = ""
	s.runner.started[name] = time.Now()
	s.runner.mu.Unlock()

	items, err := a.items(ctx, s)
	if err != nil {
		s.runner.mu.Lock()
		s.runner.running[name] = false
		s.runner.errors[name] = err.Error()
		s.runner.mu.Unlock()
		return err
	}

	// The cached verdicts are kept unless the user asked for a full
	// rebuild; the model is only asked about the rest.
	if mode != ModeFull {
		cached, _ := s.aiCache.Get(name)
		known := map[string]bool{}
		for _, v := range cached.Items {
			known[v.Key] = true
		}
		var fresh []ai.Item
		for _, it := range items {
			if !known[it.Key] {
				fresh = append(fresh, it)
			}
		}
		items = fresh
	}
	if len(items) == 0 {
		s.runner.mu.Lock()
		s.runner.running[name] = false
		s.runner.mu.Unlock()
		return nil
	}

	go func() {
		// The request that started the analysis is over long before the
		// model is; the work must not die with it.
		//
		// The items go in batches: a long list takes minutes on a
		// thinking model, and the verdicts show up as the batches come
		// in instead of all at the end.
		for _, batch := range batches(items, analysisBatchSize) {
			verdicts, err := client.Judge(context.WithoutCancel(ctx), a.system, a.question(batch), batch)
			if err != nil {
				s.runner.mu.Lock()
				s.runner.running[name] = false
				s.runner.errors[name] = err.Error()
				s.runner.mu.Unlock()
				return
			}
			resolved, dropped := resolveVerdicts(batch, verdicts)
			if _, err := s.aiCache.Merge(name, resolved, client.Model()); err != nil {
				s.runner.mu.Lock()
				s.runner.errors[name] = err.Error()
				s.runner.mu.Unlock()
				return
			}
			s.runner.mu.Lock()
			s.runner.unmatched[name] += dropped
			s.runner.mu.Unlock()
		}
		s.runner.mu.Lock()
		s.runner.running[name] = false
		s.runner.mu.Unlock()
	}()
	return nil
}

// analysisBatchSize is how many items are asked about at once.
//
// A batch is small enough that the model answers in a minute or two, and big
// enough that the answer is still worth the tokens.
const analysisBatchSize = 20

// batches splits the items into batches.
func batches(items []ai.Item, size int) [][]ai.Item {
	if size <= 0 || len(items) <= size {
		return [][]ai.Item{items}
	}
	var out [][]ai.Item
	for len(items) > size {
		out = append(out, items[:size])
		items = items[size:]
	}
	if len(items) > 0 {
		out = append(out, items)
	}
	return out
}

// resolveVerdicts puts the verdicts on the items they belong to.
//
// The model is asked to copy the identifier of the item, but a model is a
// model: it sometimes answers with the name instead. A verdict that cannot
// be placed is dropped rather than shown on the wrong item.
func resolveVerdicts(items []ai.Item, verdicts []ai.Verdict) ([]ai.Verdict, int) {
	byKey := map[string]string{}
	byName := map[string]string{}
	for _, it := range items {
		byKey[strings.ToLower(it.Key)] = it.Key
		byName[strings.ToLower(it.Name)] = it.Key
	}
	out := make([]ai.Verdict, 0, len(verdicts))
	seen := map[string]bool{}
	dropped := 0
	for _, v := range verdicts {
		key := strings.ToLower(strings.TrimSpace(v.Key))
		resolved := ""
		switch {
		case byKey[key] != "":
			resolved = byKey[key]
		case byName[key] != "":
			resolved = byName[key]
		default:
			// A prefix is enough: the model sometimes cuts the name
			// short.
			for _, it := range items {
				if key == "" || !strings.HasPrefix(strings.ToLower(it.Name), key) {
					continue
				}
				if resolved != "" {
					// Two items match: neither is certain.
					resolved = ""
					break
				}
				resolved = it.Key
			}
		}
		if resolved == "" || seen[resolved] {
			dropped++
			continue
		}
		seen[resolved] = true
		v.Key = resolved
		out = append(out, v)
	}
	return out, dropped
}

// analysisStates returns the state of every analysis.
func (s *Server) analysisStates() []api.AIAnalysisState {
	out := make([]api.AIAnalysisState, 0, len(analyses))
	for _, a := range analyses {
		st := api.AIAnalysisState{Name: a.name}
		s.runner.mu.Lock()
		st.Running = s.runner.running[a.name]
		st.Error = s.runner.errors[a.name]
		s.runner.mu.Unlock()
		if entry, ok := s.aiCache.Get(a.name); ok {
			st.Count = len(entry.Items)
			st.At = entry.At
			st.Model = entry.Model
		}
		s.runner.mu.Lock()
		st.Unmatched = s.runner.unmatched[a.name]
		s.runner.mu.Unlock()
		out = append(out, st)
	}
	return out
}

// verdictOf returns the verdict of one item, if there is one.
func (s *Server) verdictOf(name, key string) *ai.Verdict {
	entry, ok := s.aiCache.Get(name)
	if !ok {
		return nil
	}
	v, ok := entry.VerdictOf(key)
	if !ok {
		return nil
	}
	return &v
}

// The modes of an analysis.
const (
	// ModeFull asks the model about everything again.
	ModeFull = "full"
	// ModeCache only asks about the items that have no verdict yet.
	ModeCache = "cache"
)

// helper functions for the item lists.

// extraOf sets a value in the extra fields.
func extraOf(m map[string]string, key, value string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	m[key] = value
	return m
}

// countOf counts the processes of one name.
func countOf(groups map[string]*ai.Item, name string) int {
	it, ok := groups[name]
	if !ok {
		return 0
	}
	n := 0
	fmt.Sscanf(it.Extra["个数"], "%d", &n)
	return n + 1
}

// boolText renders a boolean the way a person would say it.
func boolText(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

// batteryText describes the battery.
func batteryText(o sysinfo.Overview) string {
	if o.Battery == nil {
		return "没有电池"
	}
	if o.Charging {
		return fmt.Sprintf("充电中 %d%%", *o.Battery)
	}
	return fmt.Sprintf("%d%%", *o.Battery)
}
