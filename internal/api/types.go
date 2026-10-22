package api

import (
	"time"

	"mymac/internal/apps"
	"mymac/internal/audit"
	"mymac/internal/envs"
	"mymac/internal/errs"
	"mymac/internal/perm"
	"mymac/internal/procs"
	"mymac/internal/sysinfo"
	"mymac/internal/trash"
)

// The types below are the wire contract of the API. The handlers decode
// and encode them, and the OpenAPI document describes them, so the two can
// never disagree about a field name or a type.

// ---------------------------------------------------------------- system

// HealthResponse is the payload of GET /api/health.
type HealthResponse struct {
	Version string `json:"version"`
	Uptime  string `json:"uptime"`
	PID     int    `json:"pid"`
	Port    int    `json:"port"`
}

// OKResponse is the plain success answer.
type OKResponse struct {
	OK bool `json:"ok"`
}

// ErrorResponse is the answer of every failed request.
type ErrorResponse struct {
	Error errs.Error `json:"error"`
}

// PermStatusResponse lists the privacy permissions of the machine.
type PermStatusResponse struct {
	Items []perm.Status `json:"items"`
}

// PermOpenRequest asks the server to open a system settings pane.
type PermOpenRequest struct {
	Target perm.Target `json:"target"`
}

// AuditResponse lists the audit log entries.
type AuditResponse struct {
	Items []audit.Entry `json:"items"`
}

// ---------------------------------------------------------------- procs

// ProcListResponse is the payload of GET /api/procs.
type ProcListResponse struct {
	Processes   []procs.Process `json:"processes"`
	Count       int             `json:"count"`
	CollectedAt time.Time       `json:"collectedAt"`
	DurationMS  int64           `json:"durationMs"`
}

// ProcStreamResponse is one frame of the process stream.
type ProcStreamResponse struct {
	Processes []procs.Process   `json:"processes"`
	Count     int               `json:"count"`
	Overview  *sysinfo.Overview `json:"overview"`
	At        string            `json:"at"`
}

// ProcDetailResponse is the payload of GET /api/procs/{pid}.
type ProcDetailResponse struct {
	Process *procs.ProcessDetail `json:"process"`
}

// ProcSignalRequest asks for a signal to be sent to a process.
type ProcSignalRequest struct {
	Sig       string `json:"sig"`
	Confirmed bool   `json:"confirmed"`
}

// ProcSignalResponse reports the signal that was sent.
type ProcSignalResponse struct {
	OK  bool   `json:"ok"`
	PID int    `json:"pid"`
	Sig string `json:"sig"`
}

// ProcReniceRequest asks for a new scheduling priority.
type ProcReniceRequest struct {
	Nice      int  `json:"nice"`
	Confirmed bool `json:"confirmed"`
}

// ProcReniceResponse reports the new scheduling priority.
type ProcReniceResponse struct {
	OK   bool `json:"ok"`
	PID  int  `json:"pid"`
	Nice int  `json:"nice"`
}

// ProcSampleResponse is the payload of GET /api/procs/{pid}/sample.
type ProcSampleResponse struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// ProcFilesResponse lists the open files of a process.
type ProcFilesResponse struct {
	Files []string `json:"files"`
}

// AlertRulesResponse lists the alert thresholds.
type AlertRulesResponse struct {
	Items []procs.AlertRule `json:"items"`
}

// AlertRulesRequest replaces the alert thresholds.
type AlertRulesRequest struct {
	Items []procs.AlertRule `json:"items"`
}

// ---------------------------------------------------------------- ports

// PortActionRequest asks for a process behind a port to be handled.
type PortActionRequest struct {
	// CreateTime is the start time of the process. It guards against
	// acting on a pid that was recycled in the meantime.
	CreateTime int64 `json:"createTime"`
}

// PortActionErrorResponse is the answer of a failed port action.
type PortActionErrorResponse struct {
	OK    bool       `json:"ok"`
	Error errs.Error `json:"error"`
}

