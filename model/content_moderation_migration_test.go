package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
