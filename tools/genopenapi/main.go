// Command genopenapi writes the OpenAPI description of the mymac API.
//
// The description is generated from the endpoint table in internal/api, so
// it always matches the routes the server registers. The files it writes are
// meant to be committed: other tools read them without running mymac.
//
// Usage:
//
//	go run ./tools/genopenapi [-out docs]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mymac/internal/api"
)

func main() {
	dir := flag.String("out", "docs", "directory the files are written to")
	flag.Parse()

	if err := run(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "genopenapi:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	jsonDoc, err := api.JSON()
	if err != nil {
		return fmt.Errorf("render json: %w", err)
	}
	yamlDoc, err := api.YAML()
	if err != nil {
		return fmt.Errorf("render yaml: %w", err)
	}

	files := map[string][]byte{
		"openapi.json": jsonDoc,
		"openapi.yaml": yamlDoc,
	}
	for name, data := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Println("wrote", path)
	}
	return nil
}
