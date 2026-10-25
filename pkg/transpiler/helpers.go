package transpiler

import (
	"go/ast"
	"go/token"
	"strings"
)

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
				case "FromJsonInt64", "JSONLength", "FromJsonArray":
					return intType
				case "FromJsonString", "FromJsonStringOptional", "JSONExtractString":
					return strType
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
