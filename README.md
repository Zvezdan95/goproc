# gormproc

A Go library, AST transpiler, and code generator that compiles a strictly validated subset of idiomatic Go functions into SQL stored procedures (PostgreSQL PL/pgSQL and MySQL SQL).

## Overview

Stored procedures offer execution speed and transactional isolation inside the database engine, but managing them in modern application stacks suffers from poor developer experience: raw SQL strings, lack of type safety, deployment sync issues, and brittle migration scripts. `gormproc` solves this by enabling developers to author stored procedures in idiomatic Go code while leveraging GORM for data access.

## Architecture & Core Concepts

### 1. The Polyfill Interceptor Pattern
Because Go executes in-memory and SQL executes in the database engine, there is an impedance mismatch for native database functions (like JSON manipulation, time, or math). `gormproc` solves this using Polyfills. 

You call `gormproc` methods in your Go code (which execute native Go logic in memory), but the transpiler intercepts these calls and replaces them with native SQL equivalents.

```go
// Go Code
count := gormproc.JSONLength(jsonStr, "$")
extId := gormproc.JSONExtractString(jsonStr, "$[0].id")

// Generated MySQL
SET v_count = JSON_LENGTH(p_jsonStr, "$");
SET v_extId = JSON_UNQUOTE(JSON_EXTRACT(p_jsonStr, '$[0].id'));
```

### 2. Dependency Injection (DI) Interceptor for Sub-Procedures
When your stored procedure needs to call another stored procedure, you don't write raw `tx.Exec("CALL...")`. Instead, you use standard Go Dependency Injection. 

If an injected service embeds `gormproc.BaseService`, the transpiler knows that calling its methods represents a cross-procedure boundary and automatically emits a `CALL` statement.

```go
type BetLimitService struct {
	gormproc.BaseService // Marks this as a stored procedure service
	db *gorm.DB
}

func (s *BookService) ProcessBook(ctx context.Context) error {
	// Transpiler intercepts this and generates: CALL casinogamebetlimitcheck();
	if err := s.betLimits.CasinogameBetlimitCheck(123); err != nil {
		return err
	}
	return nil
}
```

### 3. Querying and Scanning (Select Into)
To fetch data directly into local variables without allocating complex Go structs (which are difficult to map to SQL `INTO` statements), use `gormproc.SelectInto`:

```go
var balance float64
var accountId int64
gormproc.SelectInto(tx, "SELECT balance, id FROM accounts WHERE user_id = ? FOR UPDATE", userID, &balance, &accountId)

// Generated MySQL:
// SELECT balance, id INTO v_balance, v_accountId FROM accounts WHERE user_id = p_userID FOR UPDATE;
```

## Supported Go Subset

To guarantee 100% transpilability across PostgreSQL and MySQL, the static analyzer enforces strict bounds:

- **Allowed:**
  - Primitive type declarations (`int64`, `float64`, `string`, `bool`).
  - Standard control structures: `if`, `else if`, `else`, `for` loop ranges, `switch`.
  - Polyfill calls (from the `gormproc` package).
  - Dependency-injected sub-procedure calls.

- **Forbidden:**
  - Goroutines (`go func()`) and channels.
  - Heap allocations, pointer arithmetic, complex structs.
  - Network or file I/O operations.
