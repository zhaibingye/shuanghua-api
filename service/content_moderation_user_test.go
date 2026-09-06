package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModerationUserMutationRejectsPeerAdministrators(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.ModerationUserRecord{}))

	userID := int(time.Now().UnixNano()%1_000_000_000 + 1)
	user := &model.User{
		Id:       userID,
		Username: fmt.Sprintf("moderation-admin-target-%d", userID),
		Password: "password",
		Status:   common.UserStatusEnabled,
		Role:     common.RoleAdminUser,
		Group:    "default",
	}
	require.NoError(t, model.DB.Create(user).Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("user_id = ?", userID).Delete(&model.ModerationUserRecord{}).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&model.User{}, userID).Error)
	})

	err := UpdateModerationUserRecord(userID, userID+1, common.RoleAdminUser, 1, "not allowed")
	require.ErrorIs(t, err, ErrModerationUserPermissionDenied)

	err = SetModerationUserAccountStatus(userID, userID+1, common.RoleAdminUser, false, "not allowed")
	require.ErrorIs(t, err, ErrModerationUserPermissionDenied)

	err = DeleteModerationUserHistory(userID, userID+1, common.RoleAdminUser)
	require.ErrorIs(t, err, ErrModerationUserPermissionDenied)
}

func TestModerationUserEnableExplicitlyRestoresDisabledAccountWithoutModerationState(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.UserSession{}, &model.ModerationAccountState{}, &model.ModerationAction{}))

	userID := int(time.Now().UnixNano()%1_000_000_000 + 2)
	user := &model.User{
		Id:       userID,
		Username: fmt.Sprintf("moderation-enable-target-%d", userID),
		Password: "password",
		Status:   common.UserStatusDisabled,
		Role:     common.RoleCommonUser,
		Group:    "default",
	}
	require.NoError(t, model.DB.Create(user).Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("user_id = ?", userID).Delete(&model.ModerationAccountState{}).Error)
		require.NoError(t, model.DB.Where("user_id = ?", userID).Delete(&model.UserSession{}).Error)
		require.NoError(t, model.DB.Where("user_id = ?", userID).Delete(&model.ModerationAction{}).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&model.User{}, userID).Error)
	})

	require.NoError(t, SetModerationUserAccountStatus(userID, userID+1, common.RoleRootUser, true, "restore after manual review"))
	var restored model.User
	require.NoError(t, model.DB.Unscoped().First(&restored, userID).Error)
	assert.Equal(t, common.UserStatusEnabled, restored.Status)

	accountState := &model.ModerationAccountState{
		UserID:         userID,
		PreviousStatus: common.UserStatusEnabled,
		CreatedAt:      common.GetTimestamp(),
	}
	require.NoError(t, model.DB.Create(accountState).Error)
	require.NoError(t, model.SetUserAccountStatusForModeration(userID, common.UserStatusDisabled, common.GetTimestamp()))
	restoreAttempt, err := model.RestoreUserAndTokensAfterModeration(userID, common.GetTimestamp())
	require.NoError(t, err)
	assert.False(t, restoreAttempt)
	require.NoError(t, model.DB.Unscoped().First(&restored, userID).Error)
	assert.Equal(t, common.UserStatusDisabled, restored.Status)
}

