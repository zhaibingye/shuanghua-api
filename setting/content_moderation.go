package setting

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	ContentModerationEnabledOption                = "ContentModerationEnabled"
	ContentModerationChannelsOption               = "ContentModerationChannels"
	ContentModerationUserWhitelistOption          = "ContentModerationUserWhitelist"
	ContentModerationViolationRetentionDaysOption = "ContentModerationViolationRetentionDays"
	ContentModerationBaseURLOption                = "ContentModerationBaseURL"
	ContentModerationAPIKeyOption                 = "ContentModerationAPIKey"
	ContentModerationModelOption                  = "ContentModerationModel"
	ContentModerationPreflightOption              = "ContentModerationPreflight"
	ContentModerationFailureModeOption            = "ContentModerationFailureMode"
	ContentModerationTimeoutSecondsOption         = "ContentModerationTimeoutSeconds"
	ContentModerationMaxRetriesOption             = "ContentModerationMaxRetries"
)

const (
	DefaultContentModerationBaseURL                = "https://api.openai.com/v1"
	DefaultContentModerationModel                  = "omni-moderation-latest"
	DefaultContentModerationPreflight              = true
	DefaultContentModerationFailureMode            = "closed"
	DefaultContentModerationTimeoutSeconds         = 30
	DefaultContentModerationMaxRetries             = 3
	DefaultContentModerationUserWhitelist          = "1"
	DefaultContentModerationViolationRetentionDays = 7
	RootAdminUserID                                = 1
)

type ContentModerationSetting struct {
	Enabled                bool
	Channels               string
	ChannelIDs             []int
	UserWhitelist          string
	UserWhitelistIDs       []int
	ViolationRetentionDays int
	BaseURL                string
	APIKey                 string
	Model                  string
	PreflightEnabled       bool
	FailureMode            string
	TimeoutSeconds         int
	MaxRetries             int
}

func GetContentModerationSetting() ContentModerationSetting {
	apiKey := optionString(ContentModerationAPIKeyOption, "")
	if apiKey != "" {
		if strings.HasPrefix(apiKey, "enc:v1:") {
			decrypted, err := common.DecryptSecret(apiKey)
			if err != nil {
				common.SysError("failed to decrypt content moderation API key: " + err.Error())
				apiKey = ""
			} else {
				apiKey = strings.TrimSpace(decrypted)
			}
		} else {
			apiKey = strings.TrimSpace(apiKey)
		}
	}
	if len(apiKey) > 4096 {
		common.SysError("content moderation API key exceeds the configured length limit")
		apiKey = ""
	}
	timeoutSeconds := optionInt(ContentModerationTimeoutSecondsOption, DefaultContentModerationTimeoutSeconds)
	if timeoutSeconds < 1 || timeoutSeconds > 120 {
		timeoutSeconds = DefaultContentModerationTimeoutSeconds
	}
	maxRetries := optionInt(ContentModerationMaxRetriesOption, DefaultContentModerationMaxRetries)
	if maxRetries < 1 || maxRetries > 5 {
		maxRetries = DefaultContentModerationMaxRetries
	}
	baseURL := optionString(ContentModerationBaseURLOption, DefaultContentModerationBaseURL)
	if baseURL == "" {
		baseURL = DefaultContentModerationBaseURL
	}
	if len(baseURL) > 2048 {
		common.SysError("content moderation API URL exceeds the configured length limit")
		baseURL = DefaultContentModerationBaseURL
	}
	modelName := optionString(ContentModerationModelOption, DefaultContentModerationModel)
	if modelName == "" {
		modelName = DefaultContentModerationModel
	}
	if len(modelName) > 128 {
		common.SysError("content moderation model exceeds the configured length limit")
		modelName = ""
	}
	channelsRaw := optionString(ContentModerationChannelsOption, "")
	channelIDs := ParseChannelIDs(channelsRaw)
	retentionDays := optionInt(ContentModerationViolationRetentionDaysOption, DefaultContentModerationViolationRetentionDays)
	if retentionDays < 1 || retentionDays > 365 {
		retentionDays = DefaultContentModerationViolationRetentionDays
	}
	userWhitelistRaw := optionString(ContentModerationUserWhitelistOption, DefaultContentModerationUserWhitelist)
	userWhitelistIDs := ParseUserIDs(userWhitelistRaw)
	if !slices.Contains(userWhitelistIDs, RootAdminUserID) {
		userWhitelistIDs = append([]int{RootAdminUserID}, userWhitelistIDs...)
		slices.Sort(userWhitelistIDs)
	}
	preflightEnabled := optionBool(ContentModerationPreflightOption, DefaultContentModerationPreflight)
	failureMode := strings.ToLower(optionString(ContentModerationFailureModeOption, DefaultContentModerationFailureMode))
	if failureMode != "open" && failureMode != "closed" {
		failureMode = DefaultContentModerationFailureMode
	}
	return ContentModerationSetting{
		Enabled:                optionBool(ContentModerationEnabledOption, false),
		Channels:               FormatChannelIDs(channelIDs),
		ChannelIDs:             channelIDs,
		UserWhitelist:          FormatUserIDs(userWhitelistIDs),
		UserWhitelistIDs:       userWhitelistIDs,
		ViolationRetentionDays: retentionDays,
		BaseURL:                baseURL,
		APIKey:                 apiKey,
		Model:                  modelName,
		PreflightEnabled:       preflightEnabled,
		FailureMode:            failureMode,
		TimeoutSeconds:         timeoutSeconds,
		MaxRetries:             maxRetries,
	}
}

