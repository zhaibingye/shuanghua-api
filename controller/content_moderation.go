package controller

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type contentModerationSettingsResponse struct {
	Enabled             bool   `json:"enabled"`
	Provider            string `json:"provider"`
	BaseURL             string `json:"base_url"`
	Model               string `json:"model"`
	TimeoutSeconds      int    `json:"timeout_seconds"`
	MaxRetries          int    `json:"max_retries"`
	NormalSampleRate    int    `json:"normal_sample_rate"`
	ElevatedSampleRate  int    `json:"elevated_sample_rate"`
	PromptVersion       string `json:"prompt_version"`
	PolicyPrompt        string `json:"policy_prompt"`
	DefaultPolicyPrompt string `json:"default_policy_prompt"`
	APIKeyConfigured    bool   `json:"api_key_configured"`
}

type contentModerationSettingsRequest struct {
	Enabled            bool   `json:"enabled"`
	Provider           string `json:"provider"`
	BaseURL            string `json:"base_url"`
	APIKey             string `json:"api_key"`
	Model              string `json:"model"`
	TimeoutSeconds     int    `json:"timeout_seconds"`
	MaxRetries         int    `json:"max_retries"`
	NormalSampleRate   int    `json:"normal_sample_rate"`
	ElevatedSampleRate int    `json:"elevated_sample_rate"`
	PromptVersion      string `json:"prompt_version"`
	PolicyPrompt       string `json:"policy_prompt"`
}

func GetContentModerationSettings(c *gin.Context) {
	recordManageAudit(c, "moderation.settings_view", nil)
	config := setting.GetContentModerationSetting()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": contentModerationSettingsResponse{
			Enabled:             config.Enabled,
			Provider:            config.Provider,
			BaseURL:             config.BaseURL,
			Model:               config.Model,
			TimeoutSeconds:      config.TimeoutSeconds,
			MaxRetries:          config.MaxRetries,
			NormalSampleRate:    config.NormalSampleRate,
			ElevatedSampleRate:  config.ElevatedSampleRate,
			PromptVersion:       config.PromptVersion,
			PolicyPrompt:        config.PolicyPrompt,
			DefaultPolicyPrompt: setting.DefaultContentModerationPolicyPrompt,
			APIKeyConfigured:    strings.TrimSpace(config.APIKey) != "",
		},
	})
}

