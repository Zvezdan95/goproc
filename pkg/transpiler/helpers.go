package transpiler

import (
	"bytes"
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

// normalizeQueryForPostgres translates common MySQL SQL dialect functions (IFNULL, IF(c,a,b),
// JSON_ARRAYAGG, JSON_EXTRACT, JSON_LENGTH, CAST(... AS UNSIGNED)) and strips MySQL index hints
// inside raw SQL strings into standard PostgreSQL equivalents.
func normalizeQueryForPostgres(query string) string {
	out := stripMySQLIndexHints(query)
	out = replaceFuncNameOutsideQuotes(out, "IFNULL", "COALESCE")
	out = rewriteInlineIfToCase(out)
	out = rewriteAsUnsigned(out)
	out = rewriteJSONLength(out)
	out = rewriteJSONExtract(out)
	out = rewriteJSONArrayAgg(out)
	return out
}

// skipQuotedOrAdvance writes/skips a quoted string literal starting at s[i].
func skipQuotedSpan(s string, i int) int {
	q := s[i]
	i++
	for i < len(s) {
		if s[i] == q {
			return i + 1
		}
		i++
	}
	return i
}

// findMatchingParen returns the index of the ')' matching '(' at openIdx, or -1.
func findMatchingParen(s string, openIdx int) int {
	depth := 0
	for i := openIdx; i < len(s); i++ {
		c := s[i]
		if c == '\'' || c == '"' || c == '`' {
			i = skipQuotedSpan(s, i) - 1
			continue
		}
		if c == '(' {
			depth++
		} else if c == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTopLevelArgs splits a comma-separated argument list at depth 0 outside quotes.
func splitTopLevelArgs(argList string) []string {
	var args []string
	depth := 0
	start := 0
	for i := 0; i < len(argList); i++ {
		c := argList[i]
		if c == '\'' || c == '"' || c == '`' {
			i = skipQuotedSpan(argList, i) - 1
			continue
		}
		if c == '(' {
			depth++
		} else if c == ')' && depth > 0 {
			depth--
		} else if c == ',' && depth == 0 {
			args = append(args, strings.TrimSpace(argList[start:i]))
			start = i + 1
		}
	}
	args = append(args, strings.TrimSpace(argList[start:]))
	return args
}

// matchKeywordAt checks if upper[i:] starts with kw with word boundaries on both sides.
func matchKeywordAt(upper string, i int, kw string) bool {
	if i+len(kw) > len(upper) {
		return false
	}
	if i > 0 && isIdentByte(upper[i-1]) {
		return false
	}
	if upper[i:i+len(kw)] != kw {
		return false
	}
	if i+len(kw) < len(upper) && isIdentByte(upper[i+len(kw)]) {
		return false
	}
	return true
}

func stripMySQLIndexHints(s string) string {
	var b strings.Builder
	upper := strings.ToUpper(s)
	n := len(s)
	for i := 0; i < n; {
		c := s[i]
		if c == '\'' || c == '"' || c == '`' {
			end := skipQuotedSpan(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		}
		matchedHint := ""
		for _, kw := range []string{"USE", "FORCE", "IGNORE"} {
			if matchKeywordAt(upper, i, kw) {
				matchedHint = kw
				break
			}
		}
		if matchedHint != "" {
			j := i + len(matchedHint)
			for j < n && isSQLSpace(s[j]) {
				j++
			}
			if matchKeywordAt(upper, j, "INDEX") {
				j += len("INDEX")
				for j < n && isSQLSpace(s[j]) {
					j++
				}
				if j < n && s[j] == '(' {
					closeIdx := findMatchingParen(s, j)
					if closeIdx != -1 {
						// Trim a single preceding space/tab if there is also space after ')' or end of line/string
						cur := b.String()
						if len(cur) > 0 && (cur[len(cur)-1] == ' ' || cur[len(cur)-1] == '\t') {
							if closeIdx+1 >= n || isSQLSpace(s[closeIdx+1]) {
								trimmed := cur[:len(cur)-1]
								b.Reset()
								b.WriteString(trimmed)
							}
						}
						i = closeIdx + 1
						continue
					}
				}
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func rewriteAsUnsigned(s string) string {
	var b strings.Builder
	upper := strings.ToUpper(s)
	n := len(s)
	for i := 0; i < n; {
		c := s[i]
		if c == '\'' || c == '"' || c == '`' {
			end := skipQuotedSpan(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		}
		if matchKeywordAt(upper, i, "AS") {
			j := i + 2
			wsStart := j
			for j < n && isSQLSpace(s[j]) {
				j++
			}
			if j > wsStart && matchKeywordAt(upper, j, "UNSIGNED") {
				b.WriteString("AS BIGINT")
				i = j + len("UNSIGNED")
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func rewriteJSONLength(s string) string {
	var b strings.Builder
	upper := strings.ToUpper(s)
	n := len(s)
	const fn = "JSON_LENGTH"
	for i := 0; i < n; {
		c := s[i]
		if c == '\'' || c == '"' || c == '`' {
			end := skipQuotedSpan(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		}
		if matchKeywordAt(upper, i, fn) {
			j := i + len(fn)
			for j < n && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			if j < n && s[j] == '(' {
				closeIdx := findMatchingParen(s, j)
				if closeIdx != -1 {
					args := splitTopLevelArgs(s[j+1 : closeIdx])
					if len(args) == 1 {
						b.WriteString(fmt.Sprintf("jsonb_array_length((%s)::jsonb)", rewriteJSONLength(args[0])))
						i = closeIdx + 1
						continue
					} else if len(args) >= 2 {
						b.WriteString(fmt.Sprintf("jsonb_array_length(jsonb_path_query_first((%s)::jsonb, (%s)::jsonpath))", rewriteJSONLength(args[0]), args[1]))
						i = closeIdx + 1
						continue
					}
				}
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func rewriteJSONExtract(s string) string {
	var b strings.Builder
	upper := strings.ToUpper(s)
	n := len(s)
	const fn = "JSON_EXTRACT"
	for i := 0; i < n; {
		c := s[i]
		if c == '\'' || c == '"' || c == '`' {
			end := skipQuotedSpan(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		}
		if matchKeywordAt(upper, i, fn) {
			j := i + len(fn)
			for j < n && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			if j < n && s[j] == '(' {
				closeIdx := findMatchingParen(s, j)
				if closeIdx != -1 {
					args := splitTopLevelArgs(s[j+1 : closeIdx])
					if len(args) >= 2 {
						doc := rewriteJSONExtract(args[0])
						path := args[1]
						b.WriteString(fmt.Sprintf("(jsonb_path_query_first((%s)::jsonb, (%s)::jsonpath) #>> '{}')", doc, path))
						i = closeIdx + 1
						continue
					}
				}
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func hasTopLevelKeyword(s, kw1, kw2 string) int {
	upper := strings.ToUpper(s)
	depth := 0
	n := len(s)
	for i := 0; i < n; i++ {
		c := s[i]
		if c == '\'' || c == '"' || c == '`' {
			i = skipQuotedSpan(s, i) - 1
			continue
		}
		if c == '(' {
			depth++
			continue
		}
		if c == ')' && depth > 0 {
			depth--
			continue
		}
		if depth == 0 && matchKeywordAt(upper, i, kw1) {
			if kw2 == "" {
				return i
			}
			j := i + len(kw1)
			wsStart := j
			for j < n && isSQLSpace(s[j]) {
				j++
			}
			if j > wsStart && matchKeywordAt(upper, j, kw2) {
				return i
			}
		}
	}
	return -1
}

func rewriteJSONArrayAgg(s string) string {
	upper := strings.ToUpper(s)
	const fn = "JSON_ARRAYAGG"
	// Find first top-level JSON_ARRAYAGG(
	aggIdx := -1
	openIdx := -1
	closeIdx := -1
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' || c == '"' || c == '`' {
			i = skipQuotedSpan(s, i) - 1
			continue
		}
		if c == '(' {
			depth++
			continue
		}
		if c == ')' && depth > 0 {
			depth--
			continue
		}
		if depth == 0 && matchKeywordAt(upper, i, fn) {
			j := i + len(fn)
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			if j < len(s) && s[j] == '(' {
				cl := findMatchingParen(s, j)
				if cl != -1 {
					aggIdx = i
					openIdx = j
					closeIdx = cl
					break
				}
			}
		}
	}
	if aggIdx == -1 {
		return s
	}

	expr := strings.TrimSpace(s[openIdx+1 : closeIdx])
	hasGroupBy := hasTopLevelKeyword(s, "GROUP", "BY") != -1
	if !hasGroupBy {
		tail := s[closeIdx+1:]
		orderIdx := hasTopLevelKeyword(tail, "ORDER", "BY")
		if orderIdx != -1 {
			// Advance past "ORDER" + whitespace + "BY" + whitespace
			j := orderIdx + len("ORDER")
			for j < len(tail) && isSQLSpace(tail[j]) {
				j++
			}
			j += len("BY")
			for j < len(tail) && isSQLSpace(tail[j]) {
				j++
			}
			// Find end of ORDER BY expression (stop at LIMIT, OFFSET, FETCH, FOR, ;, or end)
			orderEnd := len(tail)
			tailUpper := strings.ToUpper(tail)
			d := 0
			for k := j; k < len(tail); k++ {
				c := tail[k]
				if c == '\'' || c == '"' || c == '`' {
					k = skipQuotedSpan(tail, k) - 1
					continue
				}
				if c == '(' {
					d++
					continue
				}
				if c == ')' && d > 0 {
					d--
					continue
				}
				if d == 0 {
					if c == ';' || matchKeywordAt(tailUpper, k, "LIMIT") || matchKeywordAt(tailUpper, k, "OFFSET") || matchKeywordAt(tailUpper, k, "FETCH") || matchKeywordAt(tailUpper, k, "FOR") {
						orderEnd = k
						break
					}
				}
			}
			orderExpr := strings.TrimSpace(tail[j:orderEnd])
			if orderExpr != "" {
				beforeOrder := strings.TrimRight(tail[:orderIdx], " \t\r\n")
				afterOrder := strings.TrimLeft(tail[orderEnd:], " \t")
				newTail := beforeOrder
				if afterOrder != "" {
					newTail += " " + afterOrder
				}
				return s[:aggIdx] + fmt.Sprintf("jsonb_agg(%s ORDER BY %s)::text", expr, orderExpr) + newTail
			}
		}
	}

	return s[:aggIdx] + fmt.Sprintf("jsonb_agg(%s)::text", expr) + rewriteJSONArrayAgg(s[closeIdx+1:])
}

// normalizeQueryForMySQL strips trailing top-level ORDER BY clauses from JSON_ARRAYAGG queries
// without GROUP BY to avoid MySQL 8.0 ONLY_FULL_GROUP_BY (Error 1140).
func normalizeQueryForMySQL(query string) string {
	if hasTopLevelKeyword(query, "JSON_ARRAYAGG", "") == -1 || hasTopLevelKeyword(query, "GROUP", "BY") != -1 {
		return query
	}
	orderIdx := hasTopLevelKeyword(query, "ORDER", "BY")
	if orderIdx == -1 {
		return query
	}
	j := orderIdx + len("ORDER")
	for j < len(query) && isSQLSpace(query[j]) {
		j++
	}
	j += len("BY")
	for j < len(query) && isSQLSpace(query[j]) {
		j++
	}
	orderEnd := len(query)
	upper := strings.ToUpper(query)
	d := 0
	for k := j; k < len(query); k++ {
		c := query[k]
		if c == '\'' || c == '"' || c == '`' {
			k = skipQuotedSpan(query, k) - 1
			continue
		}
		if c == '(' {
			d++
			continue
		}
		if c == ')' && d > 0 {
			d--
			continue
		}
		if d == 0 {
			if c == ';' || matchKeywordAt(upper, k, "LIMIT") || matchKeywordAt(upper, k, "OFFSET") || matchKeywordAt(upper, k, "FETCH") || matchKeywordAt(upper, k, "FOR") {
				orderEnd = k
				break
			}
		}
	}
	beforeOrder := strings.TrimRight(query[:orderIdx], " \t\r\n")
	afterOrder := strings.TrimLeft(query[orderEnd:], " \t")
	if afterOrder != "" {
		return beforeOrder + " " + afterOrder
	}
	return beforeOrder
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
			if isSQLSpace(upper[i]) && upper[i+1:i+5] == "FROM" && isSQLSpace(upper[i+5]) {
				return i
			}
		}
	}
	return -1
}

func isSQLSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
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

// defaultZeroValueForSQLType returns the SQL literal representing Go's zero value for the given SQL type.
func defaultZeroValueForSQLType(sqlType string) string {
	upper := strings.ToUpper(strings.TrimSpace(sqlType))
	switch {
	case upper == "BIGINT" || upper == "INT" || upper == "INTEGER" || upper == "SMALLINT" || upper == "TINYINT" ||
		upper == "DOUBLE" || upper == "FLOAT" || upper == "REAL" ||
		strings.HasPrefix(upper, "NUMERIC") || strings.HasPrefix(upper, "DECIMAL"):
		return "0"
	case upper == "BOOLEAN" || upper == "BOOL":
		return "FALSE"
	case upper == "TEXT" || strings.HasPrefix(upper, "VARCHAR") || strings.HasPrefix(upper, "CHAR"):
		return "''"
	default:
		return ""
	}
}

// dedentSQL strips leading/trailing blank lines and removes the common leading whitespace
// prefix from all non-empty lines of a multi-line SQL string.
func dedentSQL(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	lines = lines[start:end]
	if len(lines) == 0 {
		return ""
	}
	for i, line := range lines {
		lines[i] = strings.ReplaceAll(line, "\t", "    ")
	}
	minIndent := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := 0
		for indent < len(line) && line[indent] == ' ' {
			indent++
		}
		if i == 0 && indent == 0 && len(lines) > 1 {
			continue
		}
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent > 0 {
		for i, line := range lines {
			if strings.TrimSpace(line) == "" {
				lines[i] = ""
				continue
			}
			indent := 0
			for indent < len(line) && line[indent] == ' ' {
				indent++
			}
			if indent >= minIndent {
				lines[i] = line[minIndent:]
			}
		}
	}
	return strings.Join(lines, "\n")
}

// formatSQLCommentLines converts Go comment text (// or /* ... */) to SQL comment lines (-- or /* ... */).
func formatSQLCommentLines(text string) string {
	rawLines := strings.Split(text, "\n")
	var out []string
	for _, l := range rawLines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "//gorm:") {
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			body := strings.TrimPrefix(trimmed, "//")
			body = strings.TrimPrefix(body, " ")
			if body == "" {
				out = append(out, "--")
			} else {
				out = append(out, "-- "+body)
			}
		} else {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

// formatInlineVarComment formats a Go CommentGroup on a var declaration into an inline SQL /* ... */ comment.
func formatInlineVarComment(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	var parts []string
	for _, c := range cg.List {
		t := strings.TrimSpace(c.Text)
		if strings.HasPrefix(t, "//gorm:") {
			continue
		}
		if strings.HasPrefix(t, "//") {
			t = strings.TrimSpace(strings.TrimPrefix(t, "//"))
		} else if strings.HasPrefix(t, "/*") && strings.HasSuffix(t, "*/") {
			t = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, "/*"), "*/"))
		}
		t = strings.ReplaceAll(t, "/*", "")
		t = strings.ReplaceAll(t, "*/", "")
		if t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " /* " + strings.Join(parts, " ") + " */"
}

// formatCallStmt formats a SQL CALL statement onto a single line when <= 4 args,
// or across multiple lines (1 arg per line) when > 4 args.
func formatCallStmt(subName string, args []string) string {
	if len(args) <= 4 {
		return fmt.Sprintf("CALL %s(%s);\n", subName, strings.Join(args, ", "))
	}
	return fmt.Sprintf("CALL %s(\n\t%s\n);\n", subName, strings.Join(args, ",\n\t"))
}

// emitPostgresSelectInto emits a PL/pgSQL SELECT ... INTO statement wrapped in a sub-block that
// preserves target variables' prior values when 0 rows are returned (matching Go and MySQL semantics),
// while keeping legitimate NULL column values when a row IS found.
func emitPostgresSelectInto(bodyBuf *bytes.Buffer, call *ast.CallExpr, extractArg func(ast.Expr) string) bool {
	if len(call.Args) < 2 {
		return false
	}
	queryLit, ok := call.Args[1].(*ast.BasicLit)
	if !ok {
		return false
	}
	rawQuery := strings.Trim(queryLit.Value, "`\"")
	isMultiline := strings.Contains(rawQuery, "\n")
	query := normalizeQueryForPostgres(rawQuery)
	if isMultiline {
		query = dedentSQL(query)
	} else {
		query = strings.ReplaceAll(query, "\t", " ")
	}

	numQs := strings.Count(query, "?")
	if 2+numQs > len(call.Args) {
		return false
	}
	inArgs := call.Args[2 : 2+numQs]
	outArgs := call.Args[2+numQs:]

	for _, inArg := range inArgs {
		query = strings.Replace(query, "?", extractArg(inArg), 1)
	}

	idxFrom := findTopLevelFrom(query)
	if idxFrom == -1 {
		return false
	}

	selectPart := strings.TrimSpace(query[:idxFrom])
	fromPart := strings.TrimSpace(query[idxFrom:])

	var outs []string
	for _, outArg := range outArgs {
		outs = append(outs, extractArg(outArg))
	}

	var selectStmt string
	if isMultiline {
		selectStmt = fmt.Sprintf("%s\nINTO %s\n%s;", selectPart, strings.Join(outs, ", "), fromPart)
	} else {
		selectStmt = fmt.Sprintf("%s INTO %s %s;", selectPart, strings.Join(outs, ", "), fromPart)
	}

	if len(outs) == 0 {
		if isMultiline {
			bodyBuf.WriteString(fmt.Sprintf("%s\n%s;\n", selectPart, fromPart))
		} else {
			bodyBuf.WriteString(fmt.Sprintf("%s %s;\n", selectPart, fromPart))
		}
		return true
	}

	bodyBuf.WriteString("DECLARE\n")
	for i, outVar := range outs {
		bodyBuf.WriteString(fmt.Sprintf("\t_prev_%d %s%%TYPE := %s;\n", i, outVar, outVar))
	}
	bodyBuf.WriteString("BEGIN\n")
	for _, line := range strings.Split(selectStmt, "\n") {
		bodyBuf.WriteString("\t" + line + "\n")
	}
	bodyBuf.WriteString("\tIF NOT FOUND THEN\n")
	for i, outVar := range outs {
		bodyBuf.WriteString(fmt.Sprintf("\t\t%s := _prev_%d;\n", outVar, i))
	}
	bodyBuf.WriteString("\tEND IF;\n")
	bodyBuf.WriteString("END;\n")
	return true
}
