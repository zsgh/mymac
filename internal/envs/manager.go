package envs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mymac/internal/ai"
	"mymac/internal/audit"
	"mymac/internal/config"
	"mymac/internal/errs"
)

// RunningProcess identifies a process that is already running.
type RunningProcess struct {
	PID       int
	Name      string
	StartTime time.Time
}

// ProcessLister reports the running processes that might hold a stale copy
// of an environment variable. It is satisfied by the procs package.
type ProcessLister interface {
	// StartedBefore returns the processes that started before the given
	// time and are still running.
	StartedBefore(t time.Time) []RunningProcess
}

// Manager is the high level environment variable API.
type Manager struct {
	storePath string
	envSh     string
	lc        Launchctl
	audit     *audit.Logger
	procs     ProcessLister
	agent     *AgentManager
}

// Options configures a Manager.
type Options struct {
	StorePath string
	EnvSh     string
	Launchctl Launchctl
	Audit     *audit.Logger
	Processes ProcessLister
	// LaunchctlBinary is the launchctl executable used for bootstrap /
	// bootout / print. Tests point it at a stub.
	LaunchctlBinary string
}

// NewManager returns a manager wired to the real filesystem and launchctl.
func NewManager(opts Options) *Manager {
	if opts.StorePath == "" {
		opts.StorePath = config.EnvFile()
	}
	if opts.EnvSh == "" {
		opts.EnvSh = config.EnvSh()
	}
	if opts.Launchctl == nil {
		opts.Launchctl = NewSystemLaunchctl()
	}
	if opts.Audit == nil {
		opts.Audit = audit.New(config.AuditLog())
	}
	return &Manager{
		storePath: opts.StorePath,
		envSh:     opts.EnvSh,
		lc:        opts.Launchctl,
		audit:     opts.Audit,
		procs:     opts.Processes,
		agent:     NewAgentManagerWithBinary(opts.Launchctl, opts.LaunchctlBinary),
	}
}

// SetProcessLister attaches the process list used for "needs restart" hints.
func (m *Manager) SetProcessLister(p ProcessLister) { m.procs = p }

// Store loads the store from disk.
func (m *Manager) Store() (*Store, error) { return Load(m.storePath) }

// ------------------------------------------------------- layer status view

// ManagedVar is a managed variable enriched with the state of each layer.
type ManagedVar struct {
	Var
	// AI is what the model said about the variable, if it was asked.
	AI           *ai.Verdict `json:"ai,omitempty"`
	ShellApplied bool        `json:"shellApplied"`
	LaunchdValue string      `json:"launchdValue"`
	LaunchdSet   bool        `json:"launchdSet"`
	LaunchdOK    bool        `json:"launchdOk"`
	Conflicts    []Conflict  `json:"conflicts"`
	NeedsRestart []string    `json:"needsRestart,omitempty"`
}

// Conflict describes another definition of the same variable.
type Conflict struct {
	Source string `json:"source"`
	Value  string `json:"value"`
	Wins   bool   `json:"wins"`
	Note   string `json:"note,omitempty"`
}

// LaunchdVar is one variable from the launchd GUI domain.
type LaunchdVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SelfVar is one variable of the running mymac process.
type SelfVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Snapshot is the full multi-layer view of the environment.
type Snapshot struct {
	Managed    []ManagedVar  `json:"managed"`
	Launchd    []LaunchdVar  `json:"launchd"`
	ShellFiles []ShellExport `json:"shellFiles"`
	Self       []SelfVar     `json:"self"`
	Warnings   []string      `json:"warnings"`
	Agent      AgentState    `json:"agent"`
	Marker     MarkerReport  `json:"marker"`
}