func TestModerationUserRecordCountsEventsAndSupportsOverride(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(
		&model.User{},
		&model.ModerationEvent{},
		&model.ModerationUserRecord{},
	))

	userID := int(time.Now().UnixNano() % 1_000_000_000)
	now := common.GetTimestamp()
	user := &model.User{
		Id:          userID,
		Username:    fmt.Sprintf("moderation-user-%d", userID),
		Password:    "password",
		DisplayName: "Moderation test user",
		Status:      common.UserStatusEnabled,
		Role:        common.RoleCommonUser,
		Group:       "default",
	}
	require.NoError(t, model.DB.Create(user).Error)
	require.NoError(t, persistModerationEvent(model.ModerationEvent{
		UserID:      userID,
		Source:      model.ModerationEventSourcePreflight,
		Actor:       model.ModerationEventActorUser,
		Decision:    "block",
		Severity:    "high",
		UserExcerpt: "unsafe",
		Status:      model.ModerationEventActive,
		CreatedAt:   now,
		ExpiresAt:   now + 3600,
	}, true))
	require.NoError(t, persistModerationEvent(model.ModerationEvent{
		UserID:           userID,
		Source:           model.ModerationEventSourcePostflight,
		Actor:            model.ModerationEventActorAssistant,
		Decision:         "block",
		Severity:         "high",
		AssistantExcerpt: "unsafe reply",
		Status:           model.ModerationEventActive,
		CreatedAt:        now,
		ExpiresAt:        now + 3600,
	}, false))
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("user_id = ?", userID).Delete(&model.ModerationUserRecord{}).Error)
		require.NoError(t, model.DB.Where("user_id = ?", userID).Delete(&model.ModerationEvent{}).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&model.User{}, userID).Error)
	})

	active, total, err := ListModerationUsers("active", userID, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, active, 1)
	assert.Equal(t, 1, active[0].ViolationCount)
	assert.Equal(t, 1, active[0].ActualViolationCount)

	require.NoError(t, UpdateModerationUserRecord(userID, 999999, common.RoleRootUser, 5, "reviewed by admin"))
	active, total, err = ListModerationUsers("active", userID, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	assert.Equal(t, 5, active[0].ViolationCount)
	assert.Equal(t, 1, active[0].ActualViolationCount)
	assert.Equal(t, "reviewed by admin", active[0].Note)

	detail, err := GetModerationUserDetail(userID)
	require.NoError(t, err)
	require.Len(t, detail.Events, 2)

	require.NoError(t, UpdateModerationUserRecord(userID, 999999, common.RoleRootUser, 0, "cleared after review"))
	active, total, err = ListModerationUsers("active", userID, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.Empty(t, active)
	history, total, err := ListModerationUsers("history", userID, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, history, 1)
	assert.Equal(t, 0, history[0].ViolationCount)
	assert.Equal(t, "history", history[0].RecordStatus)

	var cleared model.ModerationUserRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&cleared).Error)
	require.NoError(t, persistModerationEvent(model.ModerationEvent{
		UserID:      userID,
		Source:      model.ModerationEventSourcePreflight,
		Actor:       model.ModerationEventActorUser,
		Decision:    "block",
		Severity:    "high",
		UserExcerpt: "new unsafe",
		Status:      model.ModerationEventActive,
		CreatedAt:   cleared.OverrideAt + 1,
		ExpiresAt:   now + 3600,
	}, true))
	active, total, err = ListModerationUsers("active", userID, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	assert.Equal(t, 1, active[0].ViolationCount)

	require.NoError(t, UpdateModerationUserRecord(userID, 999999, common.RoleRootUser, 0, "clear new violation"))
	require.NoError(t, model.DB.Model(&model.ModerationUserRecord{}).Where("user_id = ?", userID).Update("override_at", cleared.OverrideAt+2).Error)
	history, total, err = ListModerationUsers("history", userID, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, history, 1)
	assert.Equal(t, 0, history[0].ViolationCount)
	assert.Equal(t, "history", history[0].RecordStatus)

	require.NoError(t, DeleteModerationUserHistory(userID, 999999, common.RoleRootUser))
	history, total, err = ListModerationUsers("history", userID, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.Empty(t, history)
}

func TestMaybeAutoDisableUserUsesThreshold(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(
		&model.User{},
		&model.ModerationEvent{},
		&model.ModerationAccountState{},
		&model.ModerationTokenState{},
	))
	userID := int(time.Now().UnixNano()%1_000_000_000 + 7)
	user := &model.User{
		Id:       userID,
		Username: fmt.Sprintf("moderation-auto-disable-%d", userID),
		Password: "password",
		Status:   common.UserStatusEnabled,
		Role:     common.RoleCommonUser,
		Group:    "default",
	}
	require.NoError(t, model.DB.Create(user).Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("user_id = ?", userID).Delete(&model.ModerationEvent{}).Error)
		require.NoError(t, model.DB.Where("user_id = ?", userID).Delete(&model.ModerationAccountState{}).Error)
		require.NoError(t, model.DB.Unscoped().Delete(&model.User{}, userID).Error)
	})

	now := common.GetTimestamp()
	require.NoError(t, persistModerationEvent(model.ModerationEvent{
		UserID: userID, Source: model.ModerationEventSourcePreflight, Actor: model.ModerationEventActorUser,
		Decision: "block", Severity: "high", Status: model.ModerationEventActive, CreatedAt: now, ExpiresAt: now + 3600,
	}, true))
	maybeAutoDisableUser(userID, now)
	var stillEnabled model.User
	require.NoError(t, model.DB.First(&stillEnabled, userID).Error)
	assert.Equal(t, common.UserStatusEnabled, stillEnabled.Status)

	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	previous := common.OptionMap[setting.ContentModerationAutoDisableViolationsOption]
	common.OptionMap[setting.ContentModerationAutoDisableViolationsOption] = "1"
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		if previous == "" {
			delete(common.OptionMap, setting.ContentModerationAutoDisableViolationsOption)
		} else {
			common.OptionMap[setting.ContentModerationAutoDisableViolationsOption] = previous
		}
		common.OptionMapRWMutex.Unlock()
	})
	maybeAutoDisableUser(userID, now)
	var disabled model.User
	require.NoError(t, model.DB.First(&disabled, userID).Error)
	assert.Equal(t, common.UserStatusDisabled, disabled.Status)
}

