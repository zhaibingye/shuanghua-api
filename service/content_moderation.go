package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	ErrInvalidModerationUserRequest   = errors.New("invalid moderation user request")
	ErrModerationUserPermissionDenied = errors.New("moderation user permission denied")
)

const (
	defaultModerationViolationRetention = 7 * 24 * time.Hour
	moderationUserViolationThreshold    = 3
	moderationHighConfidence            = 0.85
	moderationAssistantConfidence       = 0.75
	moderationMaxCategories             = 32
	moderationMaxCategoryLength         = 128
	moderationMaxReasonCodeLength       = 128
	moderationJobBatchSize              = 20
	moderationMaxRequestBytes           = 4 << 20
	moderationMaxResponseBytes          = 1 << 20
	moderationConversationLookupLimit   = 200
	moderationConversationSeedMaxBytes  = 32 << 10
	moderationMaxManualViolationCount   = 1_000_000
	moderationAlertTypeViolation        = "violation"
	moderationAlertTypeAccountDisabled  = "account_disabled"
	moderationConversationHeader        = "X-Conversation-ID"
)

func getModerationViolationRetention() time.Duration {
	retention := setting.GetContentModerationSetting().GetViolationRetentionDuration()
	if retention <= 0 {
		return defaultModerationViolationRetention
	}
	return retention
}

func getModerationUserRawCounts(db *gorm.DB, cutoff int64, userID int) (map[int]moderationUserRawCount, error) {
	if db == nil {
		return nil, errors.New("moderation database is unavailable")
	}
	now := common.GetTimestamp()
	query := db.Model(&model.ModerationViolation{}).
		Select("user_id, COUNT(DISTINCT conversation_id) AS violation_count, MAX(created_at) AS last_violation_at").
		Where("user_violation = ? AND status = ? AND created_at >= ? AND expires_at > ?", true, model.ModerationViolationActive, cutoff, now).
		Group("user_id")
	if userID > 0 {
		query = query.Where("user_id = ?", userID)
	}
	var rows []moderationUserRawCount
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[int]moderationUserRawCount, len(rows))
	for _, row := range rows {
		if row.UserID > 0 {
			counts[row.UserID] = row
		}
	}
	return counts, nil
}

type moderationUserRawConversation struct {
	UserID         int    `gorm:"column:user_id"`
	ConversationID string `gorm:"column:conversation_id"`
}

func getModerationUserRawConversationKeys(db *gorm.DB, cutoff int64, userID int) (map[int]map[string]struct{}, error) {
	if db == nil {
		return nil, errors.New("moderation database is unavailable")
	}
	now := common.GetTimestamp()
	query := db.Model(&model.ModerationViolation{}).
		Select("user_id, conversation_id").
		Where("user_violation = ? AND status = ? AND created_at >= ? AND expires_at > ?", true, model.ModerationViolationActive, cutoff, now).
		Distinct()
	if userID > 0 {
		query = query.Where("user_id = ?", userID)
	}
	var rows []moderationUserRawConversation
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	keys := make(map[int]map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.UserID <= 0 || row.ConversationID == "" {
			continue
		}
		if keys[row.UserID] == nil {
			keys[row.UserID] = make(map[string]struct{})
		}
		keys[row.UserID][row.ConversationID] = struct{}{}
	}
	return keys, nil
}

func encodeModerationUserConversationSnapshot(keys map[string]struct{}) (string, error) {
	values := make([]string, 0, len(keys))
	for key := range keys {
		if key != "" {
			values = append(values, key)
		}
	}
	sort.Strings(values)
	data, err := common.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeModerationUserConversationSnapshot(record *model.ModerationUserRecord) (map[string]struct{}, bool) {
	if record == nil || !record.OverrideActive || strings.TrimSpace(string(record.ViolationConversationSnapshot)) == "" {
		return nil, false
	}
	var snapshot []string
	if err := common.Unmarshal([]byte(string(record.ViolationConversationSnapshot)), &snapshot); err != nil {
		return nil, false
	}
	keys := make(map[string]struct{}, len(snapshot))
	for _, key := range snapshot {
		if key != "" {
			keys[key] = struct{}{}
		}
	}
	return keys, true
}

func clampModerationUserViolationCount(count int64) int {
	if count <= 0 {
		return 0
	}
	if count >= moderationMaxManualViolationCount {
		return moderationMaxManualViolationCount
	}
	return int(count)
}

func effectiveModerationUserViolationCount(rawCount int64, record *model.ModerationUserRecord, currentKeys map[string]struct{}) int {
	if rawCount < 0 {
		rawCount = 0
	}
	if record == nil || !record.OverrideActive {
		return clampModerationUserViolationCount(rawCount)
	}
	if snapshot, initialized := decodeModerationUserConversationSnapshot(record); initialized {
		newConversationCount := int64(0)
		for conversationKey := range currentKeys {
			if _, exists := snapshot[conversationKey]; !exists {
				newConversationCount++
			}
		}
		return clampModerationUserViolationCount(int64(record.ViolationCountOverride) + newConversationCount)
	}
	// Records written before conversation snapshots were introduced retain the
	// old numeric baseline. Keep their historical behavior until the next edit,
	// which writes a durable conversation snapshot.
	count := int64(record.ViolationCountOverride) + rawCount - int64(record.RawViolationCountAtEdit)
	return clampModerationUserViolationCount(count)
}

func countEffectiveModerationUserViolations(db *gorm.DB, userID int, cutoff int64) (int64, error) {
	counts, err := getModerationUserRawCounts(db, cutoff, userID)
	if err != nil {
		return 0, err
	}
	conversationKeys, err := getModerationUserRawConversationKeys(db, cutoff, userID)
	if err != nil {
		return 0, err
	}
	rawCount := int64(0)
	if raw, ok := counts[userID]; ok {
		rawCount = raw.ViolationCount
	}
	var record model.ModerationUserRecord
	result := db.Where("user_id = ?", userID).Limit(1).Find(&record)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		return rawCount, nil
	}
	return int64(effectiveModerationUserViolationCount(rawCount, &record, conversationKeys[userID])), nil
}

func moderationUserRecordView(record *model.ModerationUserRecord, user *model.User, raw moderationUserRawCount, currentKeys map[string]struct{}) ModerationUserListItem {
	actualCount := raw.ViolationCount
	if actualCount < 0 {
		actualCount = 0
	}
	if actualCount > moderationMaxManualViolationCount {
		actualCount = moderationMaxManualViolationCount
	}
	effectiveCount := effectiveModerationUserViolationCount(actualCount, record, currentKeys)
	maxCount := effectiveCount
	if record != nil && record.MaxViolationCount > maxCount {
		maxCount = record.MaxViolationCount
	}
	username := ""
	displayName := ""
	email := ""
	accountStatus := 0
	if record != nil {
		username = record.UsernameSnapshot
		displayName = record.DisplayNameSnapshot
		email = record.EmailSnapshot
	}
	if user != nil {
		username = user.Username
		displayName = user.DisplayName
		email = user.Email
		accountStatus = user.Status
	}
	lastViolationAt := raw.LastViolationAt
	if record != nil && record.LastViolationAt > lastViolationAt {
		lastViolationAt = record.LastViolationAt
	}
	view := ModerationUserListItem{
		ViolationCount:       effectiveCount,
		ActualViolationCount: int(actualCount),
		MaxViolationCount:    maxCount,
		LastViolationAt:      lastViolationAt,
		AccountStatus:        accountStatus,
	}
	if record != nil {
		view.RecordID = record.ID
		view.UserID = record.UserID
		view.Note = record.Note
		view.ArchivedAt = record.ArchivedAt
		view.CreatedAt = record.CreatedAt
		view.UpdatedAt = record.UpdatedAt
		if record.ID > 0 && effectiveCount == 0 {
			view.RecordStatus = "history"
		} else {
			view.RecordStatus = "active"
		}
	}
	if view.UserID == 0 && user != nil {
		view.UserID = user.Id
	}
	view.Username = username
	view.DisplayName = displayName
	view.Email = email
	return view
}

func loadModerationUsers(userIDs []int) (map[int]*model.User, error) {
	users := make(map[int]*model.User, len(userIDs))
	if len(userIDs) == 0 {
		return users, nil
	}
	var rows []model.User
	if err := model.DB.Unscoped().Where("id IN ?", userIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		users[rows[i].Id] = &rows[i]
	}
	return users, nil
}

func updateModerationUserRecordSnapshots(record *model.ModerationUserRecord, user *model.User) {
	if record == nil || user == nil {
		return
	}
	record.UsernameSnapshot = user.Username
	record.DisplayNameSnapshot = user.DisplayName
	record.EmailSnapshot = user.Email
}

func validateModerationUserMutation(user *model.User, adminID, adminRole int) error {
	if user == nil || adminID <= 0 || adminRole <= 0 {
		return fmt.Errorf("%w: invalid operator or user", ErrModerationUserPermissionDenied)
	}
	if user.Role == common.RoleRootUser {
		return fmt.Errorf("%w: cannot change the root account", ErrModerationUserPermissionDenied)
	}
	if adminRole != common.RoleRootUser && adminRole <= user.Role {
		return fmt.Errorf("%w: administrator cannot manage this account", ErrModerationUserPermissionDenied)
	}
	return nil
}

func syncModerationUserRecord(userID int, now int64) error {
	return syncModerationUserRecordWithHistory(userID, now, false)
}

