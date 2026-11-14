package parser

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/tools/go/packages"
)

// FuncInfo holds the AST, type object, and package context for a function or method.
type FuncInfo struct {
	Decl *ast.FuncDecl
	Obj  *types.Func
	Pkg  *packages.Package
	Info *types.Info
	Fset *token.FileSet
	File *ast.File
}

// Program indexes all loaded user packages, functions/methods, and named types
// to support recursive cross-function, cross-package, and interface call resolution.
type Program struct {
	Fset       *token.FileSet
	Funcs      map[*types.Func]*FuncInfo
	NamedTypes []*types.Named
}

// Procedure represents a parsed and validated gormproc procedure function.
type Procedure struct {
	Name              string
	Func              *ast.FuncDecl
	InlinedFunc       *ast.FuncDecl
	File              *ast.File
	Type              *types.Signature
	Obj               *types.Func
	Pkg               *packages.Package
	Info              *types.Info
	Fset              *token.FileSet
	Program           *Program
	ExternalCallNames map[*ast.CallExpr]string
}

// EffectiveFunc returns the recursively inlined FuncDecl if available, or the raw FuncDecl.
func (p *Procedure) EffectiveFunc() *ast.FuncDecl {
	if p.InlinedFunc != nil {
		return p.InlinedFunc
	}
	return p.Func
}

// ResolveCallName returns an explicit SQL procedure name (e.g. "pokermem.TriggerWinnerFeed2")
// if the call targets a sub-procedure annotated with //gorm:proc, or "" otherwise.
func (p *Procedure) ResolveCallName(call *ast.CallExpr) string {
	if p != nil && p.ExternalCallNames != nil {
		if name, ok := p.ExternalCallNames[call]; ok {
			return name
		}
	}
	return ""
}

func isUserIndexablePkg(pkgPath string, rootPaths map[string]bool) bool {
	if rootPaths[pkgPath] {
		return true
	}
	if pkgPath == "" {
		return false
	}
	if pkgPath == "github.com/user/gormproc" || strings.HasPrefix(pkgPath, "gorm.io/") || strings.HasPrefix(pkgPath, "golang.org/") {
		return false
	}
	// Standard library packages do not contain a dot in the first path element
	// (unless they are local module names like "goproc-test/..." or "test/...").
	firstSlash := strings.IndexByte(pkgPath, '/')
	firstElem := pkgPath
	if firstSlash != -1 {
		firstElem = pkgPath[:firstSlash]
	}
	if strings.Contains(firstElem, ".") || strings.Contains(firstElem, "-") || firstElem == "test" || firstElem == "main" {
		return true
	}
	return false
}

// ParseDir loads the package at the given directory path (and its dependencies)
// and extracts all functions annotated with //gorm:procedure.
func ParseDir(dir string) ([]*Procedure, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedTypesSizes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir: dir,
	}

	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to load packages: %w", err)
	}

	if packages.PrintErrors(pkgs) > 0 {
		return nil, fmt.Errorf("package load errors found")
	}

	rootPaths := make(map[string]bool)
	for _, p := range pkgs {
		rootPaths[p.PkgPath] = true
	}

	prog := &Program{
		Funcs: make(map[*types.Func]*FuncInfo),
	}
	if len(pkgs) > 0 {
		prog.Fset = pkgs[0].Fset
	}

	seenNamed := make(map[*types.Named]bool)
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if !isUserIndexablePkg(p.PkgPath, rootPaths) {
			return
		}
		if p.Types != nil && p.Types.Scope() != nil {
			scope := p.Types.Scope()
			for _, name := range scope.Names() {
				obj := scope.Lookup(name)
				if tn, ok := obj.(*types.TypeName); ok && !tn.IsAlias() {
					if named, ok := tn.Type().(*types.Named); ok && !seenNamed[named] {
						seenNamed[named] = true
						prog.NamedTypes = append(prog.NamedTypes, named)
					}
				}
			}
		}
		if p.TypesInfo == nil {
			return
		}
		for _, file := range p.Syntax {
			for _, decl := range file.Decls {
				funcDecl, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				obj, ok := p.TypesInfo.Defs[funcDecl.Name].(*types.Func)
				if !ok || obj == nil {
					continue
				}
				prog.Funcs[obj] = &FuncInfo{
					Decl: funcDecl,
					Obj:  obj,
					Pkg:  p,
					Info: p.TypesInfo,
					Fset: p.Fset,
					File: file,
				}
			}
		}
	})

	var procedures []*Procedure

	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				funcDecl, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}

				if HasGormProcedureComment(funcDecl.Doc) {
					obj, ok := pkg.TypesInfo.Defs[funcDecl.Name].(*types.Func)
					if !ok || obj == nil {
						continue
					}

					sig, ok := obj.Type().(*types.Signature)
					if !ok {
						continue
					}

					procedures = append(procedures, &Procedure{
						Name:              funcDecl.Name.Name,
						Func:              funcDecl,
						File:              file,
						Type:              sig,
						Obj:               obj,
						Pkg:               pkg,
						Info:              pkg.TypesInfo,
						Fset:              pkg.Fset,
						Program:           prog,
						ExternalCallNames: make(map[*ast.CallExpr]string),
					})
				}
			}
		}
	}

	return procedures, nil
}

