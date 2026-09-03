package setting

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const (
	ContentModerationEnabledOption            = "ContentModerationEnabled"
	ContentModerationProviderOption           = "ContentModerationProvider"
	ContentModerationBaseURLOption            = "ContentModerationBaseURL"
	ContentModerationAPIKeyOption             = "ContentModerationAPIKey"
	ContentModerationModelOption              = "ContentModerationModel"
	ContentModerationTimeoutSecondsOption     = "ContentModerationTimeoutSeconds"
	ContentModerationMaxRetriesOption         = "ContentModerationMaxRetries"
	ContentModerationNormalSampleRateOption   = "ContentModerationNormalSampleRate"
	ContentModerationElevatedSampleRateOption = "ContentModerationElevatedSampleRate"
	ContentModerationPromptVersionOption      = "ContentModerationPromptVersion"
)

const (
	DefaultContentModerationProvider           = "responses"
	DefaultContentModerationTimeoutSeconds     = 30
	DefaultContentModerationMaxRetries         = 3
	DefaultContentModerationNormalSampleRate   = 10
	DefaultContentModerationElevatedSampleRate = 50
	DefaultContentModerationPromptVersion      = "v1"
)

type ContentModerationSetting struct {
	Enabled            bool
	Provider           string
	BaseURL            string
	APIKey             string
	Model              string
	TimeoutSeconds     int
	MaxRetries         int
	NormalSampleRate   int
	ElevatedSampleRate int
	PromptVersion      string
}

func GetContentModerationSetting() ContentModerationSetting {
	apiKey := optionString(ContentModerationAPIKeyOption, "")
	if apiKey != "" {
		decrypted, err := common.DecryptSecret(apiKey)
		if err != nil {
			common.SysError("failed to decrypt content moderation API key: " + err.Error())
			apiKey = ""
		} else {
			apiKey = strings.TrimSpace(decrypted)
		}
	}
	if len(apiKey) > 4096 {
		common.SysError("content moderation API key exceeds the configured length limit")
		apiKey = ""
	}
	provider := strings.ToLower(optionString(ContentModerationProviderOption, DefaultContentModerationProvider))
	if provider != "responses" && provider != "gemini" {
		provider = DefaultContentModerationProvider
	}
	timeoutSeconds := optionInt(ContentModerationTimeoutSecondsOption, DefaultContentModerationTimeoutSeconds)
	if timeoutSeconds < 1 || timeoutSeconds > 120 {
		timeoutSeconds = DefaultContentModerationTimeoutSeconds
	}
	maxRetries := optionInt(ContentModerationMaxRetriesOption, DefaultContentModerationMaxRetries)
	if maxRetries < 1 || maxRetries > 5 {
		maxRetries = DefaultContentModerationMaxRetries
	}
	normalSampleRate := optionInt(ContentModerationNormalSampleRateOption, DefaultContentModerationNormalSampleRate)
	if normalSampleRate < 0 || normalSampleRate > 100 {
		normalSampleRate = DefaultContentModerationNormalSampleRate
	}
	elevatedSampleRate := optionInt(ContentModerationElevatedSampleRateOption, DefaultContentModerationElevatedSampleRate)
	if elevatedSampleRate < 0 || elevatedSampleRate > 100 {
		elevatedSampleRate = DefaultContentModerationElevatedSampleRate
	}
	baseURL := optionString(ContentModerationBaseURLOption, "")
	if len(baseURL) > 2048 {
		common.SysError("content moderation API URL exceeds the configured length limit")
		baseURL = ""
	}
	modelName := optionString(ContentModerationModelOption, "")
	if len(modelName) > 128 {
		common.SysError("content moderation model exceeds the configured length limit")
		modelName = ""
	}
	promptVersion := optionString(ContentModerationPromptVersionOption, DefaultContentModerationPromptVersion)
	if promptVersion == "" || len(promptVersion) > 32 {
		promptVersion = DefaultContentModerationPromptVersion
	}
	return ContentModerationSetting{
		Enabled:            optionBool(ContentModerationEnabledOption, false),
		Provider:           provider,
		BaseURL:            baseURL,
		APIKey:             apiKey,
		Model:              modelName,
		TimeoutSeconds:     timeoutSeconds,
		MaxRetries:         maxRetries,
		NormalSampleRate:   normalSampleRate,
		ElevatedSampleRate: elevatedSampleRate,
		PromptVersion:      promptVersion,
	}
}

func optionString(key, fallback string) string {
	common.OptionMapRWMutex.RLock()
	value, ok := common.OptionMap[key]
	common.OptionMapRWMutex.RUnlock()
	if !ok {
		return fallback
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func optionBool(key string, fallback bool) bool {
	common.OptionMapRWMutex.RLock()
	value, ok := common.OptionMap[key]
	common.OptionMapRWMutex.RUnlock()
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

func optionInt(key string, fallback int) int {
	common.OptionMapRWMutex.RLock()
	value, ok := common.OptionMap[key]
	common.OptionMapRWMutex.RUnlock()
	if !ok {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}
