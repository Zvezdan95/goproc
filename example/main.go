package main

import (
	"context"
	"log"
	
	// We'll use sqlite for the example dummy DB just to get GORM initialized
	// However, gormproc currently supports postgres and mysql logic.
	// Let's use mysql driver. We don't actually need a real DB connection if we just want it to fail trying to connect,
	// but it will prove the Go wrapper compiles.
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"github.com/user/gormproc"
	"github.com/user/gormproc/example/gen"
)

func main() {
	log.Println("Starting gormproc example...")
	
	// Try to connect to a dummy DB
	dsn := "user:pass@tcp(127.0.0.1:3306)/dbname?charset=utf8mb4&parseTime=True&loc=Local"
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		// Just log and continue, we are mostly proving it compiles
		log.Printf("DB connect error (expected without real DB): %v", err)
	} else {
		// Run migrations
		log.Println("Running AutoMigrate...")
		if err := gormproc.AutoMigrate(db, gen.RegisteredProcedures...); err != nil {
			log.Printf("Migration failed: %v", err)
		}
		
		procs := gen.NewProcedures(db)
		ctx := context.Background()
		res, err := procs.ProcessBilling(ctx, 1001, 49.99)
		if err != nil {
			log.Printf("ProcessBilling error: %v", err)
		} else {
			log.Printf("ProcessBilling Result: %+v", res)
		}
	}
	
	log.Println("Done.")
}
