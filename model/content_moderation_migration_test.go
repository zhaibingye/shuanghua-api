package model

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestMigrateModerationViolationUniqueIndexOnSQLite(t *testing.T) {
	previousDB := DB
	previousDatabaseType := common.MainDatabaseType()
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousDatabaseType)
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&ModerationViolation{}))
	require.NoError(t, db.Migrator().DropIndex(&ModerationViolation{}, "idx_moderation_violations_user_conversation_actor"))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX idx_moderation_violations_user_conversation_actor ON moderation_violations (conversation_id, user_violation, actor)").Error)

	rows := []ModerationViolation{
		{UserID: 1, ConversationID: "conversation", TurnID: 1, Actor: "user", UserViolation: true, Decision: "block", Severity: "high", Status: ModerationViolationActive, CreatedAt: 1, ExpiresAt: 10},
		{UserID: 1, ConversationID: "conversation", TurnID: 1, Actor: "assistant", UserViolation: true, Decision: "block", Severity: "high", Status: ModerationViolationActive, CreatedAt: 2, ExpiresAt: 10},
	}
	for i := range rows {
		require.NoError(t, db.Create(&rows[i]).Error)
	}

	require.NoError(t, migrateModerationViolationUniqueIndex())
	require.NoError(t, migrateModerationViolationUniqueIndex())

	var count int64
	require.NoError(t, db.Model(&ModerationViolation{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)

	var indexes []struct {
		Name   string `gorm:"column:name"`
		Unique int    `gorm:"column:unique"`
	}
	require.NoError(t, db.Raw("PRAGMA index_list('moderation_violations')").Scan(&indexes).Error)
	for _, index := range indexes {
		if index.Name != "idx_moderation_violations_user_conversation_actor" {
			continue
		}
		assert.Equal(t, 1, index.Unique)
		var columns []struct {
			Name string `gorm:"column:name"`
		}
		require.NoError(t, db.Raw("PRAGMA index_info('idx_moderation_violations_user_conversation_actor')").Scan(&columns).Error)
		columnNames := make([]string, 0, len(columns))
		for _, column := range columns {
			columnNames = append(columnNames, column.Name)
		}
		assert.Equal(t, []string{"user_id", "conversation_id", "turn_id", "user_violation"}, columnNames)
		return
	}
	require.FailNow(t, "moderation violation unique index is missing")
}

func testModerationUniqueMigrationNonPostgreSQL(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(
		&ModerationNotification{},
		&ModerationTokenState{},
		&ModerationAccountState{},
		&ModerationUserRecord{},
		&ModerationJob{},
	))

	notification := ModerationNotification{
		ViolationID: 101,
		AlertType:   "email",
		Recipient:   "admin@example.com",
		DedupeKey:   "d1e2d3u4p5e6k7e8y9",
		Status:      "pending",
	}
	require.NoError(t, db.Create(&notification).Error)

	for range 2 {
		require.NoError(t, migrateModerationUniqueConstraints(db))
		require.NoError(t, db.AutoMigrate(&ModerationNotification{}))
	}

	var preserved ModerationNotification
	require.NoError(t, db.Where("id = ?", notification.ID).First(&preserved).Error)
	assert.Equal(t, "d1e2d3u4p5e6k7e8y9", preserved.DedupeKey)
	assert.Equal(t, "admin@example.com", preserved.Recipient)
}

func TestMigrateModerationUniqueConstraintsSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	testModerationUniqueMigrationNonPostgreSQL(t, db)
}

func TestMigrateModerationUniqueConstraintsMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is not configured")
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	testModerationUniqueMigrationNonPostgreSQL(t, db)
}

