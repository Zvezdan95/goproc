package parser

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"
)

type callKind int

const (
	callKindBuiltinOrSQL callKind = iota
	callKindTxBlock
	callKindSubProc
	callKindInline
)

type classifiedCall struct {
	kind         callKind
	target       *FuncInfo
	concreteRecv types.Type
	extProcName  string
}

type structValInfo struct {
	concreteType types.Type
	fieldExprs   map[string]ast.Expr
	fieldNested  map[string]*structValInfo
}

type expandCtx struct {
	fn             *FuncInfo
	callChain      []string
	callStack      map[*types.Func]bool
	concreteTypes  map[types.Object]types.Type
	varStructInfo  map[types.Object]*structValInfo
	concreteRecv   types.Type
	recvStructInfo *structValInfo
	prefix         string
	paramSub       map[types.Object]ast.Expr
	derefSub       map[types.Object]ast.Expr
	localRename    map[types.Object]string
	lhsTargets     []ast.Expr
	useRetDone     bool
	retDoneIdent   string
}

type inlineEngine struct {
	prog              *Program
	rootProc          *Procedure
	rootRecvNamed     *types.Named
	counter           int
	externalCallNames map[*ast.CallExpr]string
}

// ResolveAndInline validates the full transitive call graph of proc (including interface
// implementations, factory pattern rejection, and forbidden I/O rejection) and returns
// a new *ast.FuncDecl with all helper functions and unmarked struct/interface methods
// hygienically inlined into flat SQL-ready Go AST statements.
func ResolveAndInline(proc *Procedure) (*ast.FuncDecl, error) {
	if proc == nil || proc.Func == nil {
		return nil, nil
	}
	if proc.InlinedFunc != nil {
		return proc.InlinedFunc, nil
	}
	if proc.Program == nil || proc.Info == nil || proc.Obj == nil {
		return proc.Func, nil
	}
	if proc.ExternalCallNames == nil {
		proc.ExternalCallNames = make(map[*ast.CallExpr]string)
	}

	var rootRecvNamed *types.Named
	if proc.Type != nil && proc.Type.Recv() != nil {
		rootRecvNamed = unwrapNamed(proc.Type.Recv().Type())
	}

	rootInfo, ok := proc.Program.Funcs[proc.Obj]
	if !ok {
		rootInfo = &FuncInfo{
			Decl: proc.Func,
			Obj:  proc.Obj,
			Pkg:  proc.Pkg,
			Info: proc.Info,
			Fset: proc.Fset,
		}
	}

	eng := &inlineEngine{
		prog:              proc.Program,
		rootProc:          proc,
		rootRecvNamed:     rootRecvNamed,
		externalCallNames: proc.ExternalCallNames,
	}

	var rootConcreteRecv types.Type
	if proc.Type != nil && proc.Type.Recv() != nil {
		rootConcreteRecv = proc.Type.Recv().Type()
	}

	ctx := &expandCtx{
		fn:            rootInfo,
		callChain:     []string{proc.Name},
		callStack:     map[*types.Func]bool{proc.Obj: true},
		concreteTypes: make(map[types.Object]types.Type),
		varStructInfo: make(map[types.Object]*structValInfo),
		concreteRecv:  rootConcreteRecv,
		paramSub:      make(map[types.Object]ast.Expr),
		derefSub:      make(map[types.Object]ast.Expr),
		localRename:   make(map[types.Object]string),
	}

	if err := eng.validateFuncBodyConstructs(ctx, proc.Func.Body); err != nil {
		return nil, err
	}
	if err := eng.scanInterfaceAndHelperDecls(ctx, proc.Func.Body, 0); err != nil {
		return nil, err
	}

	newStmts, err := eng.expandStmtList(ctx, proc.Func.Body.List, true, true)
	if err != nil {
		return nil, err
	}

	clonedDecl := *proc.Func
	clonedDecl.Body = &ast.BlockStmt{
		Lbrace: proc.Func.Body.Lbrace,
		List:   newStmts,
		Rbrace: proc.Func.Body.Rbrace,
	}
	proc.InlinedFunc = &clonedDecl
	return proc.InlinedFunc, nil
}

func (e *inlineEngine) nextPrefix() string {
	e.counter++
	return fmt.Sprintf("inl%d_", e.counter)
}

func formatChain(chain []string) string {
	return strings.Join(chain, " -> ")
}

func unwrapNamed(t types.Type) *types.Named {
	if t == nil {
		return nil
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		return named
	}
	return nil
}

func embedsBaseService(t types.Type) bool {
	named := unwrapNamed(t)
	if named == nil {
		return false
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if !f.Embedded() {
			continue
		}
		fNamed := unwrapNamed(f.Type())
		if fNamed != nil && fNamed.Obj() != nil && fNamed.Obj().Name() == "BaseService" {
			if fNamed.Obj().Pkg() != nil && strings.HasSuffix(fNamed.Obj().Pkg().Path(), "gormproc") {
				return true
			}
		}
		if embedsBaseService(f.Type()) {
			return true
		}
	}
	return false
}

func isGormDBType(t types.Type) bool {
	named := unwrapNamed(t)
	if named == nil || named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Name() == "DB" && named.Obj().Pkg().Path() == "gorm.io/gorm"
}

func isContextType(t types.Type) bool {
	named := unwrapNamed(t)
	if named == nil || named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Name() == "Context" && named.Obj().Pkg().Path() == "context"
}

func isErrorType(t types.Type) bool {
	return t != nil && t.String() == "error"
}

func isNonErrorInterface(t types.Type) bool {
	if t == nil || isErrorType(t) {
		return false
	}
	_, ok := t.Underlying().(*types.Interface)
	return ok
}

