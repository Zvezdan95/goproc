package procedures

import (
	"context"
	"gorm.io/gorm"
)

type DummyService struct {
	db *gorm.DB
}

//gorm:procedure
func (s *DummyService) ComplexFormatTest(ctx context.Context, action int64, data string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var status string
		var i int64

		if action == 1 {
			status = "START"
		} else {
			status = "STOP"
		}

		for i = 0; i < 5; i++ {
			if status == "START" {
				switch action {
				case 1:
					status = "ONGOING"
				case 2:
					status = "PAUSED"
				}
			}
		}
		
		return nil
	})
}
