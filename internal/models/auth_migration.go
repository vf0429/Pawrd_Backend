package models

import (
	"fmt"
	"log"
	"strings"

	"gorm.io/gorm"
)

type legacyAuthUsernameColumn struct {
	DataType      string  `gorm:"column:data_type"`
	IsNullable    string  `gorm:"column:is_nullable"`
	ColumnDefault *string `gorm:"column:column_default"`
}

// normalizeLegacyAuthUsernameColumn repairs a schema left by the historical
// username feature. Current AuthUser inserts no username, so a leftover
// NOT NULL/default-empty column makes registration fail even though the model
// and request are valid.
//
// The migration intentionally preserves the column, its existing values, and
// its unique index. PostgreSQL unique indexes allow multiple NULL values, so
// new users remain compatible without destroying legacy data.
func normalizeLegacyAuthUsernameColumn(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	switch db.Dialector.Name() {
	case "sqlite":
		return rejectLegacyAuthUsernameSQLite(db)
	case "postgres":
		// Continue with the production PostgreSQL migration below.
	default:
		return nil
	}

	changed := false
	err := db.Transaction(func(tx *gorm.DB) error {
		var columns []legacyAuthUsernameColumn
		result := tx.Raw(`
			SELECT data_type, is_nullable, column_default
			FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = 'auth_users'
			  AND column_name = 'username'
		`).Scan(&columns)
		if result.Error != nil {
			return fmt.Errorf("inspect auth_users.username: %w", result.Error)
		}
		if len(columns) == 0 {
			return nil
		}
		if len(columns) != 1 {
			return fmt.Errorf("expected one auth_users.username column, found %d", len(columns))
		}

		column := columns[0]
		switch strings.ToLower(strings.TrimSpace(column.DataType)) {
		case "text", "character varying", "character":
		default:
			return fmt.Errorf("unexpected auth_users.username data type %q", column.DataType)
		}
		switch strings.ToUpper(strings.TrimSpace(column.IsNullable)) {
		case "YES", "NO":
		default:
			return fmt.Errorf("unexpected auth_users.username nullable state %q", column.IsNullable)
		}
		if !isLegacyUsernameDefault(column.ColumnDefault) {
			return fmt.Errorf("unexpected auth_users.username default %q", strings.TrimSpace(*column.ColumnDefault))
		}

		alreadyNormalized := strings.EqualFold(column.IsNullable, "YES") && column.ColumnDefault == nil
		if alreadyNormalized {
			return nil
		}

		if err := tx.Exec(`
			ALTER TABLE "auth_users"
			  ALTER COLUMN "username" DROP DEFAULT,
			  ALTER COLUMN "username" DROP NOT NULL
		`).Error; err != nil {
			return fmt.Errorf("relax auth_users.username legacy constraints: %w", err)
		}

		changed = true
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		log.Println("Normalized legacy auth_users.username constraints for current AuthUser inserts.")
	}
	return nil
}

func isLegacyUsernameDefault(value *string) bool {
	if value == nil {
		return true
	}

	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(*value), " ", ""))
	switch normalized {
	case "''::text",
		"''::charactervarying",
		"''::character",
		"null::text",
		"null::charactervarying",
		"null::character":
		return true
	default:
		return false
	}
}

func rejectLegacyAuthUsernameSQLite(db *gorm.DB) error {
	var columns []struct {
		Name string `gorm:"column:name"`
	}
	if err := db.Raw(`PRAGMA table_info("auth_users")`).Scan(&columns).Error; err != nil {
		return fmt.Errorf("inspect SQLite auth_users schema: %w", err)
	}
	for _, column := range columns {
		if strings.EqualFold(strings.TrimSpace(column.Name), "username") {
			return fmt.Errorf(
				"legacy SQLite auth_users.username requires an explicit backup and table-rebuild migration",
			)
		}
	}
	return nil
}