func isHelperOrServiceStruct(t types.Type) bool {
	if t == nil || isGormDBType(t) || isContextType(t) {
		return false
	}
	named := unwrapNamed(t)
	if named == nil {
		return false
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	if named.NumMethods() > 0 || types.NewMethodSet(types.NewPointer(named)).Len() > 0 {
		return true
	}
	for i := 0; i < st.NumFields(); i++ {
		ft := st.Field(i).Type()
		if isGormDBType(ft) || isNonErrorInterface(ft) {
			return true
		}
		if _, isSig := ft.Underlying().(*types.Signature); isSig {
			return true
		}
	}
	return false
}

func isFactoryStructReturn(t types.Type) bool {
	if t == nil {
		return false
	}
	if ptr, ok := t.(*types.Pointer); ok {
		if _, isStruct := ptr.Elem().Underlying().(*types.Struct); isStruct {
			return true
		}
	}
	return isHelperOrServiceStruct(t)
}

func bodyContainsGormSQL(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			switch sel.Sel.Name {
			case "Exec", "Raw", "SelectInto", "Transaction":
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func (e *inlineEngine) validateFuncBodyConstructs(ctx *expandCtx, body *ast.BlockStmt) error {
	if body == nil {
		return nil
	}
	var firstErr error
	txFuncLits := make(map[*ast.FuncLit]bool)

	ast.Inspect(body, func(n ast.Node) bool {
		if firstErr != nil || n == nil {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Transaction" && len(call.Args) > 0 {
				if lit, ok := call.Args[0].(*ast.FuncLit); ok {
					txFuncLits[lit] = true
				}
			}
		}
		switch node := n.(type) {
		case *ast.GoStmt:
			firstErr = fmt.Errorf("goroutines (go stmt) are not allowed in gormproc (call chain: %s)", formatChain(ctx.callChain))
		case *ast.SendStmt:
			firstErr = fmt.Errorf("channel send operations are not allowed in gormproc (call chain: %s)", formatChain(ctx.callChain))
		case *ast.UnaryExpr:
			if node.Op == token.ARROW {
				firstErr = fmt.Errorf("channel receive operations are not allowed in gormproc (call chain: %s)", formatChain(ctx.callChain))
			}
		case *ast.DeferStmt:
			firstErr = fmt.Errorf("defer statements are not supported in gormproc (call chain: %s)", formatChain(ctx.callChain))
		case *ast.SelectStmt:
			firstErr = fmt.Errorf("select statements are not supported in gormproc (call chain: %s)", formatChain(ctx.callChain))
		case *ast.RangeStmt:
			firstErr = fmt.Errorf("range loops cannot be transpiled to SQL; use indexed for loops (call chain: %s)", formatChain(ctx.callChain))
		case *ast.FuncLit:
			if !txFuncLits[node] {
				firstErr = fmt.Errorf("closures / anonymous functions cannot be transpiled to SQL (call chain: %s)", formatChain(ctx.callChain))
			}
		case *ast.CompositeLit:
			if tv, ok := ctx.fn.Info.Types[node]; ok && tv.Type != nil {
				switch tv.Type.Underlying().(type) {
				case *types.Slice, *types.Map:
					firstErr = fmt.Errorf("slice/map literals (%s) cannot be transpiled to SQL (call chain: %s)", tv.Type.String(), formatChain(ctx.callChain))
				}
			}
		}
		return firstErr == nil
	})
	return firstErr
}

// scanInterfaceAndHelperDecls detects conditional/polymorphic interface assignments (inline factory pattern)
// and records concrete types for static helper struct / interface bindings.
func (e *inlineEngine) scanInterfaceAndHelperDecls(ctx *expandCtx, block *ast.BlockStmt, branchDepth int) error {
	if block == nil {
		return nil
	}
	assignedIfaces := make(map[types.Object]bool)

	var walk func(stmt ast.Stmt, depth int) error
	walk = func(stmt ast.Stmt, depth int) error {
		if stmt == nil {
			return nil
		}
		switch s := stmt.(type) {
		case *ast.BlockStmt:
			for _, sub := range s.List {
				if err := walk(sub, depth); err != nil {
					return err
				}
			}
		case *ast.DeclStmt:
			if gen, ok := s.Decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
				for _, spec := range gen.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vs.Names {
						obj := ctx.fn.Info.Defs[name]
						if obj == nil {
							continue
						}
						if isNonErrorInterface(obj.Type()) && i < len(vs.Values) {
							if depth > 0 {
								return fmt.Errorf("factory/strategy pattern detected on interface variable '%s' (call chain: %s): conditional runtime selection of interface implementations cannot be transpiled to SQL", name.Name, formatChain(ctx.callChain))
							}
							assignedIfaces[obj] = true
							if si, err := e.extractStructValInfo(ctx, vs.Values[i]); err != nil {
								return err
							} else if si != nil && si.concreteType != nil {
								ctx.concreteTypes[obj] = si.concreteType
								ctx.varStructInfo[obj] = si
							}
						} else if isHelperOrServiceStruct(obj.Type()) {
							ctx.concreteTypes[obj] = obj.Type()
							if i < len(vs.Values) {
								if si, err := e.extractStructValInfo(ctx, vs.Values[i]); err != nil {
									return err
								} else if si != nil {
									if si.concreteType == nil {
										si.concreteType = obj.Type()
									}
									ctx.varStructInfo[obj] = si
								}
							}
						}
					}
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range s.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name == "_" {
					continue
				}
				obj := ctx.fn.Info.Defs[id]
				if obj == nil {
					obj = ctx.fn.Info.Uses[id]
				}
				if obj == nil {
					continue
				}
				var rhs ast.Expr
				if i < len(s.Rhs) {
					rhs = s.Rhs[i]
				} else if len(s.Rhs) == 1 {
					rhs = s.Rhs[0]
				}
				if isNonErrorInterface(obj.Type()) {
					if depth > 0 || assignedIfaces[obj] {
						return fmt.Errorf("factory/strategy pattern detected on interface variable '%s' (call chain: %s): dynamic or conditional selection of interface implementations cannot be transpiled to SQL", id.Name, formatChain(ctx.callChain))
					}
					assignedIfaces[obj] = true
					if rhs != nil {
						si, err := e.extractStructValInfo(ctx, rhs)
						if err != nil {
							return err
						}
						if si == nil || si.concreteType == nil {
							return fmt.Errorf("factory/strategy pattern detected on interface variable '%s' (call chain: %s): cannot statically resolve concrete implementation", id.Name, formatChain(ctx.callChain))
						}
						ctx.concreteTypes[obj] = si.concreteType
						ctx.varStructInfo[obj] = si
					}
				} else if isHelperOrServiceStruct(obj.Type()) && rhs != nil {
					si, err := e.extractStructValInfo(ctx, rhs)
					if err != nil {
						return err
					}
					if si != nil && si.concreteType != nil {
						ctx.concreteTypes[obj] = si.concreteType
						ctx.varStructInfo[obj] = si
					} else {
						ctx.concreteTypes[obj] = obj.Type()
						if si != nil {
							si.concreteType = obj.Type()
							ctx.varStructInfo[obj] = si
						}
					}
				}
			}
		case *ast.IfStmt:
			if s.Init != nil {
				if err := walk(s.Init, depth+1); err != nil {
					return err
				}
			}
			if err := walk(s.Body, depth+1); err != nil {
				return err
			}
			if s.Else != nil {
				if err := walk(s.Else, depth+1); err != nil {
					return err
				}
			}
		case *ast.SwitchStmt:
			if err := walk(s.Body, depth+1); err != nil {
				return err
			}
		case *ast.CaseClause:
			for _, sub := range s.Body {
				if err := walk(sub, depth+1); err != nil {
					return err
				}
			}
		case *ast.ForStmt:
			if err := walk(s.Body, depth+1); err != nil {
				return err
			}
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Transaction" && len(call.Args) > 0 {
					if lit, ok := call.Args[0].(*ast.FuncLit); ok {
						if err := walk(lit.Body, depth); err != nil {
							return err
						}
					}
				}
			}
		case *ast.ReturnStmt:
			for _, r := range s.Results {
				if call, ok := r.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Transaction" && len(call.Args) > 0 {
						if lit, ok := call.Args[0].(*ast.FuncLit); ok {
							if err := walk(lit.Body, depth); err != nil {
								return err
							}
						}
					}
				}
			}
		}
		return nil
	}

	for _, st := range block.List {
		// Also check err := s.db.Transaction(...) assignments
		if assign, ok := st.(*ast.AssignStmt); ok && len(assign.Rhs) == 1 {
			if call, ok := assign.Rhs[0].(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Transaction" && len(call.Args) > 0 {
					if lit, ok := call.Args[0].(*ast.FuncLit); ok {
						if err := walk(lit.Body, branchDepth); err != nil {
							return err
						}
						continue
					}
				}
			}
		}
		if err := walk(st, branchDepth); err != nil {
			return err
		}
	}
	return nil
}

func (e *inlineEngine) extractStructValInfo(ctx *expandCtx, expr ast.Expr) (*structValInfo, error) {
	if expr == nil {
		return nil, nil
	}
	switch ex := expr.(type) {
	case *ast.ParenExpr:
		return e.extractStructValInfo(ctx, ex.X)
	case *ast.UnaryExpr:
		if ex.Op == token.AND {
			si, err := e.extractStructValInfo(ctx, ex.X)
			if err != nil || si == nil {
				return si, err
			}
			if si.concreteType != nil {
				copied := *si
				copied.concreteType = types.NewPointer(si.concreteType)
				return &copied, nil
			}
			return si, nil
		}
	case *ast.CompositeLit:
		tv, ok := ctx.fn.Info.Types[ex]
		if !ok || tv.Type == nil {
			return nil, nil
		}
		si := &structValInfo{
			concreteType: tv.Type,
			fieldExprs:   make(map[string]ast.Expr),
			fieldNested:  make(map[string]*structValInfo),
		}
		for _, elt := range ex.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if kId, ok := kv.Key.(*ast.Ident); ok {
					si.fieldExprs[kId.Name] = e.rewriteExprSimple(ctx, kv.Value)
					if nested, err := e.extractStructValInfo(ctx, kv.Value); err != nil {
						return nil, err
					} else if nested != nil {
						si.fieldNested[kId.Name] = nested
					}
				}
			}
		}
		return si, nil
	case *ast.Ident:
		obj := ctx.fn.Info.Uses[ex]
		if obj == nil {
			return nil, nil
		}
		if ctx.fn.Decl.Recv != nil && len(ctx.fn.Decl.Recv.List) > 0 && len(ctx.fn.Decl.Recv.List[0].Names) > 0 {
			if ctx.fn.Info.Defs[ctx.fn.Decl.Recv.List[0].Names[0]] == obj && ctx.recvStructInfo != nil {
				return ctx.recvStructInfo, nil
			}
		}
		if si, ok := ctx.varStructInfo[obj]; ok {
			return si, nil
		}
		if ct, ok := ctx.concreteTypes[obj]; ok {
			return &structValInfo{concreteType: ct}, nil
		}
		if !isNonErrorInterface(obj.Type()) {
			return &structValInfo{concreteType: obj.Type()}, nil
		}
	case *ast.SelectorExpr:
		baseInfo, err := e.extractStructValInfo(ctx, ex.X)
		if err != nil {
			return nil, err
		}
		if baseInfo != nil && baseInfo.fieldNested != nil {
			if nested, ok := baseInfo.fieldNested[ex.Sel.Name]; ok {
				return nested, nil
			}
		}
		if baseInfo != nil && baseInfo.concreteType != nil {
			fObj, _, _ := types.LookupFieldOrMethod(baseInfo.concreteType, true, e.rootProc.Obj.Pkg(), ex.Sel.Name)
			if fVar, ok := fObj.(*types.Var); ok && !isNonErrorInterface(fVar.Type()) {
				return &structValInfo{concreteType: fVar.Type()}, nil
			}
		}
	case *ast.CallExpr:
		// If RHS is a function/method call, classifyCall will catch factory patterns!
		_, err := e.classifyCall(ctx, ex)
		if err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (e *inlineEngine) resolveExprConcreteType(ctx *expandCtx, expr ast.Expr) types.Type {
	if expr == nil {
		return nil
	}
	if si, err := e.extractStructValInfo(ctx, expr); err == nil && si != nil && si.concreteType != nil {
		return si.concreteType
	}
	switch ex := expr.(type) {
	case *ast.ParenExpr:
		return e.resolveExprConcreteType(ctx, ex.X)
	case *ast.UnaryExpr:
		if ex.Op == token.AND {
			inner := e.resolveExprConcreteType(ctx, ex.X)
			if inner != nil {
				return types.NewPointer(inner)
			}
		}
	case *ast.CompositeLit:
		if tv, ok := ctx.fn.Info.Types[ex]; ok && tv.Type != nil {
			return tv.Type
		}
	case *ast.Ident:
		obj := ctx.fn.Info.Uses[ex]
		if obj == nil {
			return nil
		}
		if ctx.fn.Decl.Recv != nil && len(ctx.fn.Decl.Recv.List) > 0 && len(ctx.fn.Decl.Recv.List[0].Names) > 0 {
			recvIdent := ctx.fn.Decl.Recv.List[0].Names[0]
			if ctx.fn.Info.Defs[recvIdent] == obj && ctx.concreteRecv != nil {
				return ctx.concreteRecv
			}
		}
		if ct, ok := ctx.concreteTypes[obj]; ok {
			return ct
		}
		if !isNonErrorInterface(obj.Type()) {
			return obj.Type()
		}
	case *ast.SelectorExpr:
		baseType := e.resolveExprConcreteType(ctx, ex.X)
		if baseType != nil {
			fObj, _, _ := types.LookupFieldOrMethod(baseType, true, e.rootProc.Obj.Pkg(), ex.Sel.Name)
			if fVar, ok := fObj.(*types.Var); ok {
				if !isNonErrorInterface(fVar.Type()) {
					return fVar.Type()
				}
			}
		}
		if sel, ok := ctx.fn.Info.Selections[ex]; ok {
			if !isNonErrorInterface(sel.Type()) {
				return sel.Type()
			}
		}
	}
	return nil
}