// Snapshot builds the multi-layer view.
func (m *Manager) Snapshot(ctx context.Context) (*Snapshot, error) {
	store, err := m.Store()
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{
		Managed:  []ManagedVar{},
		Launchd:  []LaunchdVar{},
		Self:     []SelfVar{},
		Warnings: []string{},
	}

	// launchd layer.
	launchdVars, err := m.lc.List(ctx)
	if err != nil {
		snap.Warnings = append(snap.Warnings, "无法读取 launchd 环境: "+err.Error())
	}
	for k, v := range launchdVars {
		snap.Launchd = append(snap.Launchd, LaunchdVar{Name: k, Value: v})
	}
	sort.Slice(snap.Launchd, func(i, j int) bool { return snap.Launchd[i].Name < snap.Launchd[j].Name })

	// Shell configuration files (read only).
	shellExports := m.shellExports()
	snap.ShellFiles = shellExports

	// Managed variables with their per-layer state.
	shellApplied := m.shellAppliedNames()
	running := m.staleProcesses()
	for _, v := range store.Vars {
		mv := ManagedVar{Var: v}
		mv.ShellApplied = shellApplied[v.Name]
		mv.LaunchdValue = launchdVars[v.Name]
		mv.LaunchdSet = launchdVars[v.Name] != "" || containsKey(launchdVars, v.Name)
		mv.LaunchdOK = mv.LaunchdSet && launchdVars[v.Name] == v.Value
		mv.Conflicts = detectConflicts(v, shellExports, launchdVars)
		if v.Enabled && !v.UpdatedAt.IsZero() {
			mv.NeedsRestart = staleNamesBefore(running, v.UpdatedAt)
		}
		snap.Managed = append(snap.Managed, mv)
	}
	sort.Slice(snap.Managed, func(i, j int) bool { return snap.Managed[i].Name < snap.Managed[j].Name })

	// The running process environment (reference only).
	for _, e := range os.Environ() {
		i := strings.IndexByte(e, '=')
		if i <= 0 {
			continue
		}
		snap.Self = append(snap.Self, SelfVar{Name: e[:i], Value: e[i+1:]})
	}
	sort.Slice(snap.Self, func(i, j int) bool { return snap.Self[i].Name < snap.Self[j].Name })

	snap.Agent = m.agent.State(ctx)

	// Health of the .zshenv marker block.
	rep, err := InspectMarker(config.ZshEnv())
	if err == nil {
		snap.Marker = rep
		if rep.State != MarkerOK {
			snap.Warnings = append(snap.Warnings,
				"~/.zshenv 的 mymac 标记块异常："+rep.Detail+"。可在权限页或环境变量页执行「修复标记块」。")
		}
	}

	return snap, nil
}

// staleProcesses returns the running processes, so the UI can tell the user
// which applications still hold the old value.
func (m *Manager) staleProcesses() []RunningProcess {
	if m.procs == nil {
		return nil
	}
	return m.procs.StartedBefore(time.Time{})
}

// staleNamesBefore returns the distinct names of the processes that started
// before the given time.
func staleNamesBefore(list []RunningProcess, t time.Time) []string {
	if t.IsZero() {
		return nil
	}
	names := make([]string, 0, 8)
	seen := map[string]bool{}
	for _, p := range list {
		if p.Name == "" || seen[p.Name] {
			continue
		}
		seen[p.Name] = true
		names = append(names, p.Name)
	}
	return names
}

func containsKey(m map[string]string, k string) bool {
	_, ok := m[k]
	return ok
}

// shellFiles are the configuration files scanned for exports.
func (m *Manager) shellFiles() []string {
	home := config.Home()
	files := []string{
		filepath.Join(home, ".zshenv"),
		filepath.Join(home, ".zprofile"),
		filepath.Join(home, ".zshrc"),
	}
	return files
}

func (m *Manager) shellExports() []ShellExport {
	var out []ShellExport
	for _, f := range m.shellFiles() {
		out = append(out, ParseShellExports(f)...)
	}
	return out
}

