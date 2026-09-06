package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testGinContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c, recorder
}

func TestCallNativeModerationOpenAIFormat(t *testing.T) {
	originalHTTPClient := httpClient
	t.Cleanup(func() { httpClient = originalHTTPClient })
	fetchSetting := system_setting.GetFetchSetting()
	originalFetchSetting := *fetchSetting
	t.Cleanup(func() { *fetchSetting = originalFetchSetting })
	fetchSetting.EnableSSRFProtection = false

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/v1/moderations", request.URL.Path)
		assert.Equal(t, "Bearer mod-api-key", request.Header.Get("Authorization"))
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))

		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)

		var reqPayload map[string]any
		require.NoError(t, common.Unmarshal(body, &reqPayload))
		assert.Equal(t, "omni-moderation-latest", reqPayload["model"])

		_, err = writer.Write([]byte(`{
			"id": "modr-test-123",
			"model": "omni-moderation-latest",
			"results": [
				{
					"flagged": true,
					"categories": {"hate": true, "violence": false},
					"category_scores": {"hate": 0.92, "violence": 0.05}
				}
			]
		}`))
		require.NoError(t, err)
	}))
	defer server.Close()
	httpClient = server.Client()

	config := setting.ContentModerationSetting{
		BaseURL:        server.URL + "/v1",
		APIKey:         "mod-api-key",
		Model:          "omni-moderation-latest",
		TimeoutSeconds: 5,
		MaxRetries:     1,
	}

	result, _, err := callNativeModeration(context.Background(), config, "hate speech prompt")
	require.NoError(t, err)
	assert.True(t, result.Flagged)
	assert.True(t, result.Categories["hate"])
	assert.Equal(t, 0.92, result.CategoryScores["hate"])

	decision := moderationDecisionFromNativeResult(result)
	assert.Equal(t, "block", decision.Decision)
	assert.Equal(t, "critical", decision.Severity)
	assert.Contains(t, decision.Categories, "hate")
	assert.Equal(t, 0.92, decision.Confidence)
}

func TestPreflightModerationRequestBlocksFlaggedContent(t *testing.T) {
	resetModerationFingerprintCache()
	t.Cleanup(resetModerationFingerprintCache)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		var reqPayload map[string]any
		require.NoError(t, common.Unmarshal(body, &reqPayload))
		inputStr, _ := reqPayload["input"].(string)
		flagged := strings.Contains(inputStr, "unsafe")
		resp := map[string]any{
			"id":    "modr-preflight",
			"model": "omni-moderation-latest",
			"results": []any{
				map[string]any{
					"flagged":    flagged,
					"categories": map[string]bool{"violence": flagged},
					"category_scores": map[string]float64{
						"violence": 0.88,
					},
				},
			},
		}
		respBytes, _ := common.Marshal(resp)
		_, _ = writer.Write(respBytes)
	}))
	defer server.Close()

	originalHTTPClient := httpClient
	httpClient = server.Client()
	t.Cleanup(func() { httpClient = originalHTTPClient })

	config := setting.ContentModerationSetting{
		Enabled:          true,
		PreflightEnabled: true,
		BaseURL:          server.URL + "/v1",
		APIKey:           "test-key",
		Model:            "omni-moderation-latest",
		FailureMode:      "closed",
		TimeoutSeconds:   2,
		MaxRetries:       1,
	}

	err := PreflightModerationRequest(context.Background(), ModerationRequestContent{UserPrompt: "hello safe world"}, config)
	assert.NoError(t, err)

	err = PreflightModerationRequest(context.Background(), ModerationRequestContent{UserPrompt: "this is unsafe"}, config)
	assert.ErrorIs(t, err, model.ErrModerationBlocked)
}