func syncModerationUserRecordWithHistory(userID int, now int64, includeHistorical bool) error {
	if userID <= 0 {
		return errors.New("invalid moderation user")
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	cutoff := now - int64(getModerationViolationRetention().Seconds())
	counts, err := getModerationUserRawCounts(model.DB, cutoff, userID)
	if err != nil {
		return err
	}
	conversationKeysByUser, err := getModerationUserRawConversationKeys(model.DB, cutoff, userID)
	if err != nil {
		return err
	}
	raw := counts[userID]
	currentKeys := conversationKeysByUser[userID]
	record, err := model.GetModerationUserRecord(userID)
	if err != nil {
		return err
	}
	var user model.User
	userErr := model.DB.Unscoped().First(&user, userID).Error
	if userErr != nil && !errors.Is(userErr, gorm.ErrRecordNotFound) {
		return userErr
	}
	var userPtr *model.User
	if userErr == nil {
		userPtr = &user
	}
	if record == nil {
		historicalCount := raw.ViolationCount
		if includeHistorical && historicalCount == 0 {
			if err := model.DB.Model(&model.ModerationViolation{}).
				Where("user_id = ? AND user_violation = ? AND status = ? AND created_at >= ? AND expires_at > ?", userID, true, model.ModerationViolationActive, cutoff, now).
				Distinct("conversation_id").Count(&historicalCount).Error; err != nil {
				return err
			}
		}
		if historicalCount == 0 {
			return nil
		}
		maxCount := raw.ViolationCount
		if historicalCount > maxCount {
			maxCount = historicalCount
		}
		if maxCount > moderationMaxManualViolationCount {
			maxCount = moderationMaxManualViolationCount
		}
		if raw.LastViolationAt == 0 {
			var latest model.ModerationViolation
			if err := model.DB.Where("user_id = ? AND user_violation = ? AND status = ? AND created_at >= ? AND expires_at > ?", userID, true, model.ModerationViolationActive, cutoff, now).
				Order("created_at desc, id desc").Limit(1).Find(&latest).Error; err != nil {
				return err
			}
			raw.LastViolationAt = latest.CreatedAt
		}
		record = &model.ModerationUserRecord{
			UserID:            userID,
			MaxViolationCount: int(maxCount),
			LastViolationAt:   raw.LastViolationAt,
			ArchivedAt:        now,
			LastSyncedAt:      now,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if raw.ViolationCount > 0 {
			record.ArchivedAt = 0
		}
		updateModerationUserRecordSnapshots(record, userPtr)
		return model.DB.Create(record).Error
	}
	effectiveCount := effectiveModerationUserViolationCount(raw.ViolationCount, record, currentKeys)
	if effectiveCount > record.MaxViolationCount {
		record.MaxViolationCount = effectiveCount
	}
	if raw.LastViolationAt > record.LastViolationAt {
		record.LastViolationAt = raw.LastViolationAt
	}
	if effectiveCount == 0 {
		if record.ArchivedAt == 0 {
			record.ArchivedAt = now
		}
	} else {
		record.ArchivedAt = 0
	}
	updateModerationUserRecordSnapshots(record, userPtr)
	return model.DB.Model(record).UpdateColumns(map[string]any{
		"max_violation_count":   record.MaxViolationCount,
		"last_violation_at":     record.LastViolationAt,
		"username_snapshot":     record.UsernameSnapshot,
		"display_name_snapshot": record.DisplayNameSnapshot,
		"email_snapshot":        record.EmailSnapshot,
		"archived_at":           record.ArchivedAt,
		"last_synced_at":        now,
	}).Error
}

func SyncModerationUserRecords() error {
	now := common.GetTimestamp()
	cutoff := now - int64(getModerationViolationRetention().Seconds())
	rawCounts, err := getModerationUserRawCounts(model.DB, cutoff, 0)
	if err != nil {
		return err
	}
	var records []model.ModerationUserRecord
	if err := model.DB.Find(&records).Error; err != nil {
		return err
	}
	userIDs := make(map[int]struct{}, len(records)+len(rawCounts))
	for _, record := range records {
		userIDs[record.UserID] = struct{}{}
	}
	for userID := range rawCounts {
		userIDs[userID] = struct{}{}
	}
	for userID := range userIDs {
		if err := syncModerationUserRecord(userID, now); err != nil {
			return err
		}
	}
	return nil
}

func ListModerationUsers(status string, userID, limit, offset int) ([]ModerationUserListItem, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	if status != "history" && status != "all" {
		status = "active"
	}
	now := common.GetTimestamp()
	cutoff := now - int64(getModerationViolationRetention().Seconds())
	rawCounts, err := getModerationUserRawCounts(model.DB, cutoff, userID)
	if err != nil {
		return nil, 0, err
	}
	conversationKeysByUser, err := getModerationUserRawConversationKeys(model.DB, cutoff, userID)
	if err != nil {
		return nil, 0, err
	}
	recordQuery := model.DB
	if userID > 0 {
		recordQuery = recordQuery.Where("user_id = ?", userID)
	}
	var records []model.ModerationUserRecord
	if err := recordQuery.Find(&records).Error; err != nil {
		return nil, 0, err
	}

	recordByUserID := make(map[int]*model.ModerationUserRecord, len(records))
	candidateIDs := make(map[int]struct{}, len(records)+len(rawCounts))
	for i := range records {
		recordByUserID[records[i].UserID] = &records[i]
		candidateIDs[records[i].UserID] = struct{}{}
	}
	for id := range rawCounts {
		candidateIDs[id] = struct{}{}
	}
	items := make([]ModerationUserListItem, 0, len(candidateIDs))
	for id := range candidateIDs {
		record := recordByUserID[id]
		raw := rawCounts[id]
		var ephemeral model.ModerationUserRecord
		if record == nil {
			ephemeral = model.ModerationUserRecord{UserID: id, MaxViolationCount: effectiveModerationUserViolationCount(raw.ViolationCount, nil, conversationKeysByUser[id]), LastViolationAt: raw.LastViolationAt}
			record = &ephemeral
		}
		item := moderationUserRecordView(record, nil, raw, conversationKeysByUser[id])
		if item.RecordStatus == "" {
			item.RecordStatus = "active"
		}
		if status == "active" && item.ViolationCount == 0 {
			continue
		}
		if status == "history" && (item.RecordStatus != "history" || item.ViolationCount != 0) {
			continue
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].LastViolationAt != items[j].LastViolationAt {
			return items[i].LastViolationAt > items[j].LastViolationAt
		}
		return items[i].UserID < items[j].UserID
	})
	total := int64(len(items))
	if offset >= len(items) {
		return []ModerationUserListItem{}, total, nil
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	pageItems := items[offset:end]
	pageUserIDs := make([]int, 0, len(pageItems))
	for _, item := range pageItems {
		pageUserIDs = append(pageUserIDs, item.UserID)
	}
	users, err := loadModerationUsers(pageUserIDs)
	if err != nil {
		return nil, 0, err
	}
	for i := range pageItems {
		id := pageItems[i].UserID
		record := recordByUserID[id]
		var ephemeral model.ModerationUserRecord
		if record == nil {
			raw := rawCounts[id]
			ephemeral = model.ModerationUserRecord{UserID: id, MaxViolationCount: clampModerationUserViolationCount(raw.ViolationCount), LastViolationAt: raw.LastViolationAt}
			record = &ephemeral
		}
		pageItems[i] = moderationUserRecordView(record, users[id], rawCounts[id], conversationKeysByUser[id])
		if pageItems[i].RecordStatus == "" {
			pageItems[i].RecordStatus = "active"
		}
	}
	return pageItems, total, nil
}

func GetModerationUserDetail(userID int, conversationMode string) (*ModerationUserDetail, error) {
	if userID <= 0 {
		return nil, errors.New("invalid moderation user")
	}
	now := common.GetTimestamp()
	cutoff := now - int64(getModerationViolationRetention().Seconds())
	counts, err := getModerationUserRawCounts(model.DB, cutoff, userID)
	if err != nil {
		return nil, err
	}
	conversationKeysByUser, err := getModerationUserRawConversationKeys(model.DB, cutoff, userID)
	if err != nil {
		return nil, err
	}
	record, err := model.GetModerationUserRecord(userID)
	if err != nil {
		return nil, err
	}
	var user model.User
	userErr := model.DB.Unscoped().First(&user, userID).Error
	if userErr != nil && !errors.Is(userErr, gorm.ErrRecordNotFound) {
		return nil, userErr
	}
	if record == nil && userErr != nil {
		return nil, gorm.ErrRecordNotFound
	}
	if record == nil {
		record = &model.ModerationUserRecord{UserID: userID}
	}
	var userPtr *model.User
	if userErr == nil {
		userPtr = &user
	}
	item := moderationUserRecordView(record, userPtr, counts[userID], conversationKeysByUser[userID])
	if item.RecordStatus == "" {
		item.RecordStatus = "active"
	}
	violations := make([]model.ModerationViolation, 0)
	if err := model.DB.Where("user_id = ? AND user_violation = ? AND created_at >= ? AND expires_at > ?", userID, true, cutoff, now).Order("created_at desc, id desc").Find(&violations).Error; err != nil {
		return nil, err
	}
	conversationQuery := model.DB.Where("user_id = ? AND last_activity_at >= ? AND expires_at > ?", userID, cutoff, now)
	if conversationMode != "all" {
		conversationIDs := make([]string, 0, len(violations))
		seen := make(map[string]struct{}, len(violations))
		for _, violation := range violations {
			if _, ok := seen[violation.ConversationID]; ok {
				continue
			}
			seen[violation.ConversationID] = struct{}{}
			conversationIDs = append(conversationIDs, violation.ConversationID)
		}
		if len(conversationIDs) == 0 {
			conversationQuery = conversationQuery.Where("1 = 0")
		} else {
			conversationQuery = conversationQuery.Where("conversation_id IN ?", conversationIDs)
		}
	}
	conversations := make([]model.ModerationConversation, 0)
	if err := conversationQuery.Order("last_activity_at desc, id desc").Find(&conversations).Error; err != nil {
		return nil, err
	}
	return &ModerationUserDetail{User: item, Conversations: conversations, Violations: violations}, nil
}

func UpdateModerationUserRecord(userID, adminID, adminRole, violationCount int, note string) error {
	if userID <= 0 {
		return fmt.Errorf("%w: invalid moderation user", ErrInvalidModerationUserRequest)
	}
	if violationCount < 0 || violationCount > moderationMaxManualViolationCount {
		return fmt.Errorf("%w: moderation violation count is out of range", ErrInvalidModerationUserRequest)
	}
	if len(note) > 65535 {
		return fmt.Errorf("%w: moderation user note is too long", ErrInvalidModerationUserRequest)
	}
	now := common.GetTimestamp()
	cutoff := now - int64(getModerationViolationRetention().Seconds())
	counts, err := getModerationUserRawCounts(model.DB, cutoff, userID)
	if err != nil {
		return err
	}
	conversationKeysByUser, err := getModerationUserRawConversationKeys(model.DB, cutoff, userID)
	if err != nil {
		return err
	}
	raw := counts[userID]
	currentKeys := conversationKeysByUser[userID]
	record, err := model.GetModerationUserRecord(userID)
	if err != nil {
		return err
	}
	var user model.User
	if err := model.DB.Unscoped().First(&user, userID).Error; err != nil {
		return err
	}
	if err := validateModerationUserMutation(&user, adminID, adminRole); err != nil {
		return err
	}
	snapshot, err := encodeModerationUserConversationSnapshot(currentKeys)
	if err != nil {
		return err
	}
	if record == nil {
		record = &model.ModerationUserRecord{UserID: userID, CreatedAt: now}
	}
	if violationCount > record.MaxViolationCount {
		record.MaxViolationCount = violationCount
	}
	record.ViolationCountOverride = violationCount
	record.OverrideActive = true
	record.RawViolationCountAtEdit = clampModerationUserViolationCount(raw.ViolationCount)
	record.ViolationConversationSnapshot = model.ModerationText(snapshot)
	record.Note = note
	record.LastViolationAt = max(record.LastViolationAt, raw.LastViolationAt)
	record.LastSyncedAt = now
	if violationCount == 0 {
		record.ArchivedAt = now
	} else {
		record.ArchivedAt = 0
	}
	updateModerationUserRecordSnapshots(record, &user)
	updates := map[string]any{
		"violation_count_override":        record.ViolationCountOverride,
		"override_active":                 record.OverrideActive,
		"raw_violation_count_at_edit":     record.RawViolationCountAtEdit,
		"violation_conversation_snapshot": record.ViolationConversationSnapshot,
		"max_violation_count":             record.MaxViolationCount,
		"last_violation_at":               record.LastViolationAt,
		"username_snapshot":               record.UsernameSnapshot,
		"display_name_snapshot":           record.DisplayNameSnapshot,
		"email_snapshot":                  record.EmailSnapshot,
		"note":                            record.Note,
		"archived_at":                     record.ArchivedAt,
		"last_synced_at":                  now,
		"updated_at":                      now,
	}
	if record.ID == 0 {
		return model.DB.Create(record).Error
	}
	return model.DB.Model(record).Updates(updates).Error
}

func DeleteModerationUserHistory(userID, adminID, adminRole int) error {
	if userID <= 0 {
		return fmt.Errorf("%w: invalid moderation user", ErrInvalidModerationUserRequest)
	}
	var user model.User
	if err := model.DB.Unscoped().First(&user, userID).Error; err != nil {
		return err
	}
	if err := validateModerationUserMutation(&user, adminID, adminRole); err != nil {
		return err
	}
	now := common.GetTimestamp()
	if err := syncModerationUserRecord(userID, now); err != nil {
		return err
	}
	record, err := model.GetModerationUserRecord(userID)
	if err != nil {
		return err
	}
	if record == nil {
		return gorm.ErrRecordNotFound
	}
	if record.ArchivedAt == 0 {
		return model.ErrModerationUserHistoryOnly
	}
	return model.DeleteModerationUserHistoryIfArchived(
		userID,
		now-int64(getModerationViolationRetention().Seconds()),
		now,
	)
}

func SetModerationUserAccountStatus(userID, adminID, adminRole int, enabled bool, reason string) error {
	if userID <= 0 || adminID <= 0 {
		return fmt.Errorf("%w: invalid moderation account status request", ErrInvalidModerationUserRequest)
	}
	if err := validateModerationUserStatusReason(reason); err != nil {
		return err
	}
	var user model.User
	if err := model.DB.Unscoped().First(&user, userID).Error; err != nil {
		return err
	}
	if user.DeletedAt.Valid {
		return errors.New("cannot change a deleted account status")
	}
	if err := validateModerationUserMutation(&user, adminID, adminRole); err != nil {
		return err
	}
	if enabled {
		if err := model.SetUserAccountStatusForModeration(userID, common.UserStatusEnabled, common.GetTimestamp()); err != nil {
			return err
		}
	} else if err := model.SetUserAccountStatusForModeration(userID, common.UserStatusDisabled, common.GetTimestamp()); err != nil {
		return err
	}
	if _, err := model.RevokeAllUserSessions(userID, "content_moderation_admin_status"); err != nil {
		return err
	}
	if err := model.InvalidateUserTokensCache(userID); err != nil {
		return err
	}
	if err := model.PublishUserAuthCache(userID); err != nil {
		return err
	}
	action := "disable_account"
	if enabled {
		action = "enable_account"
	}
	return model.DB.Create(&model.ModerationAction{
		AdminID:   adminID,
		UserID:    userID,
		Action:    action,
		Reason:    reason,
		CreatedAt: common.GetTimestamp(),
	}).Error
}

var moderationResponseWriterKey = constant.ContextKeyModerationCapture

type ModerationRequestContent struct {
	SystemPrompt string
	UserPrompt   string
}

type ModerationUserListItem struct {
	RecordID             int64  `json:"record_id"`
	UserID               int    `json:"user_id"`
	Username             string `json:"username"`
	DisplayName          string `json:"display_name"`
	Email                string `json:"email"`
	AccountStatus        int    `json:"account_status"`
	RecordStatus         string `json:"record_status"`
	ViolationCount       int    `json:"violation_count"`
	ActualViolationCount int    `json:"actual_violation_count"`
	MaxViolationCount    int    `json:"max_violation_count"`
	LastViolationAt      int64  `json:"last_violation_at"`
	Note                 string `json:"note"`
	ArchivedAt           int64  `json:"archived_at,omitempty"`
	CreatedAt            int64  `json:"created_at"`
	UpdatedAt            int64  `json:"updated_at"`
}

type ModerationUserDetail struct {
	User          ModerationUserListItem         `json:"user"`
	Conversations []model.ModerationConversation `json:"conversations"`
	Violations    []model.ModerationViolation    `json:"violations"`
}

type moderationUserRawCount struct {
	UserID          int   `gorm:"column:user_id"`
	ViolationCount  int64 `gorm:"column:violation_count"`
	LastViolationAt int64 `gorm:"column:last_violation_at"`
}

type ModerationCapture struct {
	gin.ResponseWriter
	mu   sync.Mutex
	data bytes.Buffer
}

func NewModerationCapture(writer gin.ResponseWriter) *ModerationCapture {
	return &ModerationCapture{ResponseWriter: writer}
}

func (w *ModerationCapture) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if n > 0 {
		w.mu.Lock()
		_, _ = w.data.Write(data[:n])
		w.mu.Unlock()
	}
	return n, err
}

func (w *ModerationCapture) WriteString(data string) (int, error) {
	n, err := w.ResponseWriter.WriteString(data)
	if n > 0 {
		w.mu.Lock()
		_, _ = w.data.WriteString(data[:n])
		w.mu.Unlock()
	}
	return n, err
}

func (w *ModerationCapture) ReadFrom(reader io.Reader) (int64, error) {
	if reader == nil {
		return 0, nil
	}
	buffer := make([]byte, 32*1024)
	var total int64
	for {
		readCount, readErr := reader.Read(buffer)
		if readCount > 0 {
			writeCount, writeErr := w.Write(buffer[:readCount])
			total += int64(writeCount)
			if writeErr != nil {
				return total, writeErr
			}
			if writeCount != readCount {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

func (w *ModerationCapture) Unwrap() http.ResponseWriter {
	if w == nil || w.ResponseWriter == nil {
		return nil
	}
	return w.ResponseWriter
}

func (w *ModerationCapture) Bytes() []byte {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.data.Bytes()...)
}

func BeginModerationCapture(c *gin.Context, request dto.Request) (string, bool) {
	if c == nil || request == nil || !moderationRequestSupported(request) {
		return "", false
	}
	config := setting.GetContentModerationSetting()
	if !config.HasModeratedChannels() && !common.GetContextKeyBool(c, constant.ContextKeyModerationEnabledAtStart) {
		return "", false
	}
	conversationID := common.GetContextKeyString(c, constant.ContextKeyModerationConversationID)
	if conversationID == "" {
		conversationID = ResolveModerationConversationID(c)
	}
	if conversationID == "" {
		return "", false
	}
	c.Header(moderationConversationHeader, conversationID)
	capture := NewModerationCapture(c.Writer)
	c.Writer = capture
	common.SetContextKey(c, constant.ContextKeyModerationCapture, capture)
	common.SetContextKey(c, constant.ContextKeyModerationConversationID, conversationID)
	return conversationID, true
}

func ResolveModerationConversationID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if cached := common.GetContextKeyString(c, constant.ContextKeyModerationConversationID); cached != "" {
		return cached
	}
	if value := normalizeConversationID(c.GetHeader(moderationConversationHeader)); value != "" {
		return cacheModerationConversationID(c, value, true)
	}
	for _, headerName := range []string{"X-Session-ID", "X-Chat-ID", "X-OpenCode-Session", "session_id", "chat_id"} {
		if value := normalizeConversationID(c.GetHeader(headerName)); value != "" {
			return cacheModerationConversationID(c, value, true)
		}
	}
	if storage, err := common.GetBodyStorage(c); err == nil {
		if data, bytesErr := storage.Bytes(); bytesErr == nil {
			common.SetContextKey(c, constant.ContextKeyModerationConversationEvidence, extractModerationConversationEvidence(data))
			if value, explicit := resolveModerationConversationIDFromJSON(data); value != "" {
				return cacheModerationConversationID(c, value, explicit)
			}
		}
	}
	return cacheModerationConversationID(c, moderationRequestConversationFallback(c), false)
}

func cacheModerationConversationID(c *gin.Context, conversationID string, explicit bool) string {
	common.SetContextKey(c, constant.ContextKeyModerationConversationID, conversationID)
	common.SetContextKey(c, constant.ContextKeyModerationConversationIDExplicit, explicit)
	c.Header(moderationConversationHeader, conversationID)
	return conversationID
}

func resolveModerationConversationIDFromJSON(data []byte) (string, bool) {
	var payload map[string]any
	if len(data) == 0 || common.Unmarshal(data, &payload) != nil {
		return "", false
	}
	for _, variant := range moderationPayloadVariants(payload) {
		for _, values := range []map[string]any{variant, moderationMetadata(variant)} {
			for _, key := range []string{"conversation_id", "conversationId", "conversation", "chat_id", "chatId", "session_id", "sessionId"} {
				if value, ok := values[key].(string); ok {
					if normalized := normalizeConversationID(value); normalized != "" {
						return normalized, true
					}
				}
			}
		}
	}

	// Some clients keep the conversation ID locally and send the complete
	// message history without an ID. The first user message is stable across
	// those requests, unlike the request ID, so use a keyed digest of it as a
	// deterministic fallback without exposing a dictionary-guessable hash.
	for _, variant := range moderationPayloadVariants(payload) {
		for _, key := range []string{"messages", "contents", "input"} {
			if seed := firstModerationUserText(variant[key]); seed != "" {
				return moderationConversationIDFromSeed(seed), false
			}
		}
		for _, key := range []string{"user_prompt", "userPrompt", "prompt"} {
			if seed := firstModerationUserText(variant[key]); seed != "" {
				return moderationConversationIDFromSeed(seed), false
			}
		}
	}
	return "", false
}

func moderationPayloadVariants(payload map[string]any) []map[string]any {
	if payload == nil {
		return nil
	}
	variants := []map[string]any{payload}
	for _, key := range []string{"generateContentRequest", "generate_content_request"} {
		if nested, ok := payload[key].(map[string]any); ok {
			variants = append(variants, nested)
		}
	}
	return variants
}

func moderationMetadata(payload map[string]any) map[string]any {
	metadata, _ := payload["metadata"].(map[string]any)
	return metadata
}

func firstModerationUserText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		for _, item := range typed {
			if text := firstModerationUserText(item); text != "" {
				return text
			}
		}
	case map[string]any:
		itemType, _ := typed["type"].(string)
		if strings.EqualFold(itemType, "output_text") {
			return ""
		}
		role, _ := typed["role"].(string)
		switch strings.ToLower(role) {
		case "system", "developer", "assistant", "model", "tool", "function":
			return ""
		}
		for _, key := range []string{"content", "text", "parts"} {
			if text := firstModerationUserText(typed[key]); text != "" {
				return text
			}
		}
		if itemType != "" && !strings.EqualFold(itemType, "message") {
			switch strings.ToLower(itemType) {
			case "input_text", "text":
				return strings.TrimSpace(openAIPromptText(typed))
			default:
				// Media-only parts do not identify a conversation. Continue
				// searching so a later text part can provide the seed.
				return ""
			}
		}
	}
	return ""
}

func moderationConversationIDFromSeed(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return ""
	}
	originalLength := len(seed)
	if len(seed) > moderationConversationSeedMaxBytes {
		// Keep both ends of oversized prompts. Hashing only the prefix would
		// collide for prompts with a shared beginning and equal length.
		prefixLength := moderationConversationSeedMaxBytes / 2
		suffixLength := moderationConversationSeedMaxBytes - prefixLength - 1
		seed = seed[:prefixLength] + "\x00" + seed[len(seed)-suffixLength:]
	}
	digest := common.GenerateHMAC(seed + "\x00" + strconv.Itoa(originalLength))
	return "conv_seed_" + digest
}