func TestMigrateModerationUniqueConstraintsPostgreSQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}

	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true,
	}), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	tests := []struct {
		name       string
		prepareOld func(*testing.T, *gorm.DB)
	}{
		{name: "fresh"},
		{
			name: "postgres_default_constraint_moderation_notifications",
			prepareOld: func(t *testing.T, tx *gorm.DB) {
				t.Helper()
				_ = tx.Migrator().DropIndex(&ModerationNotification{}, "idx_moderation_notifications_dedupe")
				require.NoError(t, tx.Exec(
					"ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (?)",
					clause.Table{Name: "moderation_notifications"},
					clause.Column{Name: "moderation_notifications_dedupe_key_key"},
					clause.Column{Name: "dedupe_key"},
				).Error)
			},
		},
		{
			name: "gorm_named_constraint_moderation_notifications",
			prepareOld: func(t *testing.T, tx *gorm.DB) {
				t.Helper()
				require.NoError(t, tx.Exec(
					"ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (?)",
					clause.Table{Name: "moderation_notifications"},
					clause.Column{Name: "uni_moderation_notifications_dedupe_key"},
					clause.Column{Name: "dedupe_key"},
				).Error)
			},
		},
		{
			name: "legacy_idx_named_constraint_moderation_notifications",
			prepareOld: func(t *testing.T, tx *gorm.DB) {
				t.Helper()
				_ = tx.Migrator().DropIndex(&ModerationNotification{}, "idx_moderation_notifications_dedupe")
				require.NoError(t, tx.Exec(
					"ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (?)",
					clause.Table{Name: "moderation_notifications"},
					clause.Column{Name: "idx_moderation_notifications_dedupe"},
					clause.Column{Name: "dedupe_key"},
				).Error)
			},
		},
		{
			name: "postgres_default_constraint_moderation_token_states",
			prepareOld: func(t *testing.T, tx *gorm.DB) {
				t.Helper()
				_ = tx.Migrator().DropIndex(&ModerationTokenState{}, "idx_moderation_token_states_token_id")
				require.NoError(t, tx.Exec(
					"ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (?)",
					clause.Table{Name: "moderation_token_states"},
					clause.Column{Name: "moderation_token_states_token_id_key"},
					clause.Column{Name: "token_id"},
				).Error)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := db.Begin()
			require.NoError(t, tx.Error)
			t.Cleanup(func() { _ = tx.Rollback().Error })

			schemaName := fmt.Sprintf("moderation_migration_%d", time.Now().UnixNano())
			require.NoError(t, tx.Exec(
				"CREATE SCHEMA ?",
				clause.Table{Name: schemaName},
			).Error)
			require.NoError(t, tx.Exec(
				"SET LOCAL search_path TO ?",
				clause.Table{Name: schemaName},
			).Error)

			require.NoError(t, migrateModerationUniqueConstraints(tx))
			require.NoError(t, tx.AutoMigrate(
				&ModerationNotification{},
				&ModerationTokenState{},
				&ModerationAccountState{},
				&ModerationUserRecord{},
				&ModerationJob{},
			))

			notification := ModerationNotification{
				ViolationID: 202,
				AlertType:   "webhook",
				Recipient:   "ops@example.com",
				DedupeKey:   "testdedupekey1234567890abcdef",
				Status:      "pending",
			}
			require.NoError(t, tx.Create(&notification).Error)

			if tt.prepareOld != nil {
				tt.prepareOld(t, tx)
			}

			// Run migration and AutoMigrate twice to ensure idempotency and compatibility
			for range 2 {
				require.NoError(t, migrateModerationUniqueConstraints(tx))
				require.NoError(t, tx.AutoMigrate(
					&ModerationNotification{},
					&ModerationTokenState{},
					&ModerationAccountState{},
					&ModerationUserRecord{},
					&ModerationJob{},
				))
			}

			var preserved ModerationNotification
			require.NoError(t, tx.Where("id = ?", notification.ID).First(&preserved).Error)
			assert.Equal(t, "testdedupekey1234567890abcdef", preserved.DedupeKey)
			assert.Equal(t, "ops@example.com", preserved.Recipient)

			// Ensure no unique constraints remain on dedupe_key
			var constraintCount int64
			require.NoError(t, tx.Raw(`
SELECT count(*)
FROM pg_catalog.pg_constraint AS constraint_meta
WHERE constraint_meta.conrelid = to_regclass('moderation_notifications')
  AND constraint_meta.contype = 'u'
  AND cardinality(constraint_meta.conkey) = 1
  AND EXISTS (
      SELECT 1
      FROM pg_catalog.pg_attribute AS attribute_meta
      WHERE attribute_meta.attrelid = constraint_meta.conrelid
        AND attribute_meta.attnum = constraint_meta.conkey[1]
        AND attribute_meta.attname = 'dedupe_key'
  )`).Scan(&constraintCount).Error)
			assert.Zero(t, constraintCount)

			// Ensure standalone unique index exists
			var indexDefinition string
			require.NoError(t, tx.Raw(`
SELECT indexdef
FROM pg_catalog.pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'moderation_notifications'
  AND indexname = 'idx_moderation_notifications_dedupe'`).Scan(&indexDefinition).Error)
			assert.Contains(t, strings.ToLower(indexDefinition), "unique index")
		})
	}
}
