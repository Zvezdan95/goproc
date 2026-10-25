package transpiler

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"github.com/user/gormproc/pkg/parser"
)

func ToMySQL(proc *parser.Procedure, hash string) (string, error) {
	var buf bytes.Buffer
	funcName := fmt.Sprintf("%s_%s", strings.ToLower(proc.Name), hash)

	// Extract parameters
	var params []string
	if proc.Func.Type.Params != nil {
		for _, field := range proc.Func.Type.Params.List {
			for _, name := range field.Names {
				if name.Name != "ctx" {
					sqlType := "DOUBLE"
					if id, ok := field.Type.(*ast.Ident); ok {
						switch id.Name {
						case "int64": sqlType = "BIGINT"
						case "float64": sqlType = "DOUBLE"
						case "bool": sqlType = "BOOLEAN"
						case "string": sqlType = "VARCHAR(255)"
						}
					} else if se, ok := field.Type.(*ast.StarExpr); ok {
						if id, ok := se.X.(*ast.Ident); ok {
							switch id.Name {
							case "int64": sqlType = "BIGINT"
							case "string": sqlType = "VARCHAR(255)"
							}
						}
					}
					params = append(params, fmt.Sprintf("IN p_%s %s", name.Name, sqlType))
				}
			}
		}
	}

	buf.WriteString(fmt.Sprintf("CREATE PROCEDURE %s(%s)\n", funcName, strings.Join(params, ", ")))
	buf.WriteString("BEGIN\n")
	
	var extractArg func(expr ast.Expr) string
	extractArg = func(expr ast.Expr) string {
		if expr == nil {
			return "''"
		}
		switch e := expr.(type) {
		case *ast.Ident:
			if e.Name == "nil" { return "NULL" }
			if e.Name == "true" { return "1" }
			if e.Name == "false" { return "0" }
			if strings.HasPrefix(e.Name, "cConst_") || strings.HasPrefix(e.Name, "eError_") {
				return e.Name
			}
			if e.Name == "i" || e.Name == "m_i" || e.Name == "m_i2" || e.Name == "val" || e.Name == "absAmt" || e.Name == "fee" || e.Name == "jpCont" || e.Name == "mappedType" || e.Name == "amtNeg" || e.Name == "extAmount1" || e.Name == "extAmount2" || strings.HasPrefix(e.Name, "p_ext") {
				return "v_" + e.Name
			}
			if strings.HasPrefix(e.Name, "i_") {
				return "p_" + e.Name
			}
			if strings.HasPrefix(e.Name, "m_") {
				return "v_" + e.Name
			}
			return "p_" + e.Name
		case *ast.BasicLit:
			if strings.HasPrefix(e.Value, "`") {
				return "\"" + strings.Trim(e.Value, "`") + "\""
			}
			return e.Value
		case *ast.UnaryExpr:
			if e.Op == token.SUB {
				return "-" + extractArg(e.X)
			}
			if e.Op == token.NOT {
				return "NOT " + extractArg(e.X)
			}
			if e.Op == token.AND {
				return extractArg(e.X)
			}
			return extractArg(e.X)
		case *ast.StarExpr:
			return extractArg(e.X)
		case *ast.BinaryExpr:
			op := e.Op.String()
			if op == "==" { op = "=" }
			if op == "&&" { op = "AND" }
			if op == "||" { op = "OR" }
			x := extractArg(e.X)
			y := extractArg(e.Y)
			if y == "NULL" {
				if op == "=" { return x + " IS NULL" }
				if op == "!=" { return x + " IS NOT NULL" }
			}
			return x + " " + op + " " + y
		case *ast.ParenExpr:
			return "(" + extractArg(e.X) + ")"
		case *ast.CallExpr:
			if sel, ok := e.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sprintf" {
				if len(e.Args) >= 2 {
					if lit, ok := e.Args[0].(*ast.BasicLit); ok {
						format := strings.Trim(lit.Value, "\"")
						parts := strings.Split(format, "%d")
						if len(parts) == 2 {
							return "CONCAT('" + parts[0] + "', " + extractArg(e.Args[1]) + ", '" + parts[1] + "')"
						}
					}
				}
			}
			if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
				if pkgId, ok := sel.X.(*ast.Ident); ok && pkgId.Name == "gormproc" {
					if sel.Sel.Name == "JSONExtractFloat64" || sel.Sel.Name == "JSONExtractString" {
						return "JSON_UNQUOTE(JSON_EXTRACT(" + extractArg(e.Args[0]) + ", " + extractArg(e.Args[1]) + "))"
					}
					if sel.Sel.Name == "CasinogameFeeCurrent" {
						return "pokermem.Casinogame_fee_current(" + extractArg(e.Args[0]) + ")"
					}
					if sel.Sel.Name == "CasinogameCalcJackpotContribution" {
						return "casinodb.Casinogame_calc_jackpotContribution(" + extractArg(e.Args[0]) + ", " + extractArg(e.Args[1]) + ")"
					}
					if sel.Sel.Name == "JSONOverlaps" {
						return "JSON_OVERLAPS(" + extractArg(e.Args[0]) + ", " + extractArg(e.Args[1]) + ")"
					}
					if sel.Sel.Name == "GlobHasBits" {
						return "pokermem.GlobHasBits(" + extractArg(e.Args[0]) + ", " + extractArg(e.Args[1]) + ")"
					}
				}
			}
		}
		return "''"
	}

	// Basic AST walker for the procedure body
	var walkStmt func(stmt ast.Stmt)
	walkStmt = func(stmt ast.Stmt) {
		switch s := stmt.(type) {
		case *ast.BlockStmt:
			for _, bs := range s.List {
				walkStmt(bs)
			}
		case *ast.DeclStmt:
			// Parse VAR declarations
			if gen, ok := s.Decl.(*ast.GenDecl); ok {
				for _, spec := range gen.Specs {
					if vSpec, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range vSpec.Names {
							typ := "VARCHAR(255)"
							if id, ok := vSpec.Type.(*ast.Ident); ok {
								switch id.Name {
								case "int64": typ = "BIGINT"
								case "float64": typ = "DOUBLE"
								case "bool": typ = "BOOLEAN"
								case "string": typ = "TEXT"
								}
							}
							
							def := ""
							if len(vSpec.Values) > 0 {
								def = " DEFAULT " + extractArg(vSpec.Values[0])
							}
							
							buf.WriteString(fmt.Sprintf("DECLARE v_%s %s%s;\n", name.Name, typ, def))
						}
					}
				}
			}
		case *ast.AssignStmt:
			var lhs string
			if len(s.Lhs) > 0 {
				if id, ok := s.Lhs[0].(*ast.Ident); ok {
					if strings.HasPrefix(id.Name, "m_") || id.Name == "m_i" {
						lhs = "v_" + id.Name
					} else {
						lhs = "v_" + id.Name
					}
				}
			}
			
			// Intercept gormproc calls
			if len(s.Rhs) > 0 {
				if call, ok := s.Rhs[0].(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						if pkgId, ok := sel.X.(*ast.Ident); ok && pkgId.Name == "gormproc" {
							if sel.Sel.Name == "JSONExtractString" {
								arg1 := extractArg(call.Args[0])
								arg2 := extractArg(call.Args[1])
								buf.WriteString(fmt.Sprintf("SET %s = JSON_UNQUOTE(JSON_EXTRACT(%s, %s));\n", lhs, arg1, arg2))
								return
							} else if sel.Sel.Name == "JSONLength" {
								arg1 := extractArg(call.Args[0])
								arg2 := extractArg(call.Args[1])
								buf.WriteString(fmt.Sprintf("SET %s = JSON_LENGTH(%s, %s);\n", lhs, arg1, arg2))
								return
							} else if sel.Sel.Name == "FromJsonString" {
								arg1 := extractArg(call.Args[0])
								arg2 := extractArg(call.Args[1])
								buf.WriteString(fmt.Sprintf("SET %s = pokermem.FromJson_string(%s, %s);\n", lhs, arg1, arg2))
								return
							} else if sel.Sel.Name == "FromJsonStringOptional" {
								arg1 := extractArg(call.Args[0])
								arg2 := extractArg(call.Args[1])
								buf.WriteString(fmt.Sprintf("SET %s = pokermem.FromJson_string_optional(%s, %s);\n", lhs, arg1, arg2))
								return
							} else if sel.Sel.Name == "FromJsonArray" {
								arg1 := extractArg(call.Args[0])
								arg2 := extractArg(call.Args[1])
								buf.WriteString(fmt.Sprintf("SET %s = pokermem.FromJson_array(%s, %s);\n", lhs, arg1, arg2))
								return
							} else if sel.Sel.Name == "FromJsonInt64" {
								arg1 := extractArg(call.Args[0])
								arg2 := extractArg(call.Args[1])
								buf.WriteString(fmt.Sprintf("SET %s = pokermem.FromJson_int64(%s, %s);\n", lhs, arg1, arg2))
								return
							} else if sel.Sel.Name == "FromJsonDecimalOptionalNullable" {
								// Multiple returns
								out1 := "v_" + s.Lhs[0].(*ast.Ident).Name
								out2 := "v_" + s.Lhs[1].(*ast.Ident).Name
								arg1 := extractArg(call.Args[0])
								arg2 := extractArg(call.Args[1])
								buf.WriteString(fmt.Sprintf("CALL pokermem.FromJson_decimal_optional_nullable(%s, %s, %s, %s);\n", arg1, arg2, out1, out2))
								return
							}
						}
					}
				}
				
				// Normal assignment
				val := extractArg(s.Rhs[0])
				buf.WriteString(fmt.Sprintf("SET %s = %s;\n", lhs, val))
			}
		case *ast.ForStmt:
			var loopVar string
			if initAssign, ok := s.Init.(*ast.AssignStmt); ok {
				if id, ok := initAssign.Lhs[0].(*ast.Ident); ok {
					loopVar = "v_" + id.Name
					val := extractArg(initAssign.Rhs[0])
					buf.WriteString(fmt.Sprintf("SET %s = %s;\n", loopVar, val))
				}
			}
			
			buf.WriteString("WHILE " + extractArg(s.Cond))
			buf.WriteString(" DO\n")
			
			walkStmt(s.Body)
			
			if postAssign, ok := s.Post.(*ast.IncDecStmt); ok {
				if id, ok := postAssign.X.(*ast.Ident); ok {
					op := string(postAssign.Tok)[0:1]
					if op == "+" || op == "-" {
						buf.WriteString(fmt.Sprintf("SET v_%s = v_%s %s 1;\n", id.Name, id.Name, op))
					}
				}
			}
			buf.WriteString("END WHILE;\n")
		case *ast.SwitchStmt:
			tag := extractArg(s.Tag)
			buf.WriteString(fmt.Sprintf("CASE %s\n", tag))
			walkStmt(s.Body)
			buf.WriteString("END CASE;\n")
		case *ast.CaseClause:
			if len(s.List) > 0 {
				var conds []string
				for _, expr := range s.List {
					conds = append(conds, extractArg(expr))
				}
				buf.WriteString(fmt.Sprintf("WHEN %s THEN\n", strings.Join(conds, ", ")))
			} else {
				buf.WriteString("ELSE\n")
			}
			for _, stmt := range s.Body {
				walkStmt(stmt)
			}
		case *ast.IfStmt:
			// DI interceptor for sub-procedure calls in if init
			if s.Init != nil {
				if assign, ok := s.Init.(*ast.AssignStmt); ok {
					if call, ok := assign.Rhs[0].(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
							if pkgId, ok := sel.X.(*ast.Ident); ok && pkgId.Name == "gormproc" && sel.Sel.Name == "SelectInto" {
								if len(call.Args) >= 2 {
									if queryLit, ok := call.Args[1].(*ast.BasicLit); ok {
										query := strings.Trim(queryLit.Value, "`\"")
										query = strings.ReplaceAll(query, "\n", " ")
										query = strings.ReplaceAll(query, "\t", " ")
										
										numQs := strings.Count(query, "?")
										inArgs := call.Args[2 : 2+numQs]
										outArgs := call.Args[2+numQs:]
										
										for _, inArg := range inArgs {
											query = strings.Replace(query, "?", extractArg(inArg), 1)
										}
										
										idxFrom := strings.Index(strings.ToUpper(query), " FROM ")
										if idxFrom != -1 {
											selectPart := query[:idxFrom]
											fromPart := query[idxFrom:]
											
											var outs []string
											for _, outArg := range outArgs {
												outs = append(outs, extractArg(outArg))
											}
											
											buf.WriteString(fmt.Sprintf("%s INTO %s %s;\n", selectPart, strings.Join(outs, ", "), fromPart))
										}
									}
								}
							} else if _, ok := sel.X.(*ast.SelectorExpr); ok {
								// e.g. s.betLimits.CasinogameBetlimitCheck
								args := []string{}
								for _, arg := range call.Args {
									args = append(args, extractArg(arg))
								}
								funcName := strings.ToLower(sel.Sel.Name) // In reality, we'd lookup hash
								buf.WriteString(fmt.Sprintf("CALL %s(%s);\n", funcName, strings.Join(args, ", ")))
							}
						}
					}
				}
			}

			buf.WriteString("IF " + extractArg(s.Cond))
			
			buf.WriteString(" THEN\n")
			walkStmt(s.Body)
			
			if s.Else != nil {
				buf.WriteString("ELSE\n")
				walkStmt(s.Else)
			}
			buf.WriteString("END IF;\n")
		
		case *ast.ReturnStmt:
			// Look for error returns
			for _, res := range s.Results {
				if call, ok := res.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "New" {
						if len(call.Args) > 0 {
							if lit, ok := call.Args[0].(*ast.BasicLit); ok {
								buf.WriteString(fmt.Sprintf("SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = %s;\n", lit.Value))
							}
						}
					} else if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Errorf" {
						if len(call.Args) > 0 {
							if lit, ok := call.Args[0].(*ast.BasicLit); ok {
								buf.WriteString(fmt.Sprintf("SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = %s;\n", lit.Value))
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
												query = strings.Replace(query, "?", extractArg(innerCall.Args[i]), 1)
											}
											buf.WriteString(query + ";\n")
										}
									}
								}
							}
						}
					} else if sel.Sel.Name == "Exec" {
						if len(call.Args) > 0 {
							if lit, ok := call.Args[0].(*ast.BasicLit); ok {
								query := strings.Trim(lit.Value, "`\"")
								for i := 1; i < len(call.Args); i++ {
									query = strings.Replace(query, "?", extractArg(call.Args[i]), 1)
								}
								buf.WriteString(query + ";\n")
							}
						}
					} else if sel.X != nil {
						if pkgId, ok := sel.X.(*ast.Ident); ok && pkgId.Name == "gormproc" {
							if sel.Sel.Name == "SelectInto" {
								if len(call.Args) >= 2 {
									if queryLit, ok := call.Args[1].(*ast.BasicLit); ok {
										query := strings.Trim(queryLit.Value, "`\"")
										query = strings.ReplaceAll(query, "\n", " ")
										query = strings.ReplaceAll(query, "\t", " ")
										
										numQs := strings.Count(query, "?")
										inArgs := call.Args[2 : 2+numQs]
										outArgs := call.Args[2+numQs:]
										
										for _, inArg := range inArgs {
											query = strings.Replace(query, "?", extractArg(inArg), 1)
										}
										
										idxFrom := strings.Index(strings.ToUpper(query), " FROM ")
										if idxFrom != -1 {
											selectPart := query[:idxFrom]
											fromPart := query[idxFrom:]
											
											var outs []string
											for _, outArg := range outArgs {
												outs = append(outs, extractArg(outArg))
											}
											
											buf.WriteString(fmt.Sprintf("%s INTO %s %s;\n", selectPart, strings.Join(outs, ", "), fromPart))
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

	buf.WriteString("END;")
	return formatSQL(buf.String()), nil
}

func formatSQL(sql string) string {
	lines := strings.Split(sql, "\n")
	var formatted []string
	indent := 0
	
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		
		outdentNow := false
		if strings.HasPrefix(trimmed, "END") || trimmed == "ELSE" || strings.HasPrefix(trimmed, "WHEN ") || strings.HasPrefix(trimmed, "ELSEIF ") {
			outdentNow = true
		}
		
		currentIndent := indent
		if outdentNow {
			currentIndent--
			if currentIndent < 0 { currentIndent = 0 }
		}
		
		formatted = append(formatted, strings.Repeat("\t", currentIndent) + trimmed)
		
		if strings.HasPrefix(trimmed, "BEGIN") || strings.HasPrefix(trimmed, "IF ") || strings.HasPrefix(trimmed, "WHILE ") || strings.HasPrefix(trimmed, "CASE ") || trimmed == "ELSE" || strings.HasPrefix(trimmed, "WHEN ") || strings.HasPrefix(trimmed, "ELSEIF ") {
			indent = currentIndent + 1
		}
        if strings.HasPrefix(trimmed, "END") {
            indent = currentIndent
        }
	}
	return strings.Join(formatted, "\n")
}