func findModuleRoot() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		wd, _ := os.Getwd()
		return wd
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	wd, _ := os.Getwd()
	return wd
}

// ParseSource parses and type-checks in-memory Go source files (which may represent
// one or multiple relative packages, e.g. "proc.go" and "subpkg/helper.go") using
// go/packages Overlay within the gormproc module context, and returns all //gorm:procedure functions.
func ParseSource(files map[string]string) ([]*Procedure, error) {
	modRoot := findModuleRoot()
	virtualRoot := filepath.Join(modRoot, "_virtual_src_")
	overlay := make(map[string][]byte, len(files))
	for relPath, content := range files {
		absPath := filepath.Join(virtualRoot, relPath)
		overlay[absPath] = []byte(content)
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedTypesSizes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:     modRoot,
		Overlay: overlay,
	}

	pkgs, err := packages.Load(cfg, "./_virtual_src_")
	if err != nil {
		return nil, fmt.Errorf("failed to load virtual package: %w", err)
	}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			return nil, fmt.Errorf("package error: %v", p.Errors[0])
		}
	}

	rootPaths := make(map[string]bool)
	for _, p := range pkgs {
		rootPaths[p.PkgPath] = true
	}

	prog := &Program{
		Funcs: make(map[*types.Func]*FuncInfo),
	}
	if len(pkgs) > 0 {
		prog.Fset = pkgs[0].Fset
	}

	seenNamed := make(map[*types.Named]bool)
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if !isUserIndexablePkg(p.PkgPath, rootPaths) {
			return
		}
		if p.Types != nil && p.Types.Scope() != nil {
			scope := p.Types.Scope()
			for _, name := range scope.Names() {
				if tn, ok := scope.Lookup(name).(*types.TypeName); ok && !tn.IsAlias() {
					if named, ok := tn.Type().(*types.Named); ok && !seenNamed[named] {
						seenNamed[named] = true
						prog.NamedTypes = append(prog.NamedTypes, named)
					}
				}
			}
		}
		if p.TypesInfo == nil {
			return
		}
		for _, file := range p.Syntax {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				fnObj, ok := p.TypesInfo.Defs[fd.Name].(*types.Func)
				if !ok || fnObj == nil {
					continue
				}
				prog.Funcs[fnObj] = &FuncInfo{
					Decl: fd,
					Obj:  fnObj,
					Pkg:  p,
					Info: p.TypesInfo,
					Fset: p.Fset,
					File: file,
				}
			}
		}
	})

	var procedures []*Procedure
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || !HasGormProcedureComment(fd.Doc) {
					continue
				}
				fnObj, ok := pkg.TypesInfo.Defs[fd.Name].(*types.Func)
				if !ok || fnObj == nil {
					continue
				}
				sig, ok := fnObj.Type().(*types.Signature)
				if !ok {
					continue
				}
				procedures = append(procedures, &Procedure{
					Name:              fd.Name.Name,
					Func:              fd,
					File:              file,
					Type:              sig,
					Obj:               fnObj,
					Pkg:               pkg,
					Info:              pkg.TypesInfo,
					Fset:              pkg.Fset,
					Program:           prog,
					ExternalCallNames: make(map[*ast.CallExpr]string),
				})
			}
		}
	}

	return procedures, nil
}

// HasGormProcedureComment reports whether doc contains //gorm:procedure.
func HasGormProcedureComment(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	for _, comment := range doc.List {
		text := strings.TrimSpace(comment.Text)
		if text == "//gorm:procedure" || strings.HasPrefix(text, "//gorm:procedure ") {
			return true
		}
	}
	return false
}

// ExtractExternalProcName extracts an optional SQL procedure or function name from //gorm:proc <name> or //gorm:func <name>.
func ExtractExternalProcName(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	for _, comment := range doc.List {
		text := strings.TrimSpace(comment.Text)
		if strings.HasPrefix(text, "//gorm:proc ") {
			return strings.TrimSpace(strings.TrimPrefix(text, "//gorm:proc "))
		}
		if strings.HasPrefix(text, "//gorm:func ") {
			return strings.TrimSpace(strings.TrimPrefix(text, "//gorm:func "))
		}
	}
	return ""
}
