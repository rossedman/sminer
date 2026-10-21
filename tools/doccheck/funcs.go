package main

import (
	"fmt"
	"go/ast"

	"github.com/rossedman/sminer/tools/internal/gofiles"
)

// funcsWithoutDoc lists the functions and methods in f with no doc comment.
func funcsWithoutDoc(f gofiles.File) []string {
	var out []string
	for _, decl := range f.AST.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Doc != nil {
			continue
		}
		pos := f.Fset.Position(fd.Pos())
		out = append(out, fmt.Sprintf("%s:%d: %s has no doc comment", f.Path, pos.Line, funcName(fd)))
	}
	return out
}

// funcName is fd's name, qualified with its receiver type for methods.
func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	switch ix := t.(type) {
	case *ast.IndexExpr: // G[T]
		t = ix.X
	case *ast.IndexListExpr: // M[K, V]
		t = ix.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}
