package setting

import (
	"errors"
	"fmt"
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
	ContentModerationPostflightOption             = "ContentModerationPostflight"
	ContentModerationFailureModeOption            = "ContentModerationFailureMode"
	ContentModerationBlockSeverityOption          = "ContentModerationBlockSeverity"
	ContentModerationTimeoutSecondsOption         = "ContentModerationTimeoutSeconds"
	ContentModerationMaxRetriesOption             = "ContentModerationMaxRetries"
	ContentModerationAutoDisableViolationsOption  = "ContentModerationAutoDisableViolations"
)

const (
	DefaultContentModerationBaseURL                = "https://api.openai.com/v1"
	DefaultContentModerationModel                  = "omni-moderation-latest"
	DefaultContentModerationPreflight              = true
	DefaultContentModerationPostflight             = false
	DefaultContentModerationFailureMode            = "closed"
	DefaultContentModerationBlockSeverity          = "critical"
	DefaultContentModerationTimeoutSeconds         = 30
	DefaultContentModerationMaxRetries             = 3
	DefaultContentModerationUserWhitelist          = "1"
	DefaultContentModerationViolationRetentionDays = 7
	DefaultContentModerationAutoDisableViolations  = 0
	RootAdminUserID                                = 1

	MaxContentModerationAPIKeys           = 50
	MaxContentModerationAPIKeyBytes       = 32 * 1024
	MaxContentModerationSingleAPIKeyBytes = 4096
)

const (
	ModerationSeverityLow      = "low"
	ModerationSeverityMedium   = "medium"
	ModerationSeverityHigh     = "high"
	ModerationSeverityCritical = "critical"
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
	APIKeys                []string
	Model                  string
	BlockSeverity          string
	PreflightEnabled       bool
	PostflightEnabled      bool
	FailureMode            string
	TimeoutSeconds         int
	MaxRetries             int
	AutoDisableViolations  int
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
	var apiKeys []string
	if apiKey != "" {
		parsed, err := ParseModerationAPIKeys(apiKey)
		if err != nil {
			common.SysError("invalid content moderation API keys: " + err.Error())
			apiKey = ""
		} else {
			apiKeys = parsed
			apiKey = FormatModerationAPIKeys(parsed)
		}
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
	postflightEnabled := optionBool(ContentModerationPostflightOption, DefaultContentModerationPostflight)
	failureMode := strings.ToLower(optionString(ContentModerationFailureModeOption, DefaultContentModerationFailureMode))
	if failureMode != "open" && failureMode != "closed" {
		failureMode = DefaultContentModerationFailureMode
	}
	blockSeverity := NormalizeBlockSeverity(optionString(ContentModerationBlockSeverityOption, DefaultContentModerationBlockSeverity))
	autoDisable := optionInt(ContentModerationAutoDisableViolationsOption, DefaultContentModerationAutoDisableViolations)
	if autoDisable < 0 || autoDisable > 1000 {
		autoDisable = DefaultContentModerationAutoDisableViolations
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
		APIKeys:                apiKeys,
		Model:                  modelName,
		BlockSeverity:          blockSeverity,
		PreflightEnabled:       preflightEnabled,
		PostflightEnabled:      postflightEnabled,
		FailureMode:            failureMode,
		TimeoutSeconds:         timeoutSeconds,
		MaxRetries:             maxRetries,
		AutoDisableViolations:  autoDisable,
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

func (s ContentModerationSetting) HasAPIKey() bool {
	return len(s.ResolvedAPIKeys()) > 0
}

func (s ContentModerationSetting) ResolvedAPIKeys() []string {
	if len(s.APIKeys) > 0 {
		return s.APIKeys
	}
	keys, err := ParseModerationAPIKeys(s.APIKey)
	if err != nil {
		return nil
	}
	return keys
}

func (s ContentModerationSetting) HasModeratedChannels() bool {
	return s.Enabled && len(s.ChannelIDs) > 0
}

// ParseModerationAPIKeys splits a stored or submitted key blob into individual
// keys. Keys are separated by newlines, matching channel multi-key input.
func ParseModerationAPIKeys(input string) ([]string, error) {
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}
	if len(input) > MaxContentModerationAPIKeyBytes {
		return nil, errors.New("content moderation API keys exceed the configured length limit")
	}
	normalized := strings.ReplaceAll(input, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	keys := make([]string, 0, len(lines))
	for _, line := range lines {
		key := strings.TrimSpace(line)
		if key == "" {
			continue
		}
		if len(key) > MaxContentModerationSingleAPIKeyBytes {
			return nil, errors.New("content moderation API key is too long")
		}
		if strings.IndexFunc(key, func(r rune) bool {
			return r < 0x20 || r == 0x7f
		}) >= 0 {
			return nil, errors.New("content moderation API key contains invalid control characters")
		}
		keys = append(keys, key)
	}
	if len(keys) > MaxContentModerationAPIKeys {
		return nil, fmt.Errorf("at most %d content moderation API keys are allowed", MaxContentModerationAPIKeys)
	}
	return keys, nil
}

func FormatModerationAPIKeys(keys []string) string {
	return strings.Join(keys, "\n")
}

func (s ContentModerationSetting) ShouldModerateChannel(channelID int) bool {
	if !s.Enabled || channelID <= 0 || len(s.ChannelIDs) == 0 {
		return false
	}
	return slices.Contains(s.ChannelIDs, channelID)
}

// SeverityRank maps severity strings to integer ranks for comparison.
// Rank: critical (4) > high (3) > medium (2) > low (1) > none (0).
func SeverityRank(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case ModerationSeverityCritical:
		return 4
	case ModerationSeverityHigh:
		return 3
	case ModerationSeverityMedium:
		return 2
	case ModerationSeverityLow:
		return 1
	default:
		return 0
	}
}

// NormalizeBlockSeverity normalizes a block severity string, falling back to DefaultContentModerationBlockSeverity if invalid.
func NormalizeBlockSeverity(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case ModerationSeverityCritical, ModerationSeverityHigh, ModerationSeverityMedium, ModerationSeverityLow:
		return strings.ToLower(strings.TrimSpace(severity))
	default:
		return DefaultContentModerationBlockSeverity
	}
}

// ShouldBlockSeverity checks whether the given severity meets or exceeds the configured block threshold.
func ShouldBlockSeverity(severity, threshold string) bool {
	thresholdRank := SeverityRank(NormalizeBlockSeverity(threshold))
	return SeverityRank(severity) >= thresholdRank
}

func (s ContentModerationSetting) ShouldBlock(severity string) bool {
	return ShouldBlockSeverity(severity, s.BlockSeverity)
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
