package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupModerationTestDB(t *testing.T) {
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
	})

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:moderation_test_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	require.NoError(t, err)
	originalDB := model.DB
	originalLOGDB := model.LOG_DB
	t.Cleanup(func() {
		model.DB = originalDB
		model.LOG_DB = originalLOGDB
	})
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Log{}, &model.User{}, &model.ModerationConversation{}))
	_ = db.Create(&model.User{Id: 1, Username: "admin"}).Error
	model.InitOptionMap()
}

func TestUpdateContentModerationSettingsAllowsCustomHTTPURLAndPolicyPrompt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	// Test updating with custom HTTP URL and custom policy prompt
	updateReqBody := `{
		"enabled": true,
		"provider": "responses",
		"base_url": "http://66.154.103.123:8317/v1/responses",
		"api_key": "test-key",
		"model": "gpt-4o-mini",
		"timeout_seconds": 30,
		"max_retries": 3,
		"normal_sample_rate": 10,
		"elevated_sample_rate": 50,
		"prompt_version": "v2",
		"policy_prompt": "Custom policy prompt for safety."
	}`

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(updateReqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("role", 100)

	UpdateContentModerationSettings(c)
	require.Equal(t, http.StatusOK, recorder.Code)

	var updateResp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &updateResp))
	assert.True(t, updateResp.Success)

	// Now verify GetContentModerationSettings returns the updated values
	getRecorder := httptest.NewRecorder()
	getC, _ := gin.CreateTestContext(getRecorder)
	getC.Request = httptest.NewRequest(http.MethodGet, "/api/moderation/settings", nil)
	getC.Set("id", 1)
	getC.Set("role", 100)

	GetContentModerationSettings(getC)
	require.Equal(t, http.StatusOK, getRecorder.Code)

	var getResp struct {
		Success bool                              `json:"success"`
		Data    contentModerationSettingsResponse `json:"data"`
	}
	require.NoError(t, common.Unmarshal(getRecorder.Body.Bytes(), &getResp))
	assert.True(t, getResp.Success)
	assert.True(t, getResp.Data.Enabled)
	assert.Equal(t, "http://66.154.103.123:8317/v1/responses", getResp.Data.BaseURL)
	assert.Equal(t, "v2", getResp.Data.PromptVersion)
	assert.Equal(t, "Custom policy prompt for safety.", getResp.Data.PolicyPrompt)
	assert.Equal(t, setting.DefaultContentModerationPolicyPrompt, getResp.Data.DefaultPolicyPrompt)
	assert.True(t, getResp.Data.APIKeyConfigured)
}

func TestUpdateContentModerationSettingsRejectsInvalidPolicyPromptControlChars(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	updateReqBody := `{
		"enabled": false,
		"provider": "responses",
		"base_url": "",
		"api_key": "",
		"model": "gpt-4o-mini",
		"timeout_seconds": 30,
		"max_retries": 3,
		"normal_sample_rate": 10,
		"elevated_sample_rate": 50,
		"prompt_version": "v1",
		"policy_prompt": "Invalid prompt with \u0001 control character"
	}`

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(updateReqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("role", 100)

	UpdateContentModerationSettings(c)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)

	var updateResp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &updateResp))
	assert.False(t, updateResp.Success)
	assert.Contains(t, updateResp.Message, "invalid control characters")
}

func TestUpdateContentModerationSettingsResetsEmptyPolicyPromptToDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	// Update with empty policy_prompt (which represents a reset to default)
	updateReqBody := `{
		"enabled": false,
		"provider": "responses",
		"base_url": "http://66.154.103.123:8317/v1/responses",
		"api_key": "",
		"model": "gpt-4o-mini",
		"timeout_seconds": 30,
		"max_retries": 3,
		"normal_sample_rate": 10,
		"elevated_sample_rate": 50,
		"prompt_version": "v1",
		"policy_prompt": ""
	}`

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(updateReqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("role", 100)

	UpdateContentModerationSettings(c)
	require.Equal(t, http.StatusOK, recorder.Code)

	// Verify that GET returns the default policy prompt
	getRecorder := httptest.NewRecorder()
	getC, _ := gin.CreateTestContext(getRecorder)
	getC.Request = httptest.NewRequest(http.MethodGet, "/api/moderation/settings", nil)
	getC.Set("id", 1)
	getC.Set("role", 100)

	GetContentModerationSettings(getC)
	require.Equal(t, http.StatusOK, getRecorder.Code)

	var getResp struct {
		Success bool                              `json:"success"`
		Data    contentModerationSettingsResponse `json:"data"`
	}
	require.NoError(t, common.Unmarshal(getRecorder.Body.Bytes(), &getResp))
	assert.True(t, getResp.Success)
	assert.Equal(t, setting.DefaultContentModerationPolicyPrompt, getResp.Data.PolicyPrompt)
}

