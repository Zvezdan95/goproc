# gormproc

A Go library, AST transpiler, and code generator that compiles a strictly validated subset of idiomatic Go functions into SQL stored procedures (PostgreSQL PL/pgSQL and MySQL SQL).

## Overview

Stored procedures offer execution speed and transactional isolation inside the database engine, but managing them in modern application stacks suffers from poor developer experience: raw SQL strings, lack of type safety, deployment sync issues, and brittle migration scripts. `gormproc` solves this by enabling developers to author stored procedures in idiomatic Go code while leveraging GORM for data access.

## Architecture & Core Concepts

### 1. Procedure Discovery & Recursive Helper Inlining
Only functions or methods explicitly annotated with `//gorm:procedure` are compiled into standalone SQL stored procedures (`CREATE PROCEDURE` in MySQL, `CREATE PROCEDURE` / `CREATE OR REPLACE FUNCTION ... RETURNS TABLE(...)` in PostgreSQL).

To keep your Go code modular and concise, you can freely extract shared logic into:
- **Standalone helper functions** (`func adjustBalance(tx *gorm.DB, ...) error`)
- **Unmarked helper structs and methods** (structs without `//gorm:procedure` or `gormproc.BaseService`)
- **Go interfaces** whose concrete implementations are statically resolvable

The transpiler recursively traces and inlines all helper function, method, and interface calls directly into the parent procedure's SQL body with hygienic local variable renaming—without creating extra stored procedures in the database.

```go
type LedgerRecorder interface {
	RecordTransfer(tx *gorm.DB, accountID int64, amount float64) error
}

type sqlLedgerRecorder struct{}

func (r *sqlLedgerRecorder) RecordTransfer(tx *gorm.DB, accountID int64, amount float64) error {
	return insertTransferRow(tx, accountID, amount)
}

func insertTransferRow(tx *gorm.DB, accountID int64, amount float64) error {
	return tx.Exec("INSERT INTO transfers (account_id, amount) VALUES (?, ?)", accountID, amount).Error
}

//gorm:procedure
func (s *BookService) ProcessRound(ctx context.Context, accountID int64, amount float64) error {
	var recorder LedgerRecorder = &sqlLedgerRecorder{}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Inlined directly as INSERT INTO transfers ... inside ProcessRound!
		return recorder.RecordTransfer(tx, accountID, amount)
	})
}
```

### 2. The Polyfill Interceptor Pattern
Because Go executes in-memory and SQL executes in the database engine, there is an impedance mismatch for native database functions (like JSON manipulation, timestamps, or last-insert IDs). `gormproc` solves this using Polyfills.

You call `gormproc` functions in your Go code (which execute native Go logic in memory), and the transpiler intercepts these calls and replaces them with native SQL equivalents:

```go
// Go Code
now := gormproc.Now()
count := gormproc.JSONLength(jsonStr, "$")
extId := gormproc.JSONExtractString(jsonStr, "$[0].id")

// Generated MySQL
SET v_now = NOW(3);
SET v_count = JSON_LENGTH(p_jsonStr, "$");
SET v_extId = JSON_UNQUOTE(JSON_EXTRACT(p_jsonStr, "$[0].id"));
```

### 3. SQL Type Aliases (`gormproc.JSON` & `gormproc.Decimal`)
Standard Go `string` and `float64` parameters map to `TEXT` and `DOUBLE` / `NUMERIC`. When you need native database `JSON` or fixed-point `DECIMAL(65,18)` types while keeping full Go `string` / `float64` compatibility in memory, use the built-in type aliases:

```go
//gorm:procedure
func (s *BookService) BookRound(
	ctx context.Context,
	roundID int64,
	actions gormproc.JSON,       // JSON in MySQL, TEXT in PostgreSQL
	exchangeRate gormproc.Decimal, // DECIMAL(65,18) in MySQL, NUMERIC in PostgreSQL
) error
```

### 4. Dependency Injection (DI) Interceptor for External Sub-Procedures
When your stored procedure needs to invoke another stored procedure in the database, use standard Go Dependency Injection.

If an injected service embeds `gormproc.BaseService`, calling its methods represents a cross-procedure boundary and automatically emits a `CALL` statement:
- Add `//gorm:proc <schema.ProcName>` above a stub method to preserve exact database schema qualification and casing.
- Pass pointer arguments (`*float64`, `*int64`, `*string`, `*bool`) for SQL `OUT` / `INOUT` parameters.

```go
type SubProcService struct {
	gormproc.BaseService // Marks methods as external stored procedure CALLs
	db *gorm.DB
}

//gorm:proc casinodb.Bonus_splitBet_Options
func (s *SubProcService) Bonus_splitBet_Options(
	balPlayer, balBonus, amount, rate float64,
	options int64,
	outPlayer, outBonus *float64,
) error {
	return nil
}

// Inside a //gorm:procedure method:
// Emits: CALL casinodb.Bonus_splitBet_Options(..., v_part_player, v_part_bonus);
if err := s.subProc.Bonus_splitBet_Options(bal, bonusBal, amt, rate, opts, &partPlayer, &partBonus); err != nil {
	return err
}
```

### 5. Querying and Scanning (`SelectInto`)
To fetch data directly into local variables without allocating complex Go structs, use `gormproc.SelectInto`:

```go
var balance float64
var accountId int64
gormproc.SelectInto(tx, "SELECT balance, id FROM accounts WHERE user_id = ? FOR UPDATE", userID, &balance, &accountId)

// Generated SQL:
// SELECT balance, id INTO v_balance, v_accountId FROM accounts WHERE user_id = p_userID FOR UPDATE;
```

## Supported Go Subset & Compile-Time Validation

To guarantee 100% transpilability across PostgreSQL and MySQL, the static analyzer enforces strict compile-time bounds across the entire call graph:

- **Allowed:**
  - Primitive types (`int64`, `float64`, `string`, `bool`) and `gormproc.JSON` / `gormproc.Decimal`.
  - Standard control structures: `if` / `else if` / `else`, `for` loops (including `continue` and `break` with automatic post-increment handling), and `switch` / `case`.
  - Compound assignments (`+=`, `-=`, `*=`, `/=`) and `++` / `--` statements.
  - Recursive calls to helper functions, unmarked struct methods, and statically bound Go interfaces.
  - Polyfill calls from the `gormproc` package and `gormproc.BaseService` sub-procedure calls.

- **Forbidden (Rejected at Compile Time with Call Chain Diagnostics):**
  - Untranspilable standard/external packages such as disk/filesystem I/O (`os.ReadFile`, `io`, `path/filepath`), network I/O (`net`, `net/http`), reflection, or OS/process calls—no matter how deeply nested behind helper functions or interfaces.
  - **Factory patterns:** functions or methods returning non-`error` interfaces, functions returning closures/`func` values, constructors returning struct pointers with methods, conditional runtime interface assignments, or ambiguous interfaces with multiple concrete implementations.
  - Recursive call cycles (`A -> B -> A`).
  - Goroutines (`go func()`), channels (`<-`), `defer`, `select`, `range`, slices, maps, and `make`.
