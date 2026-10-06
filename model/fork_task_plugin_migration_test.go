package model

import (
	"encoding/base64"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestForkTaskPluginMigration(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: fmt.Sprintf("fork_%d_", time.Now().UnixNano())}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = db.Migrator().DropTable(&Task{}, &Channel{}, &Option{}); require.NoError(t, sqlDB.Close()) })
			require.NoError(t, db.AutoMigrate(&Channel{}, &Task{}, &Option{}))
			legacy := Channel{Type: constant.ChannelTypeTaskPlugin, Name: "MediaKit", Key: "ark|media"}
			other := Channel{Type: constant.ChannelTypeTaskPlugin, Name: "Sora", Setting: common.GetPointer(`{"task_plugin_key":"sora"}`)}
			require.NoError(t, db.Create(&legacy).Error)
			require.NoError(t, db.Create(&other).Error)
			encode := base64.RawURLEncoding.EncodeToString
			tasks := []Task{
				{TaskID: "public-generation", Platform: "61", ChannelId: legacy.Id, Status: TaskStatusInProgress, Quota: 567, PrivateData: TaskPrivateData{UpstreamTaskID: "dmk:v1:g:1080p:" + encode([]byte("ark/one")), BillingContext: &TaskBillingContext{GroupRatio: 1.5}}},
				{TaskID: "public-enhancement", Platform: "61", ChannelId: legacy.Id, Status: TaskStatusInProgress, Quota: 789, PrivateData: TaskPrivateData{UpstreamTaskID: "dmk:v1:m:720p:" + encode([]byte("ark-two")) + ":" + encode([]byte("media-two")), UsageTokens: 1357, UsageDurationSeconds: 5}},
			}
			require.NoError(t, db.Create(&tasks).Error)
			for range 2 {
				require.NoError(t, migrateForkTaskPlugins(db))
			}
			var got Channel
			require.NoError(t, db.First(&got, legacy.Id).Error)
			assert.Equal(t, constant.TaskPluginDoubaoMediaKit, got.GetSetting().TaskPluginKey)
			assert.Equal(t, "ark|media", got.Key)
			require.NoError(t, db.First(&other, other.Id).Error)
			assert.Equal(t, "sora", other.GetSetting().TaskPluginKey)
			for index, original := range tasks {
				var task Task
				require.NoError(t, db.First(&task, original.ID).Error)
				assert.Equal(t, original.TaskID, task.TaskID)
				assert.Equal(t, original.Quota, task.Quota)
				assert.Equal(t, original.PrivateData.BillingContext, task.PrivateData.BillingContext)
				assert.Equal(t, constant.TaskPlatform(constant.TaskPluginDoubaoMediaKit), task.Platform)
				var state map[string]any
				require.NoError(t, common.Unmarshal(task.PrivateData.PluginState, &state))
				if index == 0 {
					assert.Equal(t, "generation", state["phase"])
					assert.Equal(t, "ark/one", task.GetUpstreamTaskID())
				} else {
					assert.Equal(t, "enhancement", state["phase"])
					assert.Equal(t, "media-two", state["mediaTaskId"])
					assert.Equal(t, float64(1357), state["tokens"])
					assert.Equal(t, float64(5), state["duration"])
				}
			}
			// Channels created after migration must never be mistaken for legacy rows.
			newChannel := Channel{Type: constant.ChannelTypeTaskPlugin, Name: "unbound"}
			require.NoError(t, db.Create(&newChannel).Error)
			require.NoError(t, migrateForkTaskPlugins(db))
			require.NoError(t, db.First(&newChannel, newChannel.Id).Error)
			assert.Empty(t, newChannel.GetSetting().TaskPluginKey)
		})
	}
}
