package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordModerationViolationTreatsEachTurnAsAnIndependentEvent(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.ModerationTurn{}, &model.ModerationViolation{}))

	userID := int(time.Now().UnixNano()%1_000_000_000 + 3)
	conversationKey := fmt.Sprintf("moderation-violation-event-%d", time.Now().UnixNano())
	now := common.GetTimestamp()
	turns := []*model.ModerationTurn{
		{UserID: userID, ConversationKey: conversationKey, RoundNumber: 1, UserPrompt: "first", ResponseStatus: "success", ExpiresAt: now + 3600},
		{UserID: userID, ConversationKey: conversationKey, RoundNumber: 2, UserPrompt: "second", ResponseStatus: "success", ExpiresAt: now + 3600},
	}
	for _, turn := range turns {
		require.NoError(t, model.DB.Create(turn).Error)
	}
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("user_id = ?", userID).Delete(&model.ModerationViolation{}).Error)
		require.NoError(t, model.DB.Where("user_id = ?", userID).Delete(&model.ModerationTurn{}).Error)
	})
	decision := moderationDecision{Decision: "block", Actor: "user", Severity: "high", Confidence: moderationHighConfidence, ReasonCode: "unsafe"}
	require.NoError(t, recordModerationViolation(turns[0], decision, true, "[]", now, now+3600))
	var first model.ModerationViolation
	require.NoError(t, model.DB.Where("turn_id = ?", turns[0].ID).First(&first).Error)
	require.NoError(t, model.DB.Model(&first).Update("status", model.ModerationViolationFalsePositive).Error)

	// A later turn in the same conversation is a new event and must be
	// recorded even when the earlier decision was resolved as a false positive.
	require.NoError(t, recordModerationViolation(turns[1], decision, true, "[]", now+1, now+3600))
	var count int64
	require.NoError(t, model.DB.Model(&model.ModerationViolation{}).Where("user_id = ?", userID).Count(&count).Error)
	require.Equal(t, int64(2), count)
}

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
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrModerationUserPermissionDenied))

	err = SetModerationUserAccountStatus(userID, userID+1, common.RoleAdminUser, false, "not allowed")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrModerationUserPermissionDenied))

	err = DeleteModerationUserHistory(userID, userID+1, common.RoleAdminUser)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrModerationUserPermissionDenied))
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

	// A later manual disable must not be undone by an automated moderation
	// restore retry while the moderation-owned state is still active.
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

func TestModerationUserRecordSupportsCountOverrideHistoryAndConversationView(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(
		&model.User{},
		&model.ModerationConversation{},
		&model.ModerationViolation{},
		&model.ModerationUserRecord{},
	))

	userID := int(time.Now().UnixNano() % 1_000_000_000)
	conversationKey := fmt.Sprintf("moderation-user-record-%d", time.Now().UnixNano())
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
	conversation := &model.ModerationConversation{
		UserID:          userID,
		ConversationID:  conversationKey,
		Status:          model.ModerationConversationBlocked,
		FirstActivityAt: now,
		LastActivityAt:  now,
		ExpiresAt:       now + 3600,
	}
	violation := &model.ModerationViolation{
		UserID:         userID,
		ConversationID: conversationKey,
		TurnID:         1,
		Actor:          "user",
		UserViolation:  true,
		Decision:       "block",
		Severity:       "high",
		Categories:     `["safety"]`,
		Confidence:     0.95,
		ReasonCode:     "unsafe",
		Status:         model.ModerationViolationActive,
		CreatedAt:      now,
		ExpiresAt:      now + 3600,
	}
	require.NoError(t, model.DB.Create(user).Error)
	require.NoError(t, model.DB.Create(conversation).Error)
	require.NoError(t, model.DB.Create(violation).Error)
	assistantViolation := &model.ModerationViolation{
		UserID:         userID,
		ConversationID: conversationKey,
		TurnID:         3,
		Actor:          "assistant",
		UserViolation:  false,
		Decision:       "block",
		Severity:       "high",
		Status:         model.ModerationViolationActive,
		CreatedAt:      now,
		ExpiresAt:      now + 3600,
	}
	require.NoError(t, model.DB.Create(assistantViolation).Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("user_id = ?", userID).Delete(&model.ModerationUserRecord{}).Error)
		require.NoError(t, model.DB.Where("user_id = ?", userID).Delete(&model.ModerationViolation{}).Error)
		require.NoError(t, model.DB.Where("user_id = ?", userID).Delete(&model.ModerationConversation{}).Error)
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
	require.Len(t, active, 1)
	assert.Equal(t, 5, active[0].ViolationCount)
	assert.Equal(t, 1, active[0].ActualViolationCount)
	assert.Equal(t, "reviewed by admin", active[0].Note)

	detail, err := GetModerationUserDetail(userID, "violations")
	require.NoError(t, err)
	require.Len(t, detail.Conversations, 1)
	assert.Equal(t, conversationKey, detail.Conversations[0].ConversationID)
	require.Len(t, detail.Violations, 1)
	assert.True(t, detail.Violations[0].UserViolation)

	require.NoError(t, UpdateModerationUserRecord(userID, 999999, common.RoleRootUser, 0, "cleared after review"))
	active, total, err = ListModerationUsers("active", userID, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.Empty(t, active)

	// Expiring the conversation that existed at edit time must not cancel a
	// new violation that arrives afterwards.
	require.NoError(t, model.DB.Model(&model.ModerationViolation{}).
		Where("id = ?", violation.ID).Update("expires_at", now-1).Error)
	newConversationKey := fmt.Sprintf("moderation-user-record-new-%d", time.Now().UnixNano())
	newConversation := &model.ModerationConversation{
		UserID:          userID,
		ConversationID:  newConversationKey,
		Status:          model.ModerationConversationBlocked,
		FirstActivityAt: now,
		LastActivityAt:  now,
		ExpiresAt:       now + 3600,
	}
	newViolation := &model.ModerationViolation{
		UserID:         userID,
		ConversationID: newConversationKey,
		TurnID:         2,
		Actor:          "user",
		UserViolation:  true,
		Decision:       "block",
		Severity:       "high",
		Status:         model.ModerationViolationActive,
		CreatedAt:      now,
		ExpiresAt:      now + 3600,
	}
	require.NoError(t, model.DB.Create(newConversation).Error)
	require.NoError(t, model.DB.Create(newViolation).Error)
	active, total, err = ListModerationUsers("active", userID, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, active, 1)
	assert.Equal(t, 1, active[0].ViolationCount)

	require.NoError(t, UpdateModerationUserRecord(userID, 999999, common.RoleRootUser, 0, "clear new violation"))
	history, total, err := ListModerationUsers("history", userID, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, history, 1)
	assert.Equal(t, 0, history[0].ViolationCount)
	assert.Equal(t, "history", history[0].RecordStatus)
	assert.Equal(t, "clear new violation", history[0].Note)

	require.NoError(t, DeleteModerationUserHistory(userID, 999999, common.RoleRootUser))
	history, total, err = ListModerationUsers("history", userID, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.Empty(t, history)
}
