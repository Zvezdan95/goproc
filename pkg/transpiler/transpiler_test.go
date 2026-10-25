package transpiler

import (
	"strings"
	"testing"

	"github.com/user/gormproc/pkg/parser"
)

func TestTranspilers(t *testing.T) {
	dir := "../../example/procedures"

	procs, err := parser.ParseDir(dir)
	if err != nil {
		t.Fatalf("ParseDir failed: %v", err)
	}

	if len(procs) == 0 {
		t.Fatalf("Expected at least 1 procedure, got 0")
	}

	procMap := make(map[string]*parser.Procedure)
	for _, p := range procs {
		procMap[p.Name] = p
	}

	// 1. Test ProcessBilling
	billingProc, ok := procMap["ProcessBilling"]
	if !ok {
		t.Fatalf("ProcessBilling procedure not found")
	}

	pgBilling, err := ToPostgres(billingProc, "testhash")
	if err != nil {
		t.Fatalf("ToPostgres(ProcessBilling) failed: %v", err)
	}
	if !strings.Contains(pgBilling, "processbilling_testhash(p_userID BIGINT, p_amount NUMERIC)") {
		t.Errorf("Postgres signature mismatch. Got:\n%s", pgBilling)
	}
	if !strings.Contains(pgBilling, "RAISE EXCEPTION 'amount must be greater than zero'") {
		t.Errorf("Postgres RAISE EXCEPTION missing. Got:\n%s", pgBilling)
	}
	if !strings.Contains(pgBilling, "SELECT id, total FROM invoices WHERE user_id = p_userID") {
		t.Errorf("Postgres query missing. Got:\n%s", pgBilling)
	}

	myBilling, err := ToMySQL(billingProc, "testhash")
	if err != nil {
		t.Fatalf("ToMySQL(ProcessBilling) failed: %v", err)
	}
	if !strings.Contains(myBilling, "CREATE PROCEDURE processbilling_testhash(IN p_userID BIGINT, IN p_amount DOUBLE)") {
		t.Errorf("MySQL signature mismatch. Got:\n%s", myBilling)
	}
	if !strings.Contains(myBilling, "SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = \"amount must be greater than zero\"") {
		t.Errorf("MySQL SIGNAL missing. Got:\n%s", myBilling)
	}
	if !strings.Contains(myBilling, "SELECT id, total FROM invoices WHERE user_id = p_userID") {
		t.Errorf("MySQL query missing. Got:\n%s", myBilling)
	}

	// 2. Test ComplexFormatTest
	complexProc, ok := procMap["ComplexFormatTest"]
	if !ok {
		t.Fatalf("ComplexFormatTest procedure not found")
	}

	pgComplex, err := ToPostgres(complexProc, "testhash")
	if err != nil {
		t.Fatalf("ToPostgres(ComplexFormatTest) failed: %v", err)
	}
	if !strings.Contains(pgComplex, "DECLARE") || !strings.Contains(pgComplex, "v_status TEXT;") || !strings.Contains(pgComplex, "v_i BIGINT;") {
		t.Errorf("Postgres local variable declarations missing. Got:\n%s", pgComplex)
	}
	if !strings.Contains(pgComplex, "WHILE v_i < 5 LOOP") || !strings.Contains(pgComplex, "v_i := v_i + 1;") {
		t.Errorf("Postgres while loop syntax error. Got:\n%s", pgComplex)
	}
	if !strings.Contains(pgComplex, "IF v_status = 'START' THEN") {
		t.Errorf("Postgres local variable reference error in IF. Got:\n%s", pgComplex)
	}

	myComplex, err := ToMySQL(complexProc, "testhash")
	if err != nil {
		t.Fatalf("ToMySQL(ComplexFormatTest) failed: %v", err)
	}
	if !strings.Contains(myComplex, "DECLARE v_status TEXT;") || !strings.Contains(myComplex, "DECLARE v_i BIGINT;") {
		t.Errorf("MySQL local variable declarations missing. Got:\n%s", myComplex)
	}
	if !strings.Contains(myComplex, "WHILE v_i < 5 DO") || !strings.Contains(myComplex, "SET v_i = v_i + 1;") {
		t.Errorf("MySQL while loop syntax error. Got:\n%s", myComplex)
	}

	// 3. Test TransferFunds
	transferProc, ok := procMap["TransferFunds"]
	if !ok {
		t.Fatalf("TransferFunds procedure not found")
	}

	pgTransfer, err := ToPostgres(transferProc, "testhash")
	if err != nil {
		t.Fatalf("ToPostgres(TransferFunds) failed: %v", err)
	}
	if !strings.Contains(pgTransfer, "v_success BOOLEAN;") {
		t.Errorf("Postgres v_success declaration missing. Got:\n%s", pgTransfer)
	}
	if !strings.Contains(pgTransfer, "UPDATE accounts SET balance = balance - p_amount WHERE id = p_fromID") ||
		!strings.Contains(pgTransfer, "UPDATE accounts SET balance = balance + p_amount WHERE id = p_toID") {
		t.Errorf("Postgres UPDATE statements missing. Got:\n%s", pgTransfer)
	}

	myTransfer, err := ToMySQL(transferProc, "testhash")
	if err != nil {
		t.Fatalf("ToMySQL(TransferFunds) failed: %v", err)
	}
	if !strings.Contains(myTransfer, "DECLARE v_success BOOLEAN;") {
		t.Errorf("MySQL v_success declaration missing. Got:\n%s", myTransfer)
	}
	if !strings.Contains(myTransfer, "UPDATE accounts SET balance = balance - p_amount WHERE id = p_fromID") ||
		!strings.Contains(myTransfer, "UPDATE accounts SET balance = balance + p_amount WHERE id = p_toID") {
		t.Errorf("MySQL UPDATE statements missing. Got:\n%s", myTransfer)
	}
}
