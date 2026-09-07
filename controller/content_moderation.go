package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type contentModerationSettingsResponse struct {
	Enabled                bool   `json:"enabled"`
	Channels               string `json:"channels"`
	ChannelIDs             []int  `json:"channel_ids"`
	UserWhitelist          string `json:"user_whitelist"`
	UserWhitelistIDs       []int  `json:"user_whitelist_ids"`
	ViolationRetentionDays int    `json:"violation_retention_days"`
	BaseURL                string `json:"base_url"`
	Model                  string `json:"model"`
	PreflightEnabled       bool   `json:"preflight_enabled"`
	PostflightEnabled      bool   `json:"postflight_enabled"`
	FailureMode            string `json:"failure_mode"`
	TimeoutSeconds         int    `json:"timeout_seconds"`
	MaxRetries             int    `json:"max_retries"`
	AutoDisableViolations  int    `json:"auto_disable_violations"`
	APIKeyConfigured       bool   `json:"api_key_configured"`
	APIKeyCount            int    `json:"api_key_count"`
}

type contentModerationSettingsRequest struct {
	Enabled                bool   `json:"enabled"`
	Channels               string `json:"channels"`
	UserWhitelist          string `json:"user_whitelist"`
	ViolationRetentionDays int    `json:"violation_retention_days"`
	BaseURL                string `json:"base_url"`
	APIKey                 string `json:"api_key"`
	ClearAPIKey            bool   `json:"clear_api_key"`
	Model                  string `json:"model"`
	PreflightEnabled       *bool  `json:"preflight_enabled"`
	PostflightEnabled      *bool  `json:"postflight_enabled"`
	FailureMode            string `json:"failure_mode"`
	TimeoutSeconds         int    `json:"timeout_seconds"`
	MaxRetries             int    `json:"max_retries"`
	AutoDisableViolations  int    `json:"auto_disable_violations"`
}

func GetContentModerationSettings(c *gin.Context) {
	recordManageAudit(c, "moderation.settings_view", nil)
	config := setting.GetContentModerationSetting()
	channelIDs := config.ChannelIDs
	if channelIDs == nil {
		channelIDs = []int{}
	}
	userWhitelistIDs := config.UserWhitelistIDs
	if userWhitelistIDs == nil {
		userWhitelistIDs = []int{}
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": contentModerationSettingsResponse{
			Enabled:                config.Enabled,
			Channels:               config.Channels,
			ChannelIDs:             channelIDs,
			UserWhitelist:          config.UserWhitelist,
			UserWhitelistIDs:       userWhitelistIDs,
			ViolationRetentionDays: config.ViolationRetentionDays,
			BaseURL:                config.BaseURL,
			Model:                  config.Model,
			PreflightEnabled:       config.PreflightEnabled,
			PostflightEnabled:      config.PostflightEnabled,
			FailureMode:            config.FailureMode,
			TimeoutSeconds:         config.TimeoutSeconds,
			MaxRetries:             config.MaxRetries,
			AutoDisableViolations:  config.AutoDisableViolations,
			APIKeyConfigured:       config.HasAPIKey(),
			APIKeyCount:            len(config.ResolvedAPIKeys()),
		},
	})
}