func UpdateContentModerationSettings(c *gin.Context) {
	var request contentModerationSettingsRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid content moderation settings"})
		return
	}
	request.Provider = strings.ToLower(strings.TrimSpace(request.Provider))
	if request.Provider != "responses" && request.Provider != "gemini" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "provider must be responses or gemini"})
		return
	}
	modelName := strings.TrimSpace(request.Model)
	apiKey := strings.TrimSpace(request.APIKey)
	baseURL := strings.TrimSpace(request.BaseURL)
	promptVersion := strings.TrimSpace(request.PromptVersion)
	policyPrompt := strings.TrimSpace(request.PolicyPrompt)
	if policyPrompt == "" {
		policyPrompt = setting.DefaultContentModerationPolicyPrompt
	}
	if len(modelName) > 128 || len(apiKey) > 4096 || len(baseURL) > 2048 || len(promptVersion) > 32 || len(policyPrompt) > 16384 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "content moderation setting is too long"})
		return
	}
	if strings.IndexFunc(apiKey, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "content moderation API key contains invalid control characters"})
		return
	}
	if strings.IndexFunc(policyPrompt, func(r rune) bool { return (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f }) >= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "content moderation policy prompt contains invalid control characters"})
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
	if request.Enabled && (modelName == "" || apiKey == "" && strings.TrimSpace(currentConfig.APIKey) == "") {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "model and API key are required when content moderation is enabled"})
		return
	}
	if request.TimeoutSeconds < 1 || request.TimeoutSeconds > 120 || request.MaxRetries < 1 || request.MaxRetries > 5 || request.NormalSampleRate < 0 || request.NormalSampleRate > 100 || request.ElevatedSampleRate < 0 || request.ElevatedSampleRate > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "content moderation limits are out of range"})
		return
	}
	if promptVersion == "" || len(promptVersion) > 32 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "prompt version is required"})
		return
	}

	values := map[string]string{
		setting.ContentModerationEnabledOption:            strconv.FormatBool(request.Enabled),
		setting.ContentModerationProviderOption:           request.Provider,
		setting.ContentModerationBaseURLOption:            baseURL,
		setting.ContentModerationModelOption:              modelName,
		setting.ContentModerationTimeoutSecondsOption:     strconv.Itoa(request.TimeoutSeconds),
		setting.ContentModerationMaxRetriesOption:         strconv.Itoa(request.MaxRetries),
		setting.ContentModerationNormalSampleRateOption:   strconv.Itoa(request.NormalSampleRate),
		setting.ContentModerationElevatedSampleRateOption: strconv.Itoa(request.ElevatedSampleRate),
		setting.ContentModerationPromptVersionOption:      promptVersion,
		setting.ContentModerationPolicyPromptOption:       policyPrompt,
	}
	effectiveAPIKey := apiKey
	if effectiveAPIKey == "" {
		effectiveAPIKey = strings.TrimSpace(currentConfig.APIKey)
	}
	if effectiveAPIKey != "" {
		encryptedAPIKey, err := common.EncryptSecret(effectiveAPIKey)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to protect moderation API key"})
			return
		}
		// Re-encrypting an existing value also migrates legacy plaintext
		// configuration the next time the dedicated settings endpoint is used.
		values[setting.ContentModerationAPIKeyOption] = encryptedAPIKey
	}
	if err := model.UpdateOptionsBulk(values); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	recordManageAudit(c, "moderation.settings_update", map[string]interface{}{
		"provider": request.Provider,
		"enabled":  request.Enabled,
	})
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func ListContentModerationConversations(c *gin.Context) {
	recordManageAudit(c, "moderation.conversations_list", map[string]interface{}{
		"user_id":         c.Query("user_id"),
		"status":          c.Query("status"),
		"conversation_id": c.Query("conversation_id"),
	})
	limit := parseModerationLimit(c.Query("limit"))
	offset := parseModerationOffset(c.Query("offset"))
	query := model.DB.Model(&model.ModerationConversation{})
	if userID := parsePositiveInt(c.Query("user_id")); userID > 0 {
		query = query.Where("user_id = ?", userID)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" && status != "all" {
		query = query.Where("status = ?", status)
	}
	if conversationID := strings.TrimSpace(c.Query("conversation_id")); conversationID != "" {
		query = query.Where("conversation_id LIKE ?", "%"+conversationID+"%")
	}
	if start := parsePositiveInt64(c.Query("start_timestamp")); start > 0 {
		query = query.Where("last_activity_at >= ?", start)
	}
	if end := parsePositiveInt64(c.Query("end_timestamp")); end > 0 {
		query = query.Where("last_activity_at <= ?", end)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	conversations := make([]model.ModerationConversation, 0)
	if err := query.Order("last_activity_at desc").Limit(limit).Offset(offset).Find(&conversations).Error; err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": conversations, "total": total})
}

func GetContentModerationConversation(c *gin.Context) {
	recordManageAudit(c, "moderation.conversation_view", map[string]interface{}{"id": c.Param("id")})
	conversationID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || conversationID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid conversation id"})
		return
	}
	var conversation model.ModerationConversation
	if err := model.DB.First(&conversation, conversationID).Error; err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	turns := make([]model.ModerationTurn, 0)
	jobs := make([]model.ModerationJob, 0)
	violations := make([]model.ModerationViolation, 0)
	actions := make([]model.ModerationAction, 0)
	notifications := make([]model.ModerationNotification, 0)
	if err := model.DB.Where("conversation_id = ?", conversationID).Order("round_number asc").Find(&turns).Error; err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	if err := model.DB.Where("conversation_id = ?", conversationID).Order("id asc").Find(&jobs).Error; err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	if err := model.DB.Where("user_id = ? AND conversation_id = ?", conversation.UserID, conversation.ConversationID).Order("id asc").Find(&violations).Error; err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	if err := model.DB.Where("user_id = ? AND conversation_id = ?", conversation.UserID, conversation.ConversationID).Order("id asc").Find(&actions).Error; err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	if len(violations) > 0 {
		violationIDs := make([]int64, 0, len(violations))
		for _, violation := range violations {
			violationIDs = append(violationIDs, violation.ID)
		}
		if err := model.DB.Where("violation_id IN ?", violationIDs).Order("id asc").Find(&notifications).Error; err != nil {
			writeModerationDatabaseError(c, err)
			return
		}
	}
	for i := range turns {
		if err := service.DecryptModerationTurnContent(&turns[i]); err != nil {
			writeModerationDatabaseError(c, err)
			return
		}
	}
	for i := range jobs {
		requestPayload, decryptErr := service.DecryptModerationStoredText(string(jobs[i].RequestPayload))
		if decryptErr != nil {
			writeModerationDatabaseError(c, decryptErr)
			return
		}
		responsePayload, decryptErr := service.DecryptModerationStoredText(string(jobs[i].ResponsePayload))
		if decryptErr != nil {
			writeModerationDatabaseError(c, decryptErr)
			return
		}
		jobs[i].RequestPayload = model.ModerationText(requestPayload)
		jobs[i].ResponsePayload = model.ModerationText(responsePayload)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"conversation":  conversation,
		"turns":         turns,
		"jobs":          jobs,
		"violations":    violations,
		"actions":       actions,
		"notifications": notifications,
	}})
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

