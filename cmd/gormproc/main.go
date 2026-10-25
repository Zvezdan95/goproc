package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"
	"fmt"

	"github.com/user/gormproc/pkg/generator"
	"github.com/user/gormproc/pkg/parser"
	"github.com/user/gormproc/pkg/transpiler"
)

func main() {
	var dir string
	var pkg string
	var out string

	flag.StringVar(&dir, "dir", ".", "Directory containing Go files to parse")
	flag.StringVar(&pkg, "pkg", "github.com/user/gormproc/example/procedures", "Import path of the procedures package")
	flag.StringVar(&out, "out", "gen/procedures.go", "Output file for generated code")
	flag.Parse()

	log.Printf("Parsing directory: %s", dir)
	procs, err := parser.ParseDir(dir)
	if err != nil {
		log.Fatalf("Parse error: %v", err)
	}

	if len(procs) == 0 {
		log.Println("No gorm:procedure functions found. Exiting.")
		return
	}

	var generated []generator.GeneratedProc

	for _, p := range procs {
		if err := parser.Validate(p); err != nil {
			log.Fatalf("Validation error in %s: %v", p.Name, err)
		}

		pgSQL, err := transpiler.ToPostgres(p, "")
		if err != nil {
			log.Fatalf("Transpile to Postgres failed for %s: %v", p.Name, err)
		}

		mysqlSQL, err := transpiler.ToMySQL(p, "")
		if err != nil {
			log.Fatalf("Transpile to MySQL failed for %s: %v", p.Name, err)
		}

		hash := generator.GenerateHash(p, pgSQL, mysqlSQL)

		// Regenerate with actual hash included in names
		pgSQL, _ = transpiler.ToPostgres(p, hash)
		mysqlSQL, _ = transpiler.ToMySQL(p, hash)

		generated = append(generated, generator.GeneratedProc{
			Proc:     p,
			Hash:     hash,
			PgSQL:    pgSQL,
			MySQLSQL: mysqlSQL,
		})
		log.Printf("Successfully transpiled %s (Hash: %s)", p.Name, hash)
	}

	codeBytes, err := generator.GenerateCode(pkg, generated)
	if err != nil {
		log.Fatalf("Code generation failed: %v", err)
	}

	outDir := filepath.Dir(out)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}
	
	// Write out the separate SQL files
	for _, p := range generated {
		baseName := strings.ToLower(p.Proc.Name)
		fullName := fmt.Sprintf("%s_%s", baseName, p.Hash)
		
		pgFile := filepath.Join(outDir, fullName+".postgres.sql")
		if err := os.WriteFile(pgFile, []byte(p.PgSQL), 0644); err != nil {
			log.Fatalf("Failed to write postgres sql: %v", err)
		}
		
		myFile := filepath.Join(outDir, fullName+".mysql.sql")
		if err := os.WriteFile(myFile, []byte(p.MySQLSQL), 0644); err != nil {
			log.Fatalf("Failed to write mysql sql: %v", err)
		}
	}

	if err := os.WriteFile(out, codeBytes, 0644); err != nil {
		log.Fatalf("Failed to write output file: %v", err)
	}

	log.Printf("Generated code written to %s", out)
}
