package api

// Document is an OpenAPI 3.0 description of the mymac API.
//
// The model follows the specification closely, so the document can be handed
// to any tool that reads OpenAPI without a translation step.
type Document struct {
	OpenAPI    string              `json:"openapi"`
	Info       Info                `json:"info"`
	Servers    []Server            `json:"servers,omitempty"`
	Tags       []Tag               `json:"tags,omitempty"`
	Paths      map[string]PathItem `json:"paths"`
	Components *Components         `json:"components,omitempty"`
}

// Info describes the API itself.
type Info struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Version     string   `json:"version"`
	License     *License `json:"license,omitempty"`
}

// License names the license of the API.
type License struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// Server is one base URL the API answers on.
type Server struct {
	URL         string                     `json:"url"`
	Description string                     `json:"description,omitempty"`
	Variables   map[string]*ServerVariable `json:"variables,omitempty"`
}

// ServerVariable is one placeholder inside a server URL.
type ServerVariable struct {
	Default     string   `json:"default"`
	Enum        []string `json:"enum,omitempty"`
	Description string   `json:"description,omitempty"`
}

// Tag groups the operations in the documentation.
type Tag struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// PathItem holds the operations of one path.
type PathItem struct {
	Summary     string       `json:"summary,omitempty"`
	Description string       `json:"description,omitempty"`
	Get         *Operation   `json:"get,omitempty"`
	Post        *Operation   `json:"post,omitempty"`
	Put         *Operation   `json:"put,omitempty"`
	Patch       *Operation   `json:"patch,omitempty"`
	Delete      *Operation   `json:"delete,omitempty"`
	Parameters  []*Parameter `json:"parameters,omitempty"`
}

// Operation is one endpoint.
type Operation struct {
	Tags        []string             `json:"tags,omitempty"`
	Summary     string               `json:"summary"`
	Description string               `json:"description,omitempty"`
	OperationID string               `json:"operationId,omitempty"`
	Parameters  []*Parameter         `json:"parameters,omitempty"`
	RequestBody *RequestBody         `json:"requestBody,omitempty"`
	Responses   map[string]*Response `json:"responses"`
	Deprecated  bool                 `json:"deprecated,omitempty"`
}

// Parameter is one query or path parameter.
type Parameter struct {
	Name        string  `json:"name"`
	In          string  `json:"in"`
	Description string  `json:"description,omitempty"`
	Required    bool    `json:"required,omitempty"`
	Schema      *Schema `json:"schema,omitempty"`
	Example     any     `json:"example,omitempty"`
}

// RequestBody describes the payload a request carries.
type RequestBody struct {
	Description string                `json:"description,omitempty"`
	Required    bool                  `json:"required,omitempty"`
	Content     map[string]*MediaType `json:"content"`
}

// Response is one possible answer.
type Response struct {
	Description string                `json:"description"`
	Content     map[string]*MediaType `json:"content,omitempty"`
}

// MediaType is one representation of a body.
type MediaType struct {
	Schema  *Schema `json:"schema,omitempty"`
	Example any     `json:"example,omitempty"`
}

// Components holds the shared schemas.
type Components struct {
	Schemas map[string]*Schema `json:"schemas,omitempty"`
}

// operation returns the operation of a method, or nil.
func (p *PathItem) operation(method string) *Operation {
	switch method {
	case "GET":
		return p.Get
	case "POST":
		return p.Post
	case "PUT":
		return p.Put
	case "PATCH":
		return p.Patch
	case "DELETE":
		return p.Delete
	}
	return nil
}

// setOperation stores the operation of a method.
func (p *PathItem) setOperation(method string, op *Operation) {
	switch method {
	case "GET":
		p.Get = op
	case "POST":
		p.Post = op
	case "PUT":
		p.Put = op
	case "PATCH":
		p.Patch = op
	case "DELETE":
		p.Delete = op
	}
}
