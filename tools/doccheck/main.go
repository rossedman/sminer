// Command doccheck fails when a function, method or package has no doc
// comment. Test files and generated files are skipped.
//
// Usage: go run ./tools/doccheck ./...
package main

import (
	"fmt"
	"os"
	"path"
	"sort"

	"github.com/rossedman/sminer/tools/internal/gofiles"
)

// main reports every undocumented function and package, exiting 1 if any.
func main() {
	missing, err := check(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "doccheck:", err)
		os.Exit(2)
	}
	for _, m := range missing {
		fmt.Println(m)
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "doccheck: %d missing doc comments\n", len(missing))
		os.Exit(1)
	}
}

// check returns one line per missing doc comment, in file order.
func check(patterns []string) ([]string, error) {
	var missing []string
	pkgDoc := map[string]bool{} // directory -> some file has a package comment
	err := gofiles.Walk(patterns, func(f gofiles.File) error {
		if f.Generated {
			return nil
		}
		dir := path.Dir(f.Path)
		pkgDoc[dir] = pkgDoc[dir] || f.AST.Doc != nil
		missing = append(missing, funcsWithoutDoc(f)...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(pkgDoc))
	for d, ok := range pkgDoc {
		if !ok {
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		missing = append(missing, d+": package has no package comment")
	}
	return missing, nil
}