func UpdateContentModerationSettings(c *gin.Context) {
	var request contentModerationSettingsRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid content moderation settings"})
		return
	}
	modelName := strings.TrimSpace(request.Model)
	if modelName == "" {
		modelName = setting.DefaultContentModerationModel
	}
	apiKey := strings.TrimSpace(request.APIKey)
	baseURL := strings.TrimSpace(request.BaseURL)
	if request.PreflightEnabled == nil {
		defaultPreflight := true
		request.PreflightEnabled = &defaultPreflight
	}
	if request.PostflightEnabled == nil {
		defaultPostflight := false
		request.PostflightEnabled = &defaultPostflight
	}
	failureMode := strings.ToLower(strings.TrimSpace(request.FailureMode))
	if failureMode == "" {
		failureMode = setting.DefaultContentModerationFailureMode
	}
	if failureMode != "open" && failureMode != "closed" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "failure mode must be open or closed"})
		return
	}
	parsedChannelIDs, err := setting.ValidateChannelIDsString(request.Channels)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel IDs must be positive integers separated by commas or spaces"})
		return
	}
	normalizedChannels := setting.FormatChannelIDs(parsedChannelIDs)
	parsedUserWhitelistIDs, err := setting.ValidateUserIDsString(request.UserWhitelist)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "user whitelist IDs must be positive integers separated by commas or spaces"})
		return
	}
	if !slices.Contains(parsedUserWhitelistIDs, setting.RootAdminUserID) {
		parsedUserWhitelistIDs = append([]int{setting.RootAdminUserID}, parsedUserWhitelistIDs...)
		slices.Sort(parsedUserWhitelistIDs)
	}
	normalizedUserWhitelist := setting.FormatUserIDs(parsedUserWhitelistIDs)
	if request.ViolationRetentionDays == 0 {
		request.ViolationRetentionDays = setting.DefaultContentModerationViolationRetentionDays
	}
	if request.ViolationRetentionDays < 1 || request.ViolationRetentionDays > 365 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "violation retention days must be between 1 and 365"})
		return
	}
	var parsedKeys []string
	if apiKey != "" {
		parsed, err := setting.ParseModerationAPIKeys(apiKey)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
			return
		}
		parsedKeys = parsed
		apiKey = setting.FormatModerationAPIKeys(parsed)
	}
	if len(modelName) > 128 || len(baseURL) > 2048 || len(normalizedChannels) > 2048 || len(normalizedUserWhitelist) > 2048 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "content moderation setting is too long"})
		return
	}
	if baseURL != "" {
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "base URL must be an absolute HTTP(S) URL without credentials"})
			return
		}
		if err := service.ValidateContentModerationURL(baseURL); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
			return
		}
	}
	currentConfig := setting.GetContentModerationSetting()
	isClearingKey := request.ClearAPIKey && len(parsedKeys) == 0
	if request.Enabled {
		hasKey := (len(parsedKeys) > 0) || (!isClearingKey && currentConfig.HasAPIKey())
		if modelName == "" || !hasKey {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "model and API key are required when content moderation is enabled"})
			return
		}
	}
	if request.TimeoutSeconds < 1 || request.TimeoutSeconds > 120 || request.MaxRetries < 1 || request.MaxRetries > 5 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "content moderation limits are out of range"})
		return
	}
	if request.AutoDisableViolations < 0 || request.AutoDisableViolations > 1000 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "auto-disable violation threshold must be between 0 and 1000"})
		return
	}

	values := map[string]string{
		setting.ContentModerationEnabledOption:                strconv.FormatBool(request.Enabled),
		setting.ContentModerationChannelsOption:               normalizedChannels,
		setting.ContentModerationUserWhitelistOption:          normalizedUserWhitelist,
		setting.ContentModerationViolationRetentionDaysOption: strconv.Itoa(request.ViolationRetentionDays),
		setting.ContentModerationBaseURLOption:                baseURL,
		setting.ContentModerationModelOption:                  modelName,
		setting.ContentModerationPreflightOption:              strconv.FormatBool(*request.PreflightEnabled),
		setting.ContentModerationPostflightOption:             strconv.FormatBool(*request.PostflightEnabled),
		setting.ContentModerationFailureModeOption:            failureMode,
		setting.ContentModerationTimeoutSecondsOption:         strconv.Itoa(request.TimeoutSeconds),
		setting.ContentModerationMaxRetriesOption:             strconv.Itoa(request.MaxRetries),
		setting.ContentModerationAutoDisableViolationsOption:  strconv.Itoa(request.AutoDisableViolations),
	}
	effectiveAPIKey := apiKey
	if isClearingKey {
		values[setting.ContentModerationAPIKeyOption] = ""
	} else if effectiveAPIKey != "" {
		values[setting.ContentModerationAPIKeyOption] = effectiveAPIKey
	}
	if err := model.UpdateOptionsBulk(values); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	recordManageAudit(c, "moderation.settings_update", map[string]interface{}{
		"model":                    modelName,
		"enabled":                  request.Enabled,
		"channels":                 normalizedChannels,
		"user_whitelist":           normalizedUserWhitelist,
		"violation_retention_days": request.ViolationRetentionDays,
	})
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// GetContentModerationKey returns the configured content moderation API key.
// Protected by SecureVerificationRequired middleware to match channel key security.
func GetContentModerationKey(c *gin.Context) {
	recordManageAudit(c, "moderation.key_view", nil)
	config := setting.GetContentModerationSetting()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"key": config.APIKey,
		},
	})
}