// detectConflicts finds other definitions of the same variable.
func detectConflicts(v Var, shellExports []ShellExport, launchd map[string]string) []Conflict {
	var out []Conflict
	for _, se := range shellExports {
		if se.Name != v.Name {
			continue
		}
		// Our block lives in .zshenv, which zsh reads before .zprofile and
		// .zshrc, so a later file wins over us.
		wins := filepath.Base(se.File) != ".zshenv" || se.File != filepath.Join(config.Home(), ".zshenv")
		out = append(out, Conflict{
			Source: filepath.Base(se.File) + ":" + itoa(se.Line),
			Value:  se.Value,
			Wins:   wins,
			Note:   "shell 配置中也有同名变量",
		})
	}
	if lv, ok := launchd[v.Name]; ok && lv != v.Value {
		out = append(out, Conflict{
			Source: "launchd",
			Value:  lv,
			Wins:   false,
			Note:   "launchd 中的值与 mymac 记录不一致",
		})
	}
	return out
}

// shellAppliedNames reads back env.sh and returns the variables it exports.
func (m *Manager) shellAppliedNames() map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(m.envSh)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "export ") {
			continue
		}
		rest := strings.TrimPrefix(line, "export ")
		i := strings.IndexByte(rest, '=')
		if i <= 0 {
			continue
		}
		out[rest[:i]] = true
	}
	return out
}

// ------------------------------------------------------------- mutations

// Create adds a new variable and pushes it to every layer.
func (m *Manager) Create(ctx context.Context, name, value, note string) (*ManagedVar, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if name == "PATH" {
		return nil, errs.New(errs.KindInvalid, "PATH 请使用专门的 PATH 编辑器管理")
	}
	store, err := m.Store()
	if err != nil {
		return nil, err
	}
	if _, exists := store.Find(name); exists {
		return nil, errs.New(errs.KindInvalid, "变量 %s 已存在", name)
	}
	store.Upsert(Var{Name: name, Value: value, Note: note, Enabled: true, Scope: ScopeUser})
	if err := store.Save(); err != nil {
		return nil, err
	}
	if err := m.syncAll(ctx, store); err != nil {
		return nil, err
	}
	m.audit.Log(audit.Entry{
		Action: audit.ActionEnvSet,
		Target: name,
		Detail: describeValue(name, value),
		Result: audit.ResultOK,
	})
	return m.managedVar(ctx, name)
}

// Update changes the value, note or enabled flag of a variable.
func (m *Manager) Update(ctx context.Context, name string, value *string, note *string, enabled *bool) (*ManagedVar, error) {
	store, err := m.Store()
	if err != nil {
		return nil, err
	}
	v, ok := store.Find(name)
	if !ok {
		return nil, errs.New(errs.KindNotFound, "变量 %s 不存在", name)
	}
	old := v.Value
	if value != nil {
		v.Value = *value
	}
	if note != nil {
		v.Note = *note
	}
	if enabled != nil {
		v.Enabled = *enabled
	}
	store.Upsert(v)
	if err := store.Save(); err != nil {
		return nil, err
	}
	if err := m.syncAll(ctx, store); err != nil {
		return nil, err
	}
	action := audit.ActionEnvSet
	if enabled != nil && !*enabled {
		action = audit.ActionEnvDelete
	}
	m.audit.Log(audit.Entry{
		Action: action,
		Target: name,
		Detail: describeValue(name, v.Value) + " (原值 " + describeValue(name, old) + ")",
		Result: audit.ResultOK,
	})
	return m.managedVar(ctx, name)
}

// Delete removes a variable from every layer.
func (m *Manager) Delete(ctx context.Context, name string) error {
	store, err := m.Store()
	if err != nil {
		return err
	}
	if _, ok := store.Find(name); !ok {
		return errs.New(errs.KindNotFound, "变量 %s 不存在", name)
	}
	store.Remove(name)
	if err := store.Save(); err != nil {
		return err
	}
	// Clean the launchd layer.
	if err := m.lc.Unsetenv(ctx, name); err != nil {
		m.audit.Log(audit.Entry{Action: audit.ActionEnvDelete, Target: name,
			Detail: "launchctl unsetenv 失败: " + err.Error(), Result: audit.ResultFailed})
	}
	if err := m.syncAll(ctx, store); err != nil {
		return err
	}
	m.audit.Log(audit.Entry{
		Action: audit.ActionEnvDelete,
		Target: name,
		Detail: "已从 env.json / env.sh 移除并 launchctl unsetenv",
		Result: audit.ResultOK,
	})
	return nil
}

