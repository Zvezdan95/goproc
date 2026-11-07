package transpiler

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
	"strings"

	"github.com/user/gormproc/pkg/parser"
)

type loopContext struct {
	label   string
	postSQL string
}

type returnColSpec struct {
	name    string
	sqlType string
}

func hasLoopBranchStmt(block *ast.BlockStmt) bool {
	if block == nil {
		return false
	}
	found := false
	var walk func(n ast.Node, inSwitch bool)
	walk = func(n ast.Node, inSwitch bool) {
		if found || n == nil {
			return
		}
		switch st := n.(type) {
		case *ast.ForStmt, *ast.FuncLit:
			// Nested loops handle their own unlabeled continue/break
			return
		case *ast.SwitchStmt:
			for _, sub := range st.Body.List {
				walk(sub, true)
			}
			return
		case *ast.BranchStmt:
			if st.Tok == token.CONTINUE {
				found = true
				return
			}
			if st.Tok == token.BREAK && (!inSwitch || st.Label != nil) {
				found = true
				return
			}
		}
		ast.Inspect(n, func(child ast.Node) bool {
			if child == n || child == nil || found {
				return !found
			}
			switch child.(type) {
			case *ast.ForStmt, *ast.FuncLit:
				return false
			case *ast.SwitchStmt:
				walk(child, true)
				return false
			case *ast.BranchStmt:
				walk(child, inSwitch)
				return false
			}
			return true
		})
	}
	walk(block, false)
	return found
}

func resolveSQLParamType(fieldType ast.Expr, dialect string) string {
	isPg := dialect == "postgres"
	defaultFloat := "DOUBLE"
	defaultInt := "INT"
	defaultStr := "TEXT"
	if isPg {
		defaultFloat = "NUMERIC"
		defaultInt = "INTEGER"
	}

	if star, ok := fieldType.(*ast.StarExpr); ok {
		fieldType = star.X
	}

	if sel, ok := fieldType.(*ast.SelectorExpr); ok {
		switch sel.Sel.Name {
		case "JSON":
			if isPg {
				return "TEXT"
			}
			return "JSON"
		case "Decimal":
			if isPg {
				return "NUMERIC"
			}
			return "DECIMAL(65,18)"
		}
	}

	if id, ok := fieldType.(*ast.Ident); ok {
		switch id.Name {
		case "int64", "uint64", "uint32", "uint":
			return "BIGINT"
		case "int", "int32":
			return defaultInt
		case "int16", "uint16":
			return "SMALLINT"
		case "int8", "uint8", "byte":
			if isPg {
				return "SMALLINT"
			}
			return "TINYINT"
		case "float64", "float32":
			return defaultFloat
		case "bool":
			return "BOOLEAN"
		case "string":
			return defaultStr
		}
	}

	return defaultFloat
}

func resolveSQLVarType(typeNode ast.Expr, valExpr ast.Expr, varName string, dialect string) string {
	isPg := dialect == "postgres"
	if star, ok := typeNode.(*ast.StarExpr); ok {
		typeNode = star.X
	}

	if sel, ok := typeNode.(*ast.SelectorExpr); ok {
		switch sel.Sel.Name {
		case "JSON":
			if isPg {
				return "TEXT"
			}
			return "JSON"
		case "Decimal":
			if isPg {
				return "NUMERIC"
			}
			return "DECIMAL(65,18)"
		}
	}

	if id, ok := typeNode.(*ast.Ident); ok {
		switch id.Name {
		case "int64", "uint64", "uint32", "uint":
			return "BIGINT"
		case "int", "int32":
			if isPg {
				return "INTEGER"
			}
			return "INT"
		case "int16", "uint16":
			return "SMALLINT"
		case "int8", "uint8", "byte":
			if isPg {
				return "SMALLINT"
			}
			return "TINYINT"
		case "float64", "float32":
			if isPg {
				return "NUMERIC"
			}
			return "DOUBLE"
		case "bool":
			return "BOOLEAN"
		case "string":
			return "TEXT"
		}
	}

	return inferTypeFromExpr(valExpr, varName, dialect)
}

