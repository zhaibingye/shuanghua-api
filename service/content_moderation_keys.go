package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
)

const (
	moderationAPIKeyPollRedisKey   = "content_moderation:api_key_poll"
	moderationAPIKeyPollRedisWait  = 200 * time.Millisecond
	moderationKeyTestTimeout       = 8 * time.Second
	moderationKeyTestConcurrency   = 5
	moderationKeyTestOverallWait   = 60 * time.Second
	moderationKeyTestProbeInput    = "ok"
	moderationKeyTestErrorMaxBytes = 300
)

var moderationAPIKeyPollIndex atomic.Uint64

type ModerationKeyTestResult struct {
	Index      int    `json:"index"`
	KeyPreview string `json:"key_preview"`
	OK         bool   `json:"ok"`
	Status     int    `json:"status"`
	LatencyMs  int64  `json:"latency_ms"`
	Error      string `json:"error,omitempty"`
}

func resetModerationAPIKeyPolling() {
	moderationAPIKeyPollIndex.Store(0)
}

func nextModerationAPIKeyIndex(n int) int {
	if n <= 1 {
		return 0
	}
	seq := nextModerationAPIKeySequence()
	return int(seq % uint64(n))
}

func nextModerationAPIKeySequence() uint64 {
	if common.RedisEnabled && common.RDB != nil {
		ctx, cancel := context.WithTimeout(context.Background(), moderationAPIKeyPollRedisWait)
		defer cancel()
		val, err := common.RDB.Incr(ctx, moderationAPIKeyPollRedisKey).Result()
		if err == nil && val > 0 {
			return uint64(val - 1)
		}
	}
	return moderationAPIKeyPollIndex.Add(1) - 1
}

func maskModerationAPIKey(key string) string {
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func isRetryableModerationStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func postOpenAIModeration(ctx context.Context, client *http.Client, timeout time.Duration, endpoint, apiKey string, reqBody []byte) (int, []byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	respBytes, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, respBytes, nil
}

func moderationHTTPClient(timeout time.Duration) *http.Client {
	if httpClient != nil {
		return httpClient
	}
	return &http.Client{Timeout: timeout}
}

func truncateModerationError(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= moderationKeyTestErrorMaxBytes {
		return message
	}
	return message[:moderationKeyTestErrorMaxBytes] + "..."
}

func TestModerationAPIKeys(ctx context.Context, config setting.ContentModerationSetting, keys []string) []ModerationKeyTestResult {
	results := make([]ModerationKeyTestResult, len(keys))
	if len(keys) == 0 {
		return results
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, moderationKeyTestOverallWait)
		defer cancel()
		deadline = time.Now().Add(moderationKeyTestOverallWait)
	}

	timeout := time.Duration(config.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > moderationKeyTestTimeout {
		timeout = moderationKeyTestTimeout
	}
	if remain := time.Until(deadline); remain > 0 && remain < timeout {
		timeout = remain
	}

	endpoint, err := moderationEndpoint(config)
	if err != nil {
		for i, key := range keys {
			results[i] = ModerationKeyTestResult{
				Index:      i,
				KeyPreview: maskModerationAPIKey(key),
				Error:      err.Error(),
			}
		}
		return results
	}

	modelName := strings.TrimSpace(config.Model)
	if modelName == "" {
		modelName = setting.DefaultContentModerationModel
	}
	reqBody, err := common.Marshal(map[string]any{
		"model": modelName,
		"input": moderationKeyTestProbeInput,
	})
	if err != nil {
		for i, key := range keys {
			results[i] = ModerationKeyTestResult{
				Index:      i,
				KeyPreview: maskModerationAPIKey(key),
				Error:      err.Error(),
			}
		}
		return results
	}

	client := moderationHTTPClient(timeout)
	sem := make(chan struct{}, moderationKeyTestConcurrency)
	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				results[i] = ModerationKeyTestResult{
					Index:      i,
					KeyPreview: maskModerationAPIKey(key),
					Error:      ctx.Err().Error(),
				}
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()

			started := time.Now()
			status, respBytes, postErr := postOpenAIModeration(ctx, client, timeout, endpoint, key, reqBody)
			result := ModerationKeyTestResult{
				Index:      i,
				KeyPreview: maskModerationAPIKey(key),
				Status:     status,
				LatencyMs:  time.Since(started).Milliseconds(),
			}
			if postErr != nil {
				result.Error = truncateModerationError(postErr.Error())
				results[i] = result
				return
			}
			if status != http.StatusOK {
				result.Error = truncateModerationError(fmt.Sprintf("status %d: %s", status, string(respBytes)))
				results[i] = result
				return
			}
			var decoded openAIModerationResponse
			if err := common.Unmarshal(respBytes, &decoded); err != nil {
				result.Error = "invalid moderation response"
				results[i] = result
				return
			}
			if len(decoded.Results) == 0 {
				result.Error = "moderation provider returned no results"
				results[i] = result
				return
			}
			result.OK = true
			results[i] = result
		}(i, key)
	}
	wg.Wait()
	return results
}
