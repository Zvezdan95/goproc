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
	var bodyBuf bytes.Buffer
	var varDecls bytes.Buffer
	funcName := fmt.Sprintf("%s_%s", strings.ToLower(proc.Name), hash)

	analysis, _ := AnalyzeCallGraph(proc)
	localVarMap := make(map[string]bool)
	paramMap := make(map[string]bool)
	if analysis != nil {
		for _, v := range analysis.LocalVars {
			localVarMap[v] = true
		}
		for _, p := range analysis.Parameters {
			paramMap[p] = true
		}
	}

	// Extract parameters
	var params []string
	if proc.Func.Type.Params != nil {
		for _, field := range proc.Func.Type.Params.List {
			for _, name := range field.Names {
				if name.Name != "ctx" {
					sqlType := "NUMERIC"
					if id, ok := field.Type.(*ast.Ident); ok {
						switch id.Name {
						case "int64":
							sqlType = "BIGINT"
						case "float64":
							sqlType = "NUMERIC"
						case "bool":
							sqlType = "BOOLEAN"
						case "string":
							sqlType = "VARCHAR(255)"
						}
					} else if se, ok := field.Type.(*ast.StarExpr); ok {
						if id, ok := se.X.(*ast.Ident); ok {
							switch id.Name {
							case "int64":
								sqlType = "BIGINT"
							case "string":
								sqlType = "VARCHAR(255)"
							}
						}
					}
					params = append(params, fmt.Sprintf("p_%s %s", name.Name, sqlType))
				}
			}
		}
	}

	var extractArg func(expr ast.Expr) string
	extractArg = func(expr ast.Expr) string {
		if expr == nil {
			return "''"
		}
		switch e := expr.(type) {
		case *ast.Ident:
			if e.Name == "nil" {
				return "NULL"
			}
			if e.Name == "true" {
				return "TRUE"
			}
			if e.Name == "false" {
				return "FALSE"
			}
			if strings.HasPrefix(e.Name, "cConst_") || strings.HasPrefix(e.Name, "eError_") {
				return e.Name
			}
			if localVarMap[e.Name] {
				return "v_" + e.Name
			}
			if paramMap[e.Name] {
				return "p_" + e.Name
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
			return "v_" + e.Name
		case *ast.BasicLit:
			if strings.HasPrefix(e.Value, "`") {
				return "'" + strings.Trim(e.Value, "`") + "'"
			}
			if strings.HasPrefix(e.Value, "\"") {
				return "'" + strings.Trim(e.Value, "\"") + "'"
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
			if op == "==" {
				op = "="
			}
			if op == "&&" {
				op = "AND"
			}
			if op == "||" {
				op = "OR"
			}
			x := extractArg(e.X)
			y := extractArg(e.Y)
			if y == "NULL" {
				if op == "=" {
					return x + " IS NULL"
				}
				if op == "!=" {
					return x + " IS NOT NULL"
				}
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
						return "(" + extractArg(e.Args[0]) + " ->> " + extractArg(e.Args[1]) + ")"
					}
					if sel.Sel.Name == "CasinogameFeeCurrent" {
						return "pokermem.Casinogame_fee_current(" + extractArg(e.Args[0]) + ")"
					}
					if sel.Sel.Name == "CasinogameCalcJackpotContribution" {
						return "casinodb.Casinogame_calc_jackpotContribution(" + extractArg(e.Args[0]) + ", " + extractArg(e.Args[1]) + ")"
					}
					if sel.Sel.Name == "JSONOverlaps" {
						return "((" + extractArg(e.Args[0]) + ")::jsonb ?| array[" + extractArg(e.Args[1]) + "])"
					}
					if sel.Sel.Name == "GlobHasBits" {
						return "pokermem.GlobHasBits(" + extractArg(e.Args[0]) + ", " + extractArg(e.Args[1]) + ")"
					}
				}
			}
		}
		return "''"
	}

	declaredVars := make(map[string]bool)
	declareVar := func(name string, typeNode ast.Expr, valExpr ast.Expr) {
		if declaredVars[name] || name == "res" || name == "err" {
			return
		}
		declaredVars[name] = true
		localVarMap[name] = true

		typ := "VARCHAR"
		if id, ok := typeNode.(*ast.Ident); ok {
			switch id.Name {
			case "int64":
				typ = "BIGINT"
			case "float64":
				typ = "NUMERIC"
			case "bool":
				typ = "BOOLEAN"
			case "string":
				typ = "TEXT"
			default:
				return
			}
		} else {
			return
		}

		def := ""
		if valExpr != nil {
			def = " := " + extractArg(valExpr)
		}
		varDecls.WriteString(fmt.Sprintf("\t%s %s%s;\n", "v_"+name, typ, def))
	}

	emitQuery := func(expr ast.Expr) bool {
		if sel, ok := expr.(*ast.SelectorExpr); ok && sel.Sel.Name == "Error" {
			expr = sel.X
		}

		call, ok := expr.(*ast.CallExpr)
		if !ok {
			return false
		}

		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Scan" {
			if innerCall, ok := sel.X.(*ast.CallExpr); ok {
				call = innerCall
			}
		}

		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Exec" || sel.Sel.Name == "Raw") {
			if len(call.Args) > 0 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok {
					query := strings.Trim(lit.Value, "`\"")
					for i := 1; i < len(call.Args); i++ {
						query = strings.Replace(query, "?", extractArg(call.Args[i]), 1)
					}
					bodyBuf.WriteString(query + ";\n")
					return true
				}
			}
		}
		return false
	}

	// Pre-collect variable declarations from function body
	if proc.Func.Body != nil {
		ast.Inspect(proc.Func.Body, func(n ast.Node) bool {
			if gen, ok := n.(*ast.GenDecl); ok && gen.Tok == token.VAR {
				for _, spec := range gen.Specs {
					if vSpec, ok := spec.(*ast.ValueSpec); ok {
						var val ast.Expr
						if len(vSpec.Values) > 0 {
							val = vSpec.Values[0]
						}
						for _, name := range vSpec.Names {
							declareVar(name.Name, vSpec.Type, val)
						}
					}
				}
			}
			return true
		})
	}

	// AST walker for the procedure body
	var walkStmt func(stmt ast.Stmt)
	walkStmt = func(stmt ast.Stmt) {
		switch s := stmt.(type) {
		case *ast.BlockStmt:
			for _, bs := range s.List {
				walkStmt(bs)
			}
		case *ast.DeclStmt:
			if gen, ok := s.Decl.(*ast.GenDecl); ok {
				for _, spec := range gen.Specs {
					if vSpec, ok := spec.(*ast.ValueSpec); ok {
						var val ast.Expr
						if len(vSpec.Values) > 0 {
							val = vSpec.Values[0]
						}
						for _, name := range vSpec.Names {
							declareVar(name.Name, vSpec.Type, val)
						}
					}
				}
			}
		case *ast.AssignStmt:
			var lhs string
			if len(s.Lhs) > 0 {
				if id, ok := s.Lhs[0].(*ast.Ident); ok {
					if s.Tok == token.DEFINE {
						localVarMap[id.Name] = true
					}
					lhs = "v_" + id.Name
				} else if sel, ok := s.Lhs[0].(*ast.SelectorExpr); ok {
					lhs = "v_" + sel.Sel.Name
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
								bodyBuf.WriteString(fmt.Sprintf("%s := %s ->> %s;\n", lhs, arg1, arg2))
								return
							} else if sel.Sel.Name == "JSONLength" {
								arg1 := extractArg(call.Args[0])
								bodyBuf.WriteString(fmt.Sprintf("%s := jsonb_array_length((%s)::jsonb);\n", lhs, arg1))
								return
							} else if sel.Sel.Name == "FromJsonString" {
								arg1 := extractArg(call.Args[0])
								arg2 := extractArg(call.Args[1])
								bodyBuf.WriteString(fmt.Sprintf("%s := pokermem.FromJson_string(%s, %s);\n", lhs, arg1, arg2))
								return
							} else if sel.Sel.Name == "FromJsonStringOptional" {
								arg1 := extractArg(call.Args[0])
								arg2 := extractArg(call.Args[1])
								bodyBuf.WriteString(fmt.Sprintf("%s := pokermem.FromJson_string_optional(%s, %s);\n", lhs, arg1, arg2))
								return
							} else if sel.Sel.Name == "FromJsonArray" {
								arg1 := extractArg(call.Args[0])
								arg2 := extractArg(call.Args[1])
								bodyBuf.WriteString(fmt.Sprintf("%s := pokermem.FromJson_array(%s, %s);\n", lhs, arg1, arg2))
								return
							} else if sel.Sel.Name == "FromJsonInt64" {
								arg1 := extractArg(call.Args[0])
								arg2 := extractArg(call.Args[1])
								bodyBuf.WriteString(fmt.Sprintf("%s := pokermem.FromJson_int64(%s, %s);\n", lhs, arg1, arg2))
								return
							} else if sel.Sel.Name == "FromJsonDecimalOptionalNullable" {
								out1 := "v_" + s.Lhs[0].(*ast.Ident).Name
								out2 := "v_" + s.Lhs[1].(*ast.Ident).Name
								arg1 := extractArg(call.Args[0])
								arg2 := extractArg(call.Args[1])
								bodyBuf.WriteString(fmt.Sprintf("CALL pokermem.FromJson_decimal_optional_nullable(%s, %s, %s, %s);\n", arg1, arg2, out1, out2))
								return
							}
						}
					}
				}

				// Normal assignment
				val := extractArg(s.Rhs[0])
				bodyBuf.WriteString(fmt.Sprintf("%s := %s;\n", lhs, val))
			}
		case *ast.ForStmt:
			var loopVar string
			if initAssign, ok := s.Init.(*ast.AssignStmt); ok {
				if id, ok := initAssign.Lhs[0].(*ast.Ident); ok {
					loopVar = "v_" + id.Name
					val := extractArg(initAssign.Rhs[0])
					bodyBuf.WriteString(fmt.Sprintf("%s := %s;\n", loopVar, val))
				}
			}

			bodyBuf.WriteString("WHILE " + extractArg(s.Cond))
			bodyBuf.WriteString(" LOOP\n")

			walkStmt(s.Body)

			if postAssign, ok := s.Post.(*ast.IncDecStmt); ok {
				if id, ok := postAssign.X.(*ast.Ident); ok {
					if op := postAssign.Tok.String()[:1]; op == "+" || op == "-" {
						bodyBuf.WriteString(fmt.Sprintf("v_%s := v_%s %s 1;\n", id.Name, id.Name, op))
					}
				}
			}
			bodyBuf.WriteString("END LOOP;\n")
		case *ast.SwitchStmt:
			tag := extractArg(s.Tag)
			bodyBuf.WriteString(fmt.Sprintf("CASE %s\n", tag))
			walkStmt(s.Body)
			bodyBuf.WriteString("END CASE;\n")
		case *ast.CaseClause:
			if len(s.List) > 0 {
				var conds []string
				for _, expr := range s.List {
					conds = append(conds, extractArg(expr))
				}
				bodyBuf.WriteString(fmt.Sprintf("WHEN %s THEN\n", strings.Join(conds, ", ")))
			} else {
				bodyBuf.WriteString("ELSE\n")
			}
			for _, stmt := range s.Body {
				walkStmt(stmt)
			}
		case *ast.IfStmt:
			if s.Init != nil {
				if assign, ok := s.Init.(*ast.AssignStmt); ok {
					if len(assign.Rhs) > 0 {
						if emitQuery(assign.Rhs[0]) {
							if bin, ok := s.Cond.(*ast.BinaryExpr); ok {
								if id, ok := bin.X.(*ast.Ident); ok && id.Name == "err" {
									return
								}
							}
						}
					}
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

											bodyBuf.WriteString(fmt.Sprintf("%s INTO %s %s;\n", selectPart, strings.Join(outs, ", "), fromPart))
										}
									}
								}
							} else if _, ok := sel.X.(*ast.SelectorExpr); ok {
								args := []string{}
								for _, arg := range call.Args {
									args = append(args, extractArg(arg))
								}
								funcName := strings.ToLower(sel.Sel.Name)
								bodyBuf.WriteString(fmt.Sprintf("CALL %s(%s);\n", funcName, strings.Join(args, ", ")))
							}
						}
					}
				}
			}

			bodyBuf.WriteString("IF " + extractArg(s.Cond))
			bodyBuf.WriteString(" THEN\n")
			walkStmt(s.Body)

			if s.Else != nil {
				bodyBuf.WriteString("ELSE\n")
				walkStmt(s.Else)
			}
			bodyBuf.WriteString("END IF;\n")

		case *ast.ReturnStmt:
			for _, res := range s.Results {
				if emitQuery(res) {
					continue
				}
				if call, ok := res.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "New" || sel.Sel.Name == "Errorf") {
						if len(call.Args) > 0 {
							if lit, ok := call.Args[0].(*ast.BasicLit); ok {
								msg := strings.Trim(lit.Value, "\"'`")
								bodyBuf.WriteString(fmt.Sprintf("RAISE EXCEPTION '%s';\n", msg))
							}
						}
					}
				}
			}
		case *ast.ExprStmt:
			if emitQuery(s.X) {
				return
			}
			if call, ok := s.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.X != nil {
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

										bodyBuf.WriteString(fmt.Sprintf("%s INTO %s %s;\n", selectPart, strings.Join(outs, ", "), fromPart))
									}
								}
							}
						}
					}
				}
			}
		}
	}

	foundTx := false
	ast.Inspect(proc.Func, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Transaction" {
				if len(call.Args) > 0 {
					if lit, ok := call.Args[0].(*ast.FuncLit); ok {
						foundTx = true
						walkStmt(lit.Body)
					}
				}
			}
		}
		return true
	})

	if !foundTx && proc.Func.Body != nil {
		walkStmt(proc.Func.Body)
	}

	var finalBuf bytes.Buffer
	finalBuf.WriteString(fmt.Sprintf("CREATE OR REPLACE PROCEDURE\n%s(%s)\nLANGUAGE plpgsql AS $$\n#variable_conflict use_variable\n", funcName, strings.Join(params, ", ")))
	if varDecls.Len() > 0 {
		finalBuf.WriteString("DECLARE\n")
		finalBuf.WriteString(varDecls.String())
	}
	finalBuf.WriteString("BEGIN\n")
	finalBuf.WriteString(bodyBuf.String())
	finalBuf.WriteString("END;\n$$;")

	return formatSQL(finalBuf.String()), nil
}
