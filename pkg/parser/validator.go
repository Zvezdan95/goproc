package parser

import (
	"fmt"
	"go/ast"
	"go/token"
)

// Validate checks a procedure's AST against the strict subset of allowed Go constructs.
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
			// In a full implementation we would check for allowed packages (like math, time, errors)
			// and block unsafe, os, net/http, etc.
			// For the sake of the MVP, we skip deep package checks here but we flag deferred calls.
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

	return nil
}