func TestPreflightModerationRequestCachesAllowAndBlock(t *testing.T) {
	resetModerationFingerprintCache()
	t.Cleanup(resetModerationFingerprintCache)

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		var reqPayload map[string]any
		require.NoError(t, common.Unmarshal(body, &reqPayload))
		inputStr, _ := reqPayload["input"].(string)
		flagged := strings.Contains(inputStr, "unsafe")
		resp := map[string]any{
			"id":    "modr-cache",
			"model": "omni-moderation-latest",
			"results": []any{
				map[string]any{
					"flagged":         flagged,
					"categories":      map[string]bool{"violence": flagged},
					"category_scores": map[string]float64{"violence": 0.91},
				},
			},
		}
		respBytes, _ := common.Marshal(resp)
		_, _ = writer.Write(respBytes)
	}))
	defer server.Close()

	originalHTTPClient := httpClient
	httpClient = server.Client()
	t.Cleanup(func() { httpClient = originalHTTPClient })

	config := setting.ContentModerationSetting{
		Enabled:                true,
		PreflightEnabled:       true,
		BaseURL:                server.URL + "/v1",
		APIKey:                 "test-key",
		Model:                  "omni-moderation-latest",
		FailureMode:            "closed",
		TimeoutSeconds:         2,
		MaxRetries:             1,
		ViolationRetentionDays: 7,
	}

	safe := ModerationRequestContent{UserPrompt: "harmless prompt"}
	require.NoError(t, PreflightModerationRequest(context.Background(), safe, config))
	require.NoError(t, PreflightModerationRequest(context.Background(), safe, config))

	blocked := ModerationRequestContent{UserPrompt: "unsafe prompt"}
	require.ErrorIs(t, PreflightModerationRequest(context.Background(), blocked, config), model.ErrModerationBlocked)
	require.ErrorIs(t, PreflightModerationRequest(context.Background(), blocked, config), model.ErrModerationBlocked)
	assert.Equal(t, 2, calls)
}

func TestSanitizeModerationTextRuneSafe(t *testing.T) {
	assert.Equal(t, "hello world", sanitizeModerationText("  hello world  "))

	chinese := strings.Repeat("中", 10000)
	sanitized := sanitizeModerationText(chinese)
	assert.LessOrEqual(t, len(sanitized), 25000)
	assert.True(t, utf8.ValidString(sanitized))
}

func TestCallModerationContentMultiModal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/v1/moderations", request.URL.Path)
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		var reqPayload map[string]any
		require.NoError(t, common.Unmarshal(body, &reqPayload))
		inputs, ok := reqPayload["input"].([]any)
		require.True(t, ok)
		require.Len(t, inputs, 2)
		textPart, ok := inputs[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "text", textPart["type"])
		assert.Equal(t, "check this image", textPart["text"])
		imagePart, ok := inputs[1].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "image_url", imagePart["type"])
		imgMap, ok := imagePart["image_url"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "https://example.com/image.png", imgMap["url"])
		_, _ = writer.Write([]byte(`{
			"id": "modr-multi-123",
			"model": "omni-moderation-latest",
			"results": [{"flagged": true, "categories": {"violence": true}, "category_scores": {"violence": 0.85}}]
		}`))
	}))
	defer server.Close()

	originalHTTPClient := httpClient
	httpClient = server.Client()
	t.Cleanup(func() { httpClient = originalHTTPClient })

	result, _, err := callModerationContent(context.Background(), setting.ContentModerationSetting{
		BaseURL:        server.URL + "/v1",
		APIKey:         "test-key",
		Model:          "omni-moderation-latest",
		TimeoutSeconds: 5,
		MaxRetries:     1,
	}, ModerationRequestContent{
		UserPrompt: "check this image",
		ImageURLs:  []string{"https://example.com/image.png"},
	})
	require.NoError(t, err)
	assert.True(t, result.Flagged)
	assert.Equal(t, 0.85, result.CategoryScores["violence"])
}

func TestExtractLatestUserTurnOnly(t *testing.T) {
	openaiReq := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "system", Content: "system instructions"},
			{Role: "user", Content: "first question"},
			{Role: "assistant", Content: "first answer"},
			{Role: "user", Content: "latest question"},
		},
	}
	content := extractLatestUserTurn(openaiReq)
	assert.Equal(t, "latest question", content.UserPrompt)
	assert.NotContains(t, content.UserPrompt, "first question")
	assert.NotContains(t, content.UserPrompt, "system instructions")

	claudeReq := &dto.ClaudeRequest{
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "old"},
			{Role: "assistant", Content: "reply"},
			{Role: "user", Content: "new"},
		},
	}
	content = extractLatestUserTurn(claudeReq)
	assert.Equal(t, "new", content.UserPrompt)

	geminiReq := &dto.GeminiChatRequest{
		Contents: []dto.GeminiChatContent{
			{Role: "user", Parts: []dto.GeminiPart{{Text: "old gemini"}}},
			{Role: "model", Parts: []dto.GeminiPart{{Text: "model reply"}}},
			{Role: "user", Parts: []dto.GeminiPart{{Text: "latest gemini"}}},
		},
	}
	content = extractLatestUserTurn(geminiReq)
	assert.Equal(t, "latest gemini", content.UserPrompt)
}

