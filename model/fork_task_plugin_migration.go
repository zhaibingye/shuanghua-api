package model

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

// migrateForkTaskPlugins resolves the historical MediaKit/type-61 collision
// once, before channel caches and pollers start. Task IDs and billing snapshots
// remain intact; the new plugin resumes the persisted upstream phase.
func migrateForkTaskPlugins(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		const migrationKey = "ForkTaskPluginsV1"
		var marker Option
		err := tx.Where(&Option{Key: migrationKey}).First(&marker).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var channels []Channel
		if err := tx.Where("type = ?", constant.ChannelTypeTaskPlugin).Find(&channels).Error; err != nil {
			return err
		}
		for _, channel := range channels {
			setting := channel.GetSetting()
			if setting.TaskPluginKey != "" {
				continue
			}
			setting.TaskPluginKey = constant.TaskPluginDoubaoMediaKit
			channel.SetSetting(setting)
			if err := tx.Model(&Channel{}).Where("id = ?", channel.Id).Update("setting", channel.Setting).Error; err != nil {
				return err
			}
		}
		var tasks []Task
		if err := tx.Where("platform = ?", "61").FindInBatches(&tasks, 100, func(batch *gorm.DB, _ int) error {
			for _, task := range tasks {
				parts := strings.Split(task.GetUpstreamTaskID(), ":")
				if len(parts) < 5 || parts[0] != "dmk" || parts[1] != "v1" {
					return fmt.Errorf("task %s has invalid legacy MediaKit state", task.TaskID)
				}
				arkID, err := base64.RawURLEncoding.DecodeString(parts[4])
				if err != nil || len(arkID) == 0 {
					return fmt.Errorf("task %s has invalid Ark task ID", task.TaskID)
				}
				hash := sha256.Sum256([]byte(string(arkID) + "\x00" + parts[3]))
				state := map[string]any{"phase": "generation", "resolution": parts[3], "arkTaskId": string(arkID), "clientToken": "new-api-" + hex.EncodeToString(hash[:16]), "tokens": task.PrivateData.UsageTokens, "duration": task.PrivateData.UsageDurationSeconds}
				if parts[2] == "m" && len(parts) == 6 {
					mediaID, err := base64.RawURLEncoding.DecodeString(parts[5])
					if err != nil || len(mediaID) == 0 {
						return fmt.Errorf("task %s has invalid MediaKit task ID", task.TaskID)
					}
					state["phase"], state["mediaTaskId"] = "enhancement", string(mediaID)
				} else if parts[2] != "g" || len(parts) != 5 {
					return fmt.Errorf("task %s has invalid MediaKit phase", task.TaskID)
				}
				encoded, err := common.Marshal(state)
				if err != nil {
					return err
				}
				task.PrivateData.PluginState = encoded
				task.PrivateData.UpstreamTaskID = string(arkID)
				if err := batch.Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]any{"platform": constant.TaskPluginDoubaoMediaKit, "private_data": task.PrivateData}).Error; err != nil {
					return err
				}
			}
			return nil
		}).Error; err != nil {
			return err
		}
		return tx.Create(&Option{Key: migrationKey, Value: "1"}).Error
	})
}
