package parser

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Procedure represents a parsed and validated gormproc procedure function.
type Procedure struct {
	Name string
	Func *ast.FuncDecl
	Type *types.Signature
	Pkg  *packages.Package
}

// ParseDir loads the package at the given directory path and extracts all functions
// annotated with //gorm:procedure.
func ParseDir(dir string) ([]*Procedure, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedTypesSizes |
			packages.NeedSyntax | packages.NeedTypesInfo,
		Dir: dir,
	}

	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to load packages: %w", err)
	}

	if packages.PrintErrors(pkgs) > 0 {
		return nil, fmt.Errorf("package load errors found")
	}

	var procedures []*Procedure

	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				funcDecl, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}

				if hasGormProcedureComment(funcDecl.Doc) {
					obj := pkg.TypesInfo.Defs[funcDecl.Name]
					if obj == nil {
						continue
					}

					sig, ok := obj.Type().(*types.Signature)
					if !ok {
						continue
					}

					procedures = append(procedures, &Procedure{
						Name: funcDecl.Name.Name,
						Func: funcDecl,
						Type: sig,
						Pkg:  pkg,
					})
				}
			}
		}
	}

	return procedures, nil
}

func hasGormProcedureComment(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	for _, comment := range doc.List {
		if strings.Contains(comment.Text, "//gorm:procedure") {
			return true
		}
	}
	return false
}
