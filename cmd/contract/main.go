// Command contract regenerates (or checks) the committed client contract files:
//
//	contract/schema.graphql    complete GraphQL SDL of the executable schema
//	contract/mcp-tools.json    tsb-mcp tool names and argument schemas
//
// Usage (from the repository root):
//
//	go run ./cmd/contract          rewrite both files
//	go run ./cmd/contract -check   exit 1 when a file differs from the code
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"tsb-service/internal/contract"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "contract:", err)
		os.Exit(1)
	}
}

func run() error {
	check := flag.Bool("check", false, "fail instead of writing when a file is out of date")
	dir := flag.String("dir", "contract", "directory holding the committed files")
	flag.Parse()

	tools, err := contract.MCPToolsJSON(context.Background())
	if err != nil {
		return err
	}
	files := map[string][]byte{
		"schema.graphql": []byte(contract.GraphQLSDL()),
		"mcp-tools.json": tools,
	}
	stale := false
	for name, want := range files {
		path := filepath.Join(*dir, name)
		got, err := os.ReadFile(path)
		if err == nil && bytes.Equal(got, want) {
			continue
		}
		if *check {
			fmt.Fprintf(os.Stderr, "%s is out of date: run `go run ./cmd/contract` and commit the result\n", path)
			stale = true
			continue
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", path)
	}
	if stale {
		return fmt.Errorf("contract files are out of date")
	}
	return nil
}