func (e *inlineEngine) findInterfaceImplementations(ifaceType types.Type) []*types.Named {
	iface, ok := ifaceType.Underlying().(*types.Interface)
	if !ok {
		return nil
	}
	var matches []*types.Named
	for _, named := range e.prog.NamedTypes {
		if _, isIface := named.Underlying().(*types.Interface); isIface {
			continue
		}
		if types.Implements(named, iface) || types.Implements(types.NewPointer(named), iface) {
			matches = append(matches, named)
		}
	}
	return matches
}

func (e *inlineEngine) classifyCall(ctx *expandCtx, call *ast.CallExpr) (*classifiedCall, error) {
	info := ctx.fn.Info

	// 1. Type conversion or Built-in
	if tv, ok := info.Types[call.Fun]; ok {
		if tv.IsType() {
			return &classifiedCall{kind: callKindBuiltinOrSQL}, nil
		}
		if tv.IsBuiltin() {
			if id, ok := call.Fun.(*ast.Ident); ok {
				switch id.Name {
				case "len":
					return &classifiedCall{kind: callKindBuiltinOrSQL}, nil
				case "new":
					return nil, fmt.Errorf("factory/heap allocation 'new(...)' cannot be transpiled to SQL (call chain: %s)", formatChain(ctx.callChain))
				default:
					return nil, fmt.Errorf("builtin '%s' cannot be transpiled to SQL (call chain: %s)", id.Name, formatChain(ctx.callChain))
				}
			}
		}
	}

	// 2. Locate target types.Object
	var targetObj types.Object
	var recvExpr ast.Expr
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		targetObj = info.Uses[fun]
	case *ast.SelectorExpr:
		if sel, ok := info.Selections[fun]; ok {
			targetObj = sel.Obj()
			recvExpr = fun.X
		} else {
			targetObj = info.Uses[fun.Sel]
		}
	default:
		return nil, fmt.Errorf("dynamic function call expression cannot be transpiled to SQL (call chain: %s)", formatChain(ctx.callChain))
	}

	if targetObj == nil {
		return nil, fmt.Errorf("unresolved call expression in procedure (call chain: %s)", formatChain(ctx.callChain))
	}

	// Dynamic closure / function-typed variable or struct field
	fnObj, isFunc := targetObj.(*types.Func)
	if !isFunc {
		return nil, fmt.Errorf("factory/strategy pattern detected: dynamic function/closure call '%s' cannot be transpiled to SQL (call chain: %s)", targetObj.Name(), formatChain(ctx.callChain))
	}

	sig, ok := fnObj.Type().(*types.Signature)
	if !ok {
		return nil, fmt.Errorf("invalid function signature on '%s' (call chain: %s)", fnObj.Name(), formatChain(ctx.callChain))
	}

	// 3. GORM *gorm.DB methods
	if sig.Recv() != nil && isGormDBType(sig.Recv().Type()) {
		switch fnObj.Name() {
		case "Transaction":
			return &classifiedCall{kind: callKindTxBlock}, nil
		case "Exec", "Raw", "Scan", "Row", "Rows", "WithContext":
			return &classifiedCall{kind: callKindBuiltinOrSQL}, nil
		default:
			return nil, fmt.Errorf("unsupported *gorm.DB method '%s' in SQL procedure (call chain: %s); use tx.Exec, tx.Raw, or gormproc.SelectInto", fnObj.Name(), formatChain(ctx.callChain))
		}
	}

	pkgPath := ""
	if fnObj.Pkg() != nil {
		pkgPath = fnObj.Pkg().Path()
	}

	// 4. gormproc polyfills & allowed SQL-mapped stdlib helpers
	if pkgPath == "github.com/user/gormproc" || strings.HasSuffix(pkgPath, "/gormproc") {
		return &classifiedCall{kind: callKindBuiltinOrSQL}, nil
	}
	if pkgPath == "fmt" && (fnObj.Name() == "Sprintf" || fnObj.Name() == "Errorf") {
		return &classifiedCall{kind: callKindBuiltinOrSQL}, nil
	}
	if pkgPath == "errors" && fnObj.Name() == "New" {
		return &classifiedCall{kind: callKindBuiltinOrSQL}, nil
	}
	if pkgPath == "math" && fnObj.Name() == "Abs" {
		return &classifiedCall{kind: callKindBuiltinOrSQL}, nil
	}

	// 5. Explicit diagnostics for forbidden stdlib / I/O / runtime packages
	if pkgPath == "os" || pkgPath == "io" || strings.HasPrefix(pkgPath, "io/") ||
		pkgPath == "bufio" || pkgPath == "path" || strings.HasPrefix(pkgPath, "path/") ||
		strings.HasPrefix(pkgPath, "archive/") || strings.HasPrefix(pkgPath, "compress/") {
		return nil, fmt.Errorf("cannot transpile call to %s.%s (call chain: %s): disk/file I/O operations cannot be compiled to SQL", pkgPath, fnObj.Name(), formatChain(ctx.callChain))
	}
	if pkgPath == "net" || strings.HasPrefix(pkgPath, "net/") || strings.HasPrefix(pkgPath, "crypto/tls") {
		return nil, fmt.Errorf("cannot transpile call to %s.%s (call chain: %s): network operations cannot be compiled to SQL", pkgPath, fnObj.Name(), formatChain(ctx.callChain))
	}
	if pkgPath == "os/exec" || pkgPath == "syscall" || pkgPath == "unsafe" ||
		pkgPath == "reflect" || pkgPath == "runtime" || pkgPath == "sync" ||
		strings.HasPrefix(pkgPath, "sync/") || pkgPath == "plugin" ||
		(pkgPath == "time" && fnObj.Name() == "Sleep") {
		return nil, fmt.Errorf("cannot transpile call to %s.%s (call chain: %s): system/runtime operation cannot be compiled to SQL", pkgPath, fnObj.Name(), formatChain(ctx.callChain))
	}

	// 6. Factory Pattern Detection on Function/Method Return Signature
	if sig.Results() != nil {
		for i := 0; i < sig.Results().Len(); i++ {
			rt := sig.Results().At(i).Type()
			if isErrorType(rt) {
				continue
			}
			if _, isIface := rt.Underlying().(*types.Interface); isIface {
				return nil, fmt.Errorf("factory pattern detected in call to %s (call chain: %s): returning interface type '%s' cannot be transpiled to SQL", fnObj.Name(), formatChain(ctx.callChain), rt.String())
			}
			if _, isSig := rt.Underlying().(*types.Signature); isSig {
				return nil, fmt.Errorf("factory pattern detected in call to %s (call chain: %s): returning function/closure '%s' cannot be transpiled to SQL", fnObj.Name(), formatChain(ctx.callChain), rt.String())
			}
			if isFactoryStructReturn(rt) {
				return nil, fmt.Errorf("factory pattern detected in call to %s (call chain: %s): constructing/returning struct instance '%s' cannot be transpiled to SQL", fnObj.Name(), formatChain(ctx.callChain), rt.String())
			}
		}
	}

	// 7. Interface Method Dispatch Resolution
	if sig.Recv() != nil && types.IsInterface(sig.Recv().Type()) {
		ifaceType := sig.Recv().Type()
		concreteRecv := e.resolveExprConcreteType(ctx, recvExpr)
		if concreteRecv == nil {
			impls := e.findInterfaceImplementations(ifaceType)
			if len(impls) == 0 {
				return nil, fmt.Errorf("cannot transpile interface call %s on '%s' (call chain: %s): no concrete implementation found", fnObj.Name(), ifaceType.String(), formatChain(ctx.callChain))
			}
			if len(impls) > 1 {
				var names []string
				for _, im := range impls {
					names = append(names, im.Obj().Name())
				}
				return nil, fmt.Errorf("factory/polymorphic dispatch detected on interface '%s' method %s (call chain: %s): multiple implementations (%s) cannot be compiled to static SQL", ifaceType.String(), fnObj.Name(), formatChain(ctx.callChain), strings.Join(names, ", "))
			}
			concreteRecv = types.NewPointer(impls[0])
		}

		mObj, _, _ := types.LookupFieldOrMethod(concreteRecv, true, e.rootProc.Obj.Pkg(), fnObj.Name())
		concreteFn, ok := mObj.(*types.Func)
		if !ok || concreteFn == nil {
			return nil, fmt.Errorf("cannot resolve method %s on concrete type '%s' (call chain: %s)", fnObj.Name(), concreteRecv.String(), formatChain(ctx.callChain))
		}
		fnObj = concreteFn
		sig = concreteFn.Type().(*types.Signature)

		targetInfo, exists := e.prog.Funcs[concreteFn]
		if !exists || targetInfo.Decl == nil || targetInfo.Decl.Body == nil {
			return nil, fmt.Errorf("cannot transpile method %s on '%s' (call chain: %s): implementation source unavailable for SQL compilation", concreteFn.Name(), concreteRecv.String(), formatChain(ctx.callChain))
		}
		if ctx.callStack[concreteFn] {
			return nil, fmt.Errorf("recursive call cycle detected (%s -> %s): recursive functions cannot be inlined into SQL", formatChain(ctx.callChain), concreteFn.Name())
		}
		return &classifiedCall{
			kind:         callKindInline,
			target:       targetInfo,
			concreteRecv: concreteRecv,
		}, nil
	}

	// 8. Concrete Function or Method
	targetInfo, exists := e.prog.Funcs[fnObj]
	if !exists || targetInfo.Decl == nil {
		return nil, fmt.Errorf("cannot transpile call to %s.%s (call chain: %s): function has no SQL mapping and cannot be inlined", pkgPath, fnObj.Name(), formatChain(ctx.callChain))
	}

	// Check if target is a standalone //gorm:procedure or an external BaseService procedure stub
	if HasGormProcedureComment(targetInfo.Decl.Doc) {
		return &classifiedCall{
			kind:        callKindSubProc,
			target:      targetInfo,
			extProcName: ExtractExternalProcName(targetInfo.Decl.Doc),
		}, nil
	}

	if sig.Recv() != nil && embedsBaseService(sig.Recv().Type()) {
		recvNamed := unwrapNamed(sig.Recv().Type())
		extName := ExtractExternalProcName(targetInfo.Decl.Doc)
		if extName != "" || (recvNamed != e.rootRecvNamed && !bodyContainsGormSQL(targetInfo.Decl.Body)) {
			return &classifiedCall{
				kind:        callKindSubProc,
				target:      targetInfo,
				extProcName: extName,
			}, nil
		}
	}

	if targetInfo.Decl.Body == nil {
		return nil, fmt.Errorf("cannot transpile call to %s (call chain: %s): function has no body", fnObj.Name(), formatChain(ctx.callChain))
	}
	if ctx.callStack[fnObj] {
		return nil, fmt.Errorf("recursive call cycle detected (%s -> %s): recursive functions cannot be inlined into SQL", formatChain(ctx.callChain), fnObj.Name())
	}

	var concreteRecv types.Type
	if sig.Recv() != nil {
		concreteRecv = sig.Recv().Type()
	}
	return &classifiedCall{
		kind:         callKindInline,
		target:       targetInfo,
		concreteRecv: concreteRecv,
	}, nil
}