func ResolveModerationConversationIDForUser(c *gin.Context, userID int) string {
	conversationID := ResolveModerationConversationID(c)
	if c == nil || userID <= 0 || common.GetContextKeyBool(c, constant.ContextKeyModerationConversationIDExplicit) {
		return conversationID
	}

	evidence, ok := common.GetContextKeyType[moderationConversationEvidence](c, constant.ContextKeyModerationConversationEvidence)
	if !ok {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return conversationID
		}
		data, err := storage.Bytes()
		if err != nil {
			return conversationID
		}
		evidence = extractModerationConversationEvidence(data)
		common.SetContextKey(c, constant.ContextKeyModerationConversationEvidence, evidence)
	}
	if len(evidence.UserTexts) == 0 && len(evidence.AssistantTexts) == 0 {
		return conversationID
	}

	isContentSeed := strings.HasPrefix(conversationID, "conv_seed_")
	if isContentSeed && len(evidence.AssistantTexts) == 0 && len(evidence.UserTexts) <= 1 {
		// Do not merge two newly-created conversations merely because their
		// first prompts are identical. The request ID is used for the initial
		// turn; later requests are joined to it by history matching.
		return cacheModerationConversationID(c, moderationRequestConversationFallback(c), false)
	}

	userFingerprints := moderationEvidenceFingerprints(evidence.UserTexts)
	assistantFingerprints := moderationEvidenceFingerprints(evidence.AssistantTexts)
	turns, err := model.FindModerationTurnsByFingerprints(userID, userFingerprints, assistantFingerprints, common.GetTimestamp(), moderationConversationLookupLimit)
	if err != nil {
		logger.LogWarn(c, fmt.Sprintf("content moderation conversation fingerprint lookup failed: %v", err))
		return conversationID
	}
	if len(turns) == 0 {
		// Existing rows may predate the fingerprint columns. Keep a bounded,
		// compatibility fallback for those rows only.
		turns, err = model.FindRecentModerationTurnsByUser(userID, common.GetTimestamp(), moderationConversationLookupLimit)
		if err != nil {
			logger.LogWarn(c, fmt.Sprintf("content moderation conversation lookup failed: %v", err))
			return conversationID
		}
	}
	candidates := make(map[string]int)
	for i := range turns {
		if err := DecryptModerationTurnContent(&turns[i]); err != nil {
			continue
		}
		matchScore := moderationTurnConversationMatchScore(&turns[i], evidence)
		if matchScore == 0 {
			continue
		}
		if previous, ok := candidates[turns[i].ConversationKey]; !ok || matchScore > previous {
			candidates[turns[i].ConversationKey] = matchScore
		}
	}
	bestConversationKey := ""
	bestMatchScore := 0
	ambiguous := false
	for conversationKey, matchScore := range candidates {
		if matchScore > bestMatchScore {
			bestConversationKey = conversationKey
			bestMatchScore = matchScore
			ambiguous = false
			continue
		}
		if matchScore == bestMatchScore {
			ambiguous = true
		}
	}
	if bestConversationKey == "" || ambiguous {
		// Never bind a request to an arbitrary conversation when the evidence is
		// missing or equally strong. A content-derived seed is not a fresh ID:
		// two independent conversations can legitimately start with the same
		// text, so use the request ID for this new conversation.
		return cacheModerationConversationID(c, moderationRequestConversationFallback(c), false)
	}
	return cacheModerationConversationID(c, bestConversationKey, false)
}

