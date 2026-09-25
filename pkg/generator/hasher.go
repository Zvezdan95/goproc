package generator

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/user/gormproc/pkg/parser"
)

// GenerateHash produces a deterministic 8-character hash for a procedure
// based on its signature and transpiled SQL bodies.
func GenerateHash(proc *parser.Procedure, pgSQL, mysqlSQL string) string {
	h := sha256.New()
	h.Write([]byte(proc.Name))
	// In a full implementation, we'd hash the exact parameter types and names too.
	h.Write([]byte(pgSQL))
	h.Write([]byte(mysqlSQL))
	
	fullHash := hex.EncodeToString(h.Sum(nil))
	return fullHash[:8]
}
