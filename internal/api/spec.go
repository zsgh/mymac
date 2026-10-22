package api

import (
	"reflect"
	"sort"
	"sync"

	"mymac/internal/config"
)

// Endpoint is one entry of the API table.
//
// The table is the single source of truth: the server registers its routes
// from it and the OpenAPI document is built from it, so a route and its
// documentation cannot drift apart.
type Endpoint struct {
	// Method and Path describe the route. The path uses the same
	// {name} placeholders as the router, so both worlds agree.
	Method string
	Path   string

	Tag         string
	Summary     string
	Description string
	OperationID string
	Deprecated  bool

	Params    []Param
	Request   *RequestSpec
	Responses []ResponseSpec

	// Probe marks an endpoint the response contract test may call. Such an
	// endpoint has to be free of side effects and cheap to run.
	Probe bool
}

// Param is one query or path parameter.
type Param struct {
	Name        string
	In          string // "path" or "query"
	Description string
	Required    bool
	// Type is the Go type the value is decoded into. It drives the
	// schema of the parameter.
	Type    any
	Example any
}

// RequestSpec describes the body of a request.
type RequestSpec struct {
	Description string
	// Type is the Go type the body is decoded into.
	Type        any
	ContentType string
	Example     any
	Required    bool
}

// ResponseSpec describes one possible answer.
type ResponseSpec struct {
	Status      string
	Description string
	// Type is the Go type the body is written from.
	Type        any
	ContentType string
	Example     any
	// OneOf lists the Go types the body may have. It is used when one
	// endpoint answers with different shapes, for example a dry run
	// against a real result.
	OneOf []any
}

// tags lists the groups the operations are sorted into.
var tags = []Tag{
	{Name: "system", Description: "运行状态、权限与审计日志"},
	{Name: "env", Description: "环境变量的三层模型"},
	{Name: "procs", Description: "进程列表、采样与信号"},
	{Name: "ports", Description: "端口占用与系统服务规则"},
	{Name: "loginitems", Description: "登录启动项"},
	{Name: "apps", Description: "已安装应用与残留文件"},
	{Name: "clean", Description: "系统清理与废纸篓"},
	{Name: "docs", Description: "接口描述本身"},
	{Name: "settings", Description: "大模型与接口文档的设置"},
	{Name: "ai", Description: "AI 分析"},
}

// build turns the endpoint table into an OpenAPI document.
func build() *Document {
	b := newBuilder()
	doc := &Document{
		OpenAPI: "3.0.3",
		Info: Info{
			Title: "mymac",
			Description: "mymac 的本地 HTTP 接口。服务只监听回环地址，" +
				"每个请求都要通过 Host、Origin 与 Sec-Fetch-Site 三道检查。",
			Version: config.Version,
			License: &License{Name: "MIT"},
		},
		Servers: []Server{{
			URL:         "http://127.0.0.1:{port}",
			Description: "mymac 启动后在本机监听，端口见 /api/health",
			Variables: map[string]*ServerVariable{
				"port": {Default: "8888", Description: "mymac 实际监听的端口"},
			},
		}},
		Tags:  tags,
		Paths: map[string]PathItem{},
	}

	for _, ep := range endpoints {
		item := doc.Paths[ep.Path]
		op := &Operation{
			Tags:        []string{ep.Tag},
			Summary:     ep.Summary,
			Description: ep.Description,
			OperationID: ep.OperationID,
			Deprecated:  ep.Deprecated,
			Responses:   map[string]*Response{},
		}
		for _, p := range ep.Params {
			op.Parameters = append(op.Parameters, &Parameter{
				Name:        p.Name,
				In:          p.In,
				Description: p.Description,
				Required:    p.Required || p.In == "path",
				Schema:      b.schemaOf(typeOf(p.Type)),
				Example:     p.Example,
			})
		}
		if ep.Request != nil {
			ct := ep.Request.ContentType
			if ct == "" {
				ct = "application/json"
			}
			op.RequestBody = &RequestBody{
				Description: ep.Request.Description,
				Required:    ep.Request.Required,
				Content: map[string]*MediaType{
					ct: {Schema: b.schemaOf(typeOf(ep.Request.Type)), Example: ep.Request.Example},
				},
			}
		}
		for _, r := range ep.Responses {
			ct := r.ContentType
			if ct == "" {
				ct = "application/json"
			}
			resp := &Response{Description: r.Description}
			switch {
			case len(r.OneOf) > 0:
				one := &Schema{}
				for _, t := range r.OneOf {
					one.OneOf = append(one.OneOf, b.schemaOf(typeOf(t)))
				}
				resp.Content = map[string]*MediaType{ct: {Schema: one}}
			case r.Type != nil:
				resp.Content = map[string]*MediaType{
					ct: {Schema: b.schemaOf(typeOf(r.Type)), Example: r.Example},
				}
			}
			op.Responses[r.Status] = resp
		}
		if _, ok := op.Responses["default"]; !ok {
			op.Responses["default"] = &Response{
				Description: "请求失败时的通用错误",
				Content: map[string]*MediaType{
					"application/json": {Schema: b.schemaOf(typeOf(ErrorResponse{}))},
				},
			}
		}
		item.setOperation(ep.Method, op)
		doc.Paths[ep.Path] = item
	}

	doc.Components = &Components{Schemas: b.components}
	return doc
}

// typeOf returns the reflect type of a Go value, or nil.
func typeOf(v any) reflect.Type {
	if v == nil {
		return nil
	}
	return reflect.TypeOf(v)
}

// spec caches the built document.
var spec = sync.OnceValue(build)

// Spec returns the OpenAPI document of the API.
func Spec() *Document { return spec() }

// JSON renders the document as indented JSON.
func JSON() ([]byte, error) {
	return marshalIndent(Spec())
}

// Endpoints returns the endpoint table.
func Endpoints() []Endpoint {
	out := make([]Endpoint, len(endpoints))
	copy(out, endpoints)
	return out
}

// Keys returns the "METHOD /path" key of every endpoint.
func Keys() []string {
	keys := make([]string, 0, len(endpoints))
	for _, ep := range endpoints {
		keys = append(keys, ep.Method+" "+ep.Path)
	}
	sort.Strings(keys)
	return keys
}
