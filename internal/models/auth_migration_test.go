package models

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestIsLegacyUsernameDefault(t *testing.T) {
	emptyText := "''::text"
	emptyVarchar := "''::character varying"
	nullText := "NULL::text"
	unexpected := "'generated'::text"

	tests := []struct {
		name  string
		value *string
		want  bool
	}{
		{name: "no default", value: nil, want: true},
		{name: "empty text", value: &emptyText, want: true},
		{name: "empty varchar", value: &emptyVarchar, want: true},
		{name: "explicit null", value: &nullText, want: true},
		{name: "unexpected generated value", value: &unexpected, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isLegacyUsernameDefault(test.value); got != test.want {
				t.Fatalf("isLegacyUsernameDefault(%v) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestNormalizeLegacyAuthUsernameColumnRejectsNilDatabase(t *testing.T) {
	if err := normalizeLegacyAuthUsernameColumn(nil); err == nil {
		t.Fatal("expected nil database to be rejected")
	}
}

func TestNormalizeLegacyAuthUsernameColumnRejectsLegacySQLiteSchema(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`
		CREATE TABLE auth_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL UNIQUE,
			username TEXT NOT NULL DEFAULT ''
		)
	`).Error; err != nil {
		t.Fatalf("create legacy sqlite table: %v", err)
	}

	err = normalizeLegacyAuthUsernameColumn(db)
	if err == nil || !strings.Contains(err.Error(), "table-rebuild migration") {
		t.Fatalf("legacy sqlite schema error = %v, want explicit migration guidance", err)
	}

	var sql string
	if err := db.Raw(`
		SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'auth_users'
	`).Scan(&sql).Error; err != nil {
		t.Fatalf("inspect sqlite schema: %v", err)
	}
	if !strings.Contains(strings.ToUpper(sql), "USERNAME TEXT NOT NULL") {
		t.Fatalf("legacy sqlite schema was unexpectedly changed: %s", sql)
	}
}

func TestNormalizeLegacyAuthUsernameColumnAcceptsCurrentSQLiteSchema(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&AuthUser{}); err != nil {
		t.Fatalf("migrate current auth schema: %v", err)
	}
	if err := normalizeLegacyAuthUsernameColumn(db); err != nil {
		t.Fatalf("current sqlite schema should pass validation: %v", err)
	}
}

func TestNormalizeLegacyAuthUsernameColumnPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PAWRD_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("PAWRD_TEST_POSTGRES_DSN is not configured")
	}
	if os.Getenv("PAWRD_ALLOW_POSTGRES_MIGRATION_TESTS") != "auth-username-migration" {
		t.Skip("PAWRD_ALLOW_POSTGRES_MIGRATION_TESTS is not explicitly enabled")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	var databaseName string
	if err := db.Raw(`SELECT current_database()`).Scan(&databaseName).Error; err != nil {
		t.Fatalf("read postgres database name: %v", err)
	}
	if !strings.HasPrefix(strings.ToLower(databaseName), "pawrd_test_") {
		t.Fatalf("refusing migration integration test against non-test database %q", databaseName)
	}

	t.Run("not null without default", func(t *testing.T) {
		testLegacyAuthUsernameMigrationPostgres(t, db, false)
	})
	t.Run("not null with empty default and unique index", func(t *testing.T) {
		testLegacyAuthUsernameMigrationPostgres(t, db, true)
	})
	t.Run("missing legacy column", func(t *testing.T) {
		testCurrentAuthSchemaMigrationPostgres(t, db)
	})
}

func testLegacyAuthUsernameMigrationPostgres(t *testing.T, db *gorm.DB, withDefault bool) {
	t.Helper()

	runInTemporaryPostgresSchema(t, db, func(tx *gorm.DB) {
		defaultClause := ""
		if withDefault {
			defaultClause = " DEFAULT ''"
		}
		if err := tx.Exec(`
			CREATE TABLE auth_users (
				id BIGSERIAL PRIMARY KEY,
				email TEXT NOT NULL UNIQUE,
				phone TEXT NOT NULL UNIQUE,
				password_hash TEXT NOT NULL,
				name TEXT NOT NULL,
				avatar_url TEXT DEFAULT '',
				created_at TIMESTAMPTZ,
				username TEXT NOT NULL` + defaultClause + `
			)
		`).Error; err != nil {
			t.Fatalf("create legacy auth table: %v", err)
		}
		if err := tx.Exec(`CREATE UNIQUE INDEX idx_auth_users_username ON auth_users(username)`).Error; err != nil {
			t.Fatalf("create legacy username index: %v", err)
		}
		if err := tx.Exec(`
			INSERT INTO auth_users (email, phone, password_hash, name, username)
			VALUES ('legacy@example.com', 'legacy-phone', 'hash', 'Legacy', 'legacy')
		`).Error; err != nil {
			t.Fatalf("seed legacy user: %v", err)
		}
		if withDefault {
			if err := tx.Exec(`
				INSERT INTO auth_users (email, phone, password_hash, name, username)
				VALUES ('legacy-empty@example.com', 'legacy-empty-phone', 'hash', 'Legacy Empty', '')
			`).Error; err != nil {
				t.Fatalf("seed legacy empty username: %v", err)
			}
		}

		preMigrationErr := tx.Transaction(func(probe *gorm.DB) error {
			return probe.Exec(`
				INSERT INTO auth_users (email, phone, password_hash, name)
				VALUES ('before@example.com', 'before-phone', 'hash', 'Before')
			`).Error
		})
		if preMigrationErr == nil {
			t.Fatal("legacy schema unexpectedly accepted an insert without username")
		}
		expectedSQLState := "SQLSTATE 23502"
		if withDefault {
			expectedSQLState = "SQLSTATE 23505"
		}
		if !strings.Contains(preMigrationErr.Error(), expectedSQLState) {
			t.Fatalf("pre-migration error = %v, want %s", preMigrationErr, expectedSQLState)
		}

		if err := normalizeLegacyAuthUsernameColumn(tx); err != nil {
			t.Fatalf("normalize legacy username: %v", err)
		}
		if err := normalizeLegacyAuthUsernameColumn(tx); err != nil {
			t.Fatalf("repeat normalization should be idempotent: %v", err)
		}

		for _, email := range []string{"first@example.com", "second@example.com"} {
			phone := strings.TrimSuffix(email, "@example.com") + "-phone"
			user := AuthUser{
				Email:        email,
				Phone:        phone,
				PasswordHash: "hash",
				Name:         "Current",
			}
			if err := tx.Create(&user).Error; err != nil {
				t.Fatalf("insert current auth user %s: %v", email, err)
			}
		}

		var nullable string
		var defaultValue *string
		if err := tx.Raw(`
			SELECT is_nullable, column_default
			FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = 'auth_users'
			  AND column_name = 'username'
		`).Row().Scan(&nullable, &defaultValue); err != nil {
			t.Fatalf("inspect normalized username: %v", err)
		}
		if nullable != "YES" || defaultValue != nil {
			t.Fatalf("username state = nullable %q default %v, want YES and nil", nullable, defaultValue)
		}

		var nullUsernames int64
		if err := tx.Raw(`
			SELECT COUNT(*) FROM auth_users
			WHERE email IN ('first@example.com', 'second@example.com')
			  AND username IS NULL
		`).Scan(&nullUsernames).Error; err != nil {
			t.Fatalf("count null usernames: %v", err)
		}
		if nullUsernames != 2 {
			t.Fatalf("new null usernames = %d, want 2", nullUsernames)
		}

		var legacyUsername string
		if err := tx.Raw(`
			SELECT username FROM auth_users WHERE email = 'legacy@example.com'
		`).Scan(&legacyUsername).Error; err != nil {
			t.Fatalf("read legacy username: %v", err)
		}
		if legacyUsername != "legacy" {
			t.Fatalf("legacy username = %q, want legacy", legacyUsername)
		}
		if !tx.Migrator().HasIndex("auth_users", "idx_auth_users_username") {
			t.Fatal("legacy username unique index was removed")
		}
	})
}

func testCurrentAuthSchemaMigrationPostgres(t *testing.T, db *gorm.DB) {
	t.Helper()

	runInTemporaryPostgresSchema(t, db, func(tx *gorm.DB) {
		if err := tx.Exec(`
			CREATE TABLE auth_users (
				id BIGSERIAL PRIMARY KEY,
				email TEXT NOT NULL UNIQUE,
				phone TEXT NOT NULL UNIQUE,
				password_hash TEXT NOT NULL,
				name TEXT NOT NULL,
				avatar_url TEXT DEFAULT '',
				created_at TIMESTAMPTZ
			)
		`).Error; err != nil {
			t.Fatalf("create current auth table: %v", err)
		}
		if err := normalizeLegacyAuthUsernameColumn(tx); err != nil {
			t.Fatalf("current schema should be a no-op: %v", err)
		}
	})
}

func runInTemporaryPostgresSchema(t *testing.T, db *gorm.DB, run func(*gorm.DB)) {
	t.Helper()

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("begin postgres transaction: %v", tx.Error)
	}
	defer tx.Rollback()

	schema := "auth_migration_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := tx.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatalf("create temporary schema: %v", err)
	}
	if err := tx.Exec(`SET LOCAL search_path TO "` + schema + `"`).Error; err != nil {
		t.Fatalf("set temporary search path: %v", err)
	}
	run(tx)
}