// ParseChannelIDs parses comma/space/newline separated channel IDs into a sorted deduplicated slice of positive ints.
func ParseChannelIDs(input string) []int {
	ids, _ := ValidateChannelIDsString(input)
	if ids == nil {
		return []int{}
	}
	return ids
}

// FormatChannelIDs formats a slice of channel IDs into a comma-separated string.
func FormatChannelIDs(ids []int) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ", ")
}

// ValidateChannelIDsString validates that the input consists of valid positive integers separated by commas/spaces.
func ValidateChannelIDsString(input string) ([]int, error) {
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}
	normalized := strings.ReplaceAll(input, "，", ",")
	fields := strings.FieldsFunc(normalized, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	seen := make(map[int]bool)
	var ids []int
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		id, err := strconv.Atoi(f)
		if err != nil || id <= 0 {
			return nil, strconv.ErrSyntax
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// ParseUserIDs parses comma/space/newline separated user IDs into a sorted deduplicated slice of positive ints.
func ParseUserIDs(input string) []int {
	ids, _ := ValidateUserIDsString(input)
	if ids == nil {
		return []int{}
	}
	return ids
}

// FormatUserIDs formats a slice of user IDs into a comma-separated string.
func FormatUserIDs(ids []int) string {
	return FormatChannelIDs(ids)
}

// ValidateUserIDsString validates that the input consists of valid positive integers separated by commas/spaces.
func ValidateUserIDsString(input string) ([]int, error) {
	return ValidateChannelIDsString(input)
}

func (s ContentModerationSetting) IsUserWhitelisted(userID int) bool {
	if userID <= 0 {
		return false
	}
	if userID == RootAdminUserID {
		return true
	}
	return slices.Contains(s.UserWhitelistIDs, userID)
}

func (s ContentModerationSetting) GetViolationRetentionDuration() time.Duration {
	days := s.ViolationRetentionDays
	if days <= 0 || days > 365 {
		days = DefaultContentModerationViolationRetentionDays
	}
	return time.Duration(days) * 24 * time.Hour
}

func (s ContentModerationSetting) HasModeratedChannels() bool {
	return s.Enabled && len(s.ChannelIDs) > 0
}

func (s ContentModerationSetting) ShouldModerateChannel(channelID int) bool {
	if !s.Enabled || channelID <= 0 || len(s.ChannelIDs) == 0 {
		return false
	}
	return slices.Contains(s.ChannelIDs, channelID)
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
