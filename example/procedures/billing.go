package procedures

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

type BillingResult struct {
	ID    int64   `gorm:"column:id"`
	Total float64 `gorm:"column:total"`
}

type BillingService struct {
	db *gorm.DB
}

func NewBillingService(db *gorm.DB) *BillingService {
	return &BillingService{db: db}
}

//gorm:procedure
func (b *BillingService) ProcessBilling(ctx context.Context, userID int64, amount float64) (BillingResult, error) {
	var res BillingResult

	err := b.db.Transaction(func(tx *gorm.DB) error {
		if amount <= 0 {
			return errors.New("amount must be greater than zero")
		}
		return tx.Raw("SELECT id, total FROM invoices WHERE user_id = ?", userID).Scan(&res).Error
	})

	return res, err
}
