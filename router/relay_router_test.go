package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModelsSupportsOpenAIAndGeminiAuthentication(t *testing.T) {
	setupRelayRouterTestDB(t)

	user := model.User{
		Username: "models-user",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         user.Id,
		Key:            "modelstestkey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	tests := []struct {
		name           string
		path           string
		headerName     string
		expectedObject string
		expectedField  string
	}{
		{
			name:           "OpenAI bearer token",
			path:           "/v1/models",
			headerName:     "Authorization",
			expectedObject: "list",
			expectedField:  "data",
		},
		{
			name:          "Gemini API key header",
			path:          "/v1/models",
			headerName:    "x-goog-api-key",
			expectedField: "models",
		},
		{
			name:          "Gemini API key query",
			path:          "/v1/models?key=modelstestkey",
			expectedField: "models",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.headerName != "" {
				value := "modelstestkey"
				if test.headerName == "Authorization" {
					value = "Bearer " + value
				}
				request.Header.Set(test.headerName, value)
			}

			engine.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			var payload map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Contains(t, payload, test.expectedField)
			assert.NotContains(t, payload, "error")
			if test.expectedObject != "" {
				assert.Equal(t, test.expectedObject, payload["object"])
			}
		})
	}
}

func TestCreditsEndpointWithAPIKey(t *testing.T) {
	setupRelayRouterTestDB(t)

	user := model.User{
		Username: "credits-user",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    int(common.QuotaPerUnit * 5),
	}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:      user.Id,
		Key:         "creditstestkey",
		Status:      common.TokenStatusEnabled,
		ExpiredTime: -1,
		RemainQuota: int(common.QuotaPerUnit * 5),
	}).Error)

	engine := gin.New()
	SetDashboardRouter(engine)

	paths := []struct {
		name       string
		path       string
		headerAuth string
	}{
		{
			name:       "GET /v1/credits with Bearer header",
			path:       "/v1/credits",
			headerAuth: "Bearer creditstestkey",
		},
		{
			name:       "GET /credits with Bearer header",
			path:       "/credits",
			headerAuth: "Bearer creditstestkey",
		},
		{
			name: "GET /v1/credits with query key",
			path: "/v1/credits?key=creditstestkey",
		},
		{
			name: "GET /credits with query key",
			path: "/credits?key=creditstestkey",
		},
		{
			name: "GET /v1/credits with query api_key",
			path: "/v1/credits?api_key=creditstestkey",
		},
		{
			name: "GET /credits with query apiKey",
			path: "/credits?apiKey=creditstestkey",
		},
		{
			name:       "GET /v1/credits/ with trailing slash",
			path:       "/v1/credits/",
			headerAuth: "Bearer creditstestkey",
		},
		{
			name:       "GET /credits/ with trailing slash",
			path:       "/credits/",
			headerAuth: "Bearer creditstestkey",
		},
	}

	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.headerAuth != "" {
				req.Header.Set("Authorization", tc.headerAuth)
			}
			engine.ServeHTTP(recorder, req)

			require.Equal(t, http.StatusOK, recorder.Code)
			var payload map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Equal(t, "credit_summary", payload["object"])
			data, ok := payload["data"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, float64(5), data["total_usage"])
		})
	}
}

func TestGeminiModelEndpointsOnRouter(t *testing.T) {
	setupRelayRouterTestDB(t)

	originalSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = true
	t.Cleanup(func() {
		operation_setting.SelfUseModeEnabled = originalSelfUse
	})

	user := model.User{
		Username: "gemini-models-user",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         user.Id,
		Key:            "geminitestkey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group:     "default",
		Model:     "gemini-2.5-flash",
		ChannelId: 1,
		Enabled:   true,
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	// Test GET /v1beta/models
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1beta/models?key=geminitestkey", nil)
	engine.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code)
	var listResp map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &listResp))
	modelsList, ok := listResp["models"].([]any)
	require.True(t, ok)
	require.Len(t, modelsList, 1)
	firstModel := modelsList[0].(map[string]any)
	assert.Equal(t, "models/gemini-2.5-flash", firstModel["name"])
	assert.Equal(t, "gemini-2.5-flash", firstModel["baseModelId"])

	// Test GET /v1beta/models/ (trailing slash)
	recorderSlash := httptest.NewRecorder()
	reqSlash := httptest.NewRequest(http.MethodGet, "/v1beta/models/?key=geminitestkey", nil)
	engine.ServeHTTP(recorderSlash, reqSlash)
	require.Equal(t, http.StatusOK, recorderSlash.Code)

	// Test GET /v1beta/models/:model
	recorder2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/v1beta/models/gemini-2.5-flash?key=geminitestkey", nil)
	engine.ServeHTTP(recorder2, req2)
	require.Equal(t, http.StatusOK, recorder2.Code)
	var singleResp map[string]any
	require.NoError(t, common.Unmarshal(recorder2.Body.Bytes(), &singleResp))
	assert.Equal(t, "models/gemini-2.5-flash", singleResp["name"])

	// Test GET /v1beta/models/models/:model
	recorder3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/v1beta/models/models/gemini-2.5-flash?key=geminitestkey", nil)
	engine.ServeHTTP(recorder3, req3)
	require.Equal(t, http.StatusOK, recorder3.Code)
	var singleResp3 map[string]any
	require.NoError(t, common.Unmarshal(recorder3.Body.Bytes(), &singleResp3))
	assert.Equal(t, "models/gemini-2.5-flash", singleResp3["name"])

	// Test GET /v1/models/:model with key query (Gemini mode)
	recorderV1 := httptest.NewRecorder()
	reqV1 := httptest.NewRequest(http.MethodGet, "/v1/models/gemini-2.5-flash?key=geminitestkey", nil)
	engine.ServeHTTP(recorderV1, reqV1)
	require.Equal(t, http.StatusOK, recorderV1.Code)
	var singleRespV1 map[string]any
	require.NoError(t, common.Unmarshal(recorderV1.Body.Bytes(), &singleRespV1))
	assert.Equal(t, "models/gemini-2.5-flash", singleRespV1["name"])
}

func setupRelayRouterTestDB(t *testing.T) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	originalIsMasterNode := common.IsMasterNode
	originalRedisEnabled := common.RedisEnabled
	originalSQLitePath := common.SQLitePath
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	originalSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")

	common.IsMasterNode = false
	common.RedisEnabled = false
	common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	require.NoError(t, model.InitDB())
	model.LOG_DB = model.DB
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}, &model.Ability{}))

	t.Cleanup(func() {
		if sqlDB, err := model.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		common.IsMasterNode = originalIsMasterNode
		common.RedisEnabled = originalRedisEnabled
		common.SQLitePath = originalSQLitePath
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
		if hadSQLDSN {
			require.NoError(t, os.Setenv("SQL_DSN", originalSQLDSN))
		} else {
			require.NoError(t, os.Unsetenv("SQL_DSN"))
		}
	})
}
