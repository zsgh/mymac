// Package web embeds the mymac single page application into the binary.
//
// The assets live next to this file so `go:embed` can reach them; the whole
// directory is baked into the executable and served from memory.
package web

import (
	"embed"
	"io/fs"
	"mime"
	"path"
	"strings"
)

//go:embed assets
var assets embed.FS

// FS returns the embedded asset tree rooted at the assets directory.
func FS() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		return assets
	}
	return sub
}

// File returns the contents of an embedded file and its content type.
func File(name string) ([]byte, string, error) {
	name = path.Clean("/" + name)[1:]
	if name == "" {
		name = "index.html"
	}
	b, err := fs.ReadFile(FS(), name)
	if err != nil {
		return nil, "", err
	}
	return b, ContentType(name), nil
}

// ContentType maps a file name to a MIME type.
func ContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	case ".txt":
		return "text/plain; charset=utf-8"
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
