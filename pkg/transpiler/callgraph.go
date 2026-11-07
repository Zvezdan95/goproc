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

	fn := proc.EffectiveFunc()
	if fn == nil {
		return analyzer, nil
	}

	// 1. Analyze parameters
	if fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			for _, name := range field.Names {
				analyzer.Parameters = append(analyzer.Parameters, name.Name)
			}
		}
	}

	// 2. Analyze returns
	if fn.Type.Results != nil {
		resList := fn.Type.Results.List
		if len(resList) > 1 {
			analyzer.ReturnsResult = true
		} else if len(resList) == 1 {
			if id, ok := resList[0].Type.(*ast.Ident); ok && id.Name == "error" {
				analyzer.ReturnsError = true
			} else {
				analyzer.ReturnsResult = true
			}
		}
		if len(resList) >= 1 {
			if id, ok := resList[len(resList)-1].Type.(*ast.Ident); ok && id.Name == "error" {
				analyzer.ReturnsError = true
			}
		}
	}

	// 3. Find Transaction blocks and local vars
	ast.Inspect(fn, func(n ast.Node) bool {
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
		case *ast.AssignStmt:
			if node.Tok.String() == ":=" {
				for _, lhs := range node.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						if id.Name != "_" && id.Name != "err" && id.Name != "res" {
							analyzer.LocalVars = append(analyzer.LocalVars, id.Name)
						}
					}
				}
			}
		}
		return true
	})

	return analyzer, nil
}
