package service

import (
	"context"
	"slices"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/samber/hot"
	"golang.org/x/sync/singleflight"
)

var cacheHitRates = hot.NewHotCache[string, model.CacheHitRates](hot.LRU, 1024).
	WithTTL(time.Minute).Build()
var cacheHitRateQueries singleflight.Group

// GetCacheHitRates shares a bounded, one-minute cache between both model tabs.
// Every visibility dimension is part of the key, including the current user.
func GetCacheHitRates(ctx context.Context, filter model.CacheUsageFilter) (model.CacheHitRates, error) {
	filter.AllowedGroups = slices.Clone(filter.AllowedGroups)
	slices.Sort(filter.AllowedGroups)
	// Match the UI's one-minute freshness while making requests in the same
	// minute share a query rather than scanning the log database repeatedly.
	filter.StartTimestamp -= filter.StartTimestamp % 60
	filter.EndTimestamp -= filter.EndTimestamp % 60
	key, err := common.Marshal(filter)
	if err != nil {
		return model.CacheHitRates{}, err
	}
	cacheKey := string(key)
	if value, found, err := cacheHitRates.Get(cacheKey); err == nil && found {
		return value, nil
	}
	result := cacheHitRateQueries.DoChan(cacheKey, func() (any, error) {
		if value, found, err := cacheHitRates.Get(cacheKey); err == nil && found {
			return value, nil
		}
		queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		value, err := model.GetCacheHitRates(queryCtx, filter)
		if err == nil {
			cacheHitRates.Set(cacheKey, value)
		}
		return value, err
	})
	select {
	case <-ctx.Done():
		return model.CacheHitRates{}, ctx.Err()
	case value := <-result:
		if value.Err != nil {
			return model.CacheHitRates{}, value.Err
		}
		return value.Val.(model.CacheHitRates), nil
	}
}