type contentModerationKeyTestRequest struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	APIKey  string `json:"api_key"`
}

func TestContentModerationKeys(c *gin.Context) {
	recordManageAudit(c, "moderation.keys_test", nil)
	var request contentModerationKeyTestRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid content moderation key test request"})
		return
	}
	config := setting.GetContentModerationSetting()
	baseURL := strings.TrimSpace(request.BaseURL)
	if baseURL == "" {
		baseURL = config.BaseURL
	}
	if baseURL != "" {
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "base URL must be an absolute HTTP(S) URL without credentials"})
			return
		}
		if err := service.ValidateContentModerationURL(baseURL); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
			return
		}
	}
	modelName := strings.TrimSpace(request.Model)
	if modelName == "" {
		modelName = config.Model
	}
	if modelName == "" {
		modelName = setting.DefaultContentModerationModel
	}
	if len(modelName) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "content moderation setting is too long"})
		return
	}
	keys, err := setting.ParseModerationAPIKeys(request.APIKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if len(keys) == 0 {
		keys = config.ResolvedAPIKeys()
	}
	if len(keys) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "no moderation API keys to test"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	results := service.TestModerationAPIKeys(ctx, setting.ContentModerationSetting{
		BaseURL:        baseURL,
		Model:          modelName,
		TimeoutSeconds: config.TimeoutSeconds,
	}, keys)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"results": results,
		},
	})
}

type moderationActionRequest struct {
	Reason string `json:"reason"`
}

func validateModerationActionRequest(c *gin.Context, reason string) bool {
	if len(reason) <= 4096 {
		return true
	}
	c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "moderation action reason is too long"})
	return false
}

func ListContentModerationEvents(c *gin.Context) {
	recordManageAudit(c, "moderation.events_list", map[string]interface{}{
		"user_id": c.Query("user_id"),
		"status":  c.Query("status"),
		"source":  c.Query("source"),
	})
	limit := parseModerationLimit(c.Query("limit"))
	offset := parseModerationOffset(c.Query("offset"))
	now := common.GetTimestamp()
	cutoff := now - int64(setting.GetContentModerationSetting().GetViolationRetentionDuration().Seconds())
	query := model.DB.Model(&model.ModerationEvent{}).
		Where("expires_at > ? AND created_at >= ?", now, cutoff)
	if userID := parsePositiveInt(c.Query("user_id")); userID > 0 {
		query = query.Where("user_id = ?", userID)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" && status != "all" {
		query = query.Where("status = ?", status)
	}
	if source := strings.TrimSpace(c.Query("source")); source != "" && source != "all" {
		query = query.Where("source = ?", source)
	}
	if start := parsePositiveInt64(c.Query("start_timestamp")); start > 0 {
		query = query.Where("created_at >= ?", start)
	}
	if end := parsePositiveInt64(c.Query("end_timestamp")); end > 0 {
		query = query.Where("created_at <= ?", end)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	events := make([]model.ModerationEvent, 0)
	if err := query.Order("created_at desc").Limit(limit).Offset(offset).Find(&events).Error; err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": events, "total": total})
}

func ResolveContentModerationEvent(c *gin.Context) {
	eventID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || eventID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid event id"})
		return
	}
	var request struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid resolution request"})
		return
	}
	if request.Status != model.ModerationEventFalsePositive && request.Status != model.ModerationEventReversed {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid moderation resolution"})
		return
	}
	if !validateModerationActionRequest(c, request.Reason) {
		return
	}
	if err := service.ResolveModerationEvent(eventID, int64(c.GetInt("id")), request.Status, request.Reason); err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	recordManageAudit(c, "moderation.event_resolve", map[string]interface{}{"id": eventID})
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func RestoreContentModerationUser(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid user id"})
		return
	}
	var request moderationActionRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid action request"})
		return
	}
	if !validateModerationActionRequest(c, request.Reason) {
		return
	}
	if err := service.RestoreUserAfterModeration(userID, c.GetInt("id"), request.Reason); err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	recordManageAudit(c, "moderation.user_restore", map[string]interface{}{"id": userID})
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func ListContentModerationUsers(c *gin.Context) {
	recordManageAudit(c, "moderation.users_list", map[string]interface{}{
		"user_id": c.Query("user_id"),
		"status":  c.Query("status"),
	})
	users, total, err := service.ListModerationUsers(
		strings.TrimSpace(c.Query("status")),
		parsePositiveInt(c.Query("user_id")),
		parseModerationLimit(c.Query("limit")),
		parseModerationOffset(c.Query("offset")),
	)
	if err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": users, "total": total})
}

