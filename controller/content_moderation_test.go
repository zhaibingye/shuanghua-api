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

func TestGetContentModerationConversationKeepsMetadataWhenContentCannotBeDecrypted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(
		&model.ModerationTurn{},
		&model.ModerationJob{},
		&model.ModerationViolation{},
		&model.ModerationAction{},
		&model.ModerationNotification{},
	))

	previousCryptoSecret := common.CryptoSecret
	t.Cleanup(func() { common.CryptoSecret = previousCryptoSecret })
	common.CryptoSecret = "moderation-history-secret"
	encrypt := func(value string) string {
		encrypted, err := common.EncryptSecret(value)
		require.NoError(t, err)
		return encrypted
	}

	now := common.GetTimestamp()
	conversation := &model.ModerationConversation{
		UserID:          2,
		ConversationID:  "history-conversation",
		Status:          model.ModerationConversationActive,
		FirstActivityAt: now,
		LastActivityAt:  now,
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.DB.Create(conversation).Error)
	turn := &model.ModerationTurn{
		ConversationID:  conversation.ID,
		UserID:          conversation.UserID,
		ConversationKey: conversation.ConversationID,
		RoundNumber:     1,
		SystemPrompt:    model.ModerationText(encrypt("system")),
		UserPrompt:      model.ModerationText(encrypt("user")),
		AssistantReply:  model.ModerationText(encrypt("assistant")),
		ResponseStatus:  "success",
		CreatedAt:       now,
		UpdatedAt:       now,
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.DB.Create(turn).Error)
	job := &model.ModerationJob{
		TurnID:          turn.ID,
		ConversationID:  conversation.ID,
		UserID:          conversation.UserID,
		Status:          model.ModerationJobSuccess,
		RequestPayload:  model.ModerationText(encrypt("request")),
		ResponsePayload: model.ModerationText(encrypt("response")),
		Provider:        "responses",
		Model:           "moderation-model",
		PromptVersion:   "v1",
		ExpiresAt:       now + 3600,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, model.DB.Create(job).Error)

	common.CryptoSecret = "a-different-secret"
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/moderation/conversations/"+fmt.Sprint(conversation.ID), nil)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(conversation.ID)}}
	c.Set("id", 1)
	c.Set("role", common.RoleRootUser)

	GetContentModerationConversation(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data struct {
			Turns []struct {
				SystemPrompt       string `json:"system_prompt"`
				ContentUnavailable bool   `json:"content_unavailable"`
			} `json:"turns"`
			Jobs []struct {
				ResponsePayloadUnavailable bool `json:"response_payload_unavailable"`
			} `json:"jobs"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.Len(t, response.Data.Turns, 1)
	require.Len(t, response.Data.Jobs, 1)
	assert.True(t, response.Data.Turns[0].ContentUnavailable)
	assert.Empty(t, response.Data.Turns[0].SystemPrompt)
	assert.True(t, response.Data.Jobs[0].ResponsePayloadUnavailable)
}

func TestModerationUserStatusRejectsPeerAdministrator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	target := &model.User{Id: 2, Username: "target-admin", Password: "password", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, AffCode: "target-admin-aff"}
	require.NoError(t, model.DB.Create(target).Error)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPatch, "/api/moderation/users/2/status", strings.NewReader(`{"enabled":false,"reason":"should be rejected"}`))
	c.Set("id", 3)
	c.Set("role", common.RoleAdminUser)
	c.Params = gin.Params{{Key: "id", Value: "2"}}

	UpdateContentModerationUserStatus(c)
	require.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestModerationUserMutationsRequireExplicitValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	target := &model.User{Id: 2, Username: "target-user", Password: "password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: "target-user-aff"}
	require.NoError(t, model.DB.Create(target).Error)

	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		handler gin.HandlerFunc
	}{
		{name: "missing violation count", method: http.MethodPut, path: "/api/moderation/users/2", body: `{"note":"must not clear the count"}`, handler: UpdateContentModerationUser},
		{name: "missing account enabled flag", method: http.MethodPatch, path: "/api/moderation/users/2/status", body: `{"reason":"must not disable the account"}`, handler: UpdateContentModerationUserStatus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			c.Set("id", 3)
			c.Set("role", common.RoleAdminUser)
			c.Params = gin.Params{{Key: "id", Value: "2"}}

			tt.handler(c)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
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

func TestUpdateContentModerationSettingsPreservesConfiguredAPIKeyWhenBlank(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	updateSettings := func(apiKey string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{
			"enabled": true,
			"provider": "responses",
			"base_url": "https://proxy.example/v1",
			"api_key": %q,
			"model": "gpt-5-mini",
			"timeout_seconds": 30,
			"max_retries": 3,
			"normal_sample_rate": 10,
			"elevated_sample_rate": 50,
			"prompt_version": "v1"
		}`, apiKey)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("id", 1)
		c.Set("role", 100)
		UpdateContentModerationSettings(c)
		return recorder
	}

	firstResponse := updateSettings("first-key")
	require.Equal(t, http.StatusOK, firstResponse.Code)
	common.OptionMapRWMutex.RLock()
	savedAPIKey := common.OptionMap[setting.ContentModerationAPIKeyOption]
	common.OptionMapRWMutex.RUnlock()
	require.NotEmpty(t, savedAPIKey)

	secondResponse := updateSettings("")
	require.Equal(t, http.StatusOK, secondResponse.Code)
	common.OptionMapRWMutex.RLock()
	storedAPIKey := common.OptionMap[setting.ContentModerationAPIKeyOption]
	common.OptionMapRWMutex.RUnlock()
	storedPlaintextKey, err := common.DecryptSecret(storedAPIKey)
	require.NoError(t, err)
	assert.Equal(t, "first-key", storedPlaintextKey)

	getRecorder := httptest.NewRecorder()
	getContext, _ := gin.CreateTestContext(getRecorder)
	getContext.Request = httptest.NewRequest(http.MethodGet, "/api/moderation/settings", nil)
	getContext.Set("id", 1)
	getContext.Set("role", 100)
	GetContentModerationSettings(getContext)
	require.Equal(t, http.StatusOK, getRecorder.Code)
	var response struct {
		Data contentModerationSettingsResponse `json:"data"`
	}
	require.NoError(t, common.Unmarshal(getRecorder.Body.Bytes(), &response))
	assert.True(t, response.Data.APIKeyConfigured)
}

func TestUpdateContentModerationSettingsRejectsMissingAPIKeyWhenEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(`{
		"enabled": true,
		"provider": "gemini",
		"base_url": "https://moderation.example.test/custom-endpoint",
		"api_key": "",
		"model": "gemini-3-flash-preview",
		"timeout_seconds": 30,
		"max_retries": 3,
		"normal_sample_rate": 10,
		"elevated_sample_rate": 50,
		"prompt_version": "v1"
	}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("role", 100)

	UpdateContentModerationSettings(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	var response struct {
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Contains(t, response.Message, "API key")
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