// Reapply pushes one variable to the launchd layer again.
func (m *Manager) Reapply(ctx context.Context, name string) (*ManagedVar, error) {
	store, err := m.Store()
	if err != nil {
		return nil, err
	}
	v, ok := store.Find(name)
	if !ok {
		return nil, errs.New(errs.KindNotFound, "变量 %s 不存在", name)
	}
	if v.Enabled {
		res, err := Apply(ctx, m.lc, v.Name, v.Value)
		if err != nil {
			return nil, err
		}
		if !res.Applied {
			return nil, errs.New(errs.KindInternal, "%s 未能生效: %s", v.Name, res.Message)
		}
		v.LastApplied = time.Now()
		store.Upsert(v)
		if err := store.Save(); err != nil {
			return nil, err
		}
	}
	m.audit.Log(audit.Entry{Action: audit.ActionEnvApply, Target: name, Result: audit.ResultOK})
	return m.managedVar(ctx, name)
}

func describeValue(name, value string) string {
	if IsSensitive(name) {
		return name + "=***"
	}
	return name + "=" + value
}

func (m *Manager) managedVar(ctx context.Context, name string) (*ManagedVar, error) {
	snap, err := m.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range snap.Managed {
		if v.Name == name {
			return &v, nil
		}
	}
	return nil, errs.New(errs.KindNotFound, "变量 %s 不存在", name)
}

// ------------------------------------------------------------ layer syncing

// syncAll rewrites the shell layer and pushes every enabled variable to
// launchd.
func (m *Manager) syncAll(ctx context.Context, store *Store) error {
	if err := m.writeShellLayer(store); err != nil {
		return err
	}
	return m.pushLaunchd(ctx, store)
}

// writeShellLayer regenerates env.sh and makes sure the rc file sources it.
func (m *Manager) writeShellLayer(store *Store) error {
	content := RenderEnvShWithPath(store.Active(), store.Path)
	if err := WriteFileAtomic(m.envSh, content, 0o600); err != nil {
		return err
	}
	_, err := UpsertShellFile(config.ZshEnv(), MarkerBlock())
	return err
}

// pushLaunchd sets every enabled variable and unsets the ones that are gone
// or disabled.
func (m *Manager) pushLaunchd(ctx context.Context, store *Store) error {
	want := map[string]string{}
	for _, v := range store.Active() {
		if v.Name == "PATH" {
			continue
		}
		want[v.Name] = v.Value
	}

	// Clear variables we used to manage but no longer want.
	previous, err := m.previousLaunchdNames(ctx)
	if err == nil {
		for name := range previous {
			if _, keep := want[name]; !keep {
				if err := m.lc.Unsetenv(ctx, name); err == nil {
					m.audit.Log(audit.Entry{Action: audit.ActionEnvDelete, Target: name,
						Detail: "已停用，launchctl unsetenv", Result: audit.ResultOK})
				}
			}
		}
	}

	for name, value := range want {
		if _, err := Apply(ctx, m.lc, name, value); err != nil {
			m.audit.Log(audit.Entry{Action: audit.ActionEnvSet, Target: name,
				Detail: "launchctl setenv 失败: " + err.Error(), Result: audit.ResultFailed})
			continue
		}
	}
	return nil
}

// previousLaunchdNames remembers which names mymac had set, so a delete can
// unset them even when the store no longer lists them.
func (m *Manager) previousLaunchdNames(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	all, err := m.lc.List(ctx)
	if err != nil {
		return out, err
	}
	store, err := m.Store()
	if err != nil {
		return out, err
	}
	for _, v := range store.Vars {
		if !v.Enabled {
			if _, ok := all[v.Name]; ok {
				out[v.Name] = true
			}
		}
	}
	return out, nil
}