func TestUpdateContentModerationSettingsChannels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	// Valid update with mixed delimiters (comma, full-width comma, spaces)
	updateReqBody := `{
		"enabled": true,
		"channels": "10, 2， 5 10",
		"provider": "responses",
		"base_url": "http://66.154.103.123:8317/v1/responses",
		"api_key": "test-key",
		"model": "gpt-4o-mini",
		"timeout_seconds": 30,
		"max_retries": 3,
		"normal_sample_rate": 10,
		"elevated_sample_rate": 50,
		"prompt_version": "v1",
		"policy_prompt": "Policy"
	}`

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(updateReqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("role", 100)

	UpdateContentModerationSettings(c)
	require.Equal(t, http.StatusOK, recorder.Code)

	// Verify GET returns normalized channels and channel_ids
	getRecorder := httptest.NewRecorder()
	getC, _ := gin.CreateTestContext(getRecorder)
	getC.Request = httptest.NewRequest(http.MethodGet, "/api/moderation/settings", nil)
	getC.Set("id", 1)
	getC.Set("role", 100)

	GetContentModerationSettings(getC)
	require.Equal(t, http.StatusOK, getRecorder.Code)

	var getResp struct {
		Success bool                              `json:"success"`
		Data    contentModerationSettingsResponse `json:"data"`
	}
	require.NoError(t, common.Unmarshal(getRecorder.Body.Bytes(), &getResp))
	assert.True(t, getResp.Success)
	assert.Equal(t, "2, 5, 10", getResp.Data.Channels)
	assert.Equal(t, []int{2, 5, 10}, getResp.Data.ChannelIDs)

	// Invalid update with non-integer channel ID
	invalidReqBody := `{
		"enabled": true,
		"channels": "1, abc, 3",
		"provider": "responses",
		"base_url": "http://66.154.103.123:8317/v1/responses",
		"api_key": "test-key",
		"model": "gpt-4o-mini",
		"timeout_seconds": 30,
		"max_retries": 3,
		"normal_sample_rate": 10,
		"elevated_sample_rate": 50,
		"prompt_version": "v1",
		"policy_prompt": "Policy"
	}`
	badRecorder := httptest.NewRecorder()
	badC, _ := gin.CreateTestContext(badRecorder)
	badC.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(invalidReqBody))
	badC.Request.Header.Set("Content-Type", "application/json")
	badC.Set("id", 1)
	badC.Set("role", 100)

	UpdateContentModerationSettings(badC)
	assert.Equal(t, http.StatusBadRequest, badRecorder.Code)
}

