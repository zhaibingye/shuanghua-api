package model

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// CacheUsageFilter scopes a cache statistic to a user or a model. Group filters
// follow the same visibility rules as the model square's performance metrics.
type CacheUsageFilter struct {
	UserID         int
	ModelName      string
	Group          string
	AllowedGroups  []string
	ByGroup        bool
	StartTimestamp int64
	EndTimestamp   int64
}

type CacheHitRates struct {
	Overall *float64
	ByGroup map[string]*float64
}

type cacheTokenTotals struct {
	input float64
	read  float64
}

func (totals cacheTokenTotals) hitRate() *float64 {
	if totals.input <= 0 || math.IsInf(totals.input, 0) || math.IsInf(totals.read, 0) {
		return nil
	}
	return new(totals.read / totals.input * 100)
}

// cacheTokensFromLog normalizes existing usage logs without changing billing.
// OpenAI prompt tokens include cache reads/writes; Anthropic input excludes them.
func cacheTokensFromLog(promptTokens int64, rawOther string) (input, read float64, known bool) {
	var other struct {
		CacheTokens         *int64 `json:"cache_tokens"`
		InputTokensTotal    *int64 `json:"input_tokens_total"`
		CacheWriteTokens    int64  `json:"cache_write_tokens"`
		CacheCreationTokens int64  `json:"cache_creation_tokens"`
		CacheCreation5m     int64  `json:"cache_creation_tokens_5m"`
		CacheCreation1h     int64  `json:"cache_creation_tokens_1h"`
		Claude              bool   `json:"claude"`
		UsageSemantic       string `json:"usage_semantic"`
		AdminInfo           struct {
			UsageBillingPath string `json:"usage_billing_path"`
			LocalCountTokens bool   `json:"local_count_tokens"`
		} `json:"admin_info"`
	}
	if err := common.UnmarshalJsonStr(rawOther, &other); err != nil {
		return 0, 0, false
	}
	if other.AdminInfo.LocalCountTokens || other.AdminInfo.UsageBillingPath == "local" || strings.HasSuffix(other.AdminInfo.UsageBillingPath, "-estimated") {
		return 0, 0, false
	}
	if other.CacheTokens == nil {
		return 0, 0, promptTokens == 0
	}
	if promptTokens < 0 || *other.CacheTokens < 0 || other.CacheWriteTokens < 0 || other.CacheCreationTokens < 0 || other.CacheCreation5m < 0 || other.CacheCreation1h < 0 {
		return 0, 0, false
	}
	input, read = float64(promptTokens), float64(*other.CacheTokens)
	if other.InputTokensTotal != nil {
		input = float64(*other.InputTokensTotal)
	} else if other.UsageSemantic == "anthropic" || (other.UsageSemantic == "" && other.Claude) {
		// The split TTL counters describe the same writes as the total, so use
		// the largest available total rather than adding overlapping counters.
		writes := max(float64(other.CacheWriteTokens), float64(other.CacheCreationTokens), float64(other.CacheCreation5m)+float64(other.CacheCreation1h))
		input += read + writes
	}
	if input < 0 || read > input {
		return 0, 0, false
	}
	return input, read, true
}

// GetCacheHitRates weights cache reads by total input tokens, never by request
// count or output tokens. Only logs with reported, valid token usage contribute
// to either total; unrelated, estimated or malformed logs cannot erase known
// samples or dilute them into zero hits. The overall rate and optional group
// rates share one scan of existing logs.
func GetCacheHitRates(ctx context.Context, filter CacheUsageFilter) (rates CacheHitRates, err error) {
	if filter.UserID <= 0 && filter.ModelName == "" {
		return CacheHitRates{}, common.NewMessage("Invalid parameters")
	}
	if filter.StartTimestamp <= 0 || filter.EndTimestamp < filter.StartTimestamp {
		return CacheHitRates{}, common.NewMessage("Invalid parameters")
	}
	if filter.AllowedGroups != nil && len(filter.AllowedGroups) == 0 {
		return CacheHitRates{}, nil
	}
	query := LOG_DB.WithContext(ctx).Model(&Log{}).
		Where("type = ? AND created_at >= ? AND created_at <= ?", LogTypeConsume, filter.StartTimestamp, filter.EndTimestamp)
	if filter.ByGroup {
		query = query.Select("prompt_tokens", "other", "group")
	} else {
		query = query.Select("prompt_tokens", "other")
	}
	if filter.UserID > 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.ModelName != "" {
		query = query.Where("model_name = ?", filter.ModelName)
	}
	if filter.Group != "" {
		query = query.Where(logGroupCol+" = ?", filter.Group)
	}
	if filter.AllowedGroups != nil {
		query = query.Where(logGroupCol+" IN ?", filter.AllowedGroups)
	}
	rows, err := query.Rows()
	if err != nil {
		return CacheHitRates{}, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var total cacheTokenTotals
	var totalsByGroup map[string]cacheTokenTotals
	if filter.ByGroup {
		totalsByGroup = make(map[string]cacheTokenTotals)
	}
	var prompt int64
	var other string
	var group sql.NullString
	dest := []any{&prompt, &other}
	if filter.ByGroup {
		dest = append(dest, &group)
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return CacheHitRates{}, err
		}
		input, read, known := cacheTokensFromLog(prompt, other)
		if !known || input == 0 {
			continue
		}
		total.input += input
		total.read += read
		if filter.ByGroup && group.Valid {
			groupTotals := totalsByGroup[group.String]
			groupTotals.input += input
			groupTotals.read += read
			totalsByGroup[group.String] = groupTotals
		}
	}
	if err := rows.Err(); err != nil {
		return CacheHitRates{}, err
	}
	rates.Overall = total.hitRate()
	if filter.ByGroup {
		rates.ByGroup = make(map[string]*float64, len(totalsByGroup))
		for group, totals := range totalsByGroup {
			rates.ByGroup[group] = totals.hitRate()
		}
	}
	return rates, nil
}