func moderationRequestConversationFallback(c *gin.Context) string {
	reqID := common.GetContextKeyString(c, common.RequestIdKey)
	if reqID == "" {
		reqID = common.NewRequestId()
	}
	return "conv_" + reqID
}

type moderationConversationEvidence struct {
	UserTexts      []string
	AssistantTexts []string
}

func extractModerationConversationEvidence(data []byte) moderationConversationEvidence {
	var payload map[string]any
	if len(data) == 0 || common.Unmarshal(data, &payload) != nil {
		return moderationConversationEvidence{}
	}
	var evidence moderationConversationEvidence
	for _, variant := range moderationPayloadVariants(payload) {
		for _, key := range []string{"messages", "contents", "input"} {
			appendModerationConversationEvidence(variant[key], &evidence)
		}
		for _, key := range []string{"user_prompt", "userPrompt", "prompt"} {
			if text := openAIPromptText(variant[key]); strings.TrimSpace(text) != "" {
				evidence.UserTexts = append(evidence.UserTexts, strings.TrimSpace(text))
			}
		}
	}
	return evidence
}

func appendModerationConversationEvidence(value any, evidence *moderationConversationEvidence) {
	if evidence == nil {
		return
	}
	switch typed := value.(type) {
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			evidence.UserTexts = append(evidence.UserTexts, text)
		}
	case []any:
		for _, item := range typed {
			appendModerationConversationEvidenceItem(item, evidence)
		}
	case map[string]any:
		appendModerationConversationEvidenceItem(typed, evidence)
	}
}

func appendModerationConversationEvidenceItem(item any, evidence *moderationConversationEvidence) {
	itemMap, ok := item.(map[string]any)
	if !ok {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			evidence.UserTexts = append(evidence.UserTexts, strings.TrimSpace(text))
		}
		return
	}
	itemType, _ := itemMap["type"].(string)
	if strings.EqualFold(itemType, "output_text") {
		return
	}
	role, _ := itemMap["role"].(string)
	text := moderationJSONItemText(itemMap)
	if strings.TrimSpace(text) == "" {
		return
	}
	switch strings.ToLower(role) {
	case "assistant", "model":
		evidence.AssistantTexts = append(evidence.AssistantTexts, text)
	case "system", "developer", "tool", "function":
		return
	default:
		evidence.UserTexts = append(evidence.UserTexts, text)
	}
}

func moderationJSONItemText(item map[string]any) string {
	for _, key := range []string{"content", "text", "parts"} {
		if text := strings.TrimSpace(openAIPromptText(item[key])); text != "" {
			return text
		}
	}
	return ""
}

func moderationEvidenceFingerprints(values []string) []string {
	fingerprints := make([]string, 0, len(values)+1)
	seen := make(map[string]struct{}, len(values)+1)
	add := func(value string) {
		normalized := normalizeModerationMatchText(value)
		if normalized == "" {
			return
		}
		fingerprint := common.GenerateHMAC(normalized)
		if _, ok := seen[fingerprint]; ok {
			return
		}
		seen[fingerprint] = struct{}{}
		fingerprints = append(fingerprints, fingerprint)
	}
	for _, value := range values {
		add(value)
	}
	add(strings.Join(values, "\n"))
	return fingerprints
}

func moderationEvidenceContainsText(values []string, target string) bool {
	target = normalizeModerationMatchText(target)
	if target == "" {
		return false
	}
	for _, value := range values {
		if normalizeModerationMatchText(value) == target {
			return true
		}
	}
	joined := normalizeModerationMatchText(strings.Join(values, "\n"))
	return len(target) >= 16 && strings.Contains(joined, target)
}

func moderationTurnConversationMatchScore(turn *model.ModerationTurn, evidence moderationConversationEvidence) int {
	if turn == nil {
		return 0
	}
	userText := string(turn.UserPrompt)
	assistantText := string(turn.AssistantReply)
	userMatch := moderationEvidenceContainsText(evidence.UserTexts, userText)
	assistantMatch := moderationEvidenceContainsText(evidence.AssistantTexts, assistantText)

	if userMatch && assistantMatch {
		return 4
	}
	// An exact, sufficiently distinctive assistant response is the safest way
	// to recover a conversation after a client truncates old user messages.
	if assistantMatch && len(normalizeModerationMatchText(assistantText)) >= 16 {
		return 3
	}
	// A failed/empty assistant response has no evidence of its own. Require
	// multiple user messages before allowing an exact history match.
	if userMatch && len(evidence.UserTexts) > 1 {
		return 1
	}
	return 0
}

func normalizeModerationMatchText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func normalizeConversationID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 128 {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

func SetModerationRequestContent(c *gin.Context, request dto.Request) {
	if c == nil || request == nil || !moderationRequestSupported(request) {
		return
	}
	content := extractModerationRequestContent(request)
	common.SetContextKey(c, constant.ContextKeyModerationRequestContent, content)
}

// SetModerationRequestContentFromJSON parses the exact JSON that will be sent
// upstream into a fresh request value. Parsing into a fresh value matters when
// a field was removed by channel settings or parameter filtering.
func SetModerationRequestContentFromJSON(c *gin.Context, data []byte, request dto.Request) {
	if c == nil || len(data) == 0 || request == nil {
		return
	}
	var parsed dto.Request
	switch request.(type) {
	case *dto.GeneralOpenAIRequest:
		parsed = &dto.GeneralOpenAIRequest{}
	case *dto.OpenAIResponsesRequest:
		parsed = &dto.OpenAIResponsesRequest{}
	case *dto.OpenAIResponsesCompactionRequest:
		parsed = &dto.OpenAIResponsesCompactionRequest{}
	case *dto.ClaudeRequest:
		parsed = &dto.ClaudeRequest{}
	case *dto.GeminiChatRequest:
		parsed = &dto.GeminiChatRequest{}
	default:
		return
	}
	var content ModerationRequestContent
	if common.Unmarshal(data, parsed) == nil {
		content = extractModerationRequestContent(parsed)
	}
	content = mergeModerationRequestContent(content, extractModerationRequestContentFromJSON(data))
	common.SetContextKey(c, constant.ContextKeyModerationRequestContent, content)
}

func mergeModerationRequestContent(primary, fallback ModerationRequestContent) ModerationRequestContent {
	primary.SystemPrompt = mergeModerationText(primary.SystemPrompt, fallback.SystemPrompt)
	primary.UserPrompt = mergeModerationText(primary.UserPrompt, fallback.UserPrompt)
	return primary
}

func mergeModerationText(current, next string) string {
	if next == "" || current == next || strings.Contains(current, next) {
		return current
	}
	if strings.Contains(next, current) {
		return next
	}
	return joinModerationText(current, next)
}

func extractModerationRequestContentFromJSON(data []byte) ModerationRequestContent {
	var payload map[string]any
	if common.Unmarshal(data, &payload) != nil {
		return ModerationRequestContent{}
	}
	var content ModerationRequestContent
	for _, key := range []string{"system", "system_prompt", "systemPrompt", "systemInstruction", "system_instruction", "developer", "instructions", "instruction"} {
		content.SystemPrompt = mergeModerationText(content.SystemPrompt, openAIPromptText(payload[key]))
	}
	for _, key := range []string{"messages", "contents"} {
		items, ok := payload[key].([]any)
		if !ok {
			continue
		}
		for _, item := range items {
			itemMap, ok := item.(map[string]any)
			if !ok {
				content.UserPrompt = mergeModerationText(content.UserPrompt, openAIPromptText(item))
				continue
			}
			role, _ := itemMap["role"].(string)
			text := openAIPromptText(itemMap["content"])
			if text == "" {
				text = openAIPromptText(itemMap["parts"])
			}
			switch strings.ToLower(role) {
			case "system", "developer":
				content.SystemPrompt = mergeModerationText(content.SystemPrompt, text)
			case "", "user", "human":
				content.UserPrompt = mergeModerationText(content.UserPrompt, text)
			}
		}
	}
	if inputContent := extractModerationInputContent(payload["input"]); inputContent.SystemPrompt != "" || inputContent.UserPrompt != "" {
		content = mergeModerationRequestContent(content, inputContent)
	}
	for _, key := range []string{"user_prompt", "userPrompt", "prompt"} {
		content.UserPrompt = mergeModerationText(content.UserPrompt, openAIPromptText(payload[key]))
	}
	return content
}

func extractModerationResponsesInputContent(raw []byte) ModerationRequestContent {
	if len(raw) == 0 {
		return ModerationRequestContent{}
	}
	var value any
	if common.Unmarshal(raw, &value) != nil {
		return ModerationRequestContent{}
	}
	return extractModerationInputContent(value)
}

func extractModerationInputContent(value any) ModerationRequestContent {
	var content ModerationRequestContent
	addItem := func(item any) {
		itemMap, ok := item.(map[string]any)
		if !ok {
			content.UserPrompt = mergeModerationText(content.UserPrompt, openAIPromptText(item))
			return
		}
		role, _ := itemMap["role"].(string)
		itemType, _ := itemMap["type"].(string)
		if strings.EqualFold(itemType, "output_text") && role == "" {
			return
		}
		text := openAIPromptText(itemMap["content"])
		if text == "" {
			text = openAIPromptText(itemMap["text"])
		}
		if text == "" {
			text = openAIPromptText(itemMap["parts"])
		}
		if text == "" && itemType != "" && itemType != "message" {
			text = openAIPromptText(itemMap)
		}
		switch strings.ToLower(role) {
		case "system", "developer":
			content.SystemPrompt = mergeModerationText(content.SystemPrompt, text)
		case "assistant", "model":
			return
		default:
			content.UserPrompt = mergeModerationText(content.UserPrompt, text)
		}
	}

	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			addItem(item)
		}
	case nil:
		return content
	default:
		addItem(typed)
	}
	return content
}

