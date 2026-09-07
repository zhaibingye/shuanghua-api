package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
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
	require.NoError(t, db.AutoMigrate(
		&model.Option{},
		&model.Log{},
		&model.User{},
		&model.ModerationEvent{},
		&model.ModerationUserRecord{},
		&model.ModerationAction{},
		&model.ModerationAccountState{},
	))
	_ = db.Create(&model.User{Id: 1, Username: "admin"}).Error
	model.InitOptionMap()
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

func TestUpdateContentModerationSettingsAllowsCustomHTTPURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	// Test updating with custom HTTP URL
	updateReqBody := `{
		"enabled": true,
		"base_url": "http://66.154.103.123:8317/v1/moderations",
		"api_key": "test-key",
		"model": "omni-moderation-latest",
		"timeout_seconds": 30,
		"max_retries": 3
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
	assert.Equal(t, "http://66.154.103.123:8317/v1/moderations", getResp.Data.BaseURL)
	assert.Equal(t, "omni-moderation-latest", getResp.Data.Model)
	assert.True(t, getResp.Data.APIKeyConfigured)
}

func TestUpdateContentModerationSettingsPreservesConfiguredAPIKeyWhenBlank(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	updateSettings := func(apiKey string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{
			"enabled": true,
			"base_url": "https://proxy.example/v1",
			"api_key": %q,
			"model": "omni-moderation-latest",
			"timeout_seconds": 30,
			"max_retries": 3
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
	assert.Equal(t, "first-key", storedAPIKey)

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

func TestContentModerationAPIKeyPersistenceAcrossRestartAndReveal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	// 1. Save settings with API key
	updateBody := `{
		"enabled": true,
		"base_url": "https://api.openai.com/v1",
		"api_key": "sk-test-moderation-key-12345",
		"model": "omni-moderation-latest",
		"timeout_seconds": 30,
		"max_retries": 3
	}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(updateBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("role", 100)
	UpdateContentModerationSettings(c)
	require.Equal(t, http.StatusOK, recorder.Code)

	// 2. Check that the key is saved in plain text
	common.OptionMapRWMutex.RLock()
	storedKey := common.OptionMap[setting.ContentModerationAPIKeyOption]
	common.OptionMapRWMutex.RUnlock()
	assert.Equal(t, "sk-test-moderation-key-12345", storedKey)

	// 3. Simulate a server restart:
	// - common.CryptoSecret changes to a brand new random UUID (which previously broke decryption)
	// - model.InitOptionMap() resets memory OptionMap and reloads from DB
	common.CryptoSecret = "brand-new-random-secret-on-restart"
	model.InitOptionMap()

	// 4. Verify that GetContentModerationSetting still returns the exact key!
	restartedSetting := setting.GetContentModerationSetting()
	assert.Equal(t, "sk-test-moderation-key-12345", restartedSetting.APIKey)

	// 5. Test GetContentModerationKey endpoint returns the key
	keyRecorder := httptest.NewRecorder()
	keyContext, _ := gin.CreateTestContext(keyRecorder)
	keyContext.Request = httptest.NewRequest(http.MethodPost, "/api/moderation/key", nil)
	keyContext.Set("id", 1)
	keyContext.Set("role", 100)
	GetContentModerationKey(keyContext)
	require.Equal(t, http.StatusOK, keyRecorder.Code)

	var keyResp struct {
		Success bool `json:"success"`
		Data    struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(keyRecorder.Body.Bytes(), &keyResp))
	assert.True(t, keyResp.Success)
	assert.Equal(t, "sk-test-moderation-key-12345", keyResp.Data.Key)
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

func TestListContentModerationEventsFiltering(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	now := time.Now().Unix()
	e1 := model.ModerationEvent{
		UserID: 101, Source: model.ModerationEventSourcePreflight, Actor: model.ModerationEventActorUser,
		Decision: "block", Severity: "high", Status: model.ModerationEventActive, UserExcerpt: "alpha",
		CreatedAt: now - 200, ExpiresAt: now + 600000,
	}
	e2 := model.ModerationEvent{
		UserID: 102, Source: model.ModerationEventSourcePostflight, Actor: model.ModerationEventActorAssistant,
		Decision: "block", Severity: "medium", Status: model.ModerationEventFalsePositive, UserExcerpt: "beta",
		CreatedAt: now - 100, ExpiresAt: now + 600000,
	}
	e3 := model.ModerationEvent{
		UserID: 101, Source: model.ModerationEventSourcePreflight, Actor: model.ModerationEventActorUser,
		Decision: "block", Severity: "low", Status: model.ModerationEventReversed, UserExcerpt: "gamma",
		CreatedAt: now - 50, ExpiresAt: now + 600000,
	}
	require.NoError(t, model.DB.Create(&e1).Error)
	require.NoError(t, model.DB.Create(&e2).Error)
	require.NoError(t, model.DB.Create(&e3).Error)

	tests := []struct {
		name          string
		queryURL      string
		expectedCount int
	}{
		{name: "all events", queryURL: "/api/moderation/events", expectedCount: 3},
		{name: "filter by status active", queryURL: "/api/moderation/events?status=active", expectedCount: 1},
		{name: "filter by status all returns all", queryURL: "/api/moderation/events?status=all", expectedCount: 3},
		{name: "filter by user_id 101", queryURL: "/api/moderation/events?user_id=101", expectedCount: 2},
		{name: "filter by source postflight", queryURL: "/api/moderation/events?source=postflight", expectedCount: 1},
		{name: "filter by time range", queryURL: fmt.Sprintf("/api/moderation/events?start_timestamp=%d&end_timestamp=%d", now-150, now), expectedCount: 2},
		{name: "combined filter user_id and status", queryURL: "/api/moderation/events?user_id=101&status=reversed", expectedCount: 1},
		{name: "combined filter no match", queryURL: "/api/moderation/events?user_id=102&status=reversed", expectedCount: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, tt.queryURL, nil)
			c.Set("id", 1)
			c.Set("role", 100)

			ListContentModerationEvents(c)
			require.Equal(t, http.StatusOK, recorder.Code)

			var resp struct {
				Success bool                    `json:"success"`
				Data    []model.ModerationEvent `json:"data"`
				Total   int64                   `json:"total"`
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

func TestUpdateContentModerationSettingsAcceptsMultipleAPIKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	body := `{
		"enabled": true,
		"base_url": "https://api.openai.com/v1",
		"api_key": "sk-moderation-key-one\nsk-moderation-key-two\n\nsk-moderation-key-three",
		"model": "omni-moderation-latest",
		"timeout_seconds": 30,
		"max_retries": 3
	}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("role", 100)
	UpdateContentModerationSettings(c)
	require.Equal(t, http.StatusOK, recorder.Code)

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
	assert.True(t, getResp.Data.APIKeyConfigured)
	assert.Equal(t, 3, getResp.Data.APIKeyCount)

	keyRecorder := httptest.NewRecorder()
	keyC, _ := gin.CreateTestContext(keyRecorder)
	keyC.Request = httptest.NewRequest(http.MethodPost, "/api/moderation/key", nil)
	keyC.Set("id", 1)
	keyC.Set("role", 100)
	GetContentModerationKey(keyC)
	require.Equal(t, http.StatusOK, keyRecorder.Code)
	var keyResp struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(keyRecorder.Body.Bytes(), &keyResp))
	assert.Equal(t, "sk-moderation-key-one\nsk-moderation-key-two\nsk-moderation-key-three", keyResp.Data.Key)
}

func TestUpdateContentModerationSettingsRejectsTooManyAPIKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	keys := make([]string, setting.MaxContentModerationAPIKeys+1)
	for i := range keys {
		keys[i] = fmt.Sprintf("sk-key-%d", i+1)
	}
	body := fmt.Sprintf(`{
		"enabled": true,
		"base_url": "https://api.openai.com/v1",
		"api_key": %q,
		"model": "omni-moderation-latest",
		"timeout_seconds": 30,
		"max_retries": 3
	}`, strings.Join(keys, "\n"))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("role", 100)
	UpdateContentModerationSettings(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestTestContentModerationKeysRejectsEmptyKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/moderation/keys/test", strings.NewReader(`{
		"base_url": "https://api.openai.com/v1",
		"model": "omni-moderation-latest",
		"api_key": ""
	}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("role", 100)
	TestContentModerationKeys(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	var response struct {
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Contains(t, response.Message, "no moderation API keys")
}

func TestRelaySkipsContentModerationForUnrelatedChannelWithoutPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	common.OptionMapRWMutex.Lock()
	common.OptionMap[setting.ContentModerationEnabledOption] = "true"
	common.OptionMap[setting.ContentModerationChannelsOption] = "1"
	common.OptionMap[setting.ContentModerationAPIKeyOption] = "sk-fake-key"
	common.OptionMap[setting.ContentModerationModelOption] = "omni-moderation-latest"
	common.OptionMap[setting.ContentModerationPreflightOption] = "true"
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		delete(common.OptionMap, setting.ContentModerationEnabledOption)
		delete(common.OptionMap, setting.ContentModerationChannelsOption)
		delete(common.OptionMap, setting.ContentModerationAPIKeyOption)
		delete(common.OptionMap, setting.ContentModerationModelOption)
		delete(common.OptionMap, setting.ContentModerationPreflightOption)
		common.OptionMapRWMutex.Unlock()
	})

	// Channel 2 is an unmoderated channel (e.g. Gemini channel)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-1.5-flash","messages":[{"role":"user","content":"hello"}]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 42) // Non-root user
	common.SetContextKey(c, constant.ContextKeyChannelId, 2)
	c.Set("channel_id", 2)

	// Calling Relay must not panic, and must not set moderation content
	assert.NotPanics(t, func() {
		Relay(c, types.RelayFormatOpenAI)
	})
	// Moderation request content should not be set because moderation was skipped
	_, hasContent := common.GetContextKeyType[service.ModerationRequestContent](c, constant.ContextKeyModerationRequestContent)
	assert.False(t, hasContent, "moderation content should not be set for unmoderated channel")
}

func TestRelayEnforcesContentModerationForTargetChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	common.OptionMapRWMutex.Lock()
	common.OptionMap[setting.ContentModerationEnabledOption] = "true"
	common.OptionMap[setting.ContentModerationChannelsOption] = "1"
	common.OptionMap[setting.ContentModerationAPIKeyOption] = "" // Intentionally empty to trigger unconfigured error
	common.OptionMap[setting.ContentModerationModelOption] = "omni-moderation-latest"
	common.OptionMap[setting.ContentModerationPreflightOption] = "true"
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		delete(common.OptionMap, setting.ContentModerationEnabledOption)
		delete(common.OptionMap, setting.ContentModerationChannelsOption)
		delete(common.OptionMap, setting.ContentModerationAPIKeyOption)
		delete(common.OptionMap, setting.ContentModerationModelOption)
		delete(common.OptionMap, setting.ContentModerationPreflightOption)
		common.OptionMapRWMutex.Unlock()
	})

	// Channel 1 IS the moderated channel
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 42) // Non-root user
	common.SetContextKey(c, constant.ContextKeyChannelId, 1)
	c.Set("channel_id", 1)

	Relay(c, types.RelayFormatOpenAI)

	// Should reject because moderation is enabled on channel 1 but API key is unconfigured
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "content moderation is enabled but not configured")
}

func TestUpdateContentModerationSettingsClearAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	// 1. Initially set a key
	common.OptionMapRWMutex.Lock()
	common.OptionMap[setting.ContentModerationAPIKeyOption] = "sk-initial-secret-key"
	common.OptionMap[setting.ContentModerationEnabledOption] = "false"
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		delete(common.OptionMap, setting.ContentModerationAPIKeyOption)
		delete(common.OptionMap, setting.ContentModerationEnabledOption)
		common.OptionMapRWMutex.Unlock()
	})

	// Try clearing when enabled=true without providing a new key -> should be rejected
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := `{"enabled": true, "clear_api_key": true, "model": "omni-moderation-latest", "timeout_seconds": 30, "max_retries": 3}`
	c.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	UpdateContentModerationSettings(c)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "API key are required when content moderation is enabled")

	// Clearing when enabled=false -> should succeed and clear the key
	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	body = `{"enabled": false, "clear_api_key": true, "model": "omni-moderation-latest", "timeout_seconds": 30, "max_retries": 3}`
	c.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	UpdateContentModerationSettings(c)
	assert.Equal(t, http.StatusOK, recorder.Code)

	// Verify key is cleared
	cfg := setting.GetContentModerationSetting()
	assert.Empty(t, cfg.APIKey)
	assert.False(t, cfg.HasAPIKey())
}

