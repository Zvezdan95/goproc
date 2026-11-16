package transpiler

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
	"strings"

	"github.com/user/gormproc/pkg/parser"
)

func ToPostgres(proc *parser.Procedure, hash string) (string, error) {
	fn, err := parser.ResolveAndInline(proc)
	if err != nil {
		return "", err
	}
	if fn == nil {
		fn = proc.Func
	}

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
	if fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			for _, name := range field.Names {
				if name.Name != "ctx" {
					sqlType := resolveSQLParamType(field.Type, "postgres")
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
						return "pokermem.CasinogameFeeCurrent(" + extractArg(e.Args[0]) + ")"
					}
					if sel.Sel.Name == "CasinogameCalcJackpotContribution" {
						return "casinodb.Casinogame_calcJackpotContribution(" + extractArg(e.Args[0]) + ", " + extractArg(e.Args[1]) + ")"
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
					if sel.Sel.Name == "Now" {
						return "CURRENT_TIMESTAMP::TEXT"
					}
					if sel.Sel.Name == "JSONArrayAppend" {
						return "((" + extractArg(e.Args[0]) + ")::jsonb || to_jsonb(" + extractArg(e.Args[2]) + "))::text"
					}
					if sel.Sel.Name == "JSONMergePreserve" {
						return "((" + extractArg(e.Args[0]) + ")::jsonb || (" + extractArg(e.Args[1]) + ")::jsonb)::text"
					}
				} else if ext := proc.ResolveCallName(e); ext != "" {
					var argStrs []string
					for _, arg := range e.Args {
						if id, ok := arg.(*ast.Ident); ok && id.Name == "ctx" {
							continue
						}
						argStrs = append(argStrs, extractArg(arg))
					}
					return fmt.Sprintf("%s(%s)", ext, strings.Join(argStrs, ", "))
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
				if ext := proc.ResolveCallName(e); ext != "" {
					return fmt.Sprintf("%s(%s)", ext, strings.Join(argStrs, ", "))
				}
				return fmt.Sprintf("%s(%s)", funId.Name, strings.Join(argStrs, ", "))
			}
		}
		return "''"
	}

	varComments := make(map[string]string)
	declaredVars := make(map[string]bool)
	resVarTypes := make(map[string]string)
	declareVar := func(name string, typeNode ast.Expr, valExpr ast.Expr) {
		if declaredVars[name] || name == "res" || name == "err" || name == "_" {
			return
		}
		declaredVars[name] = true
		localVarMap[name] = true

		typ := resolveSQLVarType(typeNode, valExpr, name, "postgres")
		resVarTypes[name] = typ

		_, isPtr := typeNode.(*ast.StarExpr)
		isAddr := false
		if u, ok := valExpr.(*ast.UnaryExpr); ok && u.Op == token.AND {
			isAddr = true
		}

		def := ""
		if !isPtr && !isAddr {
			if valExpr != nil {
				if _, ok := valExpr.(*ast.BasicLit); ok {
					def = " := " + extractArg(valExpr)
				} else if id, ok := valExpr.(*ast.Ident); ok && (id.Name == "true" || id.Name == "false") {
					def = " := " + strings.ToUpper(id.Name)
				}
			}
			if def == "" {
				if zeroVal := defaultZeroValueForSQLType(typ); zeroVal != "" {
					def = " := " + zeroVal
				}
			}
		}
		varDecls.WriteString(fmt.Sprintf("\t%s %s%s;%s\n", "v_"+name, typ, def, varComments[name]))
	}

	var resFields []string
	resFieldSet := make(map[string]bool)
	hasResFieldAssignments := false
	if fn.Body != nil {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if hasResFieldAssignments {
				return false
			}
			switch node := n.(type) {
			case *ast.AssignStmt:
				for _, l := range node.Lhs {
					if sel, ok := l.(*ast.SelectorExpr); ok {
						if xId, ok := sel.X.(*ast.Ident); ok && xId.Name == "res" {
							hasResFieldAssignments = true
							return false
						}
					} else if id, ok := l.(*ast.Ident); ok && id.Name == "res" && len(node.Rhs) > 0 {
						if _, ok := node.Rhs[0].(*ast.CompositeLit); ok {
							hasResFieldAssignments = true
							return false
						}
					}
				}
			case *ast.ReturnStmt:
				for _, r := range node.Results {
					if _, ok := r.(*ast.CompositeLit); ok {
						hasResFieldAssignments = true
						return false
					}
				}
			}
			return true
		})
	}
	hasEmittedSelect := false
	emittedResSelect := false
	emitResFieldsSelect := func() {
		if len(resFields) == 0 || emittedResSelect {
			return
		}
		var selectCols []string
		for _, field := range resFields {
			selectCols = append(selectCols, fmt.Sprintf("v_%s AS %s", field, field))
		}
		bodyBuf.WriteString(fmt.Sprintf("RETURN QUERY SELECT %s;\n", strings.Join(selectCols, ", ")))
		emittedResSelect = true
		hasEmittedSelect = true
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
					rawQuery := strings.Trim(lit.Value, "`\"")
					isMultiline := strings.Contains(rawQuery, "\n")
					query := normalizeQueryForPostgres(rawQuery)
					if isMultiline {
						query = dedentSQL(query)
					}
					for i := 1; i < len(call.Args); i++ {
						query = strings.Replace(query, "?", extractArg(call.Args[i]), 1)
					}
					trimmedQuery := strings.TrimSpace(query)
					queryUpper := strings.ToUpper(trimmedQuery)
					if strings.HasPrefix(queryUpper, "SELECT") {
						if len(resFields) > 0 || hasResFieldAssignments {
							if len(resFields) > 0 && !emittedResSelect {
								emitResFieldsSelect()
							}
							rest := strings.TrimSpace(trimmedQuery[len("SELECT"):])
							bodyBuf.WriteString("PERFORM " + rest + ";\n")
						} else {
							bodyBuf.WriteString("RETURN QUERY " + trimmedQuery + ";\n")
							hasEmittedSelect = true
						}
					} else {
						bodyBuf.WriteString(query + ";\n")
					}
					return true
				}
			}
		}
		return false
	}

	// Emit procedure doc comments (excluding //gorm:... directives) at the top of BEGIN
	if proc.Func != nil && proc.Func.Doc != nil {
		var docLines []string
		for _, c := range proc.Func.Doc.List {
			docLines = append(docLines, c.Text)
		}
		if sqlComments := formatSQLCommentLines(strings.Join(docLines, "\n")); sqlComments != "" {
			bodyBuf.WriteString(sqlComments)
		}
	}

	// Pre-collect variable declarations and their comments from function body
	if fn.Body != nil {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if gen, ok := n.(*ast.GenDecl); ok && gen.Tok == token.VAR {
				for _, spec := range gen.Specs {
					if vSpec, ok := spec.(*ast.ValueSpec); ok {
						cg := vSpec.Comment
						if cg == nil {
							cg = vSpec.Doc
						}
						if cg == nil && len(gen.Specs) == 1 {
							cg = gen.Doc
						}
						commentStr := formatInlineVarComment(cg)
						var val ast.Expr
						if len(vSpec.Values) > 0 {
							val = vSpec.Values[0]
						}
						for _, name := range vSpec.Names {
							if commentStr != "" && varComments[name.Name] == "" {
								varComments[name.Name] = commentStr
							}
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

	var loopStack []loopContext
	loopSeq := 0
	inSwitchDepth := 0
	scalarRetExpr := ""

	var walkStmt func(stmt ast.Stmt)
	walkStmt = func(stmt ast.Stmt) {
		switch s := stmt.(type) {
		case *ast.BlockStmt:
			for _, bs := range s.List {
				walkStmt(bs)
			}
		case *ast.IncDecStmt:
			op := "+"
			if s.Tok == token.DEC {
				op = "-"
			}
			target := extractArg(s.X)
			bodyBuf.WriteString(fmt.Sprintf("%s := %s %s 1;\n", target, target, op))
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
							} else if sel.Sel.Name == "FromJsonInt64Optional" {
								arg1 := extractArg(call.Args[0])
								arg2 := extractArg(call.Args[1])
								bodyBuf.WriteString(fmt.Sprintf("%s := pokermem.FromJson_int64_optional(%s, %s);\n", lhs, arg1, arg2))
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

				// Normal or compound assignment
				val := extractArg(s.Rhs[0])
				switch s.Tok {
				case token.ADD_ASSIGN:
					bodyBuf.WriteString(fmt.Sprintf("%s := %s + (%s);\n", lhs, lhs, val))
				case token.SUB_ASSIGN:
					bodyBuf.WriteString(fmt.Sprintf("%s := %s - (%s);\n", lhs, lhs, val))
				case token.MUL_ASSIGN:
					bodyBuf.WriteString(fmt.Sprintf("%s := %s * (%s);\n", lhs, lhs, val))
				case token.QUO_ASSIGN:
					bodyBuf.WriteString(fmt.Sprintf("%s := %s / (%s);\n", lhs, lhs, val))
				default:
					bodyBuf.WriteString(fmt.Sprintf("%s := %s;\n", lhs, val))
				}
			}
		case *ast.ForStmt:
			if initAssign, ok := s.Init.(*ast.AssignStmt); ok {
				if id, ok := initAssign.Lhs[0].(*ast.Ident); ok {
					loopVar := "v_" + id.Name
					val := extractArg(initAssign.Rhs[0])
					bodyBuf.WriteString(fmt.Sprintf("%s := %s;\n", loopVar, val))
				}
			}

			postSQL := ""
			if postInc, ok := s.Post.(*ast.IncDecStmt); ok {
				if id, ok := postInc.X.(*ast.Ident); ok {
					if op := postInc.Tok.String()[:1]; op == "+" || op == "-" {
						postSQL = fmt.Sprintf("v_%s := v_%s %s 1;\n", id.Name, id.Name, op)
					}
				}
			} else if postAssign, ok := s.Post.(*ast.AssignStmt); ok && len(postAssign.Lhs) > 0 && len(postAssign.Rhs) > 0 {
				target := extractArg(postAssign.Lhs[0])
				val := extractArg(postAssign.Rhs[0])
				switch postAssign.Tok {
				case token.ADD_ASSIGN:
					postSQL = fmt.Sprintf("%s := %s + (%s);\n", target, target, val)
				case token.SUB_ASSIGN:
					postSQL = fmt.Sprintf("%s := %s - (%s);\n", target, target, val)
				default:
					postSQL = fmt.Sprintf("%s := %s;\n", target, val)
				}
			}

			needsLabel := hasLoopBranchStmt(s.Body)
			if needsLabel {
				loopSeq++
				label := fmt.Sprintf("loop_%d", loopSeq)
				loopStack = append(loopStack, loopContext{label: label, postSQL: postSQL})
				bodyBuf.WriteString(fmt.Sprintf("<<%s>>\nWHILE %s LOOP\n", label, extractArg(s.Cond)))
				walkStmt(s.Body)
				loopStack = loopStack[:len(loopStack)-1]
				bodyBuf.WriteString(postSQL)
				bodyBuf.WriteString(fmt.Sprintf("END LOOP %s;\n", label))
			} else {
				bodyBuf.WriteString("WHILE " + extractArg(s.Cond) + " LOOP\n")
				walkStmt(s.Body)
				bodyBuf.WriteString(postSQL)
				bodyBuf.WriteString("END LOOP;\n")
			}
		case *ast.BranchStmt:
			if s.Tok == token.CONTINUE && len(loopStack) > 0 {
				top := loopStack[len(loopStack)-1]
				if top.postSQL != "" {
					bodyBuf.WriteString(top.postSQL)
				}
				bodyBuf.WriteString(fmt.Sprintf("CONTINUE %s;\n", top.label))
			} else if s.Tok == token.BREAK && (inSwitchDepth == 0 || s.Label != nil) && len(loopStack) > 0 {
				top := loopStack[len(loopStack)-1]
				bodyBuf.WriteString(fmt.Sprintf("EXIT %s;\n", top.label))
			}
		case *ast.SwitchStmt:
			inSwitchDepth++
			tag := extractArg(s.Tag)
			bodyBuf.WriteString(fmt.Sprintf("CASE %s\n", tag))
			walkStmt(s.Body)
			bodyBuf.WriteString("END CASE;\n")
			inSwitchDepth--
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
									if emitPostgresSelectInto(&bodyBuf, call, extractArg) {
										handledInit = true
									}
								} else {
									args := []string{}
									for _, arg := range call.Args {
										if id, ok := arg.(*ast.Ident); ok && id.Name == "ctx" {
											continue
										}
										args = append(args, extractArg(arg))
									}
									subName := resolveSubProcCallName(proc, call, sel.Sel.Name)
									bodyBuf.WriteString(formatCallStmt(subName, args))
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
								subName := resolveSubProcCallName(proc, call, id.Name)
								bodyBuf.WriteString(formatCallStmt(subName, args))
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
			for idx, res := range s.Results {
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
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						if pkgId, ok := sel.X.(*ast.Ident); ok && pkgId.Name == "gormproc" && sel.Sel.Name == "SignalError" && len(call.Args) >= 2 {
							if lit, ok := call.Args[1].(*ast.BasicLit); ok {
								msg := strings.Trim(lit.Value, "\"'`")
								bodyBuf.WriteString(fmt.Sprintf("RAISE EXCEPTION '%s' USING ERRCODE = '45000';\n", msg))
							} else {
								bodyBuf.WriteString(fmt.Sprintf("RAISE EXCEPTION '%%', %s USING ERRCODE = '45000';\n", extractArg(call.Args[1])))
							}
							continue
						}
						if sel.Sel.Name == "New" || sel.Sel.Name == "Errorf" {
							if len(call.Args) > 0 {
								if lit, ok := call.Args[0].(*ast.BasicLit); ok {
									msg := strings.Trim(lit.Value, "\"'`")
									bodyBuf.WriteString(fmt.Sprintf("RAISE EXCEPTION '%s';\n", msg))
								}
							}
							continue
						}
					}
				}
				if idx == 0 && analysis != nil && analysis.ReturnsResult {
					if id, ok := res.(*ast.Ident); !ok || (id.Name != "res" && id.Name != "nil" && id.Name != "err") {
						scalarRetExpr = extractArg(res)
					}
				}
			}
		case *ast.ExprStmt:
			if emitQuery(s.X) {
				return
			}
			if call, ok := s.X.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "__gormproc_comment__" {
					if len(call.Args) == 1 {
						if lit, ok := call.Args[0].(*ast.BasicLit); ok {
							if text, err := strconv.Unquote(lit.Value); err == nil {
								bodyBuf.WriteString(formatSQLCommentLines(text))
							}
						}
					}
					return
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if pkgId, ok := sel.X.(*ast.Ident); ok && pkgId.Name == "gormproc" {
						if sel.Sel.Name == "SelectInto" {
							emitPostgresSelectInto(&bodyBuf, call, extractArg)
						}
					} else {
						args := []string{}
						for _, arg := range call.Args {
							if id, ok := arg.(*ast.Ident); ok && id.Name == "ctx" {
								continue
							}
							args = append(args, extractArg(arg))
						}
						subName := resolveSubProcCallName(proc, call, sel.Sel.Name)
						bodyBuf.WriteString(formatCallStmt(subName, args))
					}
				} else if id, ok := call.Fun.(*ast.Ident); ok {
					args := []string{}
					for _, arg := range call.Args {
						if id, ok := arg.(*ast.Ident); ok && id.Name == "ctx" {
							continue
						}
						args = append(args, extractArg(arg))
					}
					subName := resolveSubProcCallName(proc, call, id.Name)
					bodyBuf.WriteString(formatCallStmt(subName, args))
				}
			}
		}
	}

	// Walk top-level statements, and if a statement is or contains a Transaction call, walk inside it
	if fn.Body != nil {
		for _, stmt := range fn.Body.List {
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

	// If struct fields were assigned (e.g. res.RoundID = ...) and the struct result RETURN QUERY SELECT hasn't been emitted yet,
	// emit the result projection RETURN QUERY SELECT statement so PostgreSQL returns the row cleanly.
	if len(resFields) > 0 && !emittedResSelect {
		emitResFieldsSelect()
	} else if analysis != nil && analysis.ReturnsResult && !hasEmittedSelect && scalarRetExpr != "" && scalarRetExpr != "NULL" && scalarRetExpr != "''" {
		bodyBuf.WriteString(fmt.Sprintf("RETURN QUERY SELECT %s AS result;\n", scalarRetExpr))
		hasEmittedSelect = true
	}

	var finalBuf bytes.Buffer
	retCols := extractPostgresReturnColumns(proc, resFields, resVarTypes)
	if (analysis != nil && analysis.ReturnsResult) && (len(retCols) > 0 || hasEmittedSelect) {
		if len(retCols) > 0 {
			var colSpecs []string
			for _, rc := range retCols {
				colSpecs = append(colSpecs, fmt.Sprintf("%s %s", rc.name, rc.sqlType))
			}
			finalBuf.WriteString(fmt.Sprintf("CREATE OR REPLACE FUNCTION\n%s(%s)\nRETURNS TABLE(%s)\nLANGUAGE plpgsql AS $$\n#variable_conflict use_column\n", funcName, strings.Join(params, ", "), strings.Join(colSpecs, ", ")))
		} else {
			finalBuf.WriteString(fmt.Sprintf("CREATE OR REPLACE FUNCTION\n%s(%s)\nRETURNS SETOF RECORD\nLANGUAGE plpgsql AS $$\n#variable_conflict use_column\n", funcName, strings.Join(params, ", ")))
		}
	} else {
		finalBuf.WriteString(fmt.Sprintf("CREATE OR REPLACE PROCEDURE\n%s(%s)\nLANGUAGE plpgsql AS $$\n#variable_conflict use_variable\n", funcName, strings.Join(params, ", ")))
	}
	if varDecls.Len() > 0 {
		finalBuf.WriteString("DECLARE\n")
		finalBuf.WriteString(varDecls.String())
	}
	finalBuf.WriteString("BEGIN\n")
	finalBuf.WriteString(bodyBuf.String())
	finalBuf.WriteString("END;\n$$;")

	return formatSQL(finalBuf.String()), nil
}
