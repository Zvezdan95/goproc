package transpiler

import (
	"strings"
	"testing"

	"github.com/user/gormproc/pkg/parser"
)

func TestTranspilers(t *testing.T) {
	// Parse the example procedures directory
	dir := "../../example/procedures"

	procs, err := parser.ParseDir(dir)
	if err != nil {
		t.Fatalf("ParseDir failed: %v", err)
	}

	if len(procs) == 0 {
		t.Fatalf("Expected at least 1 procedure, got 0")
	}

	proc := procs[0]

	// Test Postgres
	pgSQL, err := ToPostgres(proc, "testhash")
	if err != nil {
		t.Errorf("ToPostgres failed: %v", err)
	}
	if !strings.Contains(pgSQL, "processbilling_testhash(p_userID BIGINT") {
		t.Errorf("Postgres SQL missing expected func signature. Got:\n%s", pgSQL)
	}

	// Test MySQL
	mysqlSQL, err := ToMySQL(proc, "testhash")
	if err != nil {
		t.Errorf("ToMySQL failed: %v", err)
	}
	if !strings.Contains(mysqlSQL, "CREATE PROCEDURE processbilling_testhash") {
		t.Errorf("MySQL SQL missing expected func signature. Got:\n%s", mysqlSQL)
	}
}