func typeToASTExpr(t types.Type) ast.Expr {
	if t == nil {
		return &ast.Ident{Name: "string"}
	}
	if ptr, ok := t.(*types.Pointer); ok {
		return &ast.StarExpr{X: typeToASTExpr(ptr.Elem())}
	}
	if named, ok := t.(*types.Named); ok {
		if basic, isBasic := named.Underlying().(*types.Basic); isBasic {
			return basicToIdent(basic)
		}
	}
	if basic, ok := t.Underlying().(*types.Basic); ok {
		return basicToIdent(basic)
	}
	return &ast.Ident{Name: "string"}
}

func basicToIdent(b *types.Basic) *ast.Ident {
	switch b.Kind() {
	case types.Bool, types.UntypedBool:
		return &ast.Ident{Name: "bool"}
	case types.Int, types.UntypedInt:
		return &ast.Ident{Name: "int64"}
	case types.Int8:
		return &ast.Ident{Name: "int8"}
	case types.Int16:
		return &ast.Ident{Name: "int16"}
	case types.Int32, types.UntypedRune:
		return &ast.Ident{Name: "int32"}
	case types.Int64:
		return &ast.Ident{Name: "int64"}
	case types.Uint:
		return &ast.Ident{Name: "uint64"}
	case types.Uint8:
		return &ast.Ident{Name: "uint8"}
	case types.Uint16:
		return &ast.Ident{Name: "uint16"}
	case types.Uint32:
		return &ast.Ident{Name: "uint32"}
	case types.Uint64:
		return &ast.Ident{Name: "uint64"}
	case types.Float32:
		return &ast.Ident{Name: "float32"}
	case types.Float64, types.UntypedFloat:
		return &ast.Ident{Name: "float64"}
	default:
		return &ast.Ident{Name: "string"}
	}
}

func constToASTExpr(c *types.Const) ast.Expr {
	if c == nil || c.Val() == nil {
		return nil
	}
	val := c.Val()
	switch val.Kind() {
	case constant.Bool:
		if constant.BoolVal(val) {
			return &ast.Ident{Name: "true"}
		}
		return &ast.Ident{Name: "false"}
	case constant.String:
		return &ast.BasicLit{Kind: token.STRING, Value: fmt.Sprintf("%q", constant.StringVal(val))}
	case constant.Int:
		s := val.ExactString()
		if strings.HasPrefix(s, "-") {
			return &ast.UnaryExpr{
				Op: token.SUB,
				X:  &ast.BasicLit{Kind: token.INT, Value: s[1:]},
			}
		}
		return &ast.BasicLit{Kind: token.INT, Value: s}
	case constant.Float:
		f, _ := constant.Float64Val(val)
		s := fmt.Sprintf("%g", f)
		if strings.HasPrefix(s, "-") {
			return &ast.UnaryExpr{
				Op: token.SUB,
				X:  &ast.BasicLit{Kind: token.FLOAT, Value: s[1:]},
			}
		}
		return &ast.BasicLit{Kind: token.FLOAT, Value: s}
	}
	return nil
}

func makeVarDeclStmt(name string, typExpr ast.Expr, initVal ast.Expr) ast.Stmt {
	vs := &ast.ValueSpec{
		Names: []*ast.Ident{{Name: name}},
		Type:  typExpr,
	}
	if initVal != nil {
		vs.Values = []ast.Expr{initVal}
	}
	return &ast.DeclStmt{
		Decl: &ast.GenDecl{
			Tok:   token.VAR,
			Specs: []ast.Spec{vs},
		},
	}
}

func isParamMutatedInBody(info *types.Info, paramObj types.Object, body *ast.BlockStmt) bool {
	mutated := false
	ast.Inspect(body, func(n ast.Node) bool {
		if mutated || n == nil {
			return false
		}
		switch st := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range st.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && info.Uses[id] == paramObj {
					mutated = true
					return false
				}
			}
		case *ast.IncDecStmt:
			if id, ok := st.X.(*ast.Ident); ok && info.Uses[id] == paramObj {
				mutated = true
				return false
			}
		case *ast.UnaryExpr:
			if st.Op == token.AND {
				if id, ok := st.X.(*ast.Ident); ok && info.Uses[id] == paramObj {
					mutated = true
					return false
				}
			}
		}
		return true
	})
	return mutated
}

func isSimpleImmutableArg(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident, *ast.BasicLit:
		return true
	case *ast.UnaryExpr:
		if e.Op == token.SUB || e.Op == token.AND {
			return isSimpleImmutableArg(e.X)
		}
	}
	return false
}

