package server

import (
	"net/http"
	"sync"

	"mymac/internal/api"
	"mymac/web"
)

// The API describes itself: the document is generated from the same table
// the routes are registered from, so it can never describe an endpoint that
// does not exist.
//
// openAPIJSON renders the document once. The document never changes while
// the process runs, so there is no reason to build it again per request.
var openAPIJSON = sync.OnceValues(api.JSON)

// handleOpenAPI serves the OpenAPI description of the API.
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	doc, err := openAPIJSON()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(doc)
}

// handleAPIDocs serves the documentation page.
//
// The page reads the OpenAPI document from the server, so it always shows
// what the server actually answers.
func (s *Server) handleAPIDocs(w http.ResponseWriter, r *http.Request) {
	page, ct, err := web.File("api-docs.html")
	if err != nil {
		http.Error(w, "assets missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page)
}