// ------------------------------------------------------------------- PATH

// PathView is the PATH editor data.
type PathView struct {
	Enabled bool     `json:"enabled"`
	Prepend []string `json:"prepend"`
	Append  []string `json:"append"`
	Current string   `json:"current"`
	Merged  string   `json:"merged"`
	Missing []string `json:"missing"`
	Note    string   `json:"note"`
}

// Path returns the current PATH configuration.
func (m *Manager) Path(ctx context.Context) (*PathView, error) {
	store, err := m.Store()
	if err != nil {
		return nil, err
	}
	view := &PathView{
		Enabled: store.Path.Enabled,
		Prepend: append([]string{}, store.Path.Prepend...),
		Append:  append([]string{}, store.Path.Append...),
	}
	if len(view.Prepend) == 0 {
		view.Prepend = []string{}
	}
	if len(view.Append) == 0 {
		view.Append = []string{}
	}
	view.Current = m.currentPath(ctx)
	merged, _, err := m.mergedPath(store)
	if err == nil {
		view.Merged = merged
	}
	view.Missing = missingDirs(view.Prepend, view.Append)
	view.Note = "PATH 不会被整体覆盖：mymac 只把上面的片段加到系统 PATH 的前面/后面。"
	return view, nil
}

// SetPath replaces the PATH fragment configuration.
func (m *Manager) SetPath(ctx context.Context, prepend, append_ []string) (*PathView, error) {
	store, err := m.Store()
	if err != nil {
		return nil, err
	}
	pre, err := cleanSegments(prepend)
	if err != nil {
		return nil, err
	}
	ap, err := cleanSegments(append_)
	if err != nil {
		return nil, err
	}
	store.Path.Prepend = pre
	store.Path.Append = ap
	store.Path.Enabled = len(store.Path.Prepend) > 0 || len(store.Path.Append) > 0
	if err := store.Save(); err != nil {
		return nil, err
	}
	if err := m.syncAll(ctx, store); err != nil {
		return nil, err
	}
	m.audit.Log(audit.Entry{
		Action: audit.ActionEnvPath,
		Target: "PATH",
		Detail: "prepend=" + strings.Join(store.Path.Prepend, ":") + " append=" + strings.Join(store.Path.Append, ":"),
		Result: audit.ResultOK,
	})
	return m.Path(ctx)
}

// currentPath returns the PATH the launchd GUI domain sees.
func (m *Manager) currentPath(ctx context.Context) string {
	if v, err := m.lc.Getenv(ctx, "PATH"); err == nil && v != "" {
		return v
	}
	return os.Getenv("PATH")
}

// mergedPath computes the PATH mymac would produce, for display only.
//
// The value is never written literally: env.sh uses the dynamic
// prepend/append form so a PATH that changes later is still respected, and
// the launchd PATH is never touched at all.
func (m *Manager) mergedPath(store *Store) (string, bool, error) {
	base := m.currentPath(context.Background())
	if !store.Path.Enabled || (len(store.Path.Prepend) == 0 && len(store.Path.Append) == 0) {
		return "", false, nil
	}
	segments := append([]string{}, store.Path.Prepend...)
	segments = append(segments, splitPath(base)...)
	segments = append(segments, store.Path.Append...)
	merged := dedupe(segments)
	if len(merged) == 0 {
		return "", false, nil
	}
	return strings.Join(merged, ":"), true, nil
}

func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, ":")
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// cleanSegments trims, de-duplicates and validates PATH fragments.
func cleanSegments(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if strings.Contains(s, ":") {
			return nil, errs.New(errs.KindInvalid, "PATH 片段 %q 不能包含冒号，请拆成多项", s)
		}
		out = append(out, strings.TrimSuffix(s, "/"))
	}
	return dedupe(out), nil
}

