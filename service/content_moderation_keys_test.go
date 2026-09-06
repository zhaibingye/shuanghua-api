package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withModerationHTTPTestServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	originalHTTPClient := httpClient
	t.Cleanup(func() { httpClient = originalHTTPClient })
	fetchSetting := system_setting.GetFetchSetting()
	originalFetchSetting := *fetchSetting
	t.Cleanup(func() { *fetchSetting = originalFetchSetting })
	fetchSetting.EnableSSRFProtection = false

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	httpClient = server.Client()
	return server.URL
}

func moderationOKBody() []byte {
	return []byte(`{"id":"modr-test","model":"omni-moderation-latest","results":[{"flagged":false,"categories":{},"category_scores":{}}]}`)
}

func TestNextModerationAPIKeyIndexPollsInOrder(t *testing.T) {
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedis })
	resetModerationAPIKeyPolling()

	assert.Equal(t, 0, nextModerationAPIKeyIndex(1))
	assert.Equal(t, 0, nextModerationAPIKeyIndex(3))
	assert.Equal(t, 1, nextModerationAPIKeyIndex(3))
	assert.Equal(t, 2, nextModerationAPIKeyIndex(3))
	assert.Equal(t, 0, nextModerationAPIKeyIndex(3))
}

func TestExecuteOpenAIModerationCallRotatesKeysOn429(t *testing.T) {
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedis })
	resetModerationAPIKeyPolling()

	var mu sync.Mutex
	var auths []string
	baseURL := withModerationHTTPTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		auths = append(auths, request.Header.Get("Authorization"))
		mu.Unlock()
		if request.Header.Get("Authorization") == "Bearer sk-test-key-alpha" {
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		_, _ = writer.Write(moderationOKBody())
	})

	result, _, err := executeOpenAIModerationCall(context.Background(), setting.ContentModerationSetting{
		BaseURL:        baseURL + "/v1",
		APIKey:         "sk-test-key-alpha\nsk-test-key-bravo",
		Model:          "omni-moderation-latest",
		TimeoutSeconds: 5,
		MaxRetries:     3,
	}, "ok")
	require.NoError(t, err)
	assert.False(t, result.Flagged)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"Bearer sk-test-key-alpha", "Bearer sk-test-key-bravo"}, auths)
}

func TestExecuteOpenAIModerationCallDoesNotRetryUnauthorized(t *testing.T) {
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedis })
	resetModerationAPIKeyPolling()

	var calls int
	baseURL := withModerationHTTPTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		calls++
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":"invalid api key"}`))
	})

	_, _, err := executeOpenAIModerationCall(context.Background(), setting.ContentModerationSetting{
		BaseURL:        baseURL + "/v1",
		APIKey:         "sk-test-key-alpha\nsk-test-key-bravo",
		Model:          "omni-moderation-latest",
		TimeoutSeconds: 5,
		MaxRetries:     3,
	}, "ok")
	require.Error(t, err)
	assert.Equal(t, 1, calls)
	assert.Contains(t, err.Error(), "401")
}

func TestTestModerationAPIKeysReportsEachKey(t *testing.T) {
	baseURL := withModerationHTTPTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), `"input":"ok"`)
		switch request.Header.Get("Authorization") {
		case "Bearer sk-test-key-alpha":
			_, _ = writer.Write(moderationOKBody())
		case "Bearer sk-test-key-bravo":
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"error":"invalid api key"}`))
		default:
			writer.WriteHeader(http.StatusInternalServerError)
		}
	})

	results := TestModerationAPIKeys(context.Background(), setting.ContentModerationSetting{
		BaseURL:        baseURL + "/v1",
		Model:          "omni-moderation-latest",
		TimeoutSeconds: 5,
	}, []string{"sk-test-key-alpha", "sk-test-key-bravo"})
	require.Len(t, results, 2)
	assert.True(t, results[0].OK)
	assert.Equal(t, http.StatusOK, results[0].Status)
	assert.Equal(t, "sk-t...lpha", results[0].KeyPreview)
	assert.False(t, results[1].OK)
	assert.Equal(t, http.StatusUnauthorized, results[1].Status)
	assert.Equal(t, "sk-t...ravo", results[1].KeyPreview)
	assert.NotEmpty(t, results[1].Error)
}

func TestMaskModerationAPIKey(t *testing.T) {
	assert.Equal(t, "****", maskModerationAPIKey("abcd"))
	assert.Equal(t, "sk-t...lpha", maskModerationAPIKey("sk-test-key-alpha"))
}