func hasNonTailValueReturn(stmts []ast.Stmt) bool {
	effectiveLen := len(stmts)
	if effectiveLen > 1 && isNoOpReturn(stmts[effectiveLen-1]) {
		effectiveLen--
	}
	for i := 0; i < effectiveLen; i++ {
		s := stmts[i]
		isLast := i == effectiveLen-1
		if !isLast && containsNonErrorReturn(s) {
			return true
		}
		if isLast {
			switch st := s.(type) {
			case *ast.ForStmt, *ast.SwitchStmt:
				if containsNonErrorReturn(st) {
					return true
				}
			case *ast.IfStmt:
				if hasNonTailValueReturn(st.Body.List) {
					return true
				}
				if st.Else != nil {
					switch eb := st.Else.(type) {
					case *ast.BlockStmt:
						if hasNonTailValueReturn(eb.List) {
							return true
						}
					case *ast.IfStmt:
						if hasNonTailValueReturn([]ast.Stmt{eb}) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func isNoOpReturn(stmt ast.Stmt) bool {
	ret, ok := stmt.(*ast.ReturnStmt)
	if !ok {
		return false
	}
	if len(ret.Results) == 0 {
		return true
	}
	if len(ret.Results) == 1 {
		if id, ok := ret.Results[0].(*ast.Ident); ok && id.Name == "nil" {
			return true
		}
	}
	return false
}

func containsNonErrorReturn(stmt ast.Stmt) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		if !isErrorPropagationReturn(ret) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isErrorPropagationReturn(ret *ast.ReturnStmt) bool {
	if len(ret.Results) == 0 {
		return false
	}
	last := ret.Results[len(ret.Results)-1]
	if id, ok := last.(*ast.Ident); ok && id.Name == "err" {
		return true
	}
	if call, ok := last.(*ast.CallExpr); ok {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "New" || sel.Sel.Name == "Errorf") {
			return true
		}
	}
	return false
}

func isBoilerplateErrIf(ifStmt *ast.IfStmt) bool {
	if ifStmt.Else != nil {
		return false
	}
	bin, ok := ifStmt.Cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	isErr := false
	if id, ok := bin.X.(*ast.Ident); ok && id.Name == "err" {
		if idY, ok := bin.Y.(*ast.Ident); ok && idY.Name == "nil" {
			isErr = true
		}
	} else if id, ok := bin.Y.(*ast.Ident); ok && id.Name == "err" {
		if idX, ok := bin.X.(*ast.Ident); ok && idX.Name == "nil" {
			isErr = true
		}
	}
	if !isErr {
		return false
	}
	if len(ifStmt.Body.List) != 1 {
		return false
	}
	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) == 0 {
		return false
	}
	if id, ok := ret.Results[len(ret.Results)-1].(*ast.Ident); ok && id.Name == "err" {
		return true
	}
	return false
}

// inlineCall expands a classified callKindInline call into flat statements in the caller's coordinate space.
func (e *inlineEngine) inlineCall(callerCtx *expandCtx, call *ast.CallExpr, cc *classifiedCall, lhsTargets []ast.Expr) ([]ast.Stmt, error) {
	callee := cc.target
	prefix := e.nextPrefix()

	newStack := make(map[*types.Func]bool, len(callerCtx.callStack)+1)
	for k, v := range callerCtx.callStack {
		newStack[k] = v
	}
	newStack[callee.Obj] = true

	var recvStructInfo *structValInfo
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if si, err := e.extractStructValInfo(callerCtx, sel.X); err == nil && si != nil {
			recvStructInfo = si
		}
	}

	calleeCtx := &expandCtx{
		fn:             callee,
		callChain:      append(append([]string{}, callerCtx.callChain...), callee.Obj.Name()),
		callStack:      newStack,
		concreteTypes:  make(map[types.Object]types.Type),
		varStructInfo:  make(map[types.Object]*structValInfo),
		concreteRecv:   cc.concreteRecv,
		recvStructInfo: recvStructInfo,
		prefix:         prefix,
		paramSub:       make(map[types.Object]ast.Expr),
		derefSub:       make(map[types.Object]ast.Expr),
		localRename:    make(map[types.Object]string),
		lhsTargets:     lhsTargets,
	}

	var preStmts []ast.Stmt

	// Bind parameters to evaluated/rewritten arguments
	var paramVars []*types.Var
	if callee.Decl.Type.Params != nil {
		for _, field := range callee.Decl.Type.Params.List {
			for _, name := range field.Names {
				if v, ok := callee.Info.Defs[name].(*types.Var); ok {
					paramVars = append(paramVars, v)
				}
			}
		}
	}

	for idx, pVar := range paramVars {
		if idx >= len(call.Args) {
			break
		}
		rawArg := call.Args[idx]

		// Propagate concrete types and field bindings for interface/helper-struct arguments
		if isNonErrorInterface(pVar.Type()) || isHelperOrServiceStruct(pVar.Type()) {
			if si, err := e.extractStructValInfo(callerCtx, rawArg); err == nil && si != nil {
				calleeCtx.varStructInfo[pVar] = si
				if si.concreteType != nil {
					calleeCtx.concreteTypes[pVar] = si.concreteType
				}
			} else if ct := e.resolveExprConcreteType(callerCtx, rawArg); ct != nil {
				calleeCtx.concreteTypes[pVar] = ct
			}
			continue
		}

		// Lift any nested inlinable calls inside rawArg first
		rewrittenArg, argLiftStmts, err := e.rewriteAndLiftExpr(callerCtx, rawArg)
		if err != nil {
			return nil, err
		}
		preStmts = append(preStmts, argLiftStmts...)

		// Context and *gorm.DB parameters substitute directly
		if isContextType(pVar.Type()) || isGormDBType(pVar.Type()) {
			calleeCtx.paramSub[pVar] = rewrittenArg
			continue
		}

		// Pointer parameter (e.g. out *float64 passed &targetVar)
		if _, isPtr := pVar.Type().(*types.Pointer); isPtr {
			if unary, ok := rewrittenArg.(*ast.UnaryExpr); ok && unary.Op == token.AND {
				calleeCtx.derefSub[pVar] = unary.X
				calleeCtx.paramSub[pVar] = rewrittenArg
				continue
			}
			calleeCtx.derefSub[pVar] = rewrittenArg
			calleeCtx.paramSub[pVar] = &ast.UnaryExpr{Op: token.AND, X: rewrittenArg}
			continue
		}

		// Regular scalar parameter
		if !isParamMutatedInBody(callee.Info, pVar, callee.Decl.Body) && isSimpleImmutableArg(rewrittenArg) {
			calleeCtx.paramSub[pVar] = rewrittenArg
		} else {
			tmpName := prefix + pVar.Name()
			calleeCtx.localRename[pVar] = tmpName
			preStmts = append(preStmts,
				makeVarDeclStmt(tmpName, typeToASTExpr(pVar.Type()), nil),
				&ast.AssignStmt{
					Lhs: []ast.Expr{&ast.Ident{Name: tmpName}},
					Tok: token.ASSIGN,
					Rhs: []ast.Expr{rewrittenArg},
				},
			)
		}
	}

	// Pre-register local variables declared inside callee for hygienic renaming
	ast.Inspect(callee.Decl.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ValueSpec:
			for _, name := range node.Names {
				if name.Name == "_" || name.Name == "err" {
					continue
				}
				if obj := callee.Info.Defs[name]; obj != nil {
					if !isNonErrorInterface(obj.Type()) && !isHelperOrServiceStruct(obj.Type()) {
						calleeCtx.localRename[obj] = prefix + name.Name
					}
				}
			}
		case *ast.AssignStmt:
			if node.Tok == token.DEFINE {
				for _, lhs := range node.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" && id.Name != "err" {
						if obj := callee.Info.Defs[id]; obj != nil {
							if !isNonErrorInterface(obj.Type()) && !isHelperOrServiceStruct(obj.Type()) {
								calleeCtx.localRename[obj] = prefix + id.Name
							}
						}
					}
				}
			}
		}
		return true
	})

	if err := e.validateFuncBodyConstructs(calleeCtx, callee.Decl.Body); err != nil {
		return nil, err
	}
	if err := e.scanInterfaceAndHelperDecls(calleeCtx, callee.Decl.Body, 0); err != nil {
		return nil, err
	}

	if hasNonTailValueReturn(callee.Decl.Body.List) {
		calleeCtx.useRetDone = true
		calleeCtx.retDoneIdent = prefix + "ret_done"
		preStmts = append(preStmts, makeVarDeclStmt(calleeCtx.retDoneIdent, &ast.Ident{Name: "bool"}, &ast.Ident{Name: "false"}))
	}

	bodyStmts, err := e.expandStmtList(calleeCtx, callee.Decl.Body.List, false, true)
	if err != nil {
		return nil, err
	}

	return append(preStmts, bodyStmts...), nil
}

func (e *inlineEngine) expandStmtList(ctx *expandCtx, stmts []ast.Stmt, isRoot bool, allowTail bool) ([]ast.Stmt, error) {
	var out []ast.Stmt

	for i := 0; i < len(stmts); i++ {
		stmt := stmts[i]

		expanded, inlinedErrAssign, err := e.expandStmt(ctx, stmt, isRoot, allowTail && i == len(stmts)-1)
		if err != nil {
			return nil, err
		}

		// If this statement was `err := inlinedHelper(...)` or `x, err := inlinedHelper(...)`,
		// and the immediately following statement is a boilerplate `if err != nil { return ..., err }`,
		// elide that redundant error check because errors inside inlinedHelper already signal/abort inline.
		if inlinedErrAssign && i+1 < len(stmts) {
			if nextIf, ok := stmts[i+1].(*ast.IfStmt); ok && nextIf.Init == nil && isBoilerplateErrIf(nextIf) {
				i++
			}
		}

		out = append(out, expanded...)

		// If ctx.useRetDone is active and `expanded` could set retDoneIdent, wrap remaining statements in `if !retDoneIdent { ... }`
		if ctx.useRetDone && !isRoot && i < len(stmts)-1 && stmtMaySetRetDone(expanded, ctx.retDoneIdent) {
			restExpanded, err := e.expandStmtList(ctx, stmts[i+1:], false, allowTail)
			if err != nil {
				return nil, err
			}
			if len(restExpanded) > 0 {
				out = append(out, &ast.IfStmt{
					Cond: &ast.UnaryExpr{
						Op: token.NOT,
						X:  &ast.Ident{Name: ctx.retDoneIdent},
					},
					Body: &ast.BlockStmt{List: restExpanded},
				})
			}
			break
		}
	}

	return out, nil
}

func stmtMaySetRetDone(stmts []ast.Stmt, retDoneName string) bool {
	if retDoneName == "" {
		return false
	}
	found := false
	for _, s := range stmts {
		ast.Inspect(s, func(n ast.Node) bool {
			if found || n == nil {
				return false
			}
			if as, ok := n.(*ast.AssignStmt); ok {
				for _, l := range as.Lhs {
					if id, ok := l.(*ast.Ident); ok && id.Name == retDoneName {
						found = true
						return false
					}
				}
			}
			return true
		})
	}
	return found
}