// missingDirs lists the configured segments that do not exist on disk.
func missingDirs(groups ...[]string) []string {
	var out []string
	for _, g := range groups {
		for _, dir := range g {
			if dir == "" {
				continue
			}
			if _, err := os.Stat(dir); err != nil {
				out = append(out, dir)
			}
		}
	}
	return out
}

// ------------------------------------------------------------- import/export

// ExportJSON renders the managed variables as JSON.
func (m *Manager) ExportJSON() (map[string]any, error) {
	store, err := m.Store()
	if err != nil {
		return nil, err
	}
	vars := make([]map[string]any, 0, len(store.Vars))
	for _, v := range store.Vars {
		vars = append(vars, map[string]any{
			"name":    v.Name,
			"value":   v.Value,
			"note":    v.Note,
			"enabled": v.Enabled,
		})
	}
	return map[string]any{"version": storeVersion, "vars": vars}, nil
}

// ExportEnv renders the managed variables in .env format.
func (m *Manager) ExportEnv() (string, error) {
	store, err := m.Store()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# exported by mymac\n")
	for _, v := range store.Active() {
		b.WriteString("export " + v.Name + "=" + EscapeSingleQuoted(v.Value) + "\n")
	}
	return b.String(), nil
}

// ImportChange is one difference between the import and the current state.
type ImportChange struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	OldValue string `json:"oldValue,omitempty"`
	Action   string `json:"action"` // add | update
}

// ImportPreview parses the content and reports what would change.
func (m *Manager) ImportPreview(content string) ([]ImportChange, error) {
	parsed, err := parseImport(content)
	if err != nil {
		return nil, err
	}
	store, err := m.Store()
	if err != nil {
		return nil, err
	}
	var out []ImportChange
	for _, p := range parsed {
		if cur, ok := store.Find(p.Name); ok {
			if cur.Value != p.Value {
				out = append(out, ImportChange{Name: p.Name, Value: p.Value, OldValue: cur.Value, Action: "update"})
			}
			continue
		}
		out = append(out, ImportChange{Name: p.Name, Value: p.Value, Action: "add"})
	}
	return out, nil
}

// Import applies the parsed content.
func (m *Manager) Import(ctx context.Context, content string) ([]ImportChange, error) {
	parsed, err := parseImport(content)
	if err != nil {
		return nil, err
	}
	store, err := m.Store()
	if err != nil {
		return nil, err
	}
	var changes []ImportChange
	for _, p := range parsed {
		if cur, ok := store.Find(p.Name); ok {
			if cur.Value == p.Value {
				continue
			}
			changes = append(changes, ImportChange{Name: p.Name, Value: p.Value, OldValue: cur.Value, Action: "update"})
		} else {
			changes = append(changes, ImportChange{Name: p.Name, Value: p.Value, Action: "add"})
		}
		cur, _ := store.Find(p.Name)
		cur.Name = p.Name
		cur.Value = p.Value
		cur.Enabled = true
		cur.Scope = ScopeUser
		store.Upsert(cur)
	}
	if err := store.Save(); err != nil {
		return nil, err
	}
	if err := m.syncAll(ctx, store); err != nil {
		return changes, err
	}
	m.audit.Log(audit.Entry{
		Action: audit.ActionEnvImport,
		Target: "import",
		Detail: "新增 " + itoa(countAction(changes, "add")) + " 项，更新 " + itoa(countAction(changes, "update")) + " 项",
		Result: audit.ResultOK,
	})
	return changes, nil
}

func countAction(changes []ImportChange, action string) int {
	n := 0
	for _, c := range changes {
		if c.Action == action {
			n++
		}
	}
	return n
}