// PortRulesOverrideRequest changes one user rule.
type PortRulesOverrideRequest struct {
	Mode   string `json:"mode"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Remove bool   `json:"remove"`
}

// ---------------------------------------------------------------- env

// EnvVarCreateRequest adds a managed variable.
type EnvVarCreateRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Note  string `json:"note"`
}

// EnvVarUpdateRequest changes a managed variable.
//
// The fields are pointers: a missing field means "leave it alone".
type EnvVarUpdateRequest struct {
	Value   *string `json:"value"`
	Note    *string `json:"note"`
	Enabled *bool   `json:"enabled"`
}

// EnvVarDeleteResponse reports the removed variable.
type EnvVarDeleteResponse struct {
	OK   bool   `json:"ok"`
	Name string `json:"name"`
}

// EnvPathSetRequest stores the PATH fragments.
type EnvPathSetRequest struct {
	Prepend []string `json:"prepend"`
	Append  []string `json:"append"`
}

// EnvImportRequest carries the text to import.
type EnvImportRequest struct {
	Content string `json:"content"`
}

// EnvImportPreviewResponse shows what an import would change.
type EnvImportPreviewResponse struct {
	Add    []envs.ImportChange `json:"add"`
	Update []envs.ImportChange `json:"update"`
	All    []envs.ImportChange `json:"all"`
}

// EnvImportResponse reports the applied changes.
type EnvImportResponse struct {
	Changes []envs.ImportChange `json:"changes"`
	Applied int                 `json:"applied"`
}

// EnvBackupsResponse lists the stored backups.
type EnvBackupsResponse struct {
	Items []envs.Backup `json:"items"`
}

// EnvRestoreRequest asks for a backup to be restored.
type EnvRestoreRequest struct {
	Name string `json:"name"`
}

// EnvRestoreResponse reports the restored file.
type EnvRestoreResponse struct {
	OK   bool   `json:"ok"`
	Path string `json:"path"`
}

// EnvStatusResponse reports the health of every layer.
type EnvStatusResponse struct {
	Marker   envs.MarkerReport `json:"marker"`
	Agent    envs.AgentState   `json:"agent"`
	Managed  int               `json:"managed"`
	Warnings []string          `json:"warnings"`
}

// ---------------------------------------------------------- login items

// LoginItemStateRequest enables or disables a startup item.
type LoginItemStateRequest struct {
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
}

// LoginItemStateResponse reports the new state of a startup item.
type LoginItemStateResponse struct {
	OK      bool   `json:"ok"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
}

// LoginItemRemoveRequest asks for a startup item to be removed.
type LoginItemRemoveRequest struct {
	Path string `json:"path"`
}

// LoginItemGroupStateRequest turns every startup item of one program on or
// off.
type LoginItemGroupStateRequest struct {
	// Group is the key of the group, as reported by the list endpoint.
	Group   string `json:"group"`
	Enabled bool   `json:"enabled"`
}

// LoginItemGroupStateResponse reports what the group action did.
type LoginItemGroupStateResponse struct {
	OK      bool     `json:"ok"`
	Group   string   `json:"group"`
	Name    string   `json:"name"`
	Total   int      `json:"total"`
	Changed int      `json:"changed"`
	Skipped []string `json:"skipped,omitempty"`
	Failed  []string `json:"failed,omitempty"`
}

// LoginItemAnalyzeResponse reports the state of the risk analysis.
type LoginItemAnalyzeResponse struct {
	// Available reports whether an AI can be asked at all.
	Available bool `json:"available"`
	// Analyzing is true while a rating is in flight.
	Analyzing bool `json:"analyzing"`
	// Count is the number of items that have a verdict.
	Count int `json:"count"`
	// At is when the last analysis finished.
	At time.Time `json:"at,omitempty"`
	// Error explains why the last analysis failed, if it did.
	Error string `json:"error,omitempty"`
}

// AppLastUsedResponse says when the application was opened last.
type AppLastUsedResponse struct {
	// Known reports whether macOS knows the date at all.
	Known bool `json:"known"`
	// LastUsed is when the application was opened.
	LastUsed time.Time `json:"lastUsed,omitempty"`
	// DaysAgo is how many days that was.
	DaysAgo int `json:"daysAgo"`
	// Path is the bundle the answer is about.
	Path string `json:"path,omitempty"`
}

