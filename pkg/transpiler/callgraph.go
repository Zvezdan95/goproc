package transpiler

import (
	"go/ast"

	"github.com/user/gormproc/pkg/parser"
)

// CallGraphAnalyzer analyzes the AST of a procedure to find transaction blocks,
// parameter names, and local variable declarations to aid in code generation.
type CallGraphAnalyzer struct {
	Proc          *parser.Procedure
	HasTxBlock    bool
	Parameters    []string
	LocalVars     []string
	ReturnsError  bool
	ReturnsResult bool
	ResultType    string
}

func AnalyzeCallGraph(proc *parser.Procedure) (*CallGraphAnalyzer, error) {
	analyzer := &CallGraphAnalyzer{
		Proc: proc,
	}

	// Simple MVP analysis
	// 1. Analyze parameters
	if proc.Func.Type.Params != nil {
		for _, field := range proc.Func.Type.Params.List {
			for _, name := range field.Names {
				analyzer.Parameters = append(analyzer.Parameters, name.Name)
			}
		}
	}

	// 2. Analyze returns
	if proc.Func.Type.Results != nil {
		for _, field := range proc.Func.Type.Results.List {
			// Typically (ResultType, error)
			// Simplification for MVP
			if len(field.Names) > 0 {
				analyzer.ReturnsResult = true
			}
		}
		if len(proc.Func.Type.Results.List) > 1 {
			analyzer.ReturnsResult = true
		}
	}

	// 3. Find Transaction blocks and local vars
	ast.Inspect(proc.Func, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "Transaction" {
					analyzer.HasTxBlock = true
				}
			}
		case *ast.GenDecl:
			if node.Tok.String() == "var" {
				for _, spec := range node.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range vs.Names {
							analyzer.LocalVars = append(analyzer.LocalVars, name.Name)
						}
					}
				}
			}
		}
		return true
	})

	return analyzer, nil
}