func (e *inlineEngine) expandStmt(ctx *expandCtx, stmt ast.Stmt, isRoot bool, isTail bool) ([]ast.Stmt, bool, error) {
	if stmt == nil {
		return nil, false, nil
	}

	switch s := stmt.(type) {
	case *ast.BlockStmt:
		list, err := e.expandStmtList(ctx, s.List, isRoot, isTail)
		if err != nil {
			return nil, false, err
		}
		return []ast.Stmt{&ast.BlockStmt{List: list}}, false, nil

	case *ast.DeclStmt:
		gen, ok := s.Decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			return nil, false, nil
		}
		var result []ast.Stmt
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for idx, name := range vs.Names {
				obj := ctx.fn.Info.Defs[name]
				if obj != nil && (isNonErrorInterface(obj.Type()) || isHelperOrServiceStruct(obj.Type())) {
					// Elide pure compile-time helper/interface declarations from SQL output
					continue
				}
				targetName := name.Name
				if obj != nil {
					if ren, ok := ctx.localRename[obj]; ok {
						targetName = ren
					}
				}
				var typExpr ast.Expr
				if obj != nil && name.Name != "res" {
					typExpr = typeToASTExpr(obj.Type())
				}
				if vs.Type != nil {
					// Preserve gormproc.JSON / gormproc.Decimal / struct types on original declarations
					if sel, ok := vs.Type.(*ast.SelectorExpr); ok {
						typExpr = &ast.SelectorExpr{X: cloneExprBasic(sel.X), Sel: &ast.Ident{Name: sel.Sel.Name}}
					} else if name.Name == "res" {
						typExpr = cloneExprBasic(vs.Type)
					}
				}

				var valExpr ast.Expr
				if idx < len(vs.Values) {
					liftedVal, liftStmts, err := e.rewriteAndLiftExpr(ctx, vs.Values[idx])
					if err != nil {
						return nil, false, err
					}
					result = append(result, liftStmts...)
					valExpr = liftedVal
				}
				result = append(result, makeVarDeclStmt(targetName, typExpr, valExpr))
			}
		}
		return result, false, nil

	case *ast.ExprStmt:
		if call, ok := s.X.(*ast.CallExpr); ok {
			cc, err := e.classifyCall(ctx, call)
			if err != nil {
				return nil, false, err
			}
			switch cc.kind {
			case callKindTxBlock:
				return e.expandTxCall(ctx, call, isRoot)
			case callKindInline:
				inlined, err := e.inlineCall(ctx, call, cc, nil)
				return inlined, false, err
			case callKindSubProc:
				rewrittenCall, liftStmts, err := e.rewriteCallArgs(ctx, call)
				if err != nil {
					return nil, false, err
				}
				if cc.extProcName != "" {
					e.externalCallNames[rewrittenCall] = cc.extProcName
				}
				return append(liftStmts, &ast.ExprStmt{X: rewrittenCall}), false, nil
			}
		}
		rewritten, liftStmts, err := e.rewriteAndLiftExpr(ctx, s.X)
		if err != nil {
			return nil, false, err
		}
		return append(liftStmts, &ast.ExprStmt{X: rewritten}), false, nil

	case *ast.AssignStmt:
		// Elide helper/interface struct assignments
		if len(s.Lhs) == 1 {
			if id, ok := s.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
				obj := ctx.fn.Info.Defs[id]
				if obj == nil {
					obj = ctx.fn.Info.Uses[id]
				}
				if obj != nil && (isNonErrorInterface(obj.Type()) || isHelperOrServiceStruct(obj.Type())) {
					return nil, false, nil
				}
			}
		}

		// Check `err := s.db.Transaction(...)`
		if len(s.Rhs) == 1 {
			if call, ok := s.Rhs[0].(*ast.CallExpr); ok {
				cc, err := e.classifyCall(ctx, call)
				if err != nil {
					return nil, false, err
				}
				if cc.kind == callKindTxBlock {
					return e.expandTxCall(ctx, call, isRoot)
				}
				if cc.kind == callKindInline {
					var declStmts []ast.Stmt
					var nonErrTargets []ast.Expr
					hasErrOnLhs := false

					for _, lhs := range s.Lhs {
						if id, ok := lhs.(*ast.Ident); ok {
							if id.Name == "err" {
								hasErrOnLhs = true
								continue
							}
							if id.Name == "_" {
								nonErrTargets = append(nonErrTargets, &ast.Ident{Name: "_"})
								continue
							}
							if s.Tok == token.DEFINE {
								if obj := ctx.fn.Info.Defs[id]; obj != nil {
									tName := id.Name
									if ren, ok := ctx.localRename[obj]; ok {
										tName = ren
									}
									declStmts = append(declStmts, makeVarDeclStmt(tName, typeToASTExpr(obj.Type()), nil))
									nonErrTargets = append(nonErrTargets, &ast.Ident{Name: tName})
									continue
								}
							}
						}
						rewLhs, _, err := e.rewriteAndLiftExpr(ctx, lhs)
						if err != nil {
							return nil, false, err
						}
						nonErrTargets = append(nonErrTargets, rewLhs)
					}

					inlined, err := e.inlineCall(ctx, call, cc, nonErrTargets)
					if err != nil {
						return nil, false, err
					}
					return append(declStmts, inlined...), hasErrOnLhs, nil
				}
				if cc.kind == callKindSubProc {
					rewCall, liftStmts, err := e.rewriteCallArgs(ctx, call)
					if err != nil {
						return nil, false, err
					}
					if cc.extProcName != "" {
						e.externalCallNames[rewCall] = cc.extProcName
					}
					// If `err := s.subProc.Foo(...)`, emit as standalone ExprStmt so CALL is emitted cleanly
					if len(s.Lhs) == 1 {
						if id, ok := s.Lhs[0].(*ast.Ident); ok && (id.Name == "err" || id.Name == "_") {
							return append(liftStmts, &ast.ExprStmt{X: rewCall}), id.Name == "err", nil
						}
					}
				}
			}
		}

		// Standard assignment (may still contain inlinable calls inside RHS expressions!)
		var preStmts []ast.Stmt
		var newLhs []ast.Expr
		tok := s.Tok

		for _, lhs := range s.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && s.Tok == token.DEFINE && id.Name != "_" && id.Name != "err" {
				if obj := ctx.fn.Info.Defs[id]; obj != nil {
					tName := id.Name
					if ren, ok := ctx.localRename[obj]; ok {
						tName = ren
						tok = token.ASSIGN
					}
					if !isRoot {
						preStmts = append(preStmts, makeVarDeclStmt(tName, typeToASTExpr(obj.Type()), nil))
						tok = token.ASSIGN
					}
					newLhs = append(newLhs, &ast.Ident{Name: tName})
					continue
				}
			}
			rewLhs, liftLhs, err := e.rewriteAndLiftExpr(ctx, lhs)
			if err != nil {
				return nil, false, err
			}
			preStmts = append(preStmts, liftLhs...)
			newLhs = append(newLhs, rewLhs)
		}

		var newRhs []ast.Expr
		for _, rhs := range s.Rhs {
			rewRhs, liftRhs, err := e.rewriteAndLiftExpr(ctx, rhs)
			if err != nil {
				return nil, false, err
			}
			preStmts = append(preStmts, liftRhs...)
			newRhs = append(newRhs, rewRhs)
		}

		return append(preStmts, &ast.AssignStmt{
			Lhs: newLhs,
			Tok: tok,
			Rhs: newRhs,
		}), false, nil

	case *ast.IncDecStmt:
		rewX, lift, err := e.rewriteAndLiftExpr(ctx, s.X)
		if err != nil {
			return nil, false, err
		}
		return append(lift, &ast.IncDecStmt{X: rewX, Tok: s.Tok}), false, nil

	case *ast.IfStmt:
		var preStmts []ast.Stmt
		var newInit ast.Stmt

		if s.Init != nil {
			handledSpecialInit := false
			if initAssign, ok := s.Init.(*ast.AssignStmt); ok && len(initAssign.Rhs) == 1 {
				if call, ok := initAssign.Rhs[0].(*ast.CallExpr); ok {
					cc, err := e.classifyCall(ctx, call)
					if err != nil {
						return nil, false, err
					}
					if cc.kind == callKindInline {
						handledSpecialInit = true
						initExpanded, _, err := e.expandStmt(ctx, initAssign, isRoot, false)
						if err != nil {
							return nil, false, err
						}
						preStmts = append(preStmts, initExpanded...)
						if isBoilerplateErrIf(s) {
							return preStmts, false, nil
						}
						if s.Else != nil {
							tmpIf := *s
							tmpIf.Else = nil
							if isBoilerplateErrIf(&tmpIf) {
								elseStmts, _, err := e.expandStmt(ctx, s.Else, isRoot, isTail)
								if err != nil {
									return nil, false, err
								}
								return append(preStmts, elseStmts...), false, nil
							}
						}
					} else if cc.kind == callKindSubProc {
						handledSpecialInit = true
						rewCall, liftArgs, err := e.rewriteCallArgs(ctx, call)
						if err != nil {
							return nil, false, err
						}
						if cc.extProcName != "" {
							e.externalCallNames[rewCall] = cc.extProcName
						}
						preStmts = append(preStmts, liftArgs...)
						newInit = &ast.AssignStmt{
							Lhs: []ast.Expr{&ast.Ident{Name: "err"}},
							Tok: token.DEFINE,
							Rhs: []ast.Expr{rewCall},
						}
					}
				}
			}
			if !handledSpecialInit {
				initExpanded, _, err := e.expandStmt(ctx, s.Init, isRoot, false)
				if err != nil {
					return nil, false, err
				}
				if len(initExpanded) == 1 {
					newInit = initExpanded[0]
				} else if len(initExpanded) > 1 {
					preStmts = append(preStmts, initExpanded[:len(initExpanded)-1]...)
					newInit = initExpanded[len(initExpanded)-1]
				}
			}
		}

		rewCond, liftCond, err := e.rewriteAndLiftExpr(ctx, s.Cond)
		if err != nil {
			return nil, false, err
		}
		preStmts = append(preStmts, liftCond...)

		newBody, err := e.expandStmtList(ctx, s.Body.List, isRoot, false)
		if err != nil {
			return nil, false, err
		}

		var newElse ast.Stmt
		if s.Else != nil {
			elseExpanded, _, err := e.expandStmt(ctx, s.Else, isRoot, false)
			if err != nil {
				return nil, false, err
			}
			if len(elseExpanded) == 1 {
				newElse = elseExpanded[0]
			} else if len(elseExpanded) > 1 {
				newElse = &ast.BlockStmt{List: elseExpanded}
			}
		}

		return append(preStmts, &ast.IfStmt{
			Init: newInit,
			Cond: rewCond,
			Body: &ast.BlockStmt{List: newBody},
			Else: newElse,
		}), false, nil

	case *ast.ForStmt:
		var preStmts []ast.Stmt
		var newInit ast.Stmt
		if s.Init != nil {
			initExp, _, err := e.expandStmt(ctx, s.Init, isRoot, false)
			if err != nil {
				return nil, false, err
			}
			if len(initExp) == 1 {
				newInit = initExp[0]
			} else if len(initExp) > 1 {
				preStmts = append(preStmts, initExp[:len(initExp)-1]...)
				newInit = initExp[len(initExp)-1]
			}
		}

		rewCond, liftCond, err := e.rewriteAndLiftExpr(ctx, s.Cond)
		if err != nil {
			return nil, false, err
		}
		preStmts = append(preStmts, liftCond...)

		var newPost ast.Stmt
		if s.Post != nil {
			postExp, _, err := e.expandStmt(ctx, s.Post, isRoot, false)
			if err != nil {
				return nil, false, err
			}
			if len(postExp) > 0 {
				newPost = postExp[len(postExp)-1]
			}
		}

		bodyList, err := e.expandStmtList(ctx, s.Body.List, isRoot, false)
		if err != nil {
			return nil, false, err
		}
		// If liftCond generated statements, also re-evaluate them at end of loop body
		if len(liftCond) > 0 {
			for _, lc := range liftCond {
				if _, isDecl := lc.(*ast.DeclStmt); !isDecl {
					bodyList = append(bodyList, lc)
				}
			}
		}

		return append(preStmts, &ast.ForStmt{
			Init: newInit,
			Cond: rewCond,
			Post: newPost,
			Body: &ast.BlockStmt{List: bodyList},
		}), false, nil

	case *ast.SwitchStmt:
		var preStmts []ast.Stmt
		var rewTag ast.Expr
		if s.Tag != nil {
			var liftTag []ast.Stmt
			var err error
			rewTag, liftTag, err = e.rewriteAndLiftExpr(ctx, s.Tag)
			if err != nil {
				return nil, false, err
			}
			preStmts = append(preStmts, liftTag...)
		}
		var newCases []ast.Stmt
		for _, c := range s.Body.List {
			cc, ok := c.(*ast.CaseClause)
			if !ok {
				continue
			}
			var newList []ast.Expr
			for _, ce := range cc.List {
				rewCe, liftCe, err := e.rewriteAndLiftExpr(ctx, ce)
				if err != nil {
					return nil, false, err
				}
				preStmts = append(preStmts, liftCe...)
				newList = append(newList, rewCe)
			}
			caseBody, err := e.expandStmtList(ctx, cc.Body, isRoot, false)
			if err != nil {
				return nil, false, err
			}
			newCases = append(newCases, &ast.CaseClause{
				List: newList,
				Body: caseBody,
			})
		}
		return append(preStmts, &ast.SwitchStmt{
			Tag:  rewTag,
			Body: &ast.BlockStmt{List: newCases},
		}), false, nil

	case *ast.BranchStmt:
		return []ast.Stmt{&ast.BranchStmt{Tok: s.Tok, Label: s.Label}}, false, nil

	case *ast.ReturnStmt:
		// Check `return s.db.Transaction(...)` or `return inlinedHelper(...)`
		if len(s.Results) == 1 {
			if call, ok := s.Results[0].(*ast.CallExpr); ok {
				cc, err := e.classifyCall(ctx, call)
				if err != nil {
					return nil, false, err
				}
				if cc.kind == callKindTxBlock {
					return e.expandTxCall(ctx, call, isRoot)
				}
				if cc.kind == callKindInline {
					if isRoot {
						var rootTargets []ast.Expr
						// If root returns (Res, error) vs error:
						sig := ctx.fn.Obj.Type().(*types.Signature)
						if sig.Results() != nil && sig.Results().Len() == 1 && isErrorType(sig.Results().At(0).Type()) {
							inlined, err := e.inlineCall(ctx, call, cc, nil)
							if err != nil {
								return nil, false, err
							}
							return append(inlined, &ast.ReturnStmt{Results: []ast.Expr{&ast.Ident{Name: "nil"}}}), false, nil
						}
						_ = rootTargets
					} else {
						inlined, err := e.inlineCall(ctx, call, cc, ctx.lhsTargets)
						if err != nil {
							return nil, false, err
						}
						if ctx.useRetDone && !isTail {
							inlined = append(inlined, &ast.AssignStmt{
								Lhs: []ast.Expr{&ast.Ident{Name: ctx.retDoneIdent}},
								Tok: token.ASSIGN,
								Rhs: []ast.Expr{&ast.Ident{Name: "true"}},
							})
						}
						return inlined, false, nil
					}
				}
			}
		}

		if isRoot {
			var preStmts []ast.Stmt
			var newRes []ast.Expr
			for _, r := range s.Results {
				rewR, liftR, err := e.rewriteAndLiftExpr(ctx, r)
				if err != nil {
					return nil, false, err
				}
				preStmts = append(preStmts, liftR...)
				newRes = append(newRes, rewR)
			}
			return append(preStmts, &ast.ReturnStmt{Results: newRes}), false, nil
		}

		// Inside an inlined helper function:
		// 1. Error propagation (`return ..., err` or `return ..., errors.New(...)`)
		if len(s.Results) > 0 {
			last := s.Results[len(s.Results)-1]
			if id, ok := last.(*ast.Ident); ok && id.Name == "err" {
				return []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{&ast.Ident{Name: "err"}}}}, false, nil
			}
			if call, ok := last.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "New" || sel.Sel.Name == "Errorf") {
					rewCall, liftStmts, err := e.rewriteCallArgs(ctx, call)
					if err != nil {
						return nil, false, err
					}
					return append(liftStmts, &ast.ReturnStmt{Results: []ast.Expr{rewCall}}), false, nil
				}
			}
		}

		// 2. Normal value return (`return v1, ..., nil` or `return tx.Exec(...).Error` or `return v1` or `return`)
		var retStmts []ast.Stmt
		valResults := s.Results
		var errResultExpr ast.Expr
		if len(valResults) > 0 {
			sig := ctx.fn.Obj.Type().(*types.Signature)
			if sig.Results() != nil && sig.Results().Len() > 0 && isErrorType(sig.Results().At(sig.Results().Len()-1).Type()) {
				errResultExpr = valResults[len(valResults)-1]
				valResults = valResults[:len(valResults)-1]
			}
		}

		for idx, vr := range valResults {
			rewVr, liftVr, err := e.rewriteAndLiftExpr(ctx, vr)
			if err != nil {
				return nil, false, err
			}
			retStmts = append(retStmts, liftVr...)
			if idx < len(ctx.lhsTargets) {
				target := ctx.lhsTargets[idx]
				if tId, ok := target.(*ast.Ident); ok && tId.Name == "_" {
					continue
				}
				retStmts = append(retStmts, &ast.AssignStmt{
					Lhs: []ast.Expr{target},
					Tok: token.ASSIGN,
					Rhs: []ast.Expr{rewVr},
				})
			}
		}

		// If the trailing error return expression is a non-nil side-effecting SQL / subproc call
		// (e.g. `return tx.Exec(...).Error` or `return gormproc.SelectInto(...)`), emit it as a statement!
		if errResultExpr != nil {
			if id, isIdent := errResultExpr.(*ast.Ident); !isIdent || id.Name != "nil" {
				errExpanded, _, err := e.expandStmt(ctx, &ast.ExprStmt{X: errResultExpr}, false, false)
				if err != nil {
					return nil, false, err
				}
				retStmts = append(retStmts, errExpanded...)
			}
		}

		if ctx.useRetDone && !isTail {
			retStmts = append(retStmts, &ast.AssignStmt{
				Lhs: []ast.Expr{&ast.Ident{Name: ctx.retDoneIdent}},
				Tok: token.ASSIGN,
				Rhs: []ast.Expr{&ast.Ident{Name: "true"}},
			})
		}
		return retStmts, false, nil
	}

	return []ast.Stmt{stmt}, false, nil
}

