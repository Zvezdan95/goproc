package transpiler

import (
	"go/ast"
	goParser "go/parser"
	"go/token"
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

func parseCodeToProc(t *testing.T, name, code string) *parser.Procedure {
	fset := token.NewFileSet()
	f, err := goParser.ParseFile(fset, "test.go", code, 0)
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatalf("Function %s not found in parsed code", name)
	}
	return &parser.Procedure{
		Name: name,
		Func: fn,
	}
}

func TestReportDefectFixes(t *testing.T) {
	// 1. Defect 2: Blank Identifier Suppression (_ = a should emit nothing, never v__)
	codeBlank := `package test
import "context"
func BlankIdProc(ctx context.Context, m_id int64) {
	_ = m_id
}`
	procBlank := parseCodeToProc(t, "BlankIdProc", codeBlank)
	myBlank, err := ToMySQL(procBlank, "h1")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	if strings.Contains(myBlank, "v__") {
		t.Errorf("MySQL output contains blank identifier variable v__:\n%s", myBlank)
	}
	pgBlank, err := ToPostgres(procBlank, "h1")
	if err != nil {
		t.Fatalf("ToPostgres failed: %v", err)
	}
	if strings.Contains(pgBlank, "v__") {
		t.Errorf("Postgres output contains blank identifier variable v__:\n%s", pgBlank)
	}

	// 2. Defect 3: Type Cast Unwrapping & Primitive Types (uint64(id), uint32(status))
	codeCast := `package test
import (
	"context"
	"gorm.io/gorm"
)
func CastProc(ctx context.Context, id uint64, status uint32) {
	var tx *gorm.DB
	tx.Exec("UPDATE tbl SET status = ? WHERE id = ?", uint32(status), uint64(id))
}`
	procCast := parseCodeToProc(t, "CastProc", codeCast)
	myCast, err := ToMySQL(procCast, "h2")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	if !strings.Contains(myCast, "UPDATE tbl SET status = p_status WHERE id = p_id") {
		t.Errorf("MySQL did not unwrap type casts. Got:\n%s", myCast)
	}
	pgCast, err := ToPostgres(procCast, "h2")
	if err != nil {
		t.Fatalf("ToPostgres failed: %v", err)
	}
	if !strings.Contains(pgCast, "UPDATE tbl SET status = p_status WHERE id = p_id") {
		t.Errorf("Postgres did not unwrap type casts. Got:\n%s", pgCast)
	}

	// 3. Defect 1: Fatal Empty Control Blocks (IF v_err IS NOT NULL THEN END IF;)
	codeErrCheck := `package test
import (
	"context"
	"gorm.io/gorm"
)
type Svc struct{}
func (s *Svc) SubProc(ctx context.Context, id int64) error { return nil }
func ErrProc(ctx context.Context, id int64) error {
	var s Svc
	if err := s.SubProc(ctx, id); err != nil {
		return err
	}
	return nil
}`
	procErr := parseCodeToProc(t, "ErrProc", codeErrCheck)
	myErr, err := ToMySQL(procErr, "h3")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	if strings.Contains(myErr, "IF") || strings.Contains(myErr, "END IF;") {
		t.Errorf("MySQL generated empty IF error block:\n%s", myErr)
	}
	if !strings.Contains(myErr, "CALL subproc(p_id);") {
		t.Errorf("MySQL missing CALL subproc:\n%s", myErr)
	}
	pgErr, err := ToPostgres(procErr, "h3")
	if err != nil {
		t.Fatalf("ToPostgres failed: %v", err)
	}
	if strings.Contains(pgErr, "IF") || strings.Contains(pgErr, "END IF;") {
		t.Errorf("Postgres generated empty IF error block:\n%s", pgErr)
	}
	if !strings.Contains(pgErr, "CALL subproc(p_id);") {
		t.Errorf("Postgres missing CALL subproc:\n%s", pgErr)
	}

	// 4. Defect 5: Multi-argument fmt.Sprintf -> CONCAT
	codeSprintf := `package test
import (
	"context"
	"fmt"
)
func SprintfProc(ctx context.Context, id int64, name string) {
	msg := fmt.Sprintf("order_%d_user_%s", id, name)
	_ = msg
}`
	procSprintf := parseCodeToProc(t, "SprintfProc", codeSprintf)
	mySprintf, err := ToMySQL(procSprintf, "h4")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	if !strings.Contains(mySprintf, "SET v_msg = CONCAT('order_', p_id, '_user_', p_name);") {
		t.Errorf("MySQL Sprintf format mismatch. Got:\n%s", mySprintf)
	}
	pgSprintf, err := ToPostgres(procSprintf, "h4")
	if err != nil {
		t.Fatalf("ToPostgres failed: %v", err)
	}
	if !strings.Contains(pgSprintf, "v_msg := CONCAT('order_', p_id, '_user_', p_name);") {
		t.Errorf("Postgres Sprintf format mismatch. Got:\n%s", pgSprintf)
	}

	// 5. Defect 5: gormproc.LastInsertID polyfill transpilation
	codeLastInsert := `package test
import (
	"context"
	"github.com/user/gormproc"
	"gorm.io/gorm"
)
func LastInsertProc(ctx context.Context) {
	var tx *gorm.DB
	id := gormproc.LastInsertID(tx)
	_ = id
}`
	procLastInsert := parseCodeToProc(t, "LastInsertProc", codeLastInsert)
	myLast, err := ToMySQL(procLastInsert, "h5")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	if !strings.Contains(myLast, "LAST_INSERT_ID()") {
		t.Errorf("MySQL LastInsertID missing. Got:\n%s", myLast)
	}
	pgLast, err := ToPostgres(procLastInsert, "h5")
	if err != nil {
		t.Fatalf("ToPostgres failed: %v", err)
	}
	if !strings.Contains(pgLast, "LASTVAL()") {
		t.Errorf("Postgres LastInsertID missing. Got:\n%s", pgLast)
	}

	// 6. Defect 7: Subquery inside SelectInto does not slice at inner FROM
	codeSubquery := `package test
import (
	"context"
	"github.com/user/gormproc"
	"gorm.io/gorm"
)
func SubqueryProc(ctx context.Context, userID int64) {
	var tx *gorm.DB
	var vID int64
	var vRole string
	gormproc.SelectInto(tx, "SELECT id, (SELECT name FROM roles WHERE id = 1) AS role_name FROM users WHERE user_id = ?", userID, &vID, &vRole)
}`
	procSubquery := parseCodeToProc(t, "SubqueryProc", codeSubquery)
	mySub, err := ToMySQL(procSubquery, "h6")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	expectedSelect := "SELECT id, (SELECT name FROM roles WHERE id = 1) AS role_name INTO v_vID, v_vRole FROM users WHERE user_id = p_userID;"
	if !strings.Contains(mySub, expectedSelect) {
		t.Errorf("MySQL SelectInto subquery split incorrectly. Got:\n%s", mySub)
	}
	pgSub, err := ToPostgres(procSubquery, "h6")
	if err != nil {
		t.Fatalf("ToPostgres failed: %v", err)
	}
	if !strings.Contains(pgSub, expectedSelect) {
		t.Errorf("Postgres SelectInto subquery split incorrectly. Got:\n%s", pgSub)
	}

	// 7. Defect 4: Return Values and Output Result Sets populated outside transaction
	codeResField := `package test
import (
	"context"
	"gorm.io/gorm"
)
type BookResult struct {
	RoundID string
	Balance float64
}
type BookService struct {
	db *gorm.DB
}
func (s *BookService) Book(ctx context.Context, roundID string) (BookResult, error) {
	var res BookResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		tx.Exec("UPDATE rounds SET status = 'BOOKED' WHERE id = ?", roundID)
		return nil
	})
	res.RoundID = roundID
	return res, err
}`
	procRes := parseCodeToProc(t, "Book", codeResField)
	myRes, err := ToMySQL(procRes, "h7")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	if !strings.Contains(myRes, "SELECT v_RoundID AS RoundID;") {
		t.Errorf("MySQL missing result projection SELECT v_RoundID AS RoundID. Got:\n%s", myRes)
	}
	pgRes, err := ToPostgres(procRes, "h7")
	if err != nil {
		t.Fatalf("ToPostgres failed: %v", err)
	}
	if !strings.Contains(pgRes, "SELECT v_RoundID AS RoundID;") {
		t.Errorf("Postgres missing result projection SELECT v_RoundID AS RoundID. Got:\n%s", pgRes)
	}

	// 8. Inlined function calls in binary expressions (e.g. if gormproc.JSONLength(...) == 1)
	codeInlined := `package test
import (
	"context"
	"github.com/user/gormproc"
)
func InlinedProc(ctx context.Context, m_book string, m_flags uint64) {
	if gormproc.JSONLength(m_book, "$") == 1 {
		_ = m_book
	}
	if pokermem.GlobHasBits(m_flags, 16) != 0 {
		_ = m_flags
	}
}`
	procInlined := parseCodeToProc(t, "InlinedProc", codeInlined)
	myInlined, err := ToMySQL(procInlined, "h8")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	if !strings.Contains(myInlined, "IF JSON_LENGTH(p_m_book, \"$\") = 1 THEN") {
		t.Errorf("MySQL inlined JSONLength failed. Got:\n%s", myInlined)
	}
	if !strings.Contains(myInlined, "IF pokermem.GlobHasBits(p_m_flags, 16) != 0 THEN") {
		t.Errorf("MySQL inlined GlobHasBits failed. Got:\n%s", myInlined)
	}
	pgInlined, err := ToPostgres(procInlined, "h8")
	if err != nil {
		t.Fatalf("ToPostgres failed: %v", err)
	}
	if !strings.Contains(pgInlined, "IF jsonb_array_length((p_m_book)::jsonb) = 1 THEN") {
		t.Errorf("Postgres inlined JSONLength failed. Got:\n%s", pgInlined)
	}
	if !strings.Contains(pgInlined, "IF pokermem.GlobHasBits(p_m_flags, 16) != 0 THEN") {
		t.Errorf("Postgres inlined GlobHasBits failed. Got:\n%s", pgInlined)
	}

	// 9. Composite literal struct assignment (res = BookResult{...})
	codeComposite := `package test
import (
	"context"
	"gorm.io/gorm"
)
type BookResult struct {
	RoundID string
	Balance float64
}
func CompositeProc(ctx context.Context, roundID string) (BookResult, error) {
	var res BookResult
	var tx *gorm.DB
	tx.Exec("UPDATE rounds SET id = ?", roundID)
	res = BookResult{
		RoundID: roundID,
		Balance: 250.5,
	}
	return res, nil
}`
	procComposite := parseCodeToProc(t, "CompositeProc", codeComposite)
	myComp, err := ToMySQL(procComposite, "h9")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	if !strings.Contains(myComp, "SET v_RoundID = p_roundID;") || !strings.Contains(myComp, "SET v_Balance = 250.5;") {
		t.Errorf("MySQL missing composite literal SET statements. Got:\n%s", myComp)
	}
	if !strings.Contains(myComp, "SELECT v_RoundID AS RoundID, v_Balance AS Balance;") {
		t.Errorf("MySQL missing composite literal SELECT projection. Got:\n%s", myComp)
	}
	pgComp, err := ToPostgres(procComposite, "h9")
	if err != nil {
		t.Fatalf("ToPostgres failed: %v", err)
	}
	if !strings.Contains(pgComp, "v_RoundID := p_roundID;") || !strings.Contains(pgComp, "v_Balance := 250.5;") {
		t.Errorf("Postgres missing composite literal assignment statements. Got:\n%s", pgComp)
	}
	// 10. Observation 1: Idiomatic Error Checking with SelectInto (err == nil && condition)
	codeSelectIntoErr := `package test
import (
	"context"
	"github.com/user/gormproc"
	"gorm.io/gorm"
)
func SelectIntoErrProc(ctx context.Context, id int64) error {
	var tx *gorm.DB
	var bonusID int64
	if err := gormproc.SelectInto(tx, "SELECT id FROM bonus WHERE user_id = ?", id, &bonusID); err == nil && bonusID != 0 {
		tx.Exec("UPDATE users SET bonus_id = ?", bonusID)
	}
	return nil
}`
	procSelectIntoErr := parseCodeToProc(t, "SelectIntoErrProc", codeSelectIntoErr)
	mySelectErr, err := ToMySQL(procSelectIntoErr, "h10")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	if strings.Contains(mySelectErr, "v_err") {
		t.Errorf("MySQL emitted invalid v_err variable. Got:\n%s", mySelectErr)
	}
	if !strings.Contains(mySelectErr, "IF v_bonusID != 0 THEN") {
		t.Errorf("MySQL failed to simplify err == nil && bonusID != 0 condition. Got:\n%s", mySelectErr)
	}
	pgSelectErr, err := ToPostgres(procSelectIntoErr, "h10")
	if err != nil {
		t.Fatalf("ToPostgres failed: %v", err)
	}
	if strings.Contains(pgSelectErr, "v_err") {
		t.Errorf("Postgres emitted invalid v_err variable. Got:\n%s", pgSelectErr)
	}
	if !strings.Contains(pgSelectErr, "IF v_bonusID != 0 THEN") {
		t.Errorf("Postgres failed to simplify err == nil && bonusID != 0 condition. Got:\n%s", pgSelectErr)
	}

	// 11. Observation 2: Pointer variable declarations (var bAccID *uint32, var s *string)
	codePointers := `package test
import (
	"context"
	"gorm.io/gorm"
)
func PointerVarProc(ctx context.Context) error {
	var tx *gorm.DB
	var bAccID *uint32
	var sMsg *string
	tx.Exec("UPDATE test SET val = 1")
	_ = bAccID
	_ = sMsg
	return nil
}`
	procPointers := parseCodeToProc(t, "PointerVarProc", codePointers)
	myPointers, err := ToMySQL(procPointers, "h11")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	if !strings.Contains(myPointers, "DECLARE v_bAccID BIGINT;") {
		t.Errorf("MySQL missing DECLARE v_bAccID BIGINT; for *uint32 pointer. Got:\n%s", myPointers)
	}
	if !strings.Contains(myPointers, "DECLARE v_sMsg TEXT;") {
		t.Errorf("MySQL missing DECLARE v_sMsg TEXT; for *string pointer. Got:\n%s", myPointers)
	}

	// 12. Short variable declaration (:=) and struct pointer assignment (res.BonusAccountID = &bAccID)
	codeShortDecl := `package test
import (
	"context"
	"gorm.io/gorm"
)
type ResultStruct struct {
	BonusAccountID *uint32
}
func ShortDeclProc(ctx context.Context) (ResultStruct, error) {
	var res ResultStruct
	var tx *gorm.DB
	bAccID := uint32(1001)
	count := 5
	tx.Exec("UPDATE test SET count = ?", count)
	res.BonusAccountID = &bAccID
	return res, nil
}`
	procShortDecl := parseCodeToProc(t, "ShortDeclProc", codeShortDecl)
	myShortDecl, err := ToMySQL(procShortDecl, "h12")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	if !strings.Contains(myShortDecl, "DECLARE v_bAccID BIGINT;") {
		t.Errorf("MySQL missing DECLARE for := variable bAccID. Got:\n%s", myShortDecl)
	}
	if !strings.Contains(myShortDecl, "DECLARE v_count BIGINT") {
		t.Errorf("MySQL missing DECLARE for := variable count. Got:\n%s", myShortDecl)
	}
	if !strings.Contains(myShortDecl, "SET v_BonusAccountID = v_bAccID;") {
		t.Errorf("MySQL missing SET v_BonusAccountID = v_bAccID;. Got:\n%s", myShortDecl)
	}
	if !strings.Contains(myShortDecl, "DECLARE v_BonusAccountID BIGINT;") {
		t.Errorf("MySQL expected DECLARE v_BonusAccountID BIGINT; inferred from &bAccID. Got:\n%s", myShortDecl)
	}
}


