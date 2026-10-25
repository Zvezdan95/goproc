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
						case "int64", "uint64", "uint32", "uint":
							sqlType = "BIGINT"
						case "int", "int32":
							sqlType = "INTEGER"
						case "float64", "float32":
							sqlType = "NUMERIC"
						case "bool":
							sqlType = "BOOLEAN"
						case "string":
							sqlType = "VARCHAR(255)"
						}
					} else if se, ok := field.Type.(*ast.StarExpr); ok {
						if id, ok := se.X.(*ast.Ident); ok {
							switch id.Name {
							case "int64", "uint64", "uint32":
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
			if e.Name == "nil" || e.Name == "err" {
				return "NULL"
			}
			if e.Name == "true" {
				return "TRUE"
			}
			if e.Name == "false" {
				return "FALSE"
			}
			if e.Name == "_" {
				return "''"
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
			if x == "NULL" {
				if op == "=" {
					return y + " IS NULL"
				}
				if op == "!=" {
					return y + " IS NOT NULL"
				}
			}
			return x + " " + op + " " + y
		case *ast.ParenExpr:
			return "(" + extractArg(e.X) + ")"
		case *ast.CallExpr:
			// 1. Go type cast
			castTypes := map[string]bool{
				"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
				"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
				"float32": true, "float64": true, "byte": true, "rune": true, "string": true,
			}
			if funId, ok := e.Fun.(*ast.Ident); ok {
				if castTypes[funId.Name] && len(e.Args) == 1 {
					return extractArg(e.Args[0])
				}
				if funId.Name == "len" && len(e.Args) == 1 {
					return "LENGTH(" + extractArg(e.Args[0]) + ")"
				}
			}

			// 2. fmt.Sprintf formatting
			if sel, ok := e.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sprintf" {
				if len(e.Args) >= 1 {
					if lit, ok := e.Args[0].(*ast.BasicLit); ok {
						format := strings.Trim(lit.Value, "\"")
						var strArgs []string
						for i := 1; i < len(e.Args); i++ {
							strArgs = append(strArgs, extractArg(e.Args[i]))
						}
						return formatToConcat(format, strArgs)
					}
				}
			}

			// 3. gormproc polyfills
			if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
				if pkgId, ok := sel.X.(*ast.Ident); ok && pkgId.Name == "gormproc" {
					if sel.Sel.Name == "JSONExtractFloat64" || sel.Sel.Name == "JSONExtractString" {
						return "(" + extractArg(e.Args[0]) + " ->> " + extractArg(e.Args[1]) + ")"
					}
					if sel.Sel.Name == "JSONLength" {
						return "jsonb_array_length((" + extractArg(e.Args[0]) + ")::jsonb)"
					}
					if sel.Sel.Name == "LastInsertID" {
						return "LASTVAL()"
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
					if sel.Sel.Name == "FromJsonString" {
						return "pokermem.FromJson_string(" + extractArg(e.Args[0]) + ", " + extractArg(e.Args[1]) + ")"
					}
					if sel.Sel.Name == "FromJsonStringOptional" {
						return "pokermem.FromJson_string_optional(" + extractArg(e.Args[0]) + ", " + extractArg(e.Args[1]) + ")"
					}
					if sel.Sel.Name == "FromJsonInt64Optional" {
						return "pokermem.FromJson_int64_optional(" + extractArg(e.Args[0]) + ", " + extractArg(e.Args[1]) + ")"
					}
					if sel.Sel.Name == "FromJsonArray" {
						return "pokermem.FromJson_array(" + extractArg(e.Args[0]) + ", " + extractArg(e.Args[1]) + ")"
					}
					if sel.Sel.Name == "FromJsonInt64" {
						return "pokermem.FromJson_int64(" + extractArg(e.Args[0]) + ", " + extractArg(e.Args[1]) + ")"
					}
				} else if pkgId, ok := sel.X.(*ast.Ident); ok && pkgId.Name != "fmt" {
					var argStrs []string
					for _, arg := range e.Args {
						if id, ok := arg.(*ast.Ident); ok && id.Name == "ctx" {
							continue
						}
						argStrs = append(argStrs, extractArg(arg))
					}
					if pkgId.Name == "s" || pkgId.Name == "b" {
						return fmt.Sprintf("%s(%s)", strings.ToLower(sel.Sel.Name), strings.Join(argStrs, ", "))
					}
					return fmt.Sprintf("%s.%s(%s)", pkgId.Name, sel.Sel.Name, strings.Join(argStrs, ", "))
				}
			}

			if funId, ok := e.Fun.(*ast.Ident); ok && !castTypes[funId.Name] && funId.Name != "len" {
				var argStrs []string
				for _, arg := range e.Args {
					if id, ok := arg.(*ast.Ident); ok && id.Name == "ctx" {
						continue
					}
					argStrs = append(argStrs, extractArg(arg))
				}
				return fmt.Sprintf("%s(%s)", funId.Name, strings.Join(argStrs, ", "))
			}
		}
		return "''"
	}

	declaredVars := make(map[string]bool)
	declareVar := func(name string, typeNode ast.Expr, valExpr ast.Expr) {
		if declaredVars[name] || name == "res" || name == "err" || name == "_" {
			return
		}
		declaredVars[name] = true
		localVarMap[name] = true

		if star, ok := typeNode.(*ast.StarExpr); ok {
			typeNode = star.X
		}

		typ := ""
		if id, ok := typeNode.(*ast.Ident); ok {
			switch id.Name {
			case "int64", "uint64", "uint32", "uint":
				typ = "BIGINT"
			case "int", "int32":
				typ = "INTEGER"
			case "int16", "uint16":
				typ = "SMALLINT"
			case "int8", "uint8", "byte":
				typ = "SMALLINT"
			case "float64", "float32":
				typ = "NUMERIC"
			case "bool":
				typ = "BOOLEAN"
			case "string":
				typ = "TEXT"
			}
		}
		if typ == "" {
			typ = inferTypeFromExpr(valExpr, name, "postgres")
		}

		def := ""
		if valExpr != nil {
			if _, ok := valExpr.(*ast.BasicLit); ok {
				def = " := " + extractArg(valExpr)
			}
		}
		varDecls.WriteString(fmt.Sprintf("\t%s %s%s;\n", "v_"+name, typ, def))
	}

	hasEmittedSelect := false
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
					queryUpper := strings.ToUpper(strings.TrimSpace(query))
					if strings.HasPrefix(queryUpper, "SELECT") {
						hasEmittedSelect = true
					}
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
			} else if assign, ok := n.(*ast.AssignStmt); ok && assign.Tok == token.DEFINE {
				for i, lhs := range assign.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						var rhs ast.Expr
						if i < len(assign.Rhs) {
							rhs = assign.Rhs[i]
						}
						declareVar(id.Name, nil, rhs)
					}
				}
			}
			return true
		})
	}

	var resFields []string
	resFieldSet := make(map[string]bool)
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
			if len(s.Lhs) > 0 {
				if id, ok := s.Lhs[0].(*ast.Ident); ok && id.Name == "_" {
					// Blank identifier (_ = val): ignore in SQL
					return
				}
			}

			var lhs string
			if len(s.Lhs) > 0 {
				if id, ok := s.Lhs[0].(*ast.Ident); ok {
					if id.Name == "res" && len(s.Rhs) > 0 {
						if comp, ok := s.Rhs[0].(*ast.CompositeLit); ok {
							for _, elt := range comp.Elts {
								if kv, ok := elt.(*ast.KeyValueExpr); ok {
									if keyId, ok := kv.Key.(*ast.Ident); ok {
										fieldName := keyId.Name
										if !resFieldSet[fieldName] {
											resFieldSet[fieldName] = true
											resFields = append(resFields, fieldName)
										}
										if !declaredVars[fieldName] {
											declareVar(fieldName, &ast.Ident{Name: "string"}, nil)
										}
										val := extractArg(kv.Value)
										bodyBuf.WriteString(fmt.Sprintf("v_%s := %s;\n", fieldName, val))
									}
								}
							}
							return
						}
					}
					if s.Tok == token.DEFINE {
						localVarMap[id.Name] = true
					}
					lhs = "v_" + id.Name
				} else if sel, ok := s.Lhs[0].(*ast.SelectorExpr); ok {
					fieldName := sel.Sel.Name
					lhs = "v_" + fieldName
					// Track struct result assignments (e.g. res.RoundID = ...)
					if xId, ok := sel.X.(*ast.Ident); ok && xId.Name == "res" {
						if !resFieldSet[fieldName] {
							resFieldSet[fieldName] = true
							resFields = append(resFields, fieldName)
						}
						if !declaredVars[fieldName] {
							var rhs ast.Expr
							if len(s.Rhs) > 0 {
								rhs = s.Rhs[0]
							}
							declareVar(fieldName, nil, rhs)
						}
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
			handledInit := false
			if s.Init != nil {
				if assign, ok := s.Init.(*ast.AssignStmt); ok {
					if len(assign.Rhs) > 0 {
						if emitQuery(assign.Rhs[0]) {
							handledInit = true
						} else if call, ok := assign.Rhs[0].(*ast.CallExpr); ok {
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

												idxFrom := findTopLevelFrom(query)
												if idxFrom != -1 {
													selectPart := query[:idxFrom]
													fromPart := query[idxFrom:]

													var outs []string
													for _, outArg := range outArgs {
														outs = append(outs, extractArg(outArg))
													}

													bodyBuf.WriteString(fmt.Sprintf("%s INTO %s %s;\n", strings.TrimSpace(selectPart), strings.Join(outs, ", "), strings.TrimSpace(fromPart)))
													handledInit = true
												}
											}
										}
								} else {
									args := []string{}
									for _, arg := range call.Args {
										if id, ok := arg.(*ast.Ident); ok && id.Name == "ctx" {
											continue
										}
										args = append(args, extractArg(arg))
									}
									funcName := strings.ToLower(sel.Sel.Name)
									bodyBuf.WriteString(fmt.Sprintf("CALL %s(%s);\n", funcName, strings.Join(args, ", ")))
									handledInit = true
								}
							} else if id, ok := call.Fun.(*ast.Ident); ok {
								args := []string{}
								for _, arg := range call.Args {
									if id, ok := arg.(*ast.Ident); ok && id.Name == "ctx" {
										continue
									}
									args = append(args, extractArg(arg))
								}
								funcName := strings.ToLower(id.Name)
								bodyBuf.WriteString(fmt.Sprintf("CALL %s(%s);\n", funcName, strings.Join(args, ", ")))
								handledInit = true
							}
						}
					}
				}
			}

			// If init handled a query/subprocedure and condition is standard error check, omit empty IF block
			if handledInit && isErrCheck(s.Cond) && isBoilerplateErrorReturn(s.Body) && s.Else == nil {
				return
			}

			cond := cleanErrCondition(s.Cond)
			if id, ok := cond.(*ast.Ident); ok && strings.EqualFold(id.Name, "TRUE") {
				walkStmt(s.Body)
				return
			}

			bodyBuf.WriteString("IF " + extractArg(cond))
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
				if comp, ok := res.(*ast.CompositeLit); ok {
					for _, elt := range comp.Elts {
						if kv, ok := elt.(*ast.KeyValueExpr); ok {
							if keyId, ok := kv.Key.(*ast.Ident); ok {
								fieldName := keyId.Name
								if !resFieldSet[fieldName] {
									resFieldSet[fieldName] = true
									resFields = append(resFields, fieldName)
								}
								if !declaredVars[fieldName] {
									declareVar(fieldName, nil, kv.Value)
								}
								val := extractArg(kv.Value)
								bodyBuf.WriteString(fmt.Sprintf("v_%s := %s;\n", fieldName, val))
							}
						}
					}
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
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
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

									idxFrom := findTopLevelFrom(query)
									if idxFrom != -1 {
										selectPart := query[:idxFrom]
										fromPart := query[idxFrom:]

										var outs []string
										for _, outArg := range outArgs {
											outs = append(outs, extractArg(outArg))
										}

										bodyBuf.WriteString(fmt.Sprintf("%s INTO %s %s;\n", strings.TrimSpace(selectPart), strings.Join(outs, ", "), strings.TrimSpace(fromPart)))
									}
								}
							}
						}
					} else {
						args := []string{}
						for _, arg := range call.Args {
							if id, ok := arg.(*ast.Ident); ok && id.Name == "ctx" {
								continue
							}
							args = append(args, extractArg(arg))
						}
						funcName := strings.ToLower(sel.Sel.Name)
						bodyBuf.WriteString(fmt.Sprintf("CALL %s(%s);\n", funcName, strings.Join(args, ", ")))
					}
				} else if id, ok := call.Fun.(*ast.Ident); ok {
					args := []string{}
					for _, arg := range call.Args {
						if id, ok := arg.(*ast.Ident); ok && id.Name == "ctx" {
							continue
						}
						args = append(args, extractArg(arg))
					}
					funcName := strings.ToLower(id.Name)
					bodyBuf.WriteString(fmt.Sprintf("CALL %s(%s);\n", funcName, strings.Join(args, ", ")))
				}
			}
		}
	}

	// Walk top-level statements, and if a statement is or contains a Transaction call, walk inside it
	if proc.Func.Body != nil {
		for _, stmt := range proc.Func.Body.List {
			handledTx := false
			ast.Inspect(stmt, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Transaction" {
						if len(call.Args) > 0 {
							if lit, ok := call.Args[0].(*ast.FuncLit); ok {
								handledTx = true
								walkStmt(lit.Body)
							}
						}
					}
				}
				return true
			})
			if !handledTx {
				walkStmt(stmt)
			}
		}
	}

	// If struct fields were assigned (e.g. res.RoundID = ...) and no terminating SELECT query was emitted,
	// emit the result projection SELECT statement so GORM Raw().Scan() receives the row.
	if len(resFields) > 0 && !hasEmittedSelect {
		var selectCols []string
		for _, field := range resFields {
			selectCols = append(selectCols, fmt.Sprintf("v_%s AS %s", field, field))
		}
		bodyBuf.WriteString(fmt.Sprintf("SELECT %s;\n", strings.Join(selectCols, ", ")))
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
