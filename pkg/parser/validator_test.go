package parser

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		wantErr bool
	}{
		{
			name: "valid procedure",
			code: `package test
//gorm:procedure
func ValidProc() {
	var a int64 = 1
	_ = a
}`,
			wantErr: false,
		},
		{
			name: "forbidden goroutine",
			code: `package test
//gorm:procedure
func InvalidGoroutine() {
	go func() {}()
}`,
			wantErr: true,
		},
		{
			name: "forbidden channel send",
			code: `package test
//gorm:procedure
func InvalidChanSend(ch chan int) {
	ch <- 1
}`,
			wantErr: true,
		},
		{
			name: "forbidden channel receive",
			code: `package test
//gorm:procedure
func InvalidChanRecv(ch chan int) {
	<-ch
}`,
			wantErr: true,
		},
		{
			name: "forbidden defer",
			code: `package test
//gorm:procedure
func InvalidDefer() {
	defer func() {}()
}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			node, err := parser.ParseFile(fset, "test.go", tt.code, parser.ParseComments)
			if err != nil {
				t.Fatalf("ParseFile failed: %v", err)
			}

			proc := &Procedure{
				Name: tt.name,
				Func: node.Decls[0].(*ast.FuncDecl),
			}

			err = Validate(proc)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRecursiveValidationAndFactoryDetection(t *testing.T) {
	tests := []struct {
		name          string
		src           string
		wantErr       bool
		wantErrSubstr string
		wantProcCount int
	}{
		{
			name: "valid_recursive_functions_unmarked_structs_and_deep_interfaces",
			src: `package testpkg
import (
	"context"
	"gorm.io/gorm"
)

type LeafAuditable interface {
	Audit(tx *gorm.DB, userID int64, delta float64) error
}

type sqlAuditor struct{}

func (a *sqlAuditor) Audit(tx *gorm.DB, userID int64, delta float64) error {
	return recordAuditRow(tx, userID, delta)
}

func recordAuditRow(tx *gorm.DB, userID int64, delta float64) error {
	return tx.Exec("INSERT INTO audit_log (user_id, delta) VALUES (?, ?)", userID, delta).Error
}

type LedgerPort interface {
	ApplyCredit(tx *gorm.DB, userID int64, amount float64) error
}

type ledgerAdapter struct {
	auditor LeafAuditable
}

func (l *ledgerAdapter) ApplyCredit(tx *gorm.DB, userID int64, amount float64) error {
	if err := tx.Exec("UPDATE users SET balance = balance + ? WHERE id = ?", amount, userID).Error; err != nil {
		return err
	}
	return l.auditor.Audit(tx, userID, amount)
}

type unmarkedHelperStruct struct{}

func (u *unmarkedHelperStruct) runStep(tx *gorm.DB, port LedgerPort, userID int64, amount float64) error {
	return port.ApplyCredit(tx, userID, amount)
}

type ProcService struct {
	db *gorm.DB
}

//gorm:procedure
func (s *ProcService) CreditUser(ctx context.Context, userID int64, amount float64) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var leaf LeafAuditable = &sqlAuditor{}
		var port LedgerPort = &ledgerAdapter{auditor: leaf}
		helper := unmarkedHelperStruct{}
		return helper.runStep(tx, port, userID, amount)
	})
}
`,
			wantErr:       false,
			wantProcCount: 1,
		},
		{
			name: "deep_interface_misdirection_hitting_disk_file_read",
			src: `package testpkg
import (
	"context"
	"os"
	"gorm.io/gorm"
)

type ConfigLoader interface {
	LoadLimit() (int64, error)
}

type diskConfigLoader struct{}

func (d *diskConfigLoader) LoadLimit() (int64, error) {
	return readFromDisk()
}

func readFromDisk() (int64, error) {
	data, err := os.ReadFile("/etc/limits.conf")
	_ = data
	return 100, err
}

type StepRunner interface {
	Step(tx *gorm.DB) error
}

type stepRunnerImpl struct {
	loader ConfigLoader
}

func (r *stepRunnerImpl) Step(tx *gorm.DB) error {
	lim, err := r.loader.LoadLimit()
	if err != nil {
		return err
	}
	return tx.Exec("UPDATE cfg SET lim = ?", lim).Error
}

type ProcService struct {
	db *gorm.DB
}

//gorm:procedure
func (s *ProcService) SyncLimits(ctx context.Context) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var runner StepRunner = &stepRunnerImpl{loader: &diskConfigLoader{}}
		return runner.Step(tx)
	})
}
`,
			wantErr:       true,
			wantErrSubstr: "os.ReadFile",
		},
		{
			name: "factory_function_returning_interface",
			src: `package testpkg
import (
	"context"
	"gorm.io/gorm"
)

type Biller interface {
	Bill(tx *gorm.DB, uid int64) error
}

type standardBiller struct{}
func (b *standardBiller) Bill(tx *gorm.DB, uid int64) error {
	return tx.Exec("UPDATE users SET billed = 1 WHERE id = ?", uid).Error
}

func NewBiller(kind int64) Biller {
	return &standardBiller{}
}