// ---------------------------------------------------------------- settings

// LLMSettings describes the model the AI features talk to.
//
// The key itself never leaves the server: the interface only learns
// whether there is one.
type LLMSettings struct {
	// BaseURL is the root of the API.
	BaseURL string `json:"baseUrl"`
	// Model is the name of the model.
	Model string `json:"model"`
	// HasKey reports whether a key is stored.
	HasKey bool `json:"hasKey"`
}

// DocsSettings holds the settings of the interface documentation.
type DocsSettings struct {
	// Enabled reports whether the documentation may be reached.
	Enabled bool `json:"enabled"`
}

// SettingsResponse is the whole configuration.
type SettingsResponse struct {
	LLM  LLMSettings  `json:"llm"`
	Docs DocsSettings `json:"docs"`
}

// SettingsRequest changes the configuration.
type SettingsRequest struct {
	LLM struct {
		BaseURL string `json:"baseUrl"`
		Model   string `json:"model"`
		// APIKey is only used when it is filled in: an empty key keeps
		// the one that is stored.
		APIKey string `json:"apiKey"`
	} `json:"llm"`
	// Docs is optional: a missing part leaves the setting alone.
	Docs *DocsSettings `json:"docs,omitempty"`
}

// DocsToggleRequest turns the documentation on or off.
type DocsToggleRequest struct {
	Enabled bool `json:"enabled"`
}

// SettingsTestResponse reports whether the model answers.
type SettingsTestResponse struct {
	// OK reports whether the model answered.
	OK bool `json:"ok"`
	// Answer is what the model said.
	Answer string `json:"answer,omitempty"`
	// Error explains why it did not answer.
	Error string `json:"error,omitempty"`
}

// ProcAnalyzeResponse reports the state of the process explanation.
type ProcAnalyzeResponse struct {
	// Available reports whether a model can be asked at all.
	Available bool `json:"available"`
	// Analyzing is true while an explanation is in flight.
	Analyzing bool `json:"analyzing"`
	// Count is the number of programs that have an explanation.
	Count int `json:"count"`
	// At is when the last explanation finished.
	At time.Time `json:"at,omitempty"`
	// Error explains why the last run failed, if it did.
	Error string `json:"error,omitempty"`
}

// ----------------------------------------------------------------- apps

// AppListResponse lists the installed applications.
type AppListResponse struct {
	Items   []*apps.App `json:"items"`
	Count   int         `json:"count"`
	Pending int         `json:"pending"`
}

// AppResidualResponse lists the leftovers of one application.
type AppResidualResponse struct {
	Items []apps.Residual `json:"items"`
	App   *apps.App       `json:"app"`
	Count int             `json:"count"`
}

// AppResidualsResponse lists the leftovers of several applications.
type AppResidualsResponse struct {
	Groups []apps.Group `json:"groups"`
	Count  int          `json:"count"`
}

// AppUninstallRequest moves the selected items to the Trash.
type AppUninstallRequest struct {
	BundleID  string   `json:"bundleID"`
	BundleIDs []string `json:"bundleIDs"`
	Paths     []string `json:"paths"`
	DryRun    bool     `json:"dryRun"`
}

// ---------------------------------------------------------------- clean

// CleanExecuteRequest moves the selected items to the Trash.
type CleanExecuteRequest struct {
	Paths  []string `json:"paths"`
	DryRun bool     `json:"dryRun"`
}

// DryRunResponse is the answer of a request that only planned the work.
type DryRunResponse struct {
	DryRun  bool               `json:"dryRun"`
	Moved   []string           `json:"moved"`
	Failed  []trash.FailedItem `json:"failed"`
	Planned []string           `json:"planned"`
}

// ----------------------------------------------------------------- docs

// OpenAPIResponse is the payload of GET /api/openapi.json.
//
// The document describes itself, so the schema stays a plain object.
type OpenAPIResponse struct {
	OpenAPI string         `json:"openapi"`
	Info    map[string]any `json:"info"`
	Paths   map[string]any `json:"paths"`
}
