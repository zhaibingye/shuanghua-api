package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type creditsResponse struct {
	Object         string  `json:"object"`
	TotalGranted   float64 `json:"total_granted"`
	TotalUsed      float64 `json:"total_used"`
	TotalAvailable float64 `json:"total_available"`
	Data           struct {
		TotalGranted   float64 `json:"total_granted"`
		TotalUsed      float64 `json:"total_used"`
		TotalAvailable float64 `json:"total_available"`
		TotalUsage     float64 `json:"total_usage"`
	} `json:"data"`
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func setupBillingCreditsTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	gin.SetMode(gin.TestMode)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)

	oldRedisEnabled := common.RedisEnabled
	oldDisplayTokenStatEnabled := common.DisplayTokenStatEnabled
	oldQuotaDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	common.RedisEnabled = false
	common.DisplayTokenStatEnabled = true
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	t.Cleanup(func() {
		common.RedisEnabled = oldRedisEnabled
		common.DisplayTokenStatEnabled = oldDisplayTokenStatEnabled
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldQuotaDisplayType
	})

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db

	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	return db
}

func requestCredits(t *testing.T, configureContext func(*gin.Context)) creditsResponse {
	t.Helper()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/credits", nil)
	configureContext(ctx)

	GetCredits(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)

	var response creditsResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	return response
}

func TestGetCreditsUsesUserQuotaRegardlessOfTokenStats(t *testing.T) {
	db := setupBillingCreditsTestDB(t)

	user := model.User{
		Id:       34,
		Username: "credits-user",
		Password: "password123",
		Status:   common.UserStatusEnabled,
		Quota:    int(common.QuotaPerUnit * 4),
	}
	require.NoError(t, db.Create(&user).Error)

	token := model.Token{
		Id:          12,
		UserId:      user.Id,
		Key:         "credits-token",
		Status:      common.TokenStatusEnabled,
		RemainQuota: int(common.QuotaPerUnit * 2),
	}
	require.NoError(t, db.Create(&token).Error)

	// Even if DisplayTokenStatEnabled is true and token_id is set, credits must return user's quota.
	response := requestCredits(t, func(ctx *gin.Context) {
		ctx.Set("id", user.Id)
		ctx.Set("token_id", token.Id)
	})

	assert.Equal(t, 4.0, response.TotalAvailable)
	assert.Equal(t, 4.0, response.Data.TotalAvailable)
	assert.Equal(t, 4.0, response.Data.TotalUsage)
}

func TestGetCreditsUsesUserQuotaWhenTokenIsUnlimited(t *testing.T) {
	db := setupBillingCreditsTestDB(t)

	user := model.User{
		Id:       56,
		Username: "unlimited-token-user",
		Password: "password123",
		Status:   common.UserStatusEnabled,
		Quota:    int(common.QuotaPerUnit * 3),
	}
	require.NoError(t, db.Create(&user).Error)

	token := model.Token{
		Id:             78,
		UserId:         user.Id,
		Key:            "unlimited-token",
		Status:         common.TokenStatusEnabled,
		UnlimitedQuota: true,
	}
	require.NoError(t, db.Create(&token).Error)

	// Even if token is unlimited, credits must return user's real quota, not 100000000.
	response := requestCredits(t, func(ctx *gin.Context) {
		ctx.Set("id", user.Id)
		ctx.Set("token_id", token.Id)
		ctx.Set("token_unlimited_quota", true)
	})

	assert.Equal(t, 3.0, response.TotalAvailable)
	assert.Equal(t, 3.0, response.Data.TotalAvailable)
	assert.Equal(t, 3.0, response.Data.TotalUsage)
	assert.NotEqual(t, 100000000.0, response.TotalAvailable)
}

func TestGetCreditsCalculatesUsedAndTotalGranted(t *testing.T) {
	db := setupBillingCreditsTestDB(t)

	user := model.User{
		Id:        90,
		Username:  "used-quota-user",
		Password:  "password123",
		Status:    common.UserStatusEnabled,
		Quota:     int(common.QuotaPerUnit * 6),
		UsedQuota: int(common.QuotaPerUnit * 2),
	}
	require.NoError(t, db.Create(&user).Error)

	response := requestCredits(t, func(ctx *gin.Context) {
		ctx.Set("id", user.Id)
	})

	assert.Equal(t, "credit_summary", response.Object)
	assert.Equal(t, 6.0, response.TotalAvailable)
	assert.Equal(t, 2.0, response.TotalUsed)
	assert.Equal(t, 8.0, response.TotalGranted)
	assert.Equal(t, 6.0, response.Data.TotalAvailable)
	assert.Equal(t, 2.0, response.Data.TotalUsed)
	assert.Equal(t, 8.0, response.Data.TotalGranted)
	assert.Equal(t, 6.0, response.Data.TotalUsage)
}

func TestGetCreditsMissingUser(t *testing.T) {
	_ = setupBillingCreditsTestDB(t)

	response := requestCredits(t, func(ctx *gin.Context) {
		// id not set
	})

	assert.Equal(t, "user not found", response.Error.Message)
}