func TestResolveModerationEventMarksFalsePositive(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.ModerationEvent{}, &model.ModerationAction{}))
	now := common.GetTimestamp()
	event := model.ModerationEvent{
		UserID: 88, Source: model.ModerationEventSourcePreflight, Actor: model.ModerationEventActorUser,
		Decision: "block", Severity: "high", Status: model.ModerationEventActive,
		ContentFingerprint: "abc", CreatedAt: now, ExpiresAt: now + 3600,
	}
	require.NoError(t, model.DB.Create(&event).Error)
	t.Cleanup(func() {
		_ = model.DB.Where("id = ?", event.ID).Delete(&model.ModerationEvent{}).Error
		_ = model.DB.Where("event_id = ?", event.ID).Delete(&model.ModerationAction{}).Error
	})
	require.NoError(t, ResolveModerationEvent(event.ID, 1, model.ModerationEventFalsePositive, "reviewed"))
	var stored model.ModerationEvent
	require.NoError(t, model.DB.First(&stored, event.ID).Error)
	assert.Equal(t, model.ModerationEventFalsePositive, stored.Status)
	assert.Equal(t, "reviewed", stored.ResolutionNote)
}

func TestDeleteModerationUserHistoryRejectsActiveRecords(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.ModerationUserRecord{}, &model.ModerationEvent{}))
	userID := int(time.Now().UnixNano()%1_000_000_000 + 9)
	user := &model.User{
		Id: userID, Username: fmt.Sprintf("moderation-active-history-%d", userID),
		Password: "password", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, Group: "default",
	}
	require.NoError(t, model.DB.Create(user).Error)
	t.Cleanup(func() {
		_ = model.DB.Where("user_id = ?", userID).Delete(&model.ModerationUserRecord{}).Error
		_ = model.DB.Where("user_id = ?", userID).Delete(&model.ModerationEvent{}).Error
		_ = model.DB.Unscoped().Delete(&model.User{}, userID).Error
	})
	now := common.GetTimestamp()
	require.NoError(t, persistModerationEvent(model.ModerationEvent{
		UserID: userID, Source: model.ModerationEventSourcePreflight, Actor: model.ModerationEventActorUser,
		Decision: "block", Severity: "high", Status: model.ModerationEventActive, CreatedAt: now, ExpiresAt: now + 3600,
	}, true))
	err := DeleteModerationUserHistory(userID, 999999, common.RoleRootUser)
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrModerationUserHistoryOnly) || err != nil)
}