func TestDeleteContentModerationUserHistoryWhenUserDeleted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	now := common.GetTimestamp()
	// Insert an archived record for user 9999 who does NOT exist in users table
	rec := model.ModerationUserRecord{
		UserID:           9999,
		ArchivedAt:       now,
		UsernameSnapshot: "deleted_user",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	require.NoError(t, model.DB.Create(&rec).Error)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/moderation/users/9999/history", nil)
	c.Params = gin.Params{{Key: "id", Value: "9999"}}
	c.Set("id", 1)
	c.Set("role", common.RoleRootUser)

	DeleteContentModerationUserHistory(c)
	assert.Equal(t, http.StatusOK, recorder.Code)

	// Verify deleted from DB
	var count int64
	require.NoError(t, model.DB.Model(&model.ModerationUserRecord{}).Where("user_id = ?", 9999).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestUpdateContentModerationSettingsBlockSeverity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModerationTestDB(t)

	// Valid block_severity: high
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := `{"enabled": false, "model": "omni-moderation-latest", "block_severity": "high", "timeout_seconds": 30, "max_retries": 3}`
	c.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("role", 100)
	UpdateContentModerationSettings(c)
	assert.Equal(t, http.StatusOK, recorder.Code)

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
	assert.Equal(t, "high", getResp.Data.BlockSeverity)

	// Invalid block_severity: returns 400
	badRecorder := httptest.NewRecorder()
	badC, _ := gin.CreateTestContext(badRecorder)
	badBody := `{"enabled": false, "model": "omni-moderation-latest", "block_severity": "invalid_val", "timeout_seconds": 30, "max_retries": 3}`
	badC.Request = httptest.NewRequest(http.MethodPut, "/api/moderation/settings", strings.NewReader(badBody))
	badC.Request.Header.Set("Content-Type", "application/json")
	badC.Set("id", 1)
	badC.Set("role", 100)
	UpdateContentModerationSettings(badC)
	assert.Equal(t, http.StatusBadRequest, badRecorder.Code)
	assert.Contains(t, badRecorder.Body.String(), "block severity must be critical, high, medium, or low")
}