func TestSetModerationRequestContentFromJSONUsesLatestUserTurn(t *testing.T) {
	ginContext, _ := testGinContext()
	request := &dto.GeneralOpenAIRequest{}
	SetModerationRequestContentFromJSON(ginContext, []byte(`{"systemInstruction":{"parts":[{"text":"effective system"}]},"contents":[{"role":"user","parts":[{"text":"effective user"}]},{"role":"model","parts":[{"text":"prior assistant"}]}]}`), request)
	content, ok := common.GetContextKeyType[ModerationRequestContent](ginContext, constant.ContextKeyModerationRequestContent)
	require.True(t, ok)
	assert.Equal(t, "effective user", content.UserPrompt)
	assert.NotContains(t, content.UserPrompt, "prior assistant")
	assert.NotContains(t, content.UserPrompt, "effective system")

	responsesRequest := &dto.OpenAIResponsesRequest{}
	SetModerationRequestContentFromJSON(ginContext, []byte(`{"input":[{"type":"message","role":"user","content":"current input"},{"type":"message","role":"assistant","content":"prior output"}]}`), responsesRequest)
	content, ok = common.GetContextKeyType[ModerationRequestContent](ginContext, constant.ContextKeyModerationRequestContent)
	require.True(t, ok)
	assert.Equal(t, "current input", content.UserPrompt)
	assert.NotContains(t, content.UserPrompt, "prior output")
}

func TestExtractLatestUserTurnDoesNotKeepMediaBytes(t *testing.T) {
	request := &dto.ClaudeRequest{
		Messages: []dto.ClaudeMessage{{
			Role: "user",
			Content: []any{
				map[string]any{"type": "text", "text": "inspect this"},
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "secret"}},
			},
		}},
	}
	content := extractLatestUserTurn(request)
	assert.Equal(t, "inspect this", content.UserPrompt)
	require.Len(t, content.ImageURLs, 1)
	assert.Contains(t, content.ImageURLs[0], "secret")
	excerpt := moderationExcerpt(content.UserPrompt, len(content.ImageURLs))
	assert.Contains(t, excerpt, "inspect this")
	assert.Contains(t, excerpt, "[1 images not saved]")
	assert.NotContains(t, excerpt, "secret")
}

func TestModerationExcerptTruncatesUTF8(t *testing.T) {
	text := strings.Repeat("中", 3000)
	excerpt := moderationExcerpt(text, 2)
	assert.LessOrEqual(t, len(excerpt), moderationExcerptMaxBytes)
	assert.True(t, utf8.ValidString(excerpt))
	assert.Contains(t, excerpt, "[2 images not saved]")
}