func UnblockContentModerationConversation(c *gin.Context) {
	conversationID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || conversationID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid conversation id"})
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
	if err := service.UnblockModerationConversation(conversationID, c.GetInt("id"), request.Reason); err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	recordManageAudit(c, "moderation.conversation_unblock", map[string]interface{}{"id": conversationID})
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func ResolveContentModerationViolation(c *gin.Context) {
	violationID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || violationID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid violation id"})
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
	if request.Status != model.ModerationViolationFalsePositive && request.Status != model.ModerationViolationReversed {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid moderation resolution"})
		return
	}
	if !validateModerationActionRequest(c, request.Reason) {
		return
	}
	if err := service.ResolveModerationViolation(violationID, int64(c.GetInt("id")), request.Status, request.Reason); err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	recordManageAudit(c, "moderation.violation_resolve", map[string]interface{}{"id": violationID})
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

func ListContentModerationViolations(c *gin.Context) {
	recordManageAudit(c, "moderation.violations_list", map[string]interface{}{
		"user_id":         c.Query("user_id"),
		"status":          c.Query("status"),
		"conversation_id": c.Query("conversation_id"),
	})
	limit := parseModerationLimit(c.Query("limit"))
	offset := parseModerationOffset(c.Query("offset"))
	query := model.DB.Model(&model.ModerationViolation{})
	if userID := parsePositiveInt(c.Query("user_id")); userID > 0 {
		query = query.Where("user_id = ?", userID)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" && status != "all" {
		query = query.Where("status = ?", status)
	}
	if conversationID := strings.TrimSpace(c.Query("conversation_id")); conversationID != "" {
		query = query.Where("conversation_id LIKE ?", "%"+conversationID+"%")
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
	violations := make([]model.ModerationViolation, 0)
	if err := query.Order("created_at desc").Limit(limit).Offset(offset).Find(&violations).Error; err != nil {
		writeModerationDatabaseError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": violations, "total": total})
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
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "content moderation record not found"})
		return
	}
	if errors.Is(err, model.ErrModerationAccountNotDisabled) {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
}