func FinalizeModeration(c *gin.Context, info *relaycommon.RelayInfo, relayErr *types.NewAPIError) {
	if c == nil || info == nil || info.UserId <= 0 {
		return
	}
	channelID := 0
	if info.ChannelMeta != nil {
		channelID = info.ChannelId
	}
	if channelID <= 0 {
		channelID = common.GetContextKeyInt(c, constant.ContextKeyChannelId)
	}
	moderationSetting := setting.GetContentModerationSetting()
	if info != nil && moderationSetting.IsUserWhitelisted(info.UserId) {
		return
	}
	if !moderationSetting.ShouldModerateChannel(channelID) {
		return
	}
	content, ok := common.GetContextKeyType[ModerationRequestContent](c, constant.ContextKeyModerationRequestContent)
	if !ok {
		return
	}
	capture, ok := common.GetContextKeyType[*ModerationCapture](c, moderationResponseWriterKey)
	if !ok || capture == nil {
		return
	}

	assistantReply := ExtractModerationAssistantText(capture.Bytes(), c.Writer.Header().Get("Content-Type"), info.RelayFormat)
	responseStatus := moderationResponseStatus(info, relayErr, len(assistantReply) > 0)

	conversationID := common.GetContextKeyString(c, constant.ContextKeyModerationConversationID)
	if conversationID == "" {
		conversationID = ResolveModerationConversationID(c)
	}
	if conversationID == "" {
		return
	}
	turn := model.ModerationTurn{
		UserID:          info.UserId,
		ConversationKey: conversationID,
		ChannelID:       channelID,
		RequestID:       info.RequestId,
		SystemPrompt:    model.ModerationText(content.SystemPrompt),
		UserPrompt:      model.ModerationText(content.UserPrompt),
		AssistantReply:  model.ModerationText(assistantReply),
		ResponseStatus:  responseStatus,
		RelayFormat:     string(info.RelayFormat),
		Model:           info.OriginModelName,
		ExpiresAt:       time.Now().Add(getModerationViolationRetention()).Unix(),
	}
	if turn.ExpiresAt <= 0 {
		return
	}

	if err := persistModerationTurn(&turn); err != nil {
		logger.LogWarn(c, fmt.Sprintf("content moderation timeline persist failed: %v", err))
		return
	}
	if turn.ReviewRequired {
		if _, _, err := EnqueueSystemTask(model.SystemTaskTypeContentModeration, nil); err != nil {
			logger.LogWarn(c, fmt.Sprintf("content moderation task enqueue failed: %v", err))
		}
	}
}

func IsModerationRequestSupported(request dto.Request) bool {
	return moderationRequestSupported(request)
}

func moderationRequestSupported(request dto.Request) bool {
	switch request.(type) {
	case *dto.GeneralOpenAIRequest, *dto.OpenAIResponsesRequest, *dto.OpenAIResponsesCompactionRequest, *dto.ClaudeRequest, *dto.GeminiChatRequest:
		return true
	default:
		return false
	}
}

func moderationResponseStatus(info *relaycommon.RelayInfo, relayErr *types.NewAPIError, hasReply bool) string {
	if relayErr == nil {
		if info.IsStream && info.StreamStatus != nil && !info.StreamStatus.IsNormalEnd() {
			if info.StreamStatus.EndReason != "" {
				return string(info.StreamStatus.EndReason)
			}
			if hasReply {
				return "partial"
			}
			return "failed"
		}
		return "success"
	}
	if info.StreamStatus != nil && !info.StreamStatus.IsNormalEnd() && info.StreamStatus.EndReason != "" {
		return string(info.StreamStatus.EndReason)
	}
	if hasReply {
		return "partial"
	}
	return "failed"
}

func persistModerationTurn(turn *model.ModerationTurn) error {
	if turn == nil || turn.UserID <= 0 || turn.ConversationKey == "" {
		return errors.New("invalid moderation turn")
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := persistModerationTurnOnce(turn)
		if err == nil {
			return nil
		}
		if !isModerationPersistenceRetryable(err) || attempt == 2 {
			return err
		}
	}
	return errors.New("moderation turn persistence retry exhausted")
}

func persistModerationTurnOnce(turn *model.ModerationTurn) error {
	now := common.GetTimestamp()
	if turn.ExpiresAt <= now {
		turn.ExpiresAt = now + int64(getModerationViolationRetention().Seconds())
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var conversation model.ModerationConversation
		err := model.LockModerationConversation(tx, turn.UserID, turn.ConversationKey, &conversation)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			conversation = model.ModerationConversation{
				UserID:          turn.UserID,
				ConversationID:  turn.ConversationKey,
				Status:          model.ModerationConversationActive,
				FirstActivityAt: now,
				LastActivityAt:  now,
				ExpiresAt:       turn.ExpiresAt,
			}
			if err := tx.Create(&conversation).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			conversation.LastActivityAt = now
			conversationExpiresAt := turn.ExpiresAt
			if conversation.Status == model.ModerationConversationBlocked && conversation.ExpiresAt > conversationExpiresAt {
				conversationExpiresAt = conversation.ExpiresAt
			}
			conversation.ExpiresAt = conversationExpiresAt
			if err := tx.Model(&conversation).Updates(map[string]any{
				"last_activity_at": now,
				"expires_at":       conversationExpiresAt,
				"updated_at":       now,
			}).Error; err != nil {
				return err
			}
		}

		if turn.RequestID != "" {
			var existing model.ModerationTurn
			if err := tx.Where("user_id = ? AND conversation_key = ? AND request_id = ?", turn.UserID, turn.ConversationKey, turn.RequestID).First(&existing).Error; err == nil {
				turn.ID = existing.ID
				turn.ConversationID = existing.ConversationID
				turn.RoundNumber = existing.RoundNumber
				turn.ReviewRequired = existing.ReviewRequired
				return nil
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}

		var previous model.ModerationTurn
		round := 1
		if err := tx.Where("user_id = ? AND conversation_key = ?", turn.UserID, turn.ConversationKey).
			Order("round_number desc").First(&previous).Error; err == nil {
			round = previous.RoundNumber + 1
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		turn.ConversationID = conversation.ID
		turn.RoundNumber = round
		turn.CreatedAt = now
		turn.UpdatedAt = now
		turn.ReviewRequired, turn.ReviewTrigger = moderationReviewPlanWithDB(tx, turn.UserID, round, string(turn.SystemPrompt), string(turn.UserPrompt), string(turn.AssistantReply), turn.RequestID)
		turn.UserPromptFingerprint = common.GenerateHMAC(normalizeModerationMatchText(string(turn.UserPrompt)))
		turn.AssistantReplyFingerprint = common.GenerateHMAC(normalizeModerationMatchText(string(turn.AssistantReply)))
		storedTurn := *turn
		if err := encryptModerationTurnContent(&storedTurn); err != nil {
			return err
		}
		if err := tx.Create(&storedTurn).Error; err != nil {
			return err
		}
		turn.ID = storedTurn.ID
		if turn.ReviewRequired {
			config := setting.GetContentModerationSetting()
			reviewInput, err := buildModerationReviewInput(turn)
			if err != nil {
				return err
			}
			encryptedReviewInput, err := common.EncryptSecret(reviewInput)
			if err != nil {
				return err
			}
			job := &model.ModerationJob{
				TurnID:         turn.ID,
				ConversationID: conversation.ID,
				UserID:         turn.UserID,
				Status:         model.ModerationJobPending,
				NextAttemptAt:  now,
				RequestPayload: model.ModerationText(encryptedReviewInput),
				Provider:       config.Provider,
				Model:          config.Model,
				PromptVersion:  config.PromptVersion,
				ExpiresAt:      turn.ExpiresAt,
				CreatedAt:      now,
				UpdatedAt:      now,
			}
			if err := tx.Create(job).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func encryptModerationTurnContent(turn *model.ModerationTurn) error {
	if turn == nil {
		return errors.New("invalid moderation turn")
	}
	systemPrompt, err := common.EncryptSecret(string(turn.SystemPrompt))
	if err != nil {
		return err
	}
	userPrompt, err := common.EncryptSecret(string(turn.UserPrompt))
	if err != nil {
		return err
	}
	assistantReply, err := common.EncryptSecret(string(turn.AssistantReply))
	if err != nil {
		return err
	}
	turn.SystemPrompt = model.ModerationText(systemPrompt)
	turn.UserPrompt = model.ModerationText(userPrompt)
	turn.AssistantReply = model.ModerationText(assistantReply)
	return nil
}

func DecryptModerationStoredText(value string) (string, error) {
	return common.DecryptSecret(value)
}

func DecryptModerationTurnContent(turn *model.ModerationTurn) error {
	if turn == nil {
		return errors.New("invalid moderation turn")
	}
	systemPrompt, err := common.DecryptSecret(string(turn.SystemPrompt))
	if err != nil {
		return err
	}
	userPrompt, err := common.DecryptSecret(string(turn.UserPrompt))
	if err != nil {
		return err
	}
	assistantReply, err := common.DecryptSecret(string(turn.AssistantReply))
	if err != nil {
		return err
	}
	turn.SystemPrompt = model.ModerationText(systemPrompt)
	turn.UserPrompt = model.ModerationText(userPrompt)
	turn.AssistantReply = model.ModerationText(assistantReply)
	return nil
}

func isModerationPersistenceRetryable(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") ||
		strings.Contains(message, "duplicate") ||
		strings.Contains(message, "constraint failed") ||
		strings.Contains(message, "database is locked") ||
		strings.Contains(message, "table is locked") ||
		strings.Contains(message, "deadlock") ||
		strings.Contains(message, "lock wait timeout")
}

func buildModerationReviewInput(turn *model.ModerationTurn) (string, error) {
	if turn == nil {
		return "", errors.New("invalid moderation turn")
	}
	data, err := common.Marshal(map[string]string{
		"system_prompt":      string(turn.SystemPrompt),
		"user_prompt":        string(turn.UserPrompt),
		"assistant_response": string(turn.AssistantReply),
		"response_status":    turn.ResponseStatus,
	})
	if err != nil {
		return "", err
	}
	return "<review_data>\n" + string(data) + "\n</review_data>", nil
}

func moderationReviewPlan(userID, round int, systemPrompt, userPrompt, assistantReply, requestID string) (bool, string) {
	return moderationReviewPlanWithDB(model.DB, userID, round, systemPrompt, userPrompt, assistantReply, requestID)
}

func moderationReviewPlanWithDB(db *gorm.DB, userID, round int, systemPrompt, userPrompt, assistantReply, requestID string) (bool, string) {
	if round <= 3 {
		return true, "initial_rounds"
	}
	text := strings.ToLower(systemPrompt + "\n" + userPrompt + "\n" + assistantReply)
	if localModerationRiskSignal(text) {
		return true, "local_risk_signal"
	}
	violations, err := countEffectiveModerationUserViolations(db, userID, time.Now().Add(-getModerationViolationRetention()).Unix())
	if err != nil {
		logger.LogWarn(context.Background(), fmt.Sprintf("content moderation violation count failed: %v", err))
	}
	rate := setting.GetContentModerationSetting().NormalSampleRate
	if violations == 1 {
		rate = setting.GetContentModerationSetting().ElevatedSampleRate
	} else if violations >= 2 {
		return true, "escalated_user_history"
	}
	if rate <= 0 {
		return false, "sampling_disabled"
	}
	if rate > 100 {
		rate = 100
	}
	digest := sha256.Sum256([]byte(requestID + ":" + strconv.Itoa(round)))
	bucket := int(digest[0]) * 100 / 256
	if bucket < rate {
		return true, "random_sample"
	}
	return false, "local_clear"
}

func localModerationRiskSignal(text string) bool {
	for _, signal := range []string{
		"suicide", "self-harm", "kill", "murder", "bomb", "terrorist", "ransomware", "malware",
		"ddos", "sql injection", "phishing", "credential theft", "exploit", "weapon",
		"自杀", "自残", "杀人", "炸弹", "恐怖主义", "勒索软件", "恶意软件", "入侵", "木马", "钓鱼", "武器", "爆炸",
	} {
		if strings.Contains(text, signal) {
			return true
		}
	}
	return false
}

func extractModerationRequestContent(request dto.Request) ModerationRequestContent {
	var content ModerationRequestContent
	switch r := request.(type) {
	case *dto.GeneralOpenAIRequest:
		for _, message := range r.Messages {
			text := openAIMessageText(message)
			switch strings.ToLower(message.Role) {
			case "system", "developer":
				content.SystemPrompt = joinModerationText(content.SystemPrompt, text)
			case "user":
				content.UserPrompt = joinModerationText(content.UserPrompt, text)
			}
		}
		if prompt := openAIPromptText(r.Prompt); prompt != "" {
			content.UserPrompt = joinModerationText(content.UserPrompt, prompt)
		}
		for _, input := range r.ParseInput() {
			content.UserPrompt = joinModerationText(content.UserPrompt, input)
		}
		content.SystemPrompt = joinModerationText(content.SystemPrompt, r.Instruction)
	case *dto.OpenAIResponsesRequest:
		content.SystemPrompt = joinModerationText(content.SystemPrompt, openAIInstructionText(r.Instructions))
		content = mergeModerationRequestContent(content, extractModerationResponsesInputContent(r.Input))
	case *dto.OpenAIResponsesCompactionRequest:
		content.SystemPrompt = joinModerationText(content.SystemPrompt, openAIInstructionText(r.Instructions))
		content = mergeModerationRequestContent(content, extractModerationResponsesInputContent(r.Input))
	case *dto.ClaudeRequest:
		if r.IsStringSystem() {
			content.SystemPrompt = r.GetStringSystem()
		} else {
			for _, media := range r.ParseSystem() {
				content.SystemPrompt = joinModerationText(content.SystemPrompt, claudeMediaText(media))
			}
		}
		for _, message := range r.Messages {
			if strings.EqualFold(message.Role, "user") {
				content.UserPrompt = joinModerationText(content.UserPrompt, claudeMessageText(message))
			}
		}
		content.UserPrompt = joinModerationText(content.UserPrompt, r.Prompt)
	case *dto.GeminiChatRequest:
		request := r
		if len(request.Contents) == 0 && request.GenerateContentRequest != nil {
			request = request.GenerateContentRequest
		}
		if request.SystemInstructions != nil {
			content.SystemPrompt = geminiContentText(request.SystemInstructions)
		}
		for _, item := range request.Contents {
			if strings.EqualFold(item.Role, "user") || item.Role == "" {
				content.UserPrompt = joinModerationText(content.UserPrompt, geminiContentText(&item))
			}
		}
	}
	return content
}

func openAIInstructionText(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	var text string
	if err := common.Unmarshal(value, &text); err == nil {
		return text
	}
	var structured any
	if err := common.Unmarshal(value, &structured); err == nil {
		if text := openAIPromptText(structured); text != "" {
			return text
		}
	}
	return string(value)
}

func openAIPromptText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var builder strings.Builder
		for _, item := range typed {
			builder.WriteString(openAIPromptText(item))
		}
		return builder.String()
	case map[string]any:
		if mediaType, ok := typed["type"].(string); ok && mediaType != "" && mediaType != dto.ContentTypeText {
			if mediaType == "input_text" || mediaType == "output_text" {
				if text, ok := typed["text"]; ok {
					return openAIPromptText(text)
				}
			} else {
				return mediaPlaceholder(mediaType)
			}
		}
		if _, ok := typed["image_url"]; ok {
			return mediaPlaceholder("input_image")
		}
		if _, ok := typed["input_audio"]; ok {
			return mediaPlaceholder(dto.ContentTypeInputAudio)
		}
		if _, ok := typed["inlineData"]; ok {
			return mediaPlaceholder("file")
		}
		if _, ok := typed["inline_data"]; ok {
			return mediaPlaceholder("file")
		}
		if _, ok := typed["fileData"]; ok {
			return mediaPlaceholder("file")
		}
		if _, ok := typed["file_data"]; ok {
			return mediaPlaceholder("file")
		}
		if text, ok := typed["text"]; ok {
			return openAIPromptText(text)
		}
		if content, ok := typed["content"]; ok {
			return openAIPromptText(content)
		}
		if parts, ok := typed["parts"]; ok {
			return openAIPromptText(parts)
		}
	}
	return ""
}

func openAIMessageText(message dto.Message) string {
	var builder strings.Builder
	for _, item := range message.ParseContent() {
		if item.Type == dto.ContentTypeText {
			builder.WriteString(item.Text)
			continue
		}
		builder.WriteString(mediaPlaceholder(item.Type))
	}
	return builder.String()
}

func mediaPlaceholder(mediaType string) string {
	switch mediaType {
	case dto.ContentTypeImageURL, "input_image":
		return "[image content not saved]"
	case dto.ContentTypeInputAudio:
		return "[audio content not saved]"
	case dto.ContentTypeVideoUrl, "input_video":
		return "[video content not saved]"
	case dto.ContentTypeFile, "input_file":
		return "[file content not saved]"
	default:
		return "[media content not saved]"
	}
}

func claudeMessageText(message dto.ClaudeMessage) string {
	if message.IsStringContent() {
		return message.GetStringContent()
	}
	media, err := message.ParseContent()
	if err != nil {
		return ""
	}
	var builder strings.Builder
	for _, item := range media {
		builder.WriteString(claudeMediaText(item))
	}
	return builder.String()
}

func claudeMediaText(media dto.ClaudeMediaMessage) string {
	if media.Type == "text" || media.Type == "" {
		if text := media.GetText(); text != "" {
			return text
		}
		return media.GetStringContent()
	}
	return mediaPlaceholder(media.Type)
}

func geminiContentText(content *dto.GeminiChatContent) string {
	if content == nil {
		return ""
	}
	var builder strings.Builder
	for _, part := range content.Parts {
		if part.Text != "" {
			builder.WriteString(part.Text)
		} else if part.InlineData != nil || part.FileData != nil {
			builder.WriteString(mediaPlaceholder("file"))
		}
	}
	return builder.String()
}

func joinModerationText(current, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return current
	}
	if current == "" {
		return next
	}
	return current + "\n" + next
}

func ExtractModerationAssistantText(data []byte, contentType string, relayFormat types.RelayFormat) string {
	if len(data) == 0 {
		return ""
	}
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") || bytes.Contains(data, []byte("data:")) {
		var builder strings.Builder
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 64*1024), moderationMaxRequestBytes)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			if text := extractAssistantTextJSON([]byte(payload), relayFormat); text != "" {
				builder.WriteString(text)
			}
		}
		return builder.String()
	}
	return extractAssistantTextJSON(data, relayFormat)
}

