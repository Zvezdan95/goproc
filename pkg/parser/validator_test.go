package parser

import (
	"go/ast"
	"go/parser"
	"go/token"
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