type ProcService struct {
	db *gorm.DB
}

//gorm:procedure
func (s *ProcService) RunBilling(ctx context.Context, uid int64, kind int64) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		b := NewBiller(kind)
		return b.Bill(tx, uid)
	})
}
`,
			wantErr:       true,
			wantErrSubstr: "factory pattern detected",
		},
		{
			name: "factory_method_returning_closure",
			src: `package testpkg
import (
	"context"
	"gorm.io/gorm"
)

type FeeFactory struct{}

func (f *FeeFactory) BuildCalc(rate float64) func(float64) float64 {
	return func(v float64) float64 { return v * rate }
}

type ProcService struct {
	db *gorm.DB
}

//gorm:procedure
func (s *ProcService) ApplyFee(ctx context.Context, amount float64) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		ff := FeeFactory{}
		fn := ff.BuildCalc(0.05)
		fee := fn(amount)
		return tx.Exec("INSERT INTO fees (amt) VALUES (?)", fee).Error
	})
}
`,
			wantErr:       true,
			wantErrSubstr: "factory",
		},
		{
			name: "factory_constructor_returning_struct_pointer",
			src: `package testpkg
import (
	"context"
	"gorm.io/gorm"
)

type Worker struct{}
func (w *Worker) Work(tx *gorm.DB) error {
	return tx.Exec("UPDATE jobs SET done = 1").Error
}

func NewWorker() *Worker {
	return &Worker{}
}

type ProcService struct {
	db *gorm.DB
}

//gorm:procedure
func (s *ProcService) DoJob(ctx context.Context) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		w := NewWorker()
		return w.Work(tx)
	})
}
`,
			wantErr:       true,
			wantErrSubstr: "factory pattern detected",
		},
		{
			name: "conditional_interface_factory_selection",
			src: `package testpkg
import (
	"context"
	"gorm.io/gorm"
)

type Strategy interface {
	Exec(tx *gorm.DB, id int64) error
}

type stratA struct{}
func (a *stratA) Exec(tx *gorm.DB, id int64) error {
	return tx.Exec("UPDATE t SET mode = 1 WHERE id = ?", id).Error
}

type stratB struct{}
func (b *stratB) Exec(tx *gorm.DB, id int64) error {
	return tx.Exec("UPDATE t SET mode = 2 WHERE id = ?", id).Error
}

type ProcService struct {
	db *gorm.DB
}

//gorm:procedure
func (s *ProcService) RunStrategy(ctx context.Context, id int64, flag bool) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var st Strategy
		if flag {
			st = &stratA{}
		} else {
			st = &stratB{}
		}
		return st.Exec(tx, id)
	})
}
`,
			wantErr:       true,
			wantErrSubstr: "factory/strategy pattern detected",
		},
		{
			name: "ambiguous_polymorphic_interface_field",
			src: `package testpkg
import (
	"context"
	"gorm.io/gorm"
)

type Notifier interface {
	Notify(tx *gorm.DB, id int64) error
}

type implOne struct{}
func (o *implOne) Notify(tx *gorm.DB, id int64) error {
	return tx.Exec("INSERT INTO n1 VALUES (?)", id).Error
}

type implTwo struct{}
func (t *implTwo) Notify(tx *gorm.DB, id int64) error {
	return tx.Exec("INSERT INTO n2 VALUES (?)", id).Error
}

type ProcService struct {
	db       *gorm.DB
	notifier Notifier
}

//gorm:procedure
func (s *ProcService) Alert(ctx context.Context, id int64) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		return s.notifier.Notify(tx, id)
	})
}
`,
			wantErr:       true,
			wantErrSubstr: "multiple implementations",
		},
		{
			name: "recursive_cycle_rejected",
			src: `package testpkg
import (
	"context"
	"gorm.io/gorm"
)

func ping(tx *gorm.DB, n int64) error {
	if n <= 0 {
		return nil
	}
	return pong(tx, n-1)
}

func pong(tx *gorm.DB, n int64) error {
	return ping(tx, n)
}

type ProcService struct {
	db *gorm.DB
}

//gorm:procedure
func (s *ProcService) CycleProc(ctx context.Context, n int64) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		return ping(tx, n)
	})
}
`,
			wantErr:       true,
			wantErrSubstr: "recursive call cycle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			procs, err := ParseSource(map[string]string{"proc.go": tt.src})
			if err != nil {
				t.Fatalf("ParseSource failed: %v", err)
			}
			if tt.wantProcCount > 0 && len(procs) != tt.wantProcCount {
				t.Fatalf("expected %d procedure(s), got %d", tt.wantProcCount, len(procs))
			}
			if len(procs) == 0 {
				t.Fatalf("expected at least 1 procedure, got 0")
			}

			valErr := Validate(procs[0])
			if (valErr != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", valErr, tt.wantErr)
			}
			if tt.wantErr && tt.wantErrSubstr != "" && !strings.Contains(valErr.Error(), tt.wantErrSubstr) {
				t.Errorf("expected error containing %q, got: %v", tt.wantErrSubstr, valErr)
			}
		})
	}
}
