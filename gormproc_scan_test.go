package gormproc

import (
	"reflect"
	"testing"
)

func TestCaseInsensitiveFieldMapAndFlexScanner(t *testing.T) {
	type roundBookResult struct {
		RoundID        uint32   `gorm:"column:roundId"`
		Currency       string   `gorm:"column:currency"`
		BalanceReal    float64  `gorm:"column:balanceReal"`
		BalanceBonus   *float64 `gorm:"column:balanceBonus"`
		BonusAccountID *uint32  `gorm:"column:bonusAccountId"`
	}

	fieldMap := buildCaseInsensitiveFieldMap(reflect.TypeOf(roundBookResult{}))
	for _, col := range []string{"roundid", "currency", "balancereal", "balancebonus", "bonusaccountid"} {
		if _, ok := fieldMap[col]; !ok {
			t.Fatalf("expected lowercase column %q in fieldMap, got %v", col, fieldMap)
		}
	}

	var out roundBookResult
	rv := reflect.ValueOf(&out).Elem()

	if err := (&flexFieldScanner{val: rv.FieldByIndex(fieldMap["roundid"])}).Scan(int64(99)); err != nil {
		t.Fatalf("Scan roundid failed: %v", err)
	}
	if err := (&flexFieldScanner{val: rv.FieldByIndex(fieldMap["currency"])}).Scan("USD"); err != nil {
		t.Fatalf("Scan currency failed: %v", err)
	}
	if err := (&flexFieldScanner{val: rv.FieldByIndex(fieldMap["balancereal"])}).Scan([]byte("123.45")); err != nil {
		t.Fatalf("Scan balancereal failed: %v", err)
	}
	if err := (&flexFieldScanner{val: rv.FieldByIndex(fieldMap["balancebonus"])}).Scan("50.25"); err != nil {
		t.Fatalf("Scan balancebonus failed: %v", err)
	}
	if err := (&flexFieldScanner{val: rv.FieldByIndex(fieldMap["bonusaccountid"])}).Scan(nil); err != nil {
		t.Fatalf("Scan bonusaccountid nil failed: %v", err)
	}

	if out.RoundID != 99 || out.Currency != "USD" || out.BalanceReal != 123.45 {
		t.Errorf("unexpected scalar fields: %+v", out)
	}
	if out.BalanceBonus == nil || *out.BalanceBonus != 50.25 {
		t.Errorf("expected BalanceBonus = 50.25, got %v", out.BalanceBonus)
	}
	if out.BonusAccountID != nil {
		t.Errorf("expected BonusAccountID = nil, got %v", *out.BonusAccountID)
	}
}

func TestFieldByIndexAllocAndEmbeddedShadowing(t *testing.T) {
	type Embedded struct {
		Currency   string `gorm:"column:currency"`
		EmbeddedID uint32 `gorm:"column:embeddedId"`
	}
	type Outer struct {
		Currency string `gorm:"column:currency"`
		Ignored  string `gorm:"-"`
		*Embedded
	}

	fieldMap := buildCaseInsensitiveFieldMap(reflect.TypeOf(Outer{}))
	if _, ok := fieldMap["ignored"]; ok {
		t.Fatalf("expected gorm:\"-\" field to be excluded from fieldMap")
	}
	// Outer Currency (index [0]) must take precedence over Embedded.Currency (index [2, 0])
	if len(fieldMap["currency"]) != 1 || fieldMap["currency"][0] != 0 {
		t.Fatalf("expected outer Currency field [0] to take precedence, got %v", fieldMap["currency"])
	}

	var out Outer
	rv := reflect.ValueOf(&out).Elem()
	fv := fieldByIndexAlloc(rv, fieldMap["embeddedid"])
	if !fv.IsValid() || !fv.CanSet() {
		t.Fatalf("expected fieldByIndexAlloc to allocate nil embedded pointer struct")
	}
	if err := (&flexFieldScanner{val: fv}).Scan(int64(77)); err != nil {
		t.Fatalf("Scan embeddedid failed: %v", err)
	}
	if out.Embedded == nil || out.Embedded.EmbeddedID != 77 {
		t.Fatalf("expected out.Embedded.EmbeddedID == 77, got %+v", out.Embedded)
	}
}

