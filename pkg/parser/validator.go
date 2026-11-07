package parser

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
)

// Validate checks a procedure's AST and its entire transitive call graph (including
// helper functions, struct methods, and interface implementations) against the strict
// subset of SQL-transpilable Go constructs, rejecting untranspilable I/O and factory patterns.
func Validate(proc *Procedure) error {
	var errs []error

	ast.Inspect(proc.Func, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.GoStmt:
			errs = append(errs, fmt.Errorf("goroutines (go stmt) are not allowed in gormproc"))
		case *ast.SendStmt:
			errs = append(errs, fmt.Errorf("channel send operations are not allowed in gormproc"))
		case *ast.UnaryExpr:
			if node.Op == token.ARROW {
				errs = append(errs, fmt.Errorf("channel receive operations are not allowed in gormproc"))
			}
		case *ast.CallExpr:
			// Basic AST-level forbidden package check when full type-info is omitted in raw AST tests
			if proc.Program == nil {
				if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
					if pkgId, ok := sel.X.(*ast.Ident); ok {
						switch pkgId.Name {
						case "os", "io", "ioutil", "bufio", "filepath":
							errs = append(errs, fmt.Errorf("disk/file I/O (%s.%s) cannot be transpiled to SQL", pkgId.Name, sel.Sel.Name))
						case "http", "net":
							errs = append(errs, fmt.Errorf("network I/O (%s.%s) cannot be transpiled to SQL", pkgId.Name, sel.Sel.Name))
						case "exec", "syscall", "unsafe", "reflect":
							errs = append(errs, fmt.Errorf("system/runtime call (%s.%s) cannot be transpiled to SQL", pkgId.Name, sel.Sel.Name))
						}
					}
				}
			}
		case *ast.DeferStmt:
			errs = append(errs, fmt.Errorf("defer statements are not supported in gormproc"))
		case *ast.SelectStmt:
			errs = append(errs, fmt.Errorf("select statements are not supported in gormproc"))
		}
		return true
	})

	if len(errs) > 0 {
		return fmt.Errorf("validation failed for procedure %s: %v", proc.Name, errs)
	}

	if proc.Program != nil {
		if _, err := ResolveAndInline(proc); err != nil {
			if strings.Contains(err.Error(), proc.Name) {
				return fmt.Errorf("validation failed: %w", err)
			}
			return fmt.Errorf("validation failed for procedure %s: %w", proc.Name, err)
		}
	}

	return nil
}
