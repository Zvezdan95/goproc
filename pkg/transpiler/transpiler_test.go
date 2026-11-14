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
	if !strings.Contains(pgComplex, "DECLARE") || !strings.Contains(pgComplex, "v_status TEXT := '';") || !strings.Contains(pgComplex, "v_i BIGINT := 0;") {
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
	if !strings.Contains(myComplex, "DECLARE v_status TEXT DEFAULT '';") || !strings.Contains(myComplex, "DECLARE v_i BIGINT DEFAULT 0;") {
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
	if !strings.Contains(pgTransfer, "v_success BOOLEAN := FALSE;") {
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
	if !strings.Contains(myTransfer, "DECLARE v_success BOOLEAN DEFAULT FALSE;") {
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
	if !strings.Contains(myPointers, "DECLARE v_bAccID BIGINT DEFAULT 0;") {
		t.Errorf("MySQL missing DECLARE v_bAccID BIGINT DEFAULT 0; for *uint32 pointer. Got:\n%s", myPointers)
	}
	if !strings.Contains(myPointers, "DECLARE v_sMsg TEXT DEFAULT '';") {
		t.Errorf("MySQL missing DECLARE v_sMsg TEXT DEFAULT ''; for *string pointer. Got:\n%s", myPointers)
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
	if !strings.Contains(myShortDecl, "DECLARE v_bAccID BIGINT DEFAULT 0;") {
		t.Errorf("MySQL missing DECLARE for := variable bAccID. Got:\n%s", myShortDecl)
	}
	if !strings.Contains(myShortDecl, "DECLARE v_count BIGINT") {
		t.Errorf("MySQL missing DECLARE for := variable count. Got:\n%s", myShortDecl)
	}
	if !strings.Contains(myShortDecl, "SET v_BonusAccountID = v_bAccID;") {
		t.Errorf("MySQL missing SET v_BonusAccountID = v_bAccID;. Got:\n%s", myShortDecl)
	}
	if !strings.Contains(myShortDecl, "DECLARE v_BonusAccountID BIGINT DEFAULT 0;") {
		t.Errorf("MySQL expected DECLARE v_BonusAccountID BIGINT DEFAULT 0; inferred from &bAccID. Got:\n%s", myShortDecl)
	}
}

func TestRecursiveInliningAndLoopFixes(t *testing.T) {
	src := `package testpkg

import (
	"context"
	"github.com/user/gormproc"
	"gorm.io/gorm"
)

type RoundSummary struct {
	FinalBalance float64
	Processed    int64
}

type ExtSubProcs struct {
	gormproc.BaseService
}

//gorm:proc pokermem.TriggerWinnerFeed2
func (e *ExtSubProcs) TriggerWinnerFeed2(userID int64, amount float64) error {
	return nil
}

type LeafTaxCalculator interface {
	ComputeNet(gross float64) float64
}

type flatTaxCalc struct {
	rate float64
}

func (c *flatTaxCalc) ComputeNet(gross float64) float64 {
	if gross <= 0 {
		return 0
	}
	return applyMultiplier(gross, 1.0-c.rate)
}

func applyMultiplier(v float64, m float64) float64 {
	return v * m
}

type AccountRepo interface {
	CreditPlayer(tx *gorm.DB, userID int64, gross float64, outNewBal *float64) error
}

type sqlAccountRepo struct {
	tax LeafTaxCalculator
}

func (r *sqlAccountRepo) CreditPlayer(tx *gorm.DB, userID int64, gross float64, outNewBal *float64) error {
	net := r.tax.ComputeNet(gross)
	return applySqlCredit(tx, userID, net, outNewBal)
}

func applySqlCredit(tx *gorm.DB, userID int64, net float64, outBal *float64) error {
	if err := tx.Exec("UPDATE accounts SET balance = IFNULL(balance, 0) + ? WHERE id = ?", net, userID).Error; err != nil {
		return err
	}
	return gormproc.SelectInto(tx, "SELECT IF(balance > 0, balance, 0) FROM accounts WHERE id = ?", userID, outBal)
}

type UnmarkedHelper struct{}

func (u *UnmarkedHelper) ExecuteStep(tx *gorm.DB, repo AccountRepo, userID int64, amt float64, outBal *float64) error {
	return repo.CreditPlayer(tx, userID, amt, outBal)
}

type GameService struct {
	db  *gorm.DB
	ext *ExtSubProcs
}

//gorm:procedure
func (s *GameService) SettleBatch(ctx context.Context, userID int64, payload gormproc.JSON, stake gormproc.Decimal, count int64) (RoundSummary, error) {
	var res RoundSummary
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var bal float64
		var processed int64
		var calc LeafTaxCalculator = &flatTaxCalc{rate: 0.1}
		var repo AccountRepo = &sqlAccountRepo{tax: calc}
		helper := UnmarkedHelper{}

		var idx int64
		for idx = 0; idx < count; idx++ {
			kind := gormproc.FromJsonInt64(payload, "$[0]")
			switch kind {
			case 0:
				continue
			case 99:
				break
			}
			if kind < 0 {
				break
			}
			if err := helper.ExecuteStep(tx, repo, userID, stake, &bal); err != nil {
				return err
			}
			processed++
		}

		if err := s.ext.TriggerWinnerFeed2(userID, bal); err != nil {
			return err
		}

		res.FinalBalance = bal
		res.Processed = processed
		return nil
	})
	return res, err
}
`

	procs, err := parser.ParseSource(map[string]string{"settle.go": src})
	if err != nil {
		t.Fatalf("ParseSource failed: %v", err)
	}
	if len(procs) != 1 || procs[0].Name != "SettleBatch" {
		t.Fatalf("expected exactly 1 procedure (SettleBatch), got %d", len(procs))
	}

	mySQL, err := ToMySQL(procs[0], "abcd1234")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}

	// 1. Parameter types: JSON and DECIMAL(65,18) in MySQL
	if !strings.Contains(mySQL, "IN p_payload JSON, IN p_stake DECIMAL(65,18), IN p_count BIGINT") {
		t.Errorf("MySQL expected JSON and DECIMAL(65,18) parameter types. Got:\n%s", mySQL)
	}
	// 2. Helper functions & unmarked struct/interface methods must be inlined as SQL, NEVER emitted as CALL!
	for _, forbiddenCall := range []string{"CALL executestep", "CALL creditplayer", "CALL applysqlcredit", "CALL computenet", "CALL applymultiplier"} {
		if strings.Contains(strings.ToLower(mySQL), strings.ToLower(forbiddenCall)) {
			t.Errorf("MySQL emitted forbidden helper CALL %q instead of inlining:\n%s", forbiddenCall, mySQL)
		}
	}
	// 3. Inlined SQL & cross-schema CALL present
	if !strings.Contains(mySQL, "UPDATE accounts SET balance = IFNULL(balance, 0) +") {
		t.Errorf("MySQL missing inlined UPDATE from deep helper:\n%s", mySQL)
	}
	if !strings.Contains(mySQL, "INTO v_bal FROM accounts WHERE id = p_userID;") {
		t.Errorf("MySQL missing pointer out-param substitution INTO v_bal:\n%s", mySQL)
	}
	if !strings.Contains(mySQL, "CALL pokermem.TriggerWinnerFeed2(p_userID, v_bal);") {
		t.Errorf("MySQL missing schema-qualified external procedure CALL:\n%s", mySQL)
	}
	// 4. Loop label, pre-continue increment, ITERATE, and LEAVE
	if !strings.Contains(mySQL, "loop_1: WHILE v_idx < p_count DO") {
		t.Errorf("MySQL missing labeled WHILE loop:\n%s", mySQL)
	}
	if !strings.Contains(mySQL, "SET v_idx = v_idx + 1;\n\t\t\tITERATE loop_1;") {
		t.Errorf("MySQL continue did not increment loop counter before ITERATE:\n%s", mySQL)
	}
	// Switch break (case 99) must NOT emit LEAVE loop_1, while outer if break (kind < 0) MUST emit LEAVE loop_1
	if strings.Count(mySQL, "LEAVE loop_1;") != 1 {
		t.Errorf("MySQL expected exactly 1 LEAVE loop_1 (from outer break, not switch break). Got:\n%s", mySQL)
	}

	pgSQL, err := ToPostgres(procs[0], "abcd1234")
	if err != nil {
		t.Fatalf("ToPostgres failed: %v", err)
	}
	// 5. Postgres FUNCTION + RETURNS TABLE + RETURN QUERY + dialect normalization
	if !strings.Contains(pgSQL, "CREATE OR REPLACE FUNCTION\nsettlebatch_abcd1234(p_userID BIGINT, p_payload TEXT, p_stake NUMERIC, p_count BIGINT)\nRETURNS TABLE(FinalBalance NUMERIC, Processed BIGINT)") {
		t.Errorf("Postgres missing FUNCTION ... RETURNS TABLE header. Got:\n%s", pgSQL)
	}
	if !strings.Contains(pgSQL, "COALESCE(balance, 0)") || !strings.Contains(pgSQL, "(CASE WHEN balance > 0 THEN balance ELSE 0 END)") {
		t.Errorf("Postgres did not normalize IFNULL / IF(c,a,b) in raw SQL. Got:\n%s", pgSQL)
	}
	if !strings.Contains(pgSQL, "v_idx := v_idx + 1;\n\t\t\tCONTINUE loop_1;") {
		t.Errorf("Postgres continue did not increment loop counter before CONTINUE:\n%s", pgSQL)
	}
	if strings.Count(pgSQL, "EXIT loop_1;") != 1 {
		t.Errorf("Postgres expected exactly 1 EXIT loop_1. Got:\n%s", pgSQL)
	}
	if !strings.Contains(pgSQL, "RETURN QUERY SELECT v_FinalBalance AS FinalBalance, v_Processed AS Processed;") {
		t.Errorf("Postgres missing RETURN QUERY SELECT result projection. Got:\n%s", pgSQL)
	}
}

func TestExternalFunctionMappingAndZeroValueSemantics(t *testing.T) {
	src := `package testpkg

import (
	"context"
	"github.com/user/gormproc"
	"gorm.io/gorm"
)

type ExtSubProc struct {
	gormproc.BaseService
}

//gorm:func pokermem.CustomFeeFn
func (s *ExtSubProc) CustomFeeFn(cgID int64) float64 {
	return 0
}

//gorm:func casinodb.StandaloneCalcFn
func standaloneCalcFn(a float64, b float64) float64 {
	return 0
}

type Svc struct {
	gormproc.BaseService
	sub *ExtSubProc
}

//gorm:procedure
func (s *Svc) CheckRound(ctx context.Context, roundID int64, amount float64) error {
	var tx *gorm.DB
	var m_round_id int64
	var m_amount float64
	var m_flag bool
	var m_note string

	if err := gormproc.SelectInto(tx, "SELECT id, amount FROM rounds WHERE id = ?", roundID, &m_round_id, &m_amount); err != nil {
		return err
	}
	if m_round_id == 0 {
		return nil
	}

	fee := gormproc.CasinogameFeeCurrent(m_round_id)
	jp := gormproc.CasinogameCalcJackpotContribution(fee, m_amount)
	custom := s.sub.CustomFeeFn(m_round_id)
	ext := standaloneCalcFn(custom, jp)

	tx.Exec("UPDATE rounds SET jp = ? WHERE id = ?", ext, m_round_id)
	_ = m_flag
	_ = m_note
	return nil
}
`
	procs, err := parser.ParseSource(map[string]string{"proc.go": src})
	if err != nil {
		t.Fatalf("ParseSource failed: %v", err)
	}
	if len(procs) != 1 {
		t.Fatalf("Expected 1 procedure, got %d", len(procs))
	}

	mySQL, err := ToMySQL(procs[0], "h1")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	// 1. Exact DB function names
	if !strings.Contains(mySQL, "pokermem.CasinogameFeeCurrent(v_m_round_id)") {
		t.Errorf("MySQL expected pokermem.CasinogameFeeCurrent, got:\n%s", mySQL)
	}
	if !strings.Contains(mySQL, "casinodb.Casinogame_calcJackpotContribution(v_fee, v_m_amount)") {
		t.Errorf("MySQL expected casinodb.Casinogame_calcJackpotContribution, got:\n%s", mySQL)
	}
	if !strings.Contains(mySQL, "pokermem.CustomFeeFn(v_m_round_id)") {
		t.Errorf("MySQL expected //gorm:func pokermem.CustomFeeFn to preserve exact name, got:\n%s", mySQL)
	}
	if !strings.Contains(mySQL, "casinodb.StandaloneCalcFn(v_custom, v_jp)") {
		t.Errorf("MySQL expected //gorm:func casinodb.StandaloneCalcFn to preserve exact name, got:\n%s", mySQL)
	}
	// 2. Zero-value initialization
	for _, decl := range []string{
		"DECLARE v_m_round_id BIGINT DEFAULT 0;",
		"DECLARE v_m_amount DOUBLE DEFAULT 0;",
		"DECLARE v_m_flag BOOLEAN DEFAULT FALSE;",
		"DECLARE v_m_note TEXT DEFAULT '';",
	} {
		if !strings.Contains(mySQL, decl) {
			t.Errorf("MySQL missing zero-value declaration %q. Got:\n%s", decl, mySQL)
		}
	}

	pgSQL, err := ToPostgres(procs[0], "h1")
	if err != nil {
		t.Fatalf("ToPostgres failed: %v", err)
	}
	// 1. Exact DB function names
	if !strings.Contains(pgSQL, "pokermem.CasinogameFeeCurrent(v_m_round_id)") {
		t.Errorf("Postgres expected pokermem.CasinogameFeeCurrent, got:\n%s", pgSQL)
	}
	if !strings.Contains(pgSQL, "casinodb.Casinogame_calcJackpotContribution(v_fee, v_m_amount)") {
		t.Errorf("Postgres expected casinodb.Casinogame_calcJackpotContribution, got:\n%s", pgSQL)
	}
	if !strings.Contains(pgSQL, "pokermem.CustomFeeFn(v_m_round_id)") {
		t.Errorf("Postgres expected //gorm:func pokermem.CustomFeeFn to preserve exact name, got:\n%s", pgSQL)
	}
	if !strings.Contains(pgSQL, "casinodb.StandaloneCalcFn(v_custom, v_jp)") {
		t.Errorf("Postgres expected //gorm:func casinodb.StandaloneCalcFn to preserve exact name, got:\n%s", pgSQL)
	}
	// 2. Zero-value initialization
	for _, decl := range []string{
		"v_m_round_id BIGINT := 0;",
		"v_m_amount NUMERIC := 0;",
		"v_m_flag BOOLEAN := FALSE;",
		"v_m_note TEXT := '';",
	} {
		if !strings.Contains(pgSQL, decl) {
			t.Errorf("Postgres missing zero-value declaration %q. Got:\n%s", decl, pgSQL)
		}
	}
	// 3. Postgres SELECT ... INTO NOT FOUND guard preserving prior values
	for _, snippet := range []string{
		"_prev_0 v_m_round_id%TYPE := v_m_round_id;",
		"_prev_1 v_m_amount%TYPE := v_m_amount;",
		"SELECT id, amount INTO v_m_round_id, v_m_amount FROM rounds WHERE id = p_roundID;",
		"IF NOT FOUND THEN",
		"v_m_round_id := _prev_0;",
		"v_m_amount := _prev_1;",
	} {
		if !strings.Contains(pgSQL, snippet) {
			t.Errorf("Postgres missing SelectInto NOT FOUND preservation snippet %q. Got:\n%s", snippet, pgSQL)
		}
	}
}

func TestCommentsMultilineFormattingSignalAndMultiResultSet(t *testing.T) {
	src := `package testpkg

import (
	"context"
	"github.com/user/gormproc"
	"gorm.io/gorm"
)

type MultiRes struct {
	Balance float64
}

type SubSvc struct {
	gormproc.BaseService
}

//gorm:proc casinodb.BigSubProc
func (s *SubSvc) BigSubProc(a int64, b int64, c int64, d int64, e int64) error {
	return nil
}

type Svc2 struct {
	gormproc.BaseService
	db  *gorm.DB
	sub *SubSvc
}

// Procedure header comment
//gorm:procedure
func (s *Svc2) BookMulti(ctx context.Context, roundID int64, actionType int64, tags gormproc.JSON) (MultiRes, error) {
	var res MultiRes
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var bal float64 // player balance
		var foundID int64

		// Fetch round details
		if err := gormproc.SelectInto(tx, ` + "`" + `
			SELECT
				id,
				balance
			FROM rounds
			WHERE id = ?
		` + "`" + `, roundID, &foundID, &bal); err != nil {
			return err
		}

		if foundID == 0 {
			return gormproc.SignalError(10001, "round not found")
		}

		if gormproc.JSONOverlaps(tags, "[]") {
			switch actionType {
			// Bet action
			case 1:
				tx.Exec(` + "`" + `
					UPDATE rounds
					SET status = 1
					WHERE id = ?
				` + "`" + `, roundID)
			}
		}

		if err := s.sub.BigSubProc(roundID, actionType, 3, 4, 5); err != nil {
			return err
		}

		res.Balance = bal
		// Emit second result set
		s.db.Raw("SELECT id, status FROM trans WHERE round_id = ?", roundID).Scan(&foundID)
		return nil
	})
	return res, err
}
`
	procs, err := parser.ParseSource(map[string]string{"bookmulti.go": src})
	if err != nil {
		t.Fatalf("ParseSource failed: %v", err)
	}
	if len(procs) != 1 {
		t.Fatalf("Expected 1 procedure, got %d", len(procs))
	}

	mySQL, err := ToMySQL(procs[0], "m1")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	for _, want := range []string{
		"DECLARE EXIT HANDLER FOR SQLEXCEPTION BEGIN ROLLBACK AND NO CHAIN; RESIGNAL; END;",
		"DECLARE v_bal DOUBLE DEFAULT 0; /* player balance */",
		"-- Procedure header comment",
		"SET TRANSACTION ISOLATION LEVEL READ COMMITTED;",
		"START TRANSACTION;",
		"-- Fetch round details",
		"INTO v_foundID, v_bal\n\tFROM rounds\n\tWHERE id = p_roundID;",
		"SIGNAL SQLSTATE '45000' SET\n\t\t\tMYSQL_ERRNO = 10001,\n\t\t\tMESSAGE_TEXT = \"round not found\";",
		"-- Bet action\n\t\tWHEN 1 THEN",
		"CALL casinodb.BigSubProc(\n\t\tp_roundID,\n\t\tp_actionType,\n\t\t3,\n\t\t4,\n\t\t5\n\t);",
		"SELECT v_Balance AS Balance;",
		"SELECT id, status FROM trans WHERE round_id = p_roundID;",
		"COMMIT AND NO CHAIN;",
	} {
		if !strings.Contains(mySQL, want) {
			t.Errorf("MySQL missing expected snippet:\n%s\n\nGot:\n%s", want, mySQL)
		}
	}

	pgSQL, err := ToPostgres(procs[0], "m1")
	if err != nil {
		t.Fatalf("ToPostgres failed: %v", err)
	}
	for _, want := range []string{
		"v_bal NUMERIC := 0; /* player balance */",
		"-- Procedure header comment",
		"-- Fetch round details",
		"RAISE EXCEPTION 'round not found' USING ERRCODE = '45000';",
		"-- Bet action",
		"CALL casinodb.BigSubProc(",
		"PERFORM id, status FROM trans WHERE round_id = p_roundID;",
		"RETURN QUERY SELECT v_Balance AS Balance;",
	} {
		if !strings.Contains(pgSQL, want) {
			t.Errorf("Postgres missing expected snippet:\n%s\n\nGot:\n%s", want, pgSQL)
		}
	}
	if strings.Index(pgSQL, "RETURN QUERY SELECT v_Balance AS Balance;") > strings.Index(pgSQL, "PERFORM id, status FROM trans WHERE round_id = p_roundID;") {
		t.Errorf("Postgres expected RETURN QUERY SELECT before PERFORM. Got:\n%s", pgSQL)
	}
}

func TestPostgresRawQueryNormalizations(t *testing.T) {
	src := `package testpkg

import (
	"context"
	"github.com/user/gormproc"
	"gorm.io/gorm"
)

type NormSvc struct {
	gormproc.BaseService
	db *gorm.DB
}

//gorm:procedure
func (s *NormSvc) NormalizeProc(ctx context.Context, playerID int64, payload string) error {
	var tx *gorm.DB
	var accID int64
	var idsJSON string
	var cnt int64
	var extVal string

	gormproc.SelectInto(tx, "SELECT ACC_iID FROM pokermem.tbl_account USE INDEX(idx_ACC_PLY_ACP) WHERE ACC_PLY_iID = ?", playerID, &accID)
	gormproc.SelectInto(tx, "SELECT JSON_ARRAYAGG(id) FROM items WHERE player_id = ? ORDER BY id", playerID, &idsJSON)
	gormproc.SelectInto(tx, "SELECT CAST(JSON_EXTRACT(?, '$.count') AS UNSIGNED), JSON_LENGTH(?) FROM dual", payload, payload, &cnt, &extVal)
	return nil
}
`
	procs, err := parser.ParseSource(map[string]string{"norm.go": src})
	if err != nil {
		t.Fatalf("ParseSource failed: %v", err)
	}
	if len(procs) != 1 {
		t.Fatalf("Expected 1 procedure, got %d", len(procs))
	}

	mySQL, err := ToMySQL(procs[0], "n1")
	if err != nil {
		t.Fatalf("ToMySQL failed: %v", err)
	}
	if !strings.Contains(mySQL, "FROM pokermem.tbl_account USE INDEX(idx_ACC_PLY_ACP) WHERE") {
		t.Errorf("MySQL should preserve USE INDEX hint, got:\n%s", mySQL)
	}

	pgSQL, err := ToPostgres(procs[0], "n1")
	if err != nil {
		t.Fatalf("ToPostgres failed: %v", err)
	}
	for _, want := range []string{
		"FROM pokermem.tbl_account WHERE ACC_PLY_iID = p_playerID;",
		"SELECT jsonb_agg(id ORDER BY id)::text INTO v_idsJSON FROM items WHERE player_id = p_playerID;",
		"CAST((jsonb_path_query_first((p_payload)::jsonb, ('$.count')::jsonpath) #>> '{}') AS BIGINT)",
		"jsonb_array_length((p_payload)::jsonb)",
	} {
		if !strings.Contains(pgSQL, want) {
			t.Errorf("Postgres missing expected normalization %q.\nGot:\n%s", want, pgSQL)
		}
	}
	if strings.Contains(pgSQL, "USE INDEX") {
		t.Errorf("Postgres should strip USE INDEX hint, got:\n%s", pgSQL)
	}
}