func extractAssistantTextJSON(data []byte, relayFormat types.RelayFormat) string {
	var payload map[string]any
	if common.Unmarshal(data, &payload) != nil {
		return ""
	}
	var builder strings.Builder
	if relayFormat == types.RelayFormatGemini {
		if candidates, ok := payload["candidates"].([]any); ok {
			for _, candidate := range candidates {
				candidateMap, _ := candidate.(map[string]any)
				if content, ok := candidateMap["content"].(map[string]any); ok {
					appendTextValue(&builder, content["parts"])
				}
			}
		}
		return builder.String()
	}
	appendTextValue(&builder, payload["output_text"])
	appendTextValue(&builder, payload["completion"])
	if choices, ok := payload["choices"].([]any); ok {
		for _, choice := range choices {
			choiceMap, _ := choice.(map[string]any)
			appendTextValue(&builder, choiceMap["text"])
			if message, ok := choiceMap["message"].(map[string]any); ok {
				appendTextValue(&builder, message["content"])
			}
			if delta, ok := choiceMap["delta"].(map[string]any); ok {
				appendTextValue(&builder, delta["content"])
			}
		}
	}
	if output, ok := payload["output"].([]any); ok {
		for _, item := range output {
			itemMap, _ := item.(map[string]any)
			appendTextValue(&builder, itemMap["content"])
			appendTextValue(&builder, itemMap["text"])
			if delta, ok := itemMap["delta"].(map[string]any); ok {
				appendTextValue(&builder, delta["text"])
			}
		}
	}
	if content, ok := payload["content"].([]any); ok {
		appendTextValue(&builder, content)
	}
	appendTextValue(&builder, payload["text"])
	appendTextValue(&builder, payload["delta"])
	return builder.String()
}

func appendTextValue(builder *strings.Builder, value any) {
	switch typed := value.(type) {
	case string:
		builder.WriteString(typed)
	case []any:
		for _, item := range typed {
			appendTextValue(builder, item)
		}
	case map[string]any:
		if text, ok := typed["text"]; ok {
			appendTextValue(builder, text)
		}
	}
}

type moderationDecision struct {
	Decision   string   `json:"decision"`
	Actor      string   `json:"actor"`
	Severity   string   `json:"severity"`
	Categories []string `json:"categories"`
	Confidence float64  `json:"confidence"`
	ReasonCode string   `json:"reason_code"`
}

type moderationReviewPayload struct {
	Model        string `json:"model"`
	Instructions string `json:"instructions"`
	Input        string `json:"input"`
	Store        bool   `json:"store"`
	Text         any    `json:"text,omitempty"`
}

func ProcessContentModerationQueue(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	config := setting.GetContentModerationSetting()
	if !config.Enabled || strings.TrimSpace(config.APIKey) == "" || strings.TrimSpace(config.Model) == "" {
		return retryModerationNotifications(ctx)
	}
	now := common.GetTimestamp()
	maxAttempts := normalizedMaxRetries(config.MaxRetries)
	leaseSeconds := config.TimeoutSeconds*maxAttempts + 60
	for attempt := 1; attempt < maxAttempts; attempt++ {
		leaseSeconds += attempt * attempt * 5
	}
	if leaseSeconds < 5*60 {
		leaseSeconds = 5 * 60
	}
	if err := model.RequeueStaleModerationJobs(now, now-int64(leaseSeconds)); err != nil {
		return err
	}
	jobs, err := model.FindPendingModerationJobs(now, moderationJobBatchSize)
	if err != nil {
		return err
	}
	for _, candidate := range jobs {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		job, claimed, claimErr := model.ClaimModerationJob(candidate.ID, common.GetTimestamp())
		if claimErr != nil {
			logger.LogWarn(ctx, fmt.Sprintf("content moderation job claim failed: %v", claimErr))
			continue
		}
		if !claimed || job == nil {
			continue
		}
		processModerationJob(ctx, job, config)
	}
	return retryModerationNotifications(ctx)
}

func retryModerationNotifications(ctx context.Context) error {
	notifications, err := model.ListRetryableModerationNotifications(common.GetTimestamp(), 50)
	if err != nil {
		return err
	}
	for _, notification := range notifications {
		var violation model.ModerationViolation
		if err := model.DB.First(&violation, notification.ViolationID).Error; err != nil {
			continue
		}
		var turn model.ModerationTurn
		if err := model.DB.First(&turn, violation.TurnID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// Timeline text expires after seven days, while notification
				// metadata remains available for the configured violation retention window. The notification
				// body needs only the non-content identifiers below.
				turn = model.ModerationTurn{
					UserID:          violation.UserID,
					ConversationKey: violation.ConversationID,
				}
			} else {
				continue
			}
		}
		decision := moderationDecision{
			Decision:   violation.Decision,
			Actor:      violation.Actor,
			Severity:   violation.Severity,
			Confidence: violation.Confidence,
			ReasonCode: violation.ReasonCode,
		}
		if err := sendModerationNotification(&notification, &turn, decision, notification.AlertType == moderationAlertTypeAccountDisabled); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("content moderation notification retry failed: %v", err))
		}
	}
	return nil
}