func GetContentModerationUser(c *gin.Context) {
	userID := parsePositiveInt(c.Param("id"))
	if userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid user id"})
		return
	}
	recordManageAudit(c, "moderation.user_view", map[string]interface{}{"id": userID})
	detail, err := service.GetModerationUserDetail(userID)
	if err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": detail})
}

func UpdateContentModerationUser(c *gin.Context) {
	userID := parsePositiveInt(c.Param("id"))
	if userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid user id"})
		return
	}
	if err := validateModerationTargetRole(c, userID); err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	var request struct {
		ViolationCount *int   `json:"violation_count"`
		Note           string `json:"note"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.ViolationCount == nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid moderation user record"})
		return
	}
	if err := service.UpdateModerationUserRecord(userID, c.GetInt("id"), c.GetInt("role"), *request.ViolationCount, request.Note); err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	recordManageAudit(c, "moderation.user_update", map[string]interface{}{
		"id":              userID,
		"violation_count": *request.ViolationCount,
	})
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func UpdateContentModerationUserStatus(c *gin.Context) {
	userID := parsePositiveInt(c.Param("id"))
	if userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid user id"})
		return
	}
	if err := validateModerationTargetRole(c, userID); err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	var request struct {
		Enabled *bool  `json:"enabled"`
		Reason  string `json:"reason"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid account status request"})
		return
	}
	if err := service.SetModerationUserAccountStatus(userID, c.GetInt("id"), c.GetInt("role"), *request.Enabled, request.Reason); err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	action := "disabled"
	if *request.Enabled {
		action = "enabled"
	}
	recordManageAudit(c, "moderation.user_status", map[string]interface{}{"id": userID, "status": action})
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func DeleteContentModerationUserHistory(c *gin.Context) {
	userID := parsePositiveInt(c.Param("id"))
	if userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid user id"})
		return
	}
	if err := validateModerationTargetRole(c, userID); err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	if err := service.DeleteModerationUserHistory(userID, c.GetInt("id"), c.GetInt("role")); err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	recordManageAudit(c, "moderation.user_history_delete", map[string]interface{}{"id": userID})
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func validateModerationTargetRole(c *gin.Context, userID int) error {
	var target model.User
	if err := model.DB.Unscoped().Select("role").First(&target, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if !canManageTargetRole(c.GetInt("role"), target.Role) {
		return service.ErrModerationUserPermissionDenied
	}
	return nil
}

func parseModerationLimit(value string) int {
	limit, _ := strconv.Atoi(value)
	if limit < 1 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func parseModerationOffset(value string) int {
	offset, _ := strconv.Atoi(value)
	if offset < 0 {
		return 0
	}
	return offset
}

func parsePositiveInt(value string) int {
	parsed, _ := strconv.Atoi(strings.TrimSpace(value))
	if parsed < 1 {
		return 0
	}
	return parsed
}

func parsePositiveInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if parsed < 1 {
		return 0
	}
	return parsed
}

func writeModerationDatabaseError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrModerationUserPermissionDenied) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": err.Error()})
		return
	}
	if errors.Is(err, service.ErrInvalidModerationUserRequest) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "content moderation record not found"})
		return
	}
	if errors.Is(err, model.ErrModerationAccountNotDisabled) || errors.Is(err, model.ErrModerationUserHistoryOnly) {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
}
