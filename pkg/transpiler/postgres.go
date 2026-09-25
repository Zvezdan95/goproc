package transpiler

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"github.com/user/gormproc/pkg/parser"
)

func ToPostgres(proc *parser.Procedure, hash string) (string, error) {
	var buf bytes.Buffer
	funcName := fmt.Sprintf("%s_%s", strings.ToLower(proc.Name), hash)

	var params []string
	if proc.Func.Type.Params != nil {
		for _, field := range proc.Func.Type.Params.List {
			for _, name := range field.Names {
				if name.Name != "ctx" {
					sqlType := "NUMERIC"
					if id, ok := field.Type.(*ast.Ident); ok {
						switch id.Name {
						case "int64": sqlType = "BIGINT"
						case "float64": sqlType = "NUMERIC"
						case "bool": sqlType = "BOOLEAN"
						case "string": sqlType = "VARCHAR"
						}
					} else if se, ok := field.Type.(*ast.StarExpr); ok {
						if id, ok := se.X.(*ast.Ident); ok {
							switch id.Name {
							case "int64": sqlType = "BIGINT"
							case "string": sqlType = "VARCHAR"
							}
						}
					}
					params = append(params, fmt.Sprintf("p_%s %s", name.Name, sqlType))
				}
			}
		}
	}

	buf.WriteString(fmt.Sprintf("CREATE OR REPLACE PROCEDURE\n%s(%s)\nLANGUAGE plpgsql AS $$\n#variable_conflict use_variable\n", funcName, strings.Join(params, ", ")))
	buf.WriteString("BEGIN\n")
	
	var walkStmt func(stmt ast.Stmt)
	walkStmt = func(stmt ast.Stmt) {
		switch s := stmt.(type) {
		case *ast.BlockStmt:
			for _, bs := range s.List {
				walkStmt(bs)
			}
		case *ast.IfStmt:
			buf.WriteString("IF ")
			if bin, ok := s.Cond.(*ast.BinaryExpr); ok {
				if x, ok := bin.X.(*ast.Ident); ok {
					buf.WriteString(fmt.Sprintf("p_%s ", x.Name))
				}
				if y, ok := bin.Y.(*ast.BasicLit); ok {
					if bin.Op == token.LEQ {
						buf.WriteString("<= ")
					} else if bin.Op == token.EQL {
						buf.WriteString("= ")
					} else if bin.Op == token.NEQ {
						buf.WriteString("!= ")
					}
					buf.WriteString(y.Value)
				} else if y, ok := bin.Y.(*ast.Ident); ok {
					if y.Name == "nil" {
						if bin.Op == token.NEQ {
							buf.WriteString("IS NOT NULL")
						} else {
							buf.WriteString("IS NULL")
						}
					} else {
						if bin.Op == token.LEQ {
							buf.WriteString("<= ")
						} else if bin.Op == token.EQL {
							buf.WriteString("= ")
						} else if bin.Op == token.NEQ {
							buf.WriteString("!= ")
						}
						buf.WriteString(fmt.Sprintf("p_%s", y.Name))
					}
				}
			} else if id, ok := s.Cond.(*ast.Ident); ok {
				buf.WriteString(fmt.Sprintf("p_%s = TRUE", id.Name))
			} else {
				buf.WriteString(" 1=1 ")
			}
			buf.WriteString(" THEN\n")
			walkStmt(s.Body)
			
			if s.Else != nil {
				buf.WriteString("ELSE\n")
				walkStmt(s.Else)
			}
			buf.WriteString("END IF;\n")
		
		case *ast.ReturnStmt:
			for _, res := range s.Results {
				if call, ok := res.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "New" {
						if len(call.Args) > 0 {
							if lit, ok := call.Args[0].(*ast.BasicLit); ok {
								buf.WriteString(fmt.Sprintf("RAISE EXCEPTION %s;\n", lit.Value))
							}
						}
					}
				}
			}
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if sel.Sel.Name == "Error" {
						if innerCall, ok := sel.X.(*ast.CallExpr); ok {
							if innerSel, ok := innerCall.Fun.(*ast.SelectorExpr); ok {
								if innerSel.Sel.Name == "Exec" {
									if len(innerCall.Args) > 0 {
										if lit, ok := innerCall.Args[0].(*ast.BasicLit); ok {
											query := strings.Trim(lit.Value, "`\"")
											for i := 1; i < len(innerCall.Args); i++ {
												if id, ok := innerCall.Args[i].(*ast.Ident); ok {
													query = strings.Replace(query, "?", "p_"+id.Name, 1)
												}
											}
											buf.WriteString(query + ";\n")
										}
									}
								} else if innerSel.Sel.Name == "Scan" {
									if rawCall, ok := innerSel.X.(*ast.CallExpr); ok {
										if len(rawCall.Args) > 0 {
											if lit, ok := rawCall.Args[0].(*ast.BasicLit); ok {
												query := strings.Trim(lit.Value, "`\"")
												for i := 1; i < len(rawCall.Args); i++ {
													if id, ok := rawCall.Args[i].(*ast.Ident); ok {
														query = strings.Replace(query, "?", "p_"+id.Name, 1)
													}
												}
												buf.WriteString(query + " LIMIT 1;\n")
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	ast.Inspect(proc.Func, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Transaction" {
				if len(call.Args) > 0 {
					if lit, ok := call.Args[0].(*ast.FuncLit); ok {
						walkStmt(lit.Body)
					}
				}
			}
		}
		return true
	})

	buf.WriteString("END;\n$$;")
	return buf.String(), nil
}
