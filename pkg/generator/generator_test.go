package generator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	coreParser "github.com/user/gormproc/pkg/parser"
	"golang.org/x/tools/go/packages"
)

func TestGenerateHash(t *testing.T) {
	proc := &coreParser.Procedure{Name: "TestProc"}
	h1 := GenerateHash(proc, "SQL1", "SQL2")
	h2 := GenerateHash(proc, "SQL1", "SQL2")
	h3 := GenerateHash(proc, "SQL_DIFFERENT", "SQL2")

	if len(h1) != 8 {
		t.Errorf("Expected 8-character hash, got %d chars: %s", len(h1), h1)
	}
	if h1 != h2 {
		t.Errorf("Expected deterministic hash, got %s != %s", h1, h2)
	}
	if h1 == h3 {
		t.Errorf("Expected different hash for different SQL, got same: %s", h1)
	}
}

func TestGenerateCode(t *testing.T) {
	code := `package test
import "context"
func SimpleProc(ctx context.Context, id int64) error { return nil }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", code, 0)
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}

	fn := file.Decls[1].(*ast.FuncDecl)
	proc := &coreParser.Procedure{
		Name: "SimpleProc",
		Func: fn,
		Pkg:  &packages.Package{Fset: fset},
	}

	genProcs := []GeneratedProc{
		{
			Proc:     proc,
			Hash:     "12345678",
			PgSQL:    "SELECT 1;",
			MySQLSQL: "SELECT 1;",
		},
	}

	src, err := GenerateCode("github.com/user/gormproc/example/procedures", genProcs)
	if err != nil {
		t.Fatalf("GenerateCode failed: %v", err)
	}

	str := string(src)
	if !strings.Contains(str, "SimpleProc(ctx context.Context, id int64) error") {
		t.Errorf("Expected interface signature missing. Got:\n%s", str)
	}
	if !strings.Contains(str, "p.db.WithContext(ctx).Exec(\"CALL simpleproc_12345678(?)\", id).Error") {
		t.Errorf("Expected Exec call missing in generated code. Got:\n%s", str)
	}
	if !strings.Contains(str, "return err") {
		t.Errorf("Expected return err missing. Got:\n%s", str)
	}
}
