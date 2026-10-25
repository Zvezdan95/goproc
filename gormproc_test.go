package gormproc_test

import (
	"testing"

	"github.com/user/gormproc"
)

func TestREADMEExample(t *testing.T) {
	// From the README:
	// count := gormproc.JSONLength(jsonStr, "$")
	// extId := gormproc.JSONExtractString(jsonStr, "$[0].id")
	jsonStr := `[{"id": "ext-101", "name": "item1"}, {"id": "ext-102", "name": "item2"}]`

	count := gormproc.JSONLength(jsonStr, "$")
	if count != 2 {
		t.Errorf("Expected JSONLength to return 2, got %d", count)
	}

	extId := gormproc.JSONExtractString(jsonStr, "$[0].id")
	if extId != "ext-101" {
		t.Errorf("Expected JSONExtractString to return 'ext-101', got '%s'", extId)
	}

	extId2 := gormproc.JSONExtractString(jsonStr, "$[1].id")
	if extId2 != "ext-102" {
		t.Errorf("Expected JSONExtractString to return 'ext-102', got '%s'", extId2)
	}
}

func TestJSONLength(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		path     string
		expected int64
	}{
		{
			name:     "root array",
			json:     `[1, 2, 3, 4, 5]`,
			path:     "$",
			expected: 5,
		},
		{
			name:     "root object",
			json:     `{"a": 1, "b": 2, "c": 3}`,
			path:     "$",
			expected: 3,
		},
		{
			name:     "nested array",
			json:     `{"items": ["a", "b"]}`,
			path:     "$.items",
			expected: 2,
		},
		{
			name:     "scalar value",
			json:     `"hello"`,
			path:     "$",
			expected: 1,
		},
		{
			name:     "non-existent path",
			json:     `{"a": 1}`,
			path:     "$.missing",
			expected: 0,
		},
		{
			name:     "empty/invalid json",
			json:     `not-json`,
			path:     "$",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gormproc.JSONLength(tt.json, tt.path)
			if got != tt.expected {
				t.Errorf("JSONLength(%s, %s) = %d, expected %d", tt.json, tt.path, got, tt.expected)
			}
		})
	}
}

func TestJSONExtract(t *testing.T) {
	jsonStr := `{"user": {"name": "Alice", "score": 98.5, "active": true, "roles": ["admin", "editor"]}}`

	name := gormproc.JSONExtractString(jsonStr, "$.user.name")
	if name != "Alice" {
		t.Errorf("Expected name 'Alice', got '%s'", name)
	}

	role := gormproc.JSONExtractString(jsonStr, "$.user.roles[0]")
	if role != "admin" {
		t.Errorf("Expected role 'admin', got '%s'", role)
	}

	score := gormproc.JSONExtractFloat64(jsonStr, "$.user.score")
	if score != 98.5 {
		t.Errorf("Expected score 98.5, got %f", score)
	}

	active := gormproc.JSONExtractString(jsonStr, "$.user.active")
	if active != "true" {
		t.Errorf("Expected active 'true', got '%s'", active)
	}

	missing := gormproc.JSONExtractString(jsonStr, "$.user.nonexistent")
	if missing != "" {
		t.Errorf("Expected empty string for missing path, got '%s'", missing)
	}
}

func TestJSONOverlaps(t *testing.T) {
	// Overlapping arrays
	if !gormproc.JSONOverlaps(`[1, 2, 3]`, `[3, 4, 5]`) {
		t.Errorf("Expected arrays [1,2,3] and [3,4,5] to overlap on 3")
	}

	// Disjoint arrays
	if gormproc.JSONOverlaps(`[1, 2]`, `[3, 4]`) {
		t.Errorf("Expected arrays [1,2] and [3,4] not to overlap")
	}

	// Overlapping objects
	if !gormproc.JSONOverlaps(`{"a": 1}`, `{"a": 1, "b": 2}`) {
		t.Errorf("Expected objects to overlap on a: 1")
	}

	// Objects with same key different value
	if gormproc.JSONOverlaps(`{"a": 1}`, `{"a": 2}`) {
		t.Errorf("Expected objects not to overlap on different values")
	}
}

func TestGlobHasBits(t *testing.T) {
	if !gormproc.GlobHasBits(0b111, 0b011) {
		t.Errorf("Expected GlobHasBits(0b111, 0b011) to be true")
	}
	if gormproc.GlobHasBits(0b100, 0b011) {
		t.Errorf("Expected GlobHasBits(0b100, 0b011) to be false")
	}
}

func TestSelectIntoValidation(t *testing.T) {
	err := gormproc.SelectInto(nil, "SELECT 1")
	if err == nil {
		t.Errorf("Expected error when db is nil")
	}

	// Insufficient arguments for query placeholders
	// Cannot easily test full database without connection, but we verify param validation
}