func (e *inlineEngine) expandTxCall(ctx *expandCtx, call *ast.CallExpr, isRoot bool) ([]ast.Stmt, bool, error) {
	if len(call.Args) == 0 {
		return nil, false, nil
	}
	lit, ok := call.Args[0].(*ast.FuncLit)
	if !ok || lit.Body == nil {
		return nil, false, fmt.Errorf("db.Transaction expects a function literal (call chain: %s)", formatChain(ctx.callChain))
	}

	if err := e.validateFuncBodyConstructs(ctx, lit.Body); err != nil {
		return nil, false, err
	}

	// Expand statements inside the Transaction closure!
	// Inside the Transaction closure, `return nil` at tail is a no-op if unwrapped, or preserved if kept inside Transaction.
	innerList, err := e.expandStmtList(ctx, lit.Body.List, true, true)
	if err != nil {
		return nil, false, err
	}

	if isRoot {
		newLit := *lit
		newLit.Body = &ast.BlockStmt{List: innerList}
		newCall := *call
		newCall.Args = []ast.Expr{&newLit}
		return []ast.Stmt{&ast.ExprStmt{X: &newCall}}, false, nil
	}

	// If Transaction was inside an inlined helper, unwrap its body directly (filtering trailing `return nil`)
	var unwrapped []ast.Stmt
	for idx, st := range innerList {
		if idx == len(innerList)-1 {
			if ret, ok := st.(*ast.ReturnStmt); ok && len(ret.Results) == 1 {
				if id, ok := ret.Results[0].(*ast.Ident); ok && id.Name == "nil" {
					continue
				}
			}
		}
		unwrapped = append(unwrapped, st)
	}
	return unwrapped, false, nil
}