func TestListContentModerationConversationsFiltering(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	now := time.Now().Unix()
	c1 := model.ModerationConversation{
		UserID:          101,
		ConversationID:  "chat-project-alpha-001",
		Status:          "active",
		FirstActivityAt: now - 300,
		LastActivityAt:  now - 200,
		ExpiresAt:       now + 600000,
	}
	c2 := model.ModerationConversation{
		UserID:          102,
		ConversationID:  "chat-project-beta-002",
		Status:          "blocked",
		FirstActivityAt: now - 500,
		LastActivityAt:  now - 100,
		ExpiresAt:       now + 600000,
	}
	c3 := model.ModerationConversation{
		UserID:          101,
		ConversationID:  "chat-project-gamma-003",
		Status:          "resolved",
		FirstActivityAt: now - 800,
		LastActivityAt:  now - 50,
		ExpiresAt:       now + 600000,
	}
	require.NoError(t, model.DB.Create(&c1).Error)
	require.NoError(t, model.DB.Create(&c2).Error)
	require.NoError(t, model.DB.Create(&c3).Error)

	tests := []struct {
		name          string
		queryURL      string
		expectedCount int
	}{
		{
			name:          "all conversations",
			queryURL:      "/api/moderation/conversations",
			expectedCount: 3,
		},
		{
			name:          "filter by status active",
			queryURL:      "/api/moderation/conversations?status=active",
			expectedCount: 1,
		},
		{
			name:          "filter by status all returns all",
			queryURL:      "/api/moderation/conversations?status=all",
			expectedCount: 3,
		},
		{
			name:          "filter by user_id 101",
			queryURL:      "/api/moderation/conversations?user_id=101",
			expectedCount: 2,
		},
		{
			name:          "filter by conversation_id substring 'beta'",
			queryURL:      "/api/moderation/conversations?conversation_id=beta",
			expectedCount: 1,
		},
		{
			name:          "filter by time range",
			queryURL:      fmt.Sprintf("/api/moderation/conversations?start_timestamp=%d&end_timestamp=%d", now-150, now),
			expectedCount: 2, // c2 (now-100) and c3 (now-50)
		},
		{
			name:          "combined filter user_id and status",
			queryURL:      "/api/moderation/conversations?user_id=101&status=resolved",
			expectedCount: 1,
		},
		{
			name:          "combined filter no match",
			queryURL:      "/api/moderation/conversations?user_id=102&status=resolved",
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, tt.queryURL, nil)
			c.Set("id", 1)
			c.Set("role", 100)

			ListContentModerationConversations(c)
			require.Equal(t, http.StatusOK, recorder.Code)

			var resp struct {
				Success bool                           `json:"success"`
				Data    []model.ModerationConversation `json:"data"`
				Total   int64                          `json:"total"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &resp))
			assert.True(t, resp.Success)
			assert.Equal(t, int64(tt.expectedCount), resp.Total)
			assert.Len(t, resp.Data, tt.expectedCount)
		})
	}
}

func TestUpdateContentModerationSettingsUserWhitelistAndRetentionDays(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	// Verify defaults initially
	getRecorder := httptest.NewRecorder()
	getC, _ := gin.CreateTestContext(getRecorder)
	getC.Request = httptest.NewRequest(http.MethodGet, "/api/moderation/settings", nil)
	getC.Set("id", 1)
	getC.Set("role", 100)

	GetContentModerationSettings(getC)
	require.Equal(t, http.StatusOK, getRecorder.Code)

	var initialResp struct {
		Success bool                              `json:"success"`
		Data    contentModerationSettingsResponse `json:"data"`
	}
	require.NoError(t, common.Unmarshal(getRecorder.Body.Bytes(), &initialResp))
	assert.Equal(t, "1", initialResp.Data.UserWhitelist)
	assert.Equal(t, []int{1}, initialResp.Data.UserWhitelistIDs)
	assert.Equal(t, 7, initialResp.Data.ViolationRetentionDays)

	// Update user whitelist to "2, 5" and retention days to 14
	// Note: root admin ID 1 should automatically be included!
	updateReqBody := `{
		"enabled": false,
		"channels": "1, 2",
		"user_whitelist": "2, 5",
		"violation_retention_days": 14,
		"provider": "responses",
		"base_url": "",
		"api_key": "",
		"model": "gpt-4o-mini",
		"timeout_seconds": 30,
		"max_retries": 3,
		"normal_sample_rate": 10,
		"elevated_sample_rate": 50,
		"prompt_version": "v1",
		"policy_prompt": "Standard policy"
	}`

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(updateReqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("role", 100)

	UpdateContentModerationSettings(c)
	require.Equal(t, http.StatusOK, recorder.Code)

	// Verify updated values via GET
	getRecorder2 := httptest.NewRecorder()
	getC2, _ := gin.CreateTestContext(getRecorder2)
	getC2.Request = httptest.NewRequest(http.MethodGet, "/api/moderation/settings", nil)
	getC2.Set("id", 1)
	getC2.Set("role", 100)

	GetContentModerationSettings(getC2)
	require.Equal(t, http.StatusOK, getRecorder2.Code)

	var updatedResp struct {
		Success bool                              `json:"success"`
		Data    contentModerationSettingsResponse `json:"data"`
	}
	require.NoError(t, common.Unmarshal(getRecorder2.Body.Bytes(), &updatedResp))
	// 1 was automatically added and sorted
	assert.Equal(t, "1, 2, 5", updatedResp.Data.UserWhitelist)
	assert.Equal(t, []int{1, 2, 5}, updatedResp.Data.UserWhitelistIDs)
	assert.Equal(t, 14, updatedResp.Data.ViolationRetentionDays)

	// Invalid user whitelist should be rejected with 400
	invalidWhitelistBody := `{
		"enabled": false,
		"user_whitelist": "1, abc, 3",
		"provider": "responses",
		"prompt_version": "v1"
	}`
	badRecorder := httptest.NewRecorder()
	badC, _ := gin.CreateTestContext(badRecorder)
	badC.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(invalidWhitelistBody))
	badC.Request.Header.Set("Content-Type", "application/json")
	badC.Set("id", 1)
	badC.Set("role", 100)

	UpdateContentModerationSettings(badC)
	assert.Equal(t, http.StatusBadRequest, badRecorder.Code)

	// Invalid retention days (< 1 or > 365) should be rejected with 400
	invalidRetentionBody := `{
		"enabled": false,
		"violation_retention_days": 400,
		"provider": "responses",
		"prompt_version": "v1"
	}`
	badRecorder2 := httptest.NewRecorder()
	badC2, _ := gin.CreateTestContext(badRecorder2)
	badC2.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(invalidRetentionBody))
	badC2.Request.Header.Set("Content-Type", "application/json")
	badC2.Set("id", 1)
	badC2.Set("role", 100)

	UpdateContentModerationSettings(badC2)
	assert.Equal(t, http.StatusBadRequest, badRecorder2.Code)
}