func processModerationJob(ctx context.Context, job *model.ModerationJob, config setting.ContentModerationSetting) {
	var turn model.ModerationTurn
	if err := model.DB.First(&turn, job.TurnID).Error; err != nil {
		_ = model.SaveModerationJobResult(job.ID, job.LockedAt, model.ModerationJobFailed, job.Attempts+1, 0, "", err.Error())
		return
	}
	if err := DecryptModerationTurnContent(&turn); err != nil {
		_ = model.SaveModerationJobResult(job.ID, job.LockedAt, model.ModerationJobFailed, job.Attempts+1, 0, "", err.Error())
		return
	}
	attempt := job.Attempts + 1
	jobConfig := config
	if strings.TrimSpace(job.Provider) != "" {
		jobConfig.Provider = job.Provider
	}
	if strings.TrimSpace(job.Model) != "" {
		jobConfig.Model = job.Model
	}
	decision, raw, err := reviewModerationTurn(ctx, &turn, jobConfig)
	if err != nil {
		if attempt >= normalizedMaxRetries(config.MaxRetries) {
			_ = model.SaveModerationJobResult(job.ID, job.LockedAt, model.ModerationJobFailed, attempt, 0, raw, err.Error())
			logger.LogWarn(ctx, fmt.Sprintf("content moderation job %d exhausted retries: %v", job.ID, err))
			return
		}
		next := common.GetTimestamp() + int64(attempt*attempt*5)
		_ = model.SaveModerationJobResult(job.ID, job.LockedAt, model.ModerationJobPending, attempt, next, raw, err.Error())
		return
	}
	if err := applyModerationDecision(ctx, &turn, decision); err != nil {
		if attempt >= normalizedMaxRetries(config.MaxRetries) {
			_ = model.SaveModerationJobResult(job.ID, job.LockedAt, model.ModerationJobFailed, attempt, 0, raw, err.Error())
			logger.LogWarn(ctx, fmt.Sprintf("content moderation decision apply exhausted retries: %v", err))
			return
		}
		next := common.GetTimestamp() + int64(attempt*attempt*5)
		_ = model.SaveModerationJobResult(job.ID, job.LockedAt, model.ModerationJobPending, attempt, next, raw, err.Error())
		logger.LogWarn(ctx, fmt.Sprintf("content moderation decision apply failed: %v", err))
		return
	}
	if err := model.SaveModerationJobResult(job.ID, job.LockedAt, model.ModerationJobSuccess, attempt, 0, raw, ""); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("content moderation job %d result persist failed: %v", job.ID, err))
	}
}

func normalizedMaxRetries(value int) int {
	if value < 1 {
		return 1
	}
	if value > 5 {
		return 5
	}
	return value
}

func reviewModerationTurn(ctx context.Context, turn *model.ModerationTurn, config setting.ContentModerationSetting) (moderationDecision, string, error) {
	var decision moderationDecision
	input, err := buildModerationReviewInput(turn)
	if err != nil {
		return decision, "", err
	}
	prompt := strings.TrimSpace(config.PolicyPrompt)
	if prompt == "" {
		prompt = setting.DefaultContentModerationPolicyPrompt
	}
	payload := moderationReviewPayload{
		Model:        config.Model,
		Instructions: prompt,
		Input:        input,
		Store:        false,
	}
	var response map[string]any
	var raw []byte
	switch strings.ToLower(strings.TrimSpace(config.Provider)) {
	case "gemini":
		response, raw, err = callGeminiModeration(ctx, config, payload.Input)
	default:
		response, raw, err = callResponsesModeration(ctx, config, payload)
	}
	if err != nil {
		return decision, string(raw), err
	}
	text := extractReviewJSON(response)
	if text == "" {
		return decision, string(raw), errors.New("moderation provider returned no structured decision")
	}
	if err := common.UnmarshalJsonStr(text, &decision); err != nil {
		return decision, string(raw), fmt.Errorf("decode moderation decision: %w", err)
	}
	if err := validateModerationDecision(&decision); err != nil {
		return decision, string(raw), err
	}
	return decision, string(raw), nil
}

func ValidateContentModerationURL(rawURL string) error {
	endpoint := strings.TrimSpace(rawURL)
	if endpoint == "" {
		return nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("content moderation API URL must be an absolute HTTP(S) URL without credentials or fragments")
	}
	for key := range parsed.Query() {
		normalizedKey := strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(key))
		switch normalizedKey {
		case "key", "apikey", "token", "accesstoken", "secret", "apisecret":
			return errors.New("content moderation API URL must not contain credential query parameters")
		}
	}
	return nil
}

func moderationEndpoint(config setting.ContentModerationSetting, provider string) (string, error) {
	endpoint := strings.TrimSpace(config.BaseURL)
	if endpoint == "" {
		if provider == "gemini" {
			endpoint = "https://generativelanguage.googleapis.com/v1beta/models/" + url.PathEscape(config.Model) + ":generateContent"
		} else {
			endpoint = "https://api.openai.com/v1/responses"
		}
	}
	if strings.Contains(endpoint, "{model}") {
		modelName := strings.TrimSpace(config.Model)
		if modelName == "" {
			return "", errors.New("content moderation model is required when the API URL contains {model}")
		}
		endpoint = strings.ReplaceAll(endpoint, "{model}", url.PathEscape(modelName))
	}
	if err := ValidateContentModerationURL(endpoint); err != nil {
		return "", err
	}
	return endpoint, nil
}

func moderationHTTPClient(ctx context.Context, config setting.ContentModerationSetting, provider string, payload any) (map[string]any, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint, err := moderationEndpoint(config, provider)
	if err != nil {
		return nil, nil, err
	}
	body, err := common.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	if len(body) > moderationMaxRequestBytes {
		return nil, nil, errors.New("moderation provider request is too large")
	}
	timeout := config.TimeoutSeconds
	if timeout < 1 {
		timeout = 30
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if provider == "gemini" {
		if config.APIKey != "" {
			req.Header.Set("x-goog-api-key", config.APIKey)
		}
	} else {
		if config.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+config.APIKey)
		}
	}
	client := GetHttpClient()
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, moderationMaxResponseBytes+1))
	if err != nil {
		return nil, responseBody, err
	}
	if len(responseBody) > moderationMaxResponseBytes {
		return nil, responseBody[:moderationMaxResponseBytes], errors.New("moderation provider response is too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseBody, fmt.Errorf("moderation provider returned HTTP %d", resp.StatusCode)
	}
	var decoded map[string]any
	if err := common.Unmarshal(responseBody, &decoded); err != nil {
		return nil, responseBody, err
	}
	return decoded, responseBody, nil
}

func callResponsesModeration(ctx context.Context, config setting.ContentModerationSetting, payload moderationReviewPayload) (map[string]any, []byte, error) {
	payload.Text = map[string]any{"format": map[string]any{
		"type":   "json_schema",
		"name":   "content_moderation_decision",
		"strict": true,
		"schema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"decision":    map[string]any{"type": "string", "enum": []string{"allow", "block", "review"}},
				"actor":       map[string]any{"type": "string", "enum": []string{"none", "user", "assistant", "both"}},
				"severity":    map[string]any{"type": "string", "enum": []string{"none", "low", "medium", "high", "critical"}},
				"categories":  map[string]any{"type": "array", "maxItems": moderationMaxCategories, "items": map[string]any{"type": "string", "maxLength": moderationMaxCategoryLength}},
				"confidence":  map[string]any{"type": "number", "minimum": 0, "maximum": 1},
				"reason_code": map[string]any{"type": "string", "maxLength": moderationMaxReasonCodeLength},
			},
			"required": []string{"decision", "actor", "severity", "categories", "confidence", "reason_code"},
		},
	}}
	return moderationHTTPClient(ctx, config, "responses", payload)
}

func callGeminiModeration(ctx context.Context, config setting.ContentModerationSetting, input string) (map[string]any, []byte, error) {
	prompt := strings.TrimSpace(config.PolicyPrompt)
	if prompt == "" {
		prompt = setting.DefaultContentModerationPolicyPrompt
	}
	payload := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []any{map[string]any{"text": prompt}},
		},
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": input}}}},
		"generationConfig": map[string]any{
			"responseMimeType": "application/json",
			"responseSchema": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"decision":    map[string]any{"type": "STRING", "enum": []string{"allow", "block", "review"}},
					"actor":       map[string]any{"type": "STRING", "enum": []string{"none", "user", "assistant", "both"}},
					"severity":    map[string]any{"type": "STRING", "enum": []string{"none", "low", "medium", "high", "critical"}},
					"categories":  map[string]any{"type": "ARRAY", "maxItems": moderationMaxCategories, "items": map[string]any{"type": "STRING", "maxLength": moderationMaxCategoryLength}},
					"confidence":  map[string]any{"type": "NUMBER", "description": "A number from 0 to 1 inclusive"},
					"reason_code": map[string]any{"type": "STRING", "maxLength": moderationMaxReasonCodeLength},
				},
				"required": []string{"decision", "actor", "severity", "categories", "confidence", "reason_code"},
			},
		},
	}
	return moderationHTTPClient(ctx, config, "gemini", payload)
}

func extractReviewJSON(response map[string]any) string {
	if response == nil {
		return ""
	}
	if value, ok := response["output_text"].(string); ok && strings.TrimSpace(value) != "" {
		return stripJSONFence(value)
	}
	if output, ok := response["output"].([]any); ok {
		for _, item := range output {
			itemMap, _ := item.(map[string]any)
			content, _ := itemMap["content"].([]any)
			for _, contentItem := range content {
				contentMap, _ := contentItem.(map[string]any)
				if text, ok := contentMap["text"].(string); ok {
					return stripJSONFence(text)
				}
			}
		}
	}
	if candidates, ok := response["candidates"].([]any); ok {
		for _, item := range candidates {
			candidate, _ := item.(map[string]any)
			content, _ := candidate["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			for _, part := range parts {
				partMap, _ := part.(map[string]any)
				if text, ok := partMap["text"].(string); ok {
					return stripJSONFence(text)
				}
			}
		}
	}
	if choices, ok := response["choices"].([]any); ok && len(choices) > 0 {
		if firstChoice, ok := choices[0].(map[string]any); ok {
			if message, ok := firstChoice["message"].(map[string]any); ok {
				if content, ok := message["content"].(string); ok && strings.TrimSpace(content) != "" {
					return stripJSONFence(content)
				}
			}
			if text, ok := firstChoice["text"].(string); ok && strings.TrimSpace(text) != "" {
				return stripJSONFence(text)
			}
		}
	}
	if _, ok := response["decision"]; ok {
		if _, ok := response["actor"]; ok {
			if bytes, err := common.Marshal(response); err == nil {
				return string(bytes)
			}
		}
	}
	return ""
}

func stripJSONFence(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "```json")
	value = strings.TrimPrefix(value, "```")
	value = strings.TrimSuffix(value, "```")
	return strings.TrimSpace(value)
}

func validateModerationDecision(decision *moderationDecision) error {
	if decision == nil {
		return errors.New("nil moderation decision")
	}
	if decision.Decision != "allow" && decision.Decision != "block" && decision.Decision != "review" {
		return errors.New("invalid moderation decision")
	}
	if decision.Actor != "none" && decision.Actor != "user" && decision.Actor != "assistant" && decision.Actor != "both" {
		return errors.New("invalid moderation actor")
	}
	if decision.Severity != "none" && decision.Severity != "low" && decision.Severity != "medium" && decision.Severity != "high" && decision.Severity != "critical" {
		return errors.New("invalid moderation severity")
	}
	if math.IsNaN(decision.Confidence) || math.IsInf(decision.Confidence, 0) || decision.Confidence < 0 || decision.Confidence > 1 {
		return errors.New("invalid moderation confidence")
	}
	if len(decision.Categories) > moderationMaxCategories {
		return errors.New("too many moderation categories")
	}
	for _, category := range decision.Categories {
		if len(strings.TrimSpace(category)) == 0 || len(category) > moderationMaxCategoryLength || hasModerationControl(category) {
			return errors.New("invalid moderation category")
		}
	}
	if len(decision.ReasonCode) > moderationMaxReasonCodeLength || hasModerationControl(decision.ReasonCode) {
		return errors.New("moderation reason code is too long")
	}
	return nil
}

func hasModerationControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

func applyModerationDecision(ctx context.Context, turn *model.ModerationTurn, decision moderationDecision) error {
	if turn == nil || decision.Decision != "block" {
		return nil
	}
	isUserViolation := (decision.Actor == "user" || decision.Actor == "both") && decision.Confidence >= moderationHighConfidence
	isAssistantViolation := (decision.Actor == "assistant" || decision.Actor == "both") && decision.Confidence >= moderationAssistantConfidence
	if !isUserViolation && !isAssistantViolation {
		return nil
	}
	categories, err := common.Marshal(decision.Categories)
	if err != nil {
		return err
	}
	now := common.GetTimestamp()
	retention := getModerationViolationRetention()
	expiresAt := time.Now().Add(retention).Unix()
	if isUserViolation {
		if err := recordModerationViolation(turn, decision, true, string(categories), now, expiresAt); err != nil {
			return err
		}
	}
	if isAssistantViolation {
		if err := recordModerationViolation(turn, decision, false, string(categories), now, expiresAt); err != nil {
			return err
		}
	}
	if err := model.DB.Model(&model.ModerationConversation{}).
		Where("id = ?", turn.ConversationID).
		Updates(map[string]any{"status": model.ModerationConversationBlocked, "blocked_at": now, "blocked_reason": decision.ReasonCode, "expires_at": expiresAt, "updated_at": now}).Error; err != nil {
		return err
	}
	if err := queueModerationNotifications(ctx, turn, decision, false, isUserViolation); err != nil {
		return err
	}
	if isUserViolation {
		if err := syncModerationUserRecord(turn.UserID, now); err != nil {
			return err
		}
		count, err := countEffectiveModerationUserViolations(model.DB, turn.UserID, time.Now().Add(-retention).Unix())
		if err != nil {
			return err
		}
		if count >= moderationUserViolationThreshold {
			accountDisabled, err := DisableUserForModeration(turn.UserID)
			if err != nil {
				return err
			}
			// The notification row is deduplicated, so retrying this call is
			// safe even when the account was disabled by an earlier attempt.
			if accountDisabled {
				if err := queueModerationNotifications(ctx, turn, decision, true, true); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func recordModerationViolation(turn *model.ModerationTurn, decision moderationDecision, userViolation bool, categories string, now, expiresAt int64) error {
	if turn == nil || turn.ID <= 0 {
		return errors.New("invalid moderation violation turn")
	}
	var existing model.ModerationViolation
	if err := model.DB.Where("turn_id = ? AND user_violation = ?", turn.ID, userViolation).First(&existing).Error; err == nil {
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	violation := &model.ModerationViolation{
		UserID:         turn.UserID,
		ConversationID: turn.ConversationKey,
		TurnID:         turn.ID,
		Actor:          decision.Actor,
		UserViolation:  userViolation,
		Decision:       decision.Decision,
		Severity:       decision.Severity,
		Categories:     categories,
		Confidence:     decision.Confidence,
		ReasonCode:     decision.ReasonCode,
		Status:         model.ModerationViolationActive,
		CreatedAt:      now,
		ExpiresAt:      expiresAt,
	}
	if err := model.DB.Create(violation).Error; err != nil {
		if isModerationPersistenceRetryable(err) {
			if lookupErr := model.DB.Where("turn_id = ? AND user_violation = ?", turn.ID, userViolation).First(&existing).Error; lookupErr == nil {
				return nil
			}
		}
		return err
	}
	return nil
}

func queueModerationNotifications(ctx context.Context, turn *model.ModerationTurn, decision moderationDecision, accountDisabled, userViolation bool) error {
	if turn == nil {
		return errors.New("invalid moderation notification")
	}
	var violation model.ModerationViolation
	if err := model.DB.Where("user_id = ? AND conversation_id = ? AND user_violation = ? AND status = ?", turn.UserID, turn.ConversationKey, userViolation, model.ModerationViolationActive).Order("id desc").First(&violation).Error; err != nil {
		return err
	}
	var users []model.User
	if err := model.DB.Select("email").Where("status = ? AND role = ? AND email <> ''", common.UserStatusEnabled, common.RoleRootUser).Find(&users).Error; err != nil {
		return err
	}
	alertType := moderationAlertTypeViolation
	if accountDisabled {
		alertType = moderationAlertTypeAccountDisabled
	}
	for _, user := range users {
		recipient := strings.TrimSpace(user.Email)
		if recipient == "" {
			continue
		}
		dedupeDigest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%s", violation.ID, alertType, recipient)))
		notification := &model.ModerationNotification{
			ViolationID:   violation.ID,
			AlertType:     alertType,
			Recipient:     recipient,
			DedupeKey:     fmt.Sprintf("%x", dedupeDigest),
			Status:        model.ModerationNotificationPending,
			NextAttemptAt: common.GetTimestamp(),
		}
		if err := model.DB.Create(notification).Error; err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
				continue
			}
			return err
		}
		if err := sendModerationNotification(notification, turn, decision, accountDisabled); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("content moderation notification failed: %v", err))
		}
	}
	return nil
}

func sendModerationNotification(notification *model.ModerationNotification, turn *model.ModerationTurn, decision moderationDecision, accountDisabled bool) error {
	if notification == nil {
		return errors.New("invalid moderation notification")
	}
	subject := "Content moderation alert"
	message := fmt.Sprintf("A content moderation event requires administrator attention. User ID: %d, conversation: %s, actor: %s, severity: %s, reason: %s.", turn.UserID, turn.ConversationKey, decision.Actor, decision.Severity, decision.ReasonCode)
	if accountDisabled {
		subject = "User account disabled by content moderation"
		message += " The user account has been disabled after reaching the moderation violation threshold."
	}
	err := common.SendEmail(subject, notification.Recipient, message)
	status := model.ModerationNotificationSent
	lastError := ""
	nextAttemptAt := int64(0)
	attempts := notification.Attempts
	if attempts < 0 {
		attempts = 0
	}
	if attempts >= 4 {
		attempts = 5
	} else {
		attempts++
	}
	if err != nil {
		status = model.ModerationNotificationFailed
		lastError = err.Error()
		nextAttemptAt = common.GetTimestamp() + int64(attempts*attempts*60)
	}
	if updateErr := model.DB.Model(notification).Updates(map[string]any{"status": status, "attempts": attempts, "next_attempt_at": nextAttemptAt, "last_error": lastError, "updated_at": common.GetTimestamp()}).Error; updateErr != nil {
		if err != nil {
			return errors.Join(err, updateErr)
		}
		return updateErr
	}
	return err
}

func DisableUserForModeration(userID int) (bool, error) {
	if userID <= 0 {
		return false, errors.New("invalid moderation user")
	}
	changed, err := model.DisableUserAndTokensForModeration(userID, common.GetTimestamp())
	if err != nil {
		return false, err
	}
	// These operations are intentionally retried even when the database
	// transition was already committed. A failure after commit must not leave
	// an old login session or token cache usable on the next attempt.
	if _, err := model.RevokeAllUserSessions(userID, "content_moderation"); err != nil {
		return false, err
	}
	if err := model.InvalidateUserTokensCache(userID); err != nil {
		return false, err
	}
	if err := model.PublishUserAuthCache(userID); err != nil {
		return false, err
	}
	return changed, nil
}

func RestoreUserAfterModeration(userID, adminID int, reason string) error {
	if err := validateModerationActionReason(reason); err != nil {
		return err
	}
	if userID <= 0 || adminID <= 0 {
		return errors.New("invalid moderation restore request")
	}
	restored, err := model.RestoreUserAndTokensAfterModeration(userID, common.GetTimestamp())
	if err != nil {
		return err
	}
	if !restored {
		return model.ErrModerationAccountNotDisabled
	}
	if _, err := model.RevokeAllUserSessions(userID, "content_moderation_restore"); err != nil {
		return err
	}
	if err := model.InvalidateUserTokensCache(userID); err != nil {
		return err
	}
	if err := model.DB.Create(&model.ModerationAction{AdminID: adminID, UserID: userID, Action: "restore_account", Reason: reason, CreatedAt: common.GetTimestamp()}).Error; err != nil {
		return err
	}
	return model.PublishUserAuthCache(userID)
}

func UnblockModerationConversation(conversationID int64, adminID int, reason string) error {
	if err := validateModerationActionReason(reason); err != nil {
		return err
	}
	if conversationID <= 0 || adminID <= 0 {
		return errors.New("invalid moderation conversation")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var conversation model.ModerationConversation
		if err := tx.First(&conversation, conversationID).Error; err != nil {
			return err
		}
		now := common.GetTimestamp()
		if err := tx.Model(&conversation).Updates(map[string]any{
			"status":     model.ModerationConversationResolved,
			"expires_at": now + int64(getModerationViolationRetention().Seconds()),
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Create(&model.ModerationAction{AdminID: adminID, UserID: conversation.UserID, ConversationID: conversation.ConversationID, Action: "unblock_conversation", Reason: reason, CreatedAt: now}).Error
	})
}

func ResolveModerationViolation(violationID, adminID int64, status, reason string) error {
	if err := validateModerationActionReason(reason); err != nil {
		return err
	}
	if violationID <= 0 || adminID <= 0 {
		return errors.New("invalid moderation violation")
	}
	if status != model.ModerationViolationFalsePositive && status != model.ModerationViolationReversed {
		return errors.New("invalid moderation resolution")
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var violation model.ModerationViolation
		if err := tx.First(&violation, violationID).Error; err != nil {
			return err
		}
		now := common.GetTimestamp()
		if err := tx.Model(&violation).Updates(map[string]any{"status": status, "resolved_at": now, "resolved_by": adminID, "resolution_note": reason}).Error; err != nil {
			return err
		}
		return tx.Create(&model.ModerationAction{AdminID: int(adminID), UserID: violation.UserID, ConversationID: violation.ConversationID, ViolationID: violation.ID, Action: "resolve_violation", Reason: reason, CreatedAt: now}).Error
	})
	if err != nil {
		return err
	}
	var violation model.ModerationViolation
	if err := model.DB.First(&violation, violationID).Error; err == nil {
		if syncErr := syncModerationUserRecordWithHistory(violation.UserID, common.GetTimestamp(), true); syncErr != nil {
			logger.LogWarn(context.Background(), fmt.Sprintf("content moderation user record sync failed: %v", syncErr))
		}
	}
	return nil
}

func validateModerationActionReason(reason string) error {
	if len(reason) > 4096 {
		return errors.New("moderation action reason is too long")
	}
	return nil
}

func validateModerationUserStatusReason(reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: moderation account status reason is required", ErrInvalidModerationUserRequest)
	}
	if err := validateModerationActionReason(reason); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidModerationUserRequest, err)
	}
	return nil
}

func CleanupContentModerationData() error {
	now := common.GetTimestamp()
	if err := model.DeleteExpiredModerationContent(now, int64(getModerationViolationRetention().Seconds())); err != nil {
		return err
	}
	if err := model.DeleteExpiredModerationMetadata(now, int64(getModerationViolationRetention().Seconds())); err != nil {
		return err
	}
	return SyncModerationUserRecords()
}
