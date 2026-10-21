// Command generated prints the import-path-qualified name of every generated
// Go file (one with a "Code generated ... DO NOT EDIT." header), so the
// coverage gate can leave them out.
//
// Usage: go run ./tools/generated ./...
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/rossedman/sminer/tools/internal/gofiles"
)

// module is the import path prefix used in coverage profiles.
const module = "github.com/rossedman/sminer"

// main prints one generated file per line.
func main() {
	err := gofiles.Walk(os.Args[1:], func(f gofiles.File) error {
		if f.Generated {
			fmt.Println(module + "/" + strings.TrimPrefix(f.Path, "./"))
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "generated:", err)
		os.Exit(2)
	}
}