// parseImport accepts both the JSON export and .env text.
func parseImport(content string) ([]Var, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, errs.New(errs.KindInvalid, "导入内容为空")
	}
	if strings.HasPrefix(trimmed, "{") {
		var doc struct {
			Vars []struct {
				Name    string `json:"name"`
				Value   string `json:"value"`
				Note    string `json:"note"`
				Enabled *bool  `json:"enabled"`
			} `json:"vars"`
		}
		if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
			return nil, errs.Wrap(err, errs.KindInvalid, "JSON 解析失败")
		}
		var out []Var
		for _, v := range doc.Vars {
			if err := ValidateName(v.Name); err != nil {
				return nil, err
			}
			enabled := true
			if v.Enabled != nil {
				enabled = *v.Enabled
			}
			out = append(out, Var{Name: v.Name, Value: v.Value, Note: v.Note, Enabled: enabled, Scope: ScopeUser})
		}
		return out, nil
	}

	// .env style.
	var out []Var
	for i, line := range strings.Split(trimmed, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		l = strings.TrimPrefix(l, "export ")
		eq := strings.IndexByte(l, '=')
		if eq <= 0 {
			return nil, errs.New(errs.KindInvalid, "第 %d 行不是合法的 KEY=VALUE", i+1)
		}
		name := strings.TrimSpace(l[:eq])
		if err := ValidateName(name); err != nil {
			return nil, err
		}
		out = append(out, Var{Name: name, Value: unquoteShellValue(l[eq+1:]), Enabled: true, Scope: ScopeUser})
	}
	return out, nil
}

// ------------------------------------------------------------- marker block

// InspectMarker reports the health of the .zshenv marker block.
func InspectMarker(path string) (MarkerReport, error) {
	rep := MarkerReport{File: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			rep.State = MarkerMissing
			rep.Detail = "文件不存在（保存变量时会自动创建）"
			return rep, nil
		}
		return rep, errs.Wrap(err, errs.KindPermission, "无法读取 %s", path)
	}
	inner, state, line, detail := FindMarker(string(data))
	rep.State = state
	rep.Line = line
	rep.Detail = detail
	if state == MarkerOK && strings.TrimSpace(inner) != strings.TrimSpace(markerBody) {
		rep.State = MarkerDamaged
		rep.Detail = "标记块内容被手动修改"
	}
	return rep, nil
}

// RepairMarker rewrites the managed block, leaving everything else alone.
func (m *Manager) RepairMarker(ctx context.Context) (MarkerReport, error) {
	rep, err := UpsertShellFile(config.ZshEnv(), MarkerBlock())
	if err != nil {
		m.audit.Log(audit.Entry{Action: audit.ActionEnvRestore, Target: config.ZshEnv(),
			Detail: "修复标记块失败: " + err.Error(), Result: audit.ResultFailed})
		return rep, err
	}
	m.audit.Log(audit.Entry{Action: audit.ActionEnvRestore, Target: config.ZshEnv(),
		Detail: "已修复 mymac 标记块", Result: audit.ResultOK})
	return rep, nil
}

// RemoveMarker strips the managed block from the rc file.
func (m *Manager) RemoveMarker(ctx context.Context) error {
	if err := RemoveShellBlock(config.ZshEnv()); err != nil {
		return err
	}
	m.audit.Log(audit.Entry{Action: audit.ActionEnvRestore, Target: config.ZshEnv(),
		Detail: "已移除 mymac 标记块", Result: audit.ResultOK})
	return nil
}

// InstallAgent (re)creates the login launch agent.
func (m *Manager) InstallAgent(ctx context.Context) error {
	store, err := m.Store()
	if err != nil {
		return err
	}
	if err := m.agent.Install(ctx, store.Active()); err != nil {
		return err
	}
	m.audit.Log(audit.Entry{Action: audit.ActionEnvApply, Target: config.LaunchAgentLabel,
		Detail: "已安装登录代理", Result: audit.ResultOK})
	return nil
}

// UninstallAgent removes the login launch agent.
func (m *Manager) UninstallAgent(ctx context.Context) error {
	if err := m.agent.Uninstall(ctx); err != nil {
		return err
	}
	m.audit.Log(audit.Entry{Action: audit.ActionEnvApply, Target: config.LaunchAgentLabel,
		Detail: "已卸载登录代理", Result: audit.ResultOK})
	return nil
}