func (e *inlineEngine) rewriteCallArgs(ctx *expandCtx, call *ast.CallExpr) (*ast.CallExpr, []ast.Stmt, error) {
	var liftStmts []ast.Stmt
	var newArgs []ast.Expr
	for _, arg := range call.Args {
		rewArg, lift, err := e.rewriteAndLiftExpr(ctx, arg)
		if err != nil {
			return nil, nil, err
		}
		liftStmts = append(liftStmts, lift...)
		newArgs = append(newArgs, rewArg)
	}
	newFun := e.rewriteExprSimple(ctx, call.Fun)
	return &ast.CallExpr{
		Fun:  newFun,
		Args: newArgs,
	}, liftStmts, nil
}

func (e *inlineEngine) rewriteAndLiftExpr(ctx *expandCtx, expr ast.Expr) (ast.Expr, []ast.Stmt, error) {
	if expr == nil {
		return nil, nil, nil
	}

	switch ex := expr.(type) {
	case *ast.BasicLit:
		return &ast.BasicLit{Kind: ex.Kind, Value: ex.Value}, nil, nil

	case *ast.Ident:
		return e.rewriteIdent(ctx, ex), nil, nil

	case *ast.ParenExpr:
		inner, lift, err := e.rewriteAndLiftExpr(ctx, ex.X)
		if err != nil {
			return nil, nil, err
		}
		return &ast.ParenExpr{X: inner}, lift, nil

	case *ast.StarExpr:
		if id, ok := ex.X.(*ast.Ident); ok {
			if obj := ctx.fn.Info.Uses[id]; obj != nil {
				if derefTarget, ok := ctx.derefSub[obj]; ok {
					return cloneExprBasic(derefTarget), nil, nil
				}
			}
		}
		inner, lift, err := e.rewriteAndLiftExpr(ctx, ex.X)
		if err != nil {
			return nil, nil, err
		}
		return &ast.StarExpr{X: inner}, lift, nil

	case *ast.UnaryExpr:
		if ex.Op == token.AND {
			if star, ok := ex.X.(*ast.StarExpr); ok {
				return e.rewriteAndLiftExpr(ctx, star.X)
			}
		}
		inner, lift, err := e.rewriteAndLiftExpr(ctx, ex.X)
		if err != nil {
			return nil, nil, err
		}
		return &ast.UnaryExpr{Op: ex.Op, X: inner}, lift, nil

	case *ast.BinaryExpr:
		left, liftL, err := e.rewriteAndLiftExpr(ctx, ex.X)
		if err != nil {
			return nil, nil, err
		}
		right, liftR, err := e.rewriteAndLiftExpr(ctx, ex.Y)
		if err != nil {
			return nil, nil, err
		}
		return &ast.BinaryExpr{X: left, Op: ex.Op, Y: right}, append(liftL, liftR...), nil

	case *ast.SelectorExpr:
		// Check if X is callee's receiver or a helper struct with a bound field expression
		if si, err := e.extractStructValInfo(ctx, ex.X); err == nil && si != nil && si.fieldExprs != nil {
			if bound, ok := si.fieldExprs[ex.Sel.Name]; ok {
				return cloneExprBasic(bound), nil, nil
			}
		}
		// Check if selector resolves to a package-level Go const (e.g. pkg.ConstVal)
		if obj, ok := ctx.fn.Info.Uses[ex.Sel].(*types.Const); ok {
			if lit := constToASTExpr(obj); lit != nil {
				return lit, nil, nil
			}
		}
		innerX, liftX, err := e.rewriteAndLiftExpr(ctx, ex.X)
		if err != nil {
			return nil, nil, err
		}
		return &ast.SelectorExpr{X: innerX, Sel: &ast.Ident{Name: ex.Sel.Name}}, liftX, nil

	case *ast.CompositeLit:
		var liftAll []ast.Stmt
		var newElts []ast.Expr
		for _, elt := range ex.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				val, liftV, err := e.rewriteAndLiftExpr(ctx, kv.Value)
				if err != nil {
					return nil, nil, err
				}
				liftAll = append(liftAll, liftV...)
				newElts = append(newElts, &ast.KeyValueExpr{
					Key:   cloneExprBasic(kv.Key),
					Value: val,
				})
			} else {
				val, liftV, err := e.rewriteAndLiftExpr(ctx, elt)
				if err != nil {
					return nil, nil, err
				}
				liftAll = append(liftAll, liftV...)
				newElts = append(newElts, val)
			}
		}
		return &ast.CompositeLit{
			Type: cloneExprBasic(ex.Type),
			Elts: newElts,
		}, liftAll, nil

	case *ast.CallExpr:
		cc, err := e.classifyCall(ctx, ex)
		if err != nil {
			return nil, nil, err
		}
		if cc.kind == callKindInline {
			// Lift expression-level call into temporary variable(s) before the enclosing statement!
			sig := cc.target.Obj.Type().(*types.Signature)
			var retType types.Type
			if sig.Results() != nil && sig.Results().Len() > 0 {
				retType = sig.Results().At(0).Type()
			}
			tmpVar := e.nextPrefix() + "expr"
			decl := makeVarDeclStmt(tmpVar, typeToASTExpr(retType), nil)
			inlined, err := e.inlineCall(ctx, ex, cc, []ast.Expr{&ast.Ident{Name: tmpVar}})
			if err != nil {
				return nil, nil, err
			}
			return &ast.Ident{Name: tmpVar}, append([]ast.Stmt{decl}, inlined...), nil
		}
		// Normalize math.Abs(x) -> ABS(x) so SQL engines evaluate it natively
		if sel, ok := ex.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Abs" {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "math" && len(ex.Args) == 1 {
				rewArg, lift, err := e.rewriteAndLiftExpr(ctx, ex.Args[0])
				if err != nil {
					return nil, nil, err
				}
				return &ast.CallExpr{
					Fun:  &ast.Ident{Name: "ABS"},
					Args: []ast.Expr{rewArg},
				}, lift, nil
			}
		}
		rewCall, lift, err := e.rewriteCallArgs(ctx, ex)
		if err != nil {
			return nil, nil, err
		}
		if cc.kind == callKindSubProc && cc.extProcName != "" {
			e.externalCallNames[rewCall] = cc.extProcName
		}
		return rewCall, lift, nil
	}

	return cloneExprBasic(expr), nil, nil
}

func (e *inlineEngine) rewriteIdent(ctx *expandCtx, id *ast.Ident) ast.Expr {
	if id == nil {
		return nil
	}
	obj := ctx.fn.Info.Uses[id]
	if obj == nil {
		obj = ctx.fn.Info.Defs[id]
	}
	if obj != nil {
		if sub, ok := ctx.paramSub[obj]; ok {
			return cloneExprBasic(sub)
		}
		if ren, ok := ctx.localRename[obj]; ok {
			return &ast.Ident{Name: ren}
		}
		if c, ok := obj.(*types.Const); ok {
			// Preserve boolean true/false identifiers, substitute named Go constants with their literal values
			if id.Name != "true" && id.Name != "false" {
				if lit := constToASTExpr(c); lit != nil {
					return lit
				}
			}
		}
	}
	return &ast.Ident{Name: id.Name}
}

func (e *inlineEngine) rewriteExprSimple(ctx *expandCtx, expr ast.Expr) ast.Expr {
	if expr == nil {
		return nil
	}
	switch ex := expr.(type) {
	case *ast.Ident:
		return e.rewriteIdent(ctx, ex)
	case *ast.SelectorExpr:
		return &ast.SelectorExpr{
			X:   e.rewriteExprSimple(ctx, ex.X),
			Sel: &ast.Ident{Name: ex.Sel.Name},
		}
	default:
		return cloneExprBasic(expr)
	}
}

func cloneExprBasic(expr ast.Expr) ast.Expr {
	if expr == nil {
		return nil
	}
	switch ex := expr.(type) {
	case *ast.Ident:
		return &ast.Ident{Name: ex.Name}
	case *ast.BasicLit:
		return &ast.BasicLit{Kind: ex.Kind, Value: ex.Value}
	case *ast.StarExpr:
		return &ast.StarExpr{X: cloneExprBasic(ex.X)}
	case *ast.UnaryExpr:
		return &ast.UnaryExpr{Op: ex.Op, X: cloneExprBasic(ex.X)}
	case *ast.BinaryExpr:
		return &ast.BinaryExpr{X: cloneExprBasic(ex.X), Op: ex.Op, Y: cloneExprBasic(ex.Y)}
	case *ast.ParenExpr:
		return &ast.ParenExpr{X: cloneExprBasic(ex.X)}
	case *ast.SelectorExpr:
		return &ast.SelectorExpr{X: cloneExprBasic(ex.X), Sel: &ast.Ident{Name: ex.Sel.Name}}
	}
	return expr
}
