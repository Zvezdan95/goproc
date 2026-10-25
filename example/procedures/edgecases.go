package procedures

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

type UserStatusResult struct {
	Status string `gorm:"column:status"`
}

//gorm:procedure
func (b *BillingService) UpdateUserStatus(ctx context.Context, userID int64, isActive bool) (UserStatusResult, error) {
	var res UserStatusResult
	var newStatus string

	err := b.db.Transaction(func(tx *gorm.DB) error {
		if isActive {
			newStatus = "ACTIVE"
		} else {
			newStatus = "INACTIVE"
		}

		if err := tx.Exec("UPDATE users SET status = ? WHERE id = ?", newStatus, userID).Error; err != nil {
			return err
		}

		return tx.Raw("SELECT status FROM users WHERE id = ?", userID).Scan(&res).Error
	})

	return res, err
}

//gorm:procedure
func (b *BillingService) TransferFunds(ctx context.Context, fromID int64, toID int64, amount float64) (bool, error) {
	var success bool

	err := b.db.Transaction(func(tx *gorm.DB) error {
		if amount <= 0 {
			return errors.New("invalid transfer amount")
		}

		if fromID == toID {
			return errors.New("cannot transfer to self")
		}

		if err := tx.Exec("UPDATE accounts SET balance = balance - ? WHERE id = ?", amount, fromID).Error; err != nil {
			return err
		}

		if err := tx.Exec("UPDATE accounts SET balance = balance + ? WHERE id = ?", amount, toID).Error; err != nil {
			return err
		}

		success = true
		return nil
	})

	return success, err
}

type OptionalData struct {
	Notes *string `gorm:"column:notes"`
}

//gorm:procedure
func (b *BillingService) GetOptionalData(ctx context.Context, userID int64, defaultNote *string) (OptionalData, error) {
	var res OptionalData

	err := b.db.Transaction(func(tx *gorm.DB) error {
		if defaultNote == nil {
			return tx.Raw("SELECT notes FROM user_data WHERE id = ?", userID).Scan(&res).Error
		}
		
		res.Notes = defaultNote
		return nil
	})

	return res, err
}