func TestModerationRestoreOnlyRestoresTrackedAccountAndTokens(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.ModerationAccountState{}, &model.ModerationTokenState{}))

	untrackedUser := &model.User{
		Username: fmt.Sprintf("moderation-untracked-%d", time.Now().UnixNano()),
		Password: "password",
		Status:   common.UserStatusDisabled,
		AffCode:  fmt.Sprintf("u%d", time.Now().UnixNano()),
	}
	require.NoError(t, model.DB.Create(untrackedUser).Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Unscoped().Where("user_id = ?", untrackedUser.Id).Delete(&model.ModerationTokenState{}).Error)
		require.NoError(t, model.DB.Unscoped().Where("user_id = ?", untrackedUser.Id).Delete(&model.ModerationAccountState{}).Error)
		require.NoError(t, model.DB.Delete(untrackedUser).Error)
	})

	changed, err := model.RestoreUserAndTokensAfterModeration(untrackedUser.Id, time.Now().Unix())
	require.NoError(t, err)
	assert.False(t, changed)

	trackedUser := &model.User{
		Username: fmt.Sprintf("moderation-tracked-%d", time.Now().UnixNano()),
		Password: "password",
		Status:   common.UserStatusEnabled,
		AffCode:  fmt.Sprintf("t%d", time.Now().UnixNano()),
	}
	require.NoError(t, model.DB.Create(trackedUser).Error)
	trackedToken := &model.Token{
		UserId: trackedUser.Id,
		Key:    fmt.Sprintf("moderation-token-%d", time.Now().UnixNano()),
		Status: common.TokenStatusEnabled,
	}
	require.NoError(t, model.DB.Create(trackedToken).Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Unscoped().Where("user_id = ?", trackedUser.Id).Delete(&model.ModerationTokenState{}).Error)
		require.NoError(t, model.DB.Unscoped().Where("user_id = ?", trackedUser.Id).Delete(&model.ModerationAccountState{}).Error)
		require.NoError(t, model.DB.Delete(trackedToken).Error)
		require.NoError(t, model.DB.Delete(trackedUser).Error)
	})

	changed, err = model.DisableUserAndTokensForModeration(trackedUser.Id, time.Now().Unix())
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = model.RestoreUserAndTokensAfterModeration(trackedUser.Id, time.Now().Unix())
	require.NoError(t, err)
	require.True(t, changed)
	var restoredToken model.Token
	require.NoError(t, model.DB.First(&restoredToken, trackedToken.Id).Error)
	assert.Equal(t, common.TokenStatusEnabled, restoredToken.Status)
}

func TestModerationCaptureWorksWithIOCopy(t *testing.T) {
	ginContext, recorder := testGinContext()
	capture := NewModerationCapture(ginContext.Writer)
	ginContext.Writer = capture

	written, err := io.Copy(ginContext.Writer, strings.NewReader("data: first\n\ndata: second\n"))
	require.NoError(t, err)
	assert.Equal(t, int64(len("data: first\n\ndata: second\n")), written)
	assert.Equal(t, "data: first\n\ndata: second\n", recorder.Body.String())
	assert.Equal(t, recorder.Body.Bytes(), capture.Bytes())
}

func TestModerationEndpoint(t *testing.T) {
	endpoint, err := moderationEndpoint(setting.ContentModerationSetting{BaseURL: "https://api.openai.com/v1"})
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com/v1/moderations", endpoint)

	endpoint, err = moderationEndpoint(setting.ContentModerationSetting{BaseURL: "https://api.openai.com"})
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com/v1/moderations", endpoint)

	endpoint, err = moderationEndpoint(setting.ContentModerationSetting{BaseURL: "https://proxy.example/v1/moderations"})
	require.NoError(t, err)
	assert.Equal(t, "https://proxy.example/v1/moderations", endpoint)

	endpoint, err = moderationEndpoint(setting.ContentModerationSetting{BaseURL: ""})
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com/v1/moderations", endpoint)
}

func TestValidateContentModerationURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{name: "empty URL", rawURL: "", wantErr: false},
		{name: "https URL", rawURL: "https://api.openai.com/v1/responses", wantErr: false},
		{name: "http URL with IP and custom port", rawURL: "http://66.154.103.123:8317/v1/responses", wantErr: false},
		{name: "unsupported scheme", rawURL: "ftp://66.154.103.123:8317/v1/responses", wantErr: true},
		{name: "URL with credentials", rawURL: "http://user:pass@66.154.103.123:8317/v1/responses", wantErr: true},
		{name: "URL with fragment", rawURL: "http://66.154.103.123:8317/v1/responses#fragment", wantErr: true},
		{name: "URL with apikey query parameter", rawURL: "http://66.154.103.123:8317/v1/responses?apikey=secret", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateContentModerationURL(tt.rawURL)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestPreflightModerationRequestFailureModes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"error":"upstream down"}`))
	}))
	defer server.Close()

	originalHTTPClient := httpClient
	httpClient = server.Client()
	t.Cleanup(func() { httpClient = originalHTTPClient })

	content := ModerationRequestContent{UserPrompt: "test prompt"}
	err := PreflightModerationRequest(context.Background(), content, setting.ContentModerationSetting{
		Enabled: true, PreflightEnabled: true, BaseURL: server.URL + "/v1", APIKey: "test-key",
		Model: "omni-moderation-latest", FailureMode: "closed", TimeoutSeconds: 2, MaxRetries: 1,
	})
	assert.Error(t, err)

	err = PreflightModerationRequest(context.Background(), content, setting.ContentModerationSetting{
		Enabled: true, PreflightEnabled: true, BaseURL: server.URL + "/v1", APIKey: "test-key",
		Model: "omni-moderation-latest", FailureMode: "open", TimeoutSeconds: 2, MaxRetries: 1,
	})
	assert.NoError(t, err)
}

func TestPersistModerationEventWritesFlaggedOnly(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.ModerationEvent{}, &model.ModerationUserRecord{}))
	userID := int(time.Now().UnixNano()%1_000_000_000 + 11)
	t.Cleanup(func() {
		_ = model.DB.Where("user_id = ?", userID).Delete(&model.ModerationEvent{}).Error
		_ = model.DB.Where("user_id = ?", userID).Delete(&model.ModerationUserRecord{}).Error
	})
	now := common.GetTimestamp()
	require.NoError(t, persistModerationEvent(model.ModerationEvent{
		UserID:      userID,
		Source:      model.ModerationEventSourcePreflight,
		Actor:       model.ModerationEventActorUser,
		Decision:    "block",
		Severity:    "high",
		UserExcerpt: "unsafe prompt",
		Status:      model.ModerationEventActive,
		CreatedAt:   now,
		ExpiresAt:   now + 3600,
	}, true))
	var count int64
	require.NoError(t, model.DB.Model(&model.ModerationEvent{}).Where("user_id = ?", userID).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var record model.ModerationUserRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&record).Error)
	assert.Equal(t, 1, record.MaxViolationCount)
}

func TestCleanupDropsLegacyModerationTablesAndExpiredEvents(t *testing.T) {
	require.NoError(t, model.DB.Exec("CREATE TABLE IF NOT EXISTS moderation_conversations (id INTEGER PRIMARY KEY)").Error)
	require.NoError(t, model.DB.Exec("CREATE TABLE IF NOT EXISTS moderation_turns (id INTEGER PRIMARY KEY)").Error)
	require.NoError(t, model.DB.Exec("CREATE TABLE IF NOT EXISTS moderation_jobs (id INTEGER PRIMARY KEY)").Error)
	require.NoError(t, model.DB.Exec("CREATE TABLE IF NOT EXISTS moderation_violations (id INTEGER PRIMARY KEY)").Error)
	require.NoError(t, model.DB.Exec("CREATE TABLE IF NOT EXISTS moderation_notifications (id INTEGER PRIMARY KEY)").Error)
	require.NoError(t, model.DB.AutoMigrate(&model.ModerationEvent{}))

	now := common.GetTimestamp()
	stale := model.ModerationEvent{
		UserID: 42, Source: model.ModerationEventSourcePreflight, Actor: model.ModerationEventActorUser,
		Decision: "block", Severity: "low", Status: model.ModerationEventActive, CreatedAt: now - 8*24*60*60, ExpiresAt: now - 1,
	}
	fresh := model.ModerationEvent{
		UserID: 42, Source: model.ModerationEventSourcePreflight, Actor: model.ModerationEventActorUser,
		Decision: "block", Severity: "low", Status: model.ModerationEventActive, CreatedAt: now, ExpiresAt: now + 3600,
	}
	require.NoError(t, model.DB.Create(&stale).Error)
	require.NoError(t, model.DB.Create(&fresh).Error)
	t.Cleanup(func() {
		_ = model.DB.Where("id IN ?", []int64{stale.ID, fresh.ID}).Delete(&model.ModerationEvent{}).Error
	})

	require.NoError(t, CleanupContentModerationData())
	assert.False(t, model.DB.Migrator().HasTable("moderation_conversations"))
	assert.False(t, model.DB.Migrator().HasTable("moderation_turns"))
	var remaining int64
	require.NoError(t, model.DB.Model(&model.ModerationEvent{}).Where("id IN ?", []int64{stale.ID, fresh.ID}).Count(&remaining).Error)
	assert.Equal(t, int64(1), remaining)
}