func goTypeToPostgresSQL(t types.Type) string {
	if t == nil {
		return "TEXT"
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if basic, ok := t.Underlying().(*types.Basic); ok {
		switch basic.Kind() {
		case types.Bool:
			return "BOOLEAN"
		case types.Int, types.Int32:
			return "INTEGER"
		case types.Int8, types.Int16, types.Uint8, types.Uint16:
			return "SMALLINT"
		case types.Int64, types.Uint, types.Uint32, types.Uint64:
			return "BIGINT"
		case types.Float32, types.Float64:
			return "NUMERIC"
		case types.String:
			return "TEXT"
		}
	}
	return "TEXT"
}

func extractPostgresReturnColumns(proc *parser.Procedure, resFields []string, resVarTypes map[string]string) []returnColSpec {
	// 1. If we have full go/types signature on proc, extract exact struct columns or scalar return
	if proc.Type != nil && proc.Type.Results() != nil {
		res := proc.Type.Results()
		for i := 0; i < res.Len(); i++ {
			rt := res.At(i).Type()
			if rt.String() == "error" {
				continue
			}
			unwrapped := rt
			if ptr, ok := unwrapped.(*types.Pointer); ok {
				unwrapped = ptr.Elem()
			}
			if st, ok := unwrapped.Underlying().(*types.Struct); ok {
				var cols []returnColSpec
				for fIdx := 0; fIdx < st.NumFields(); fIdx++ {
					f := st.Field(fIdx)
					colName := f.Name()
					tag := reflect.StructTag(st.Tag(fIdx)).Get("gorm")
					for _, part := range strings.Split(tag, ";") {
						if strings.HasPrefix(part, "column:") {
							colName = strings.TrimPrefix(part, "column:")
						}
					}
					// If resFields used field names (e.g. RoundID), use matching order if resFields populated
					cols = append(cols, returnColSpec{
						name:    colName,
						sqlType: goTypeToPostgresSQL(f.Type()),
					})
				}
				if len(resFields) > 0 && len(resFields) == len(cols) {
					// Match exact projected alias names from SELECT v_X AS X
					var aligned []returnColSpec
					fieldTypeByName := make(map[string]string)
					for fIdx := 0; fIdx < st.NumFields(); fIdx++ {
						f := st.Field(fIdx)
						fieldTypeByName[f.Name()] = goTypeToPostgresSQL(f.Type())
					}
					for _, rf := range resFields {
						stType := fieldTypeByName[rf]
						if stType == "" {
							stType = "TEXT"
						}
						aligned = append(aligned, returnColSpec{name: rf, sqlType: stType})
					}
					return aligned
				}
				return cols
			}
			if _, ok := unwrapped.Underlying().(*types.Basic); ok {
				return []returnColSpec{{
					name:    "result",
					sqlType: goTypeToPostgresSQL(unwrapped),
				}}
			}
		}
	}

	// 2. Fallback for raw AST unit tests without go/types
	if len(resFields) > 0 {
		var cols []returnColSpec
		for _, f := range resFields {
			t := resVarTypes[f]
			if t == "" {
				t = "TEXT"
			}
			cols = append(cols, returnColSpec{name: f, sqlType: t})
		}
		return cols
	}

	return nil
}

func resolveSubProcCallName(proc *parser.Procedure, call *ast.CallExpr, defaultName string) string {
	if ext := proc.ResolveCallName(call); ext != "" {
		return ext
	}
	return strings.ToLower(defaultName)
}

func isIdentByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// normalizeQueryForPostgres translates common MySQL SQL dialect functions (IFNULL, IF(c,a,b))
// inside raw SQL strings into standard PostgreSQL equivalents (COALESCE, CASE WHEN ... END).
func normalizeQueryForPostgres(query string) string {
	out := replaceFuncNameOutsideQuotes(query, "IFNULL", "COALESCE")
	out = rewriteInlineIfToCase(out)
	return out
}

func replaceFuncNameOutsideQuotes(s, oldFn, newFn string) string {
	var b strings.Builder
	inQuote := false
	var qChar byte
	upper := strings.ToUpper(s)
	n := len(s)
	fnLen := len(oldFn)

	for i := 0; i < n; {
		c := s[i]
		if inQuote {
			b.WriteByte(c)
			if c == qChar {
				inQuote = false
			}
			i++
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			inQuote = true
			qChar = c
			b.WriteByte(c)
			i++
			continue
		}
		if i+fnLen < n && upper[i:i+fnLen] == oldFn && (i == 0 || !isIdentByte(s[i-1])) {
			j := i + fnLen
			for j < n && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			if j < n && s[j] == '(' {
				b.WriteString(newFn)
				i += fnLen
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func rewriteInlineIfToCase(s string) string {
	var b strings.Builder
	inQuote := false
	var qChar byte
	upper := strings.ToUpper(s)
	n := len(s)

	for i := 0; i < n; {
		c := s[i]
		if inQuote {
			b.WriteByte(c)
			if c == qChar {
				inQuote = false
			}
			i++
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			inQuote = true
			qChar = c
			b.WriteByte(c)
			i++
			continue
		}
		if i+3 < n && upper[i:i+2] == "IF" && (i == 0 || !isIdentByte(s[i-1])) && !isIdentByte(s[i+2]) {
			j := i + 2
			for j < n && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			if j < n && s[j] == '(' {
				endParen, args, ok := splitCallArgs(s, j)
				if ok && len(args) == 3 {
					arg0 := rewriteInlineIfToCase(strings.TrimSpace(args[0]))
					arg1 := rewriteInlineIfToCase(strings.TrimSpace(args[1]))
					arg2 := rewriteInlineIfToCase(strings.TrimSpace(args[2]))
					b.WriteString(fmt.Sprintf("(CASE WHEN %s THEN %s ELSE %s END)", arg0, arg1, arg2))
					i = endParen + 1
					continue
				}
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func splitCallArgs(s string, openParenIdx int) (int, []string, bool) {
	depth := 0
	inQuote := false
	var qChar byte
	start := openParenIdx + 1
	var args []string

	for i := openParenIdx; i < len(s); i++ {
		c := s[i]
		if inQuote {
			if c == qChar {
				inQuote = false
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			inQuote = true
			qChar = c
			continue
		}
		if c == '(' {
			depth++
			continue
		}
		if c == ')' {
			depth--
			if depth == 0 {
				args = append(args, s[start:i])
				return i, args, true
			}
			continue
		}
		if c == ',' && depth == 1 {
			args = append(args, s[start:i])
			start = i + 1
		}
	}
	return -1, nil, false
}

func findTopLevelFrom(query string) int {
	depth := 0
	inQuote := false
	var quoteChar byte
	upper := strings.ToUpper(query)
	for i := 0; i < len(query); i++ {
		c := query[i]
		if inQuote {
			if c == quoteChar {
				inQuote = false
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			inQuote = true
			quoteChar = c
			continue
		}
		if c == '(' {
			depth++
			continue
		}
		if c == ')' {
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 && i+6 <= len(upper) {
			if upper[i:i+6] == " FROM " {
				return i
			}
		}
	}
	return -1
}

func formatToConcat(format string, args []string) string {
	var parts []string
	argIdx := 0
	last := 0
	i := 0
	for i < len(format) {
		if format[i] == '%' && i+1 < len(format) {
			spec := format[i+1]
			if spec == 'd' || spec == 's' || spec == 'v' || spec == 'f' {
				if i > last {
					parts = append(parts, "'"+format[last:i]+"'")
				}
				if argIdx < len(args) {
					parts = append(parts, args[argIdx])
					argIdx++
				}
				i += 2
				last = i
				continue
			}
		}
		i++
	}
	if last < len(format) {
		parts = append(parts, "'"+format[last:]+"'")
	}
	if len(parts) == 0 {
		return "''"
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "CONCAT(" + strings.Join(parts, ", ") + ")"
}

func isErrCheck(expr ast.Expr) bool {
	if bin, ok := expr.(*ast.BinaryExpr); ok {
		if bin.Op == token.NEQ {
			if id, ok := bin.X.(*ast.Ident); ok && id.Name == "err" {
				return true
			}
			if id, ok := bin.Y.(*ast.Ident); ok && id.Name == "err" {
				return true
			}
		}
	}
	return false
}

func isErrNilCheck(expr ast.Expr) bool {
	if bin, ok := expr.(*ast.BinaryExpr); ok && bin.Op == token.EQL {
		if id, ok := bin.X.(*ast.Ident); ok && id.Name == "err" {
			if idY, ok := bin.Y.(*ast.Ident); ok && idY.Name == "nil" {
				return true
			}
		}
		if id, ok := bin.Y.(*ast.Ident); ok && id.Name == "err" {
			if idX, ok := bin.X.(*ast.Ident); ok && idX.Name == "nil" {
				return true
			}
		}
	}
	return false
}

func cleanErrCondition(expr ast.Expr) ast.Expr {
	if expr == nil {
		return nil
	}
	if isErrNilCheck(expr) {
		return &ast.Ident{Name: "TRUE"}
	}
	if bin, ok := expr.(*ast.BinaryExpr); ok {
		if bin.Op == token.LAND {
			leftIsErr := isErrNilCheck(bin.X)
			rightIsErr := isErrNilCheck(bin.Y)
			if leftIsErr && rightIsErr {
				return &ast.Ident{Name: "TRUE"}
			}
			if leftIsErr {
				return cleanErrCondition(bin.Y)
			}
			if rightIsErr {
				return cleanErrCondition(bin.X)
			}
			return &ast.BinaryExpr{
				X:  cleanErrCondition(bin.X),
				Op: bin.Op,
				Y:  cleanErrCondition(bin.Y),
			}
		}
	}
	return expr
}

func isBoilerplateErrorReturn(stmt ast.Stmt) bool {
	if stmt == nil {
		return true
	}
	if ret, ok := stmt.(*ast.ReturnStmt); ok {
		if len(ret.Results) == 1 {
			if id, ok := ret.Results[0].(*ast.Ident); ok && id.Name == "err" {
				return true
			}
		} else if len(ret.Results) == 2 {
			if id, ok := ret.Results[1].(*ast.Ident); ok && id.Name == "err" {
				return true
			}
		}
	}
	if block, ok := stmt.(*ast.BlockStmt); ok {
		if len(block.List) == 1 {
			return isBoilerplateErrorReturn(block.List[0])
		}
	}
	return false
}

func inferTypeFromExpr(expr ast.Expr, varName string, dialect string) string {
	intType := "BIGINT"
	floatType := "DOUBLE"
	strType := "VARCHAR(255)"
	if dialect == "postgres" {
		floatType = "NUMERIC"
		strType = "VARCHAR"
	}

	if expr != nil {
		switch e := expr.(type) {
		case *ast.BasicLit:
			if e.Kind == token.INT {
				if strings.Contains(e.Value, ".") {
					return floatType
				}
				return intType
			}
			if e.Kind == token.FLOAT {
				return floatType
			}
			if e.Kind == token.STRING {
				return strType
			}
		case *ast.Ident:
			if e.Name == "true" || e.Name == "false" {
				return "BOOLEAN"
			}
		case *ast.UnaryExpr:
			if e.Op == token.NOT {
				return "BOOLEAN"
			}
			if e.Op == token.AND || e.Op == token.SUB {
				return inferTypeFromExpr(e.X, varName, dialect)
			}
		case *ast.CallExpr:
			if id, ok := e.Fun.(*ast.Ident); ok {
				switch id.Name {
				case "int64", "uint64", "uint32", "uint", "int", "int32", "int16", "uint16", "len":
					return intType
				case "float64", "float32":
					return floatType
				case "string":
					return strType
				case "bool":
					return "BOOLEAN"
				}
			} else if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "FromJsonInt64", "JSONLength", "LastInsertID":
					return intType
				case "FromJsonString", "FromJsonStringOptional", "JSONExtractString", "FromJsonArray", "Now", "GlobConvAccountType2Currency", "JSONArrayAppend", "JSONMergePreserve":
					return strType
				case "JSONExtractFloat64", "CasinogameCalcJackpotContribution", "CasinogameFeeCurrent":
					return floatType
				case "GlobHasBits", "JSONOverlaps":
					return "BOOLEAN"
				}
			}
		}
	}

	lower := strings.ToLower(varName)
	if strings.Contains(lower, "id") || strings.Contains(lower, "count") || strings.Contains(lower, "num") || strings.Contains(lower, "status") || strings.Contains(lower, "index") {
		return intType
	}
	if strings.Contains(lower, "amount") || strings.Contains(lower, "balance") || strings.Contains(lower, "price") || strings.Contains(lower, "fee") || strings.Contains(lower, "cont") {
		return floatType
	}
	if strings.Contains(lower, "flag") || strings.Contains(lower, "is") || strings.Contains(lower, "has") || strings.Contains(lower, "trigger") {
		return "BOOLEAN"
	}

	return strType
}
