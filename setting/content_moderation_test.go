package setting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseChannelIDs(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []int
	}{
		{
			name:     "empty string",
			input:    "",
			expected: []int{},
		},
		{
			name:     "whitespace only",
			input:    "   \t\n  ",
			expected: []int{},
		},
		{
			name:     "comma separated half-width",
			input:    "1, 2, 3",
			expected: []int{1, 2, 3},
		},
		{
			name:     "comma separated full-width Chinese comma",
			input:    "4，5，6",
			expected: []int{4, 5, 6},
		},
		{
			name:     "mixed delimiters and duplicates and unsorted",
			input:    "10, 2， 5 10 \n 3",
			expected: []int{2, 3, 5, 10},
		},
		{
			name:     "single ID",
			input:    "42",
			expected: []int{42},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := ParseChannelIDs(tt.input)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestValidateChannelIDsString(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantIDs   []int
		expectErr bool
	}{
		{
			name:      "empty string",
			input:     "",
			wantIDs:   nil,
			expectErr: false,
		},
		{
			name:      "valid channel IDs",
			input:     "1, 2, 3",
			wantIDs:   []int{1, 2, 3},
			expectErr: false,
		},
		{
			name:      "valid with full-width comma",
			input:     "5，6",
			wantIDs:   []int{5, 6},
			expectErr: false,
		},
		{
			name:      "invalid non-integer",
			input:     "1, abc, 3",
			wantIDs:   nil,
			expectErr: true,
		},
		{
			name:      "invalid negative integer",
			input:     "1, -2, 3",
			wantIDs:   nil,
			expectErr: true,
		},
		{
			name:      "invalid zero",
			input:     "0, 1",
			wantIDs:   nil,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, err := ValidateChannelIDsString(tt.input)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantIDs, ids)
			}
		})
	}
}

func TestFormatChannelIDs(t *testing.T) {
	assert.Equal(t, "", FormatChannelIDs(nil))
	assert.Equal(t, "", FormatChannelIDs([]int{}))
	assert.Equal(t, "1, 2, 3", FormatChannelIDs([]int{1, 2, 3}))
}

func TestShouldModerateChannel(t *testing.T) {
	// Scheme A: Disabled -> false for all
	sDisabled := ContentModerationSetting{
		Enabled:    false,
		ChannelIDs: []int{1, 2, 3},
	}
	assert.False(t, sDisabled.ShouldModerateChannel(1))
	assert.False(t, sDisabled.HasModeratedChannels())

	// Scheme A: Enabled but ChannelIDs is empty -> false for all (whitelist mode)
	sEmptyChannels := ContentModerationSetting{
		Enabled:    true,
		ChannelIDs: []int{},
	}
	assert.False(t, sEmptyChannels.ShouldModerateChannel(1))
	assert.False(t, sEmptyChannels.HasModeratedChannels())

	// Scheme A: Enabled with specific channels
	sEnabled := ContentModerationSetting{
		Enabled:    true,
		ChannelIDs: []int{1, 5, 10},
	}
	assert.True(t, sEnabled.HasModeratedChannels())
	assert.True(t, sEnabled.ShouldModerateChannel(1))
	assert.True(t, sEnabled.ShouldModerateChannel(5))
	assert.True(t, sEnabled.ShouldModerateChannel(10))
	assert.False(t, sEnabled.ShouldModerateChannel(2))
	assert.False(t, sEnabled.ShouldModerateChannel(0))
	assert.False(t, sEnabled.ShouldModerateChannel(-1))
}

func TestParseUserIDs(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []int
	}{
		{
			name:     "empty string",
			input:    "",
			expected: []int{},
		},
		{
			name:     "comma separated half-width",
			input:    "1, 2, 3",
			expected: []int{1, 2, 3},
		},
		{
			name:     "comma separated full-width Chinese comma",
			input:    "4，5，6",
			expected: []int{4, 5, 6},
		},
		{
			name:     "mixed delimiters and duplicates",
			input:    "10, 2， 5 10 \n 3",
			expected: []int{2, 3, 5, 10},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := ParseUserIDs(tt.input)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestValidateUserIDsString(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantIDs   []int
		expectErr bool
	}{
		{
			name:      "empty string",
			input:     "",
			wantIDs:   nil,
			expectErr: false,
		},
		{
			name:      "valid user IDs",
			input:     "1, 2, 3",
			wantIDs:   []int{1, 2, 3},
			expectErr: false,
		},
		{
			name:      "invalid non-integer",
			input:     "1, abc, 3",
			wantIDs:   nil,
			expectErr: true,
		},
		{
			name:      "invalid negative integer",
			input:     "1, -2, 3",
			wantIDs:   nil,
			expectErr: true,
		},
		{
			name:      "invalid zero",
			input:     "0, 1",
			wantIDs:   nil,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, err := ValidateUserIDsString(tt.input)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantIDs, ids)
			}
		})
	}
}

func TestFormatUserIDs(t *testing.T) {
	assert.Equal(t, "", FormatUserIDs(nil))
	assert.Equal(t, "", FormatUserIDs([]int{}))
	assert.Equal(t, "1, 2, 3", FormatUserIDs([]int{1, 2, 3}))
}

func TestIsUserWhitelisted(t *testing.T) {
	s := ContentModerationSetting{
		UserWhitelistIDs: []int{2, 5, 10},
	}
	// RootAdminUserID (1) is always whitelisted even if not in UserWhitelistIDs
	assert.True(t, s.IsUserWhitelisted(1))
	assert.True(t, s.IsUserWhitelisted(2))
	assert.True(t, s.IsUserWhitelisted(5))
	assert.True(t, s.IsUserWhitelisted(10))
	assert.False(t, s.IsUserWhitelisted(3))
	assert.False(t, s.IsUserWhitelisted(0))
	assert.False(t, s.IsUserWhitelisted(-1))

	// Empty whitelist still protects root admin (ID 1)
	sEmpty := ContentModerationSetting{
		UserWhitelistIDs: []int{},
	}
	assert.True(t, sEmpty.IsUserWhitelisted(1))
	assert.False(t, sEmpty.IsUserWhitelisted(2))
}

func TestGetViolationRetentionDuration(t *testing.T) {
	sDefault := ContentModerationSetting{ViolationRetentionDays: 0}
	assert.Equal(t, 7*24*time.Hour, sDefault.GetViolationRetentionDuration())

	sCustom := ContentModerationSetting{ViolationRetentionDays: 14}
	assert.Equal(t, 14*24*time.Hour, sCustom.GetViolationRetentionDuration())

	sTooLarge := ContentModerationSetting{ViolationRetentionDays: 400}
	assert.Equal(t, 7*24*time.Hour, sTooLarge.GetViolationRetentionDuration())
}
