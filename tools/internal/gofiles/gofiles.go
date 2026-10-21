// Package gofiles walks a module's Go source files for the repo check tools.
package gofiles

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// File is one parsed, non-test Go source file.
type File struct {
	Path      string // slash-separated, relative to the module root
	Fset      *token.FileSet
	AST       *ast.File
	Generated bool
}

// Walk parses every non-test .go file matched by patterns and calls fn for
// each. A pattern is a directory, optionally ending in "/..." to include its
// subdirectories. testdata, vendor and dot directories are skipped.
func Walk(patterns []string, fn func(File) error) error {
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	for _, p := range patterns {
		root, recursive := strings.CutSuffix(p, "/...")
		if root == "" {
			root = "."
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if path != root && (!recursive || name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			return parse(path, fn)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// parse reads one file and hands it to fn.
func parse(path string, fn func(File) error) error {
	src, err := os.ReadFile(path) //nolint:gosec // G304: walking the repo's own sources
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return err
	}
	return fn(File{Path: filepath.ToSlash(filepath.Clean(path)), Fset: fset, AST: f, Generated: ast.IsGenerated(f)})
}
