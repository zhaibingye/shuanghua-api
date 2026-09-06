package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersistModerationTurnsAssignUniqueRoundsUnderConcurrency(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(
		&model.ModerationConversation{},
		&model.ModerationTurn{},
		&model.ModerationJob{},
		&model.ModerationViolation{},
	))

	conversationID := fmt.Sprintf("moderation-concurrency-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		turnIDs := model.DB.Model(&model.ModerationTurn{}).
			Select("id").Where("conversation_key = ?", conversationID)
		require.NoError(t, model.DB.Where("turn_id IN (?)", turnIDs).Delete(&model.ModerationJob{}).Error)
		require.NoError(t, model.DB.Where("conversation_key = ?", conversationID).Delete(&model.ModerationTurn{}).Error)
		require.NoError(t, model.DB.Where("conversation_id = ?", conversationID).Delete(&model.ModerationConversation{}).Error)
	})

	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, 4)
	for round := 1; round <= 4; round++ {
		waitGroup.Add(1)
		go func(round int) {
			defer waitGroup.Done()
			turn := &model.ModerationTurn{
				UserID:          910000,
				ConversationKey: conversationID,
				RequestID:       fmt.Sprintf("request-%d", round),
				UserPrompt:      model.ModerationText(fmt.Sprintf("prompt %d", round)),
				ResponseStatus:  "success",
				ExpiresAt:       time.Now().Add(24 * time.Hour).Unix(),
			}
			errorsChannel <- persistModerationTurn(turn)
		}(round)
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		require.NoError(t, err)
	}

	var turns []model.ModerationTurn
	require.NoError(t, model.DB.Where("conversation_key = ?", conversationID).Order("round_number asc").Find(&turns).Error)
	require.Len(t, turns, 4)
	for index, turn := range turns {
		assert.Equal(t, index+1, turn.RoundNumber)
	}
}

func TestCallNativeModerationOpenAIFormat(t *testing.T) {
	originalHTTPClient := httpClient
	originalProtectedHTTPClient := ssrfProtectedHTTPClient
	fetchSetting := system_setting.GetFetchSetting()
	originalFetchSetting := *fetchSetting
	t.Cleanup(func() {
		httpClient = originalHTTPClient
		ssrfProtectedHTTPClient = originalProtectedHTTPClient
		*fetchSetting = originalFetchSetting
	})
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

		respJSON := `{
			"id": "modr-test-123",
			"model": "omni-moderation-latest",
			"results": [
				{
					"flagged": true,
					"categories": {
						"hate": true,
						"violence": false,
						"harassment": false
					},
					"category_scores": {
						"hate": 0.92,
						"violence": 0.05,
						"harassment": 0.01
					}
				}
			]
		}`
		writer.Header().Set("Content-Type", "application/json")
		_, err = writer.Write([]byte(respJSON))
		require.NoError(t, err)
	}))
	defer server.Close()
	httpClient = server.Client()
	ssrfProtectedHTTPClient = nil

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
					"flagged": flagged,
					"categories": map[string]bool{
						"violence": flagged,
					},
					"category_scores": map[string]float64{
						"violence": 0.88,
					},
				},
			},
		}
		respBytes, _ := common.Marshal(resp)
		writer.Header().Set("Content-Type", "application/json")
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

	// Safe content passes
	safeContent := ModerationRequestContent{UserPrompt: "hello safe world"}
	err := PreflightModerationRequest(context.Background(), safeContent, config)
	assert.NoError(t, err)

	// Unsafe content is blocked
	unsafeContent := ModerationRequestContent{UserPrompt: "this is unsafe"}
	err = PreflightModerationRequest(context.Background(), unsafeContent, config)
	assert.ErrorIs(t, err, model.ErrModerationConversationBlocked)
}

func TestSanitizeModerationTextRuneSafe(t *testing.T) {
	assert.Equal(t, "hello world", sanitizeModerationText("  hello world  "))

	chinese := strings.Repeat("中", 10000) // 30000 bytes
	sanitized := sanitizeModerationText(chinese)
	assert.LessOrEqual(t, len(sanitized), 25000)
	assert.True(t, utf8.ValidString(sanitized), "sanitized text must be valid UTF-8")
}

func TestCallModerationContentMultiModal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/v1/moderations", request.URL.Path)
		assert.Equal(t, "Bearer test-key", request.Header.Get("Authorization"))

		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)

		var reqPayload map[string]any
		require.NoError(t, common.Unmarshal(body, &reqPayload))
		assert.Equal(t, "omni-moderation-latest", reqPayload["model"])

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

		respJSON := `{
			"id": "modr-multi-123",
			"model": "omni-moderation-latest",
			"results": [
				{
					"flagged": true,
					"categories": {
						"violence": true,
						"violence/graphic": false
					},
					"category_scores": {
						"violence": 0.85,
						"violence/graphic": 0.2
					},
					"category_applied_input_types": {
						"violence": ["image"]
					}
				}
			]
		}`
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(respJSON))
	}))
	defer server.Close()

	originalHTTPClient := httpClient
	httpClient = server.Client()
	t.Cleanup(func() { httpClient = originalHTTPClient })

	config := setting.ContentModerationSetting{
		BaseURL:        server.URL + "/v1",
		APIKey:         "test-key",
		Model:          "omni-moderation-latest",
		TimeoutSeconds: 5,
		MaxRetries:     1,
	}

	content := ModerationRequestContent{
		UserPrompt: "check this image",
		ImageURLs:  []string{"https://example.com/image.png"},
	}

	result, _, err := callModerationContent(context.Background(), config, content)
	require.NoError(t, err)
	assert.True(t, result.Flagged)
	assert.True(t, result.Categories["violence"])
	assert.Equal(t, 0.85, result.CategoryScores["violence"])

	decision := moderationDecisionFromNativeResult(result)
	assert.Equal(t, "block", decision.Decision)
	assert.Equal(t, "high", decision.Severity)
	assert.Equal(t, "violence", decision.ReasonCode)
}

func TestModerationJobResultCannotOverwriteAReclaimedLease(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.ModerationJob{}))
	job := &model.ModerationJob{
		TurnID:        time.Now().UnixNano(),
		Status:        model.ModerationJobRunning,
		LockedAt:      100,
		Attempts:      1,
		NextAttemptAt: 0,
		ExpiresAt:     time.Now().Add(time.Hour).Unix(),
	}
	require.NoError(t, model.DB.Create(job).Error)
	t.Cleanup(func() { require.NoError(t, model.DB.Delete(job).Error) })

	err := model.SaveModerationJobResult(job.ID, 99, model.ModerationJobSuccess, 2, 0, `{"decision":"allow"}`, "")
	require.ErrorIs(t, err, model.ErrModerationJobLeaseLost)
	var stored model.ModerationJob
	require.NoError(t, model.DB.First(&stored, job.ID).Error)
	assert.Equal(t, model.ModerationJobRunning, stored.Status)
	assert.Equal(t, int64(100), stored.LockedAt)

	require.NoError(t, model.SaveModerationJobResult(job.ID, 100, model.ModerationJobSuccess, 2, 0, `{"decision":"allow"}`, ""))
	require.NoError(t, model.DB.First(&stored, job.ID).Error)
	assert.Equal(t, model.ModerationJobSuccess, stored.Status)
	assert.Zero(t, stored.LockedAt)
}

func TestSetModerationRequestContentFromJSONUsesEffectiveGenericFields(t *testing.T) {
	ginContext, _ := testGinContext()
	request := &dto.GeneralOpenAIRequest{}
	SetModerationRequestContentFromJSON(ginContext, []byte(`{"systemInstruction":{"parts":[{"text":"effective system"}]},"contents":[{"role":"user","parts":[{"text":"effective user"}]},{"role":"model","parts":[{"text":"prior assistant"}]}]}`), request)

	content, ok := common.GetContextKeyType[ModerationRequestContent](ginContext, constant.ContextKeyModerationRequestContent)
	require.True(t, ok)
	assert.Equal(t, "effective system", content.SystemPrompt)
	assert.Equal(t, "effective user", content.UserPrompt)
	assert.NotContains(t, content.UserPrompt, "prior assistant")

	SetModerationRequestContentFromJSON(ginContext, []byte(`{"input":[{"type":"message","role":"system","content":"input system"},{"type":"message","role":"user","content":"input user"},{"type":"message","role":"assistant","content":"prior response"}]}`), request)
	content, ok = common.GetContextKeyType[ModerationRequestContent](ginContext, constant.ContextKeyModerationRequestContent)
	require.True(t, ok)
	assert.Equal(t, "input system", content.SystemPrompt)
	assert.Equal(t, "input user", content.UserPrompt)
	assert.NotContains(t, content.UserPrompt, "prior response")

	responsesRequest := &dto.OpenAIResponsesRequest{}
	SetModerationRequestContentFromJSON(ginContext, []byte(`{"input":[{"type":"message","role":"user","content":"current input"},{"type":"message","role":"assistant","content":"prior output"}]}`), responsesRequest)
	content, ok = common.GetContextKeyType[ModerationRequestContent](ginContext, constant.ContextKeyModerationRequestContent)
	require.True(t, ok)
	assert.Equal(t, "current input", content.UserPrompt)
	assert.NotContains(t, content.UserPrompt, "prior output")
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
	var storedUntrackedUser model.User
	require.NoError(t, model.DB.First(&storedUntrackedUser, untrackedUser.Id).Error)
	assert.Equal(t, common.UserStatusDisabled, storedUntrackedUser.Status)

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
	var disabledToken model.Token
	require.NoError(t, model.DB.First(&disabledToken, trackedToken.Id).Error)
	assert.Equal(t, common.TokenStatusDisabled, disabledToken.Status)

	changed, err = model.RestoreUserAndTokensAfterModeration(trackedUser.Id, time.Now().Unix())
	require.NoError(t, err)
	require.True(t, changed)
	var restoredUser model.User
	require.NoError(t, model.DB.First(&restoredUser, trackedUser.Id).Error)
	assert.Equal(t, common.UserStatusEnabled, restoredUser.Status)
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

func TestModerationResponseStatusPreservesStreamInterruption(t *testing.T) {
	status := relaycommon.NewStreamStatus()
	status.SetEndReason(relaycommon.StreamEndReasonClientGone, context.Canceled)
	info := &relaycommon.RelayInfo{IsStream: true, StreamStatus: status}

	assert.Equal(t, "client_gone", moderationResponseStatus(info, nil, true))
}

func testGinContext() (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	return context, recorder
}

func TestExtractModerationAssistantTextReadsOpenAIJSONAndSSE(t *testing.T) {
	jsonResponse := []byte(`{"choices":[{"message":{"content":"safe answer"}}]}`)
	assert.Equal(t, "safe answer", ExtractModerationAssistantText(jsonResponse, "application/json", types.RelayFormatOpenAI))

	sseResponse := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\" two\"}}]}\n\ndata: [DONE]\n\n")
	assert.Equal(t, "one two", ExtractModerationAssistantText(sseResponse, "text/event-stream", types.RelayFormatOpenAI))

	responsesSSE := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"first\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\" second\"}\n\ndata: [DONE]\n\n")
	assert.Equal(t, "first second", ExtractModerationAssistantText(responsesSSE, "text/event-stream", types.RelayFormatOpenAIResponses))
}

func TestExtractModerationAssistantTextReadsGeminiParts(t *testing.T) {
	response := []byte(`{"candidates":[{"content":{"parts":[{"text":"Gemini answer"}]}}]}`)
	assert.Equal(t, "Gemini answer", ExtractModerationAssistantText(response, "application/json", types.RelayFormatGemini))
}

func TestExtractModerationRequestContentPreservesMediaPlaceholders(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "system", Content: "system rule"},
			{Role: "user", Content: []any{
				map[string]any{"type": dto.ContentTypeText, "text": "describe this"},
				map[string]any{"type": dto.ContentTypeImageURL, "image_url": map[string]any{"url": "data:image/png;base64,secret"}},
			}},
		},
	}

	content := extractModerationRequestContent(request)
	require.Equal(t, "system rule", content.SystemPrompt)
	assert.Contains(t, content.UserPrompt, "describe this")
	assert.Contains(t, content.UserPrompt, "[image content not saved]")
	assert.NotContains(t, content.UserPrompt, "secret")
}

func TestExtractModerationRequestContentDoesNotPersistResponsesMediaData(t *testing.T) {
	request := &dto.OpenAIResponsesRequest{
		Instructions: []byte(`[{"type":"input_image","image_url":"data:image/png;base64,secret"}]`),
		Input:        []byte(`[{"type":"input_text","text":"read this"},{"type":"input_file","file_data":"secret-file"}]`),
	}

	content := extractModerationRequestContent(request)
	assert.Contains(t, content.SystemPrompt, "[image content not saved]")
	assert.Contains(t, content.UserPrompt, "read this")
	assert.Contains(t, content.UserPrompt, "[file content not saved]")
	assert.NotContains(t, content.SystemPrompt, "secret")
	assert.NotContains(t, content.UserPrompt, "secret-file")
}

func TestExtractModerationRequestContentPreservesClaudeMediaPlaceholders(t *testing.T) {
	request := &dto.ClaudeRequest{
		Messages: []dto.ClaudeMessage{{
			Role: "user",
			Content: []any{
				map[string]any{"type": "text", "text": "inspect this"},
				map[string]any{"type": "image", "source": map[string]any{"data": "secret"}},
			},
		}},
	}

	content := extractModerationRequestContent(request)
	assert.Contains(t, content.UserPrompt, "inspect this")
	assert.Contains(t, content.UserPrompt, "[media content not saved]")
	assert.NotContains(t, content.UserPrompt, "secret")
}

func TestModerationEndpoint(t *testing.T) {
	endpoint, err := moderationEndpoint(setting.ContentModerationSetting{
		BaseURL: "https://api.openai.com/v1",
		Model:   "omni-moderation-latest",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com/v1/moderations", endpoint)

	endpoint, err = moderationEndpoint(setting.ContentModerationSetting{
		BaseURL: "https://api.openai.com",
		Model:   "omni-moderation-latest",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com/v1/moderations", endpoint)

	endpoint, err = moderationEndpoint(setting.ContentModerationSetting{
		BaseURL: "https://api.openai.com/",
		Model:   "omni-moderation-latest",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com/v1/moderations", endpoint)

	endpoint, err = moderationEndpoint(setting.ContentModerationSetting{
		BaseURL: "https://proxy.example/v1/moderations",
		Model:   "omni-moderation-latest",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://proxy.example/v1/moderations", endpoint)

	endpoint, err = moderationEndpoint(setting.ContentModerationSetting{
		BaseURL: "http://66.154.103.123:8317/v1",
		Model:   "omni-moderation-latest",
	})
	require.NoError(t, err)
	assert.Equal(t, "http://66.154.103.123:8317/v1/moderations", endpoint)

	// Empty BaseURL defaults to official URL
	endpoint, err = moderationEndpoint(setting.ContentModerationSetting{
		BaseURL: "",
		Model:   "omni-moderation-latest",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com/v1/moderations", endpoint)
}

func TestValidateContentModerationURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{
			name:    "empty URL",
			rawURL:  "",
			wantErr: false,
		},
		{
			name:    "whitespace URL",
			rawURL:  "   ",
			wantErr: false,
		},
		{
			name:    "http URL with IP and custom port",
			rawURL:  "http://66.154.103.123:8317/v1/responses",
			wantErr: false,
		},
		{
			name:    "https URL",
			rawURL:  "https://api.openai.com/v1/responses",
			wantErr: false,
		},
		{
			name:    "http localhost with port",
			rawURL:  "http://127.0.0.1:8000/v1/responses",
			wantErr: false,
		},
		{
			name:    "unsupported scheme",
			rawURL:  "ftp://66.154.103.123:8317/v1/responses",
			wantErr: true,
		},
		{
			name:    "URL with credentials",
			rawURL:  "http://user:pass@66.154.103.123:8317/v1/responses",
			wantErr: true,
		},
		{
			name:    "URL with fragment",
			rawURL:  "http://66.154.103.123:8317/v1/responses#fragment",
			wantErr: true,
		},
		{
			name:    "URL with apikey query parameter",
			rawURL:  "http://66.154.103.123:8317/v1/responses?apikey=secret",
			wantErr: true,
		},
		{
			name:    "URL with token query parameter",
			rawURL:  "http://66.154.103.123:8317/v1/responses?token=secret",
			wantErr: true,
		},
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
	// Server returns 500 error
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"error":"upstream down"}`))
	}))
	defer server.Close()

	originalHTTPClient := httpClient
	httpClient = server.Client()
	t.Cleanup(func() { httpClient = originalHTTPClient })

	content := ModerationRequestContent{UserPrompt: "test prompt"}

	// 1. Closed mode fails on upstream error
	configClosed := setting.ContentModerationSetting{
		Enabled:          true,
		PreflightEnabled: true,
		BaseURL:          server.URL + "/v1",
		APIKey:           "test-key",
		Model:            "omni-moderation-latest",
		FailureMode:      "closed",
		TimeoutSeconds:   2,
		MaxRetries:       1,
	}
	err := PreflightModerationRequest(context.Background(), content, configClosed)
	assert.Error(t, err)

	// 2. Open mode allows through on upstream error
	configOpen := setting.ContentModerationSetting{
		Enabled:          true,
		PreflightEnabled: true,
		BaseURL:          server.URL + "/v1",
		APIKey:           "test-key",
		Model:            "omni-moderation-latest",
		FailureMode:      "open",
		TimeoutSeconds:   2,
		MaxRetries:       1,
	}
	err = PreflightModerationRequest(context.Background(), content, configOpen)
	assert.NoError(t, err)
}

func TestResolveModerationConversationID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1. Explicit X-Conversation-ID header
	rec1 := httptest.NewRecorder()
	c1, _ := gin.CreateTestContext(rec1)
	c1.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c1.Request.Header.Set("X-Conversation-ID", "header-chat-123")
	assert.Equal(t, "header-chat-123", ResolveModerationConversationID(c1))

	// 2. Body with conversation_id
	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	c2.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"conversation_id":"body-conv-456"}`))
	s2, err := common.CreateBodyStorage([]byte(`{"conversation_id":"body-conv-456"}`))
	require.NoError(t, err)
	c2.Set(common.KeyBodyStorage, s2)
	assert.Equal(t, "body-conv-456", ResolveModerationConversationID(c2))

	// 3. Body with session_id
	rec3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(rec3)
	c3.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"session_id":"sess-789"}`))
	s3, err := common.CreateBodyStorage([]byte(`{"session_id":"sess-789"}`))
	require.NoError(t, err)
	c3.Set(common.KeyBodyStorage, s3)
	assert.Equal(t, "sess-789", ResolveModerationConversationID(c3))

	// 4. No explicit ID - derive a deterministic fallback from request content
	rec4 := httptest.NewRecorder()
	c4, _ := gin.CreateTestContext(rec4)
	c4.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	common.SetContextKey(c4, common.RequestIdKey, "test-req-999")
	convID := ResolveModerationConversationID(c4)
	assert.Equal(t, moderationConversationIDFromSeed("hello"), convID)

	// Verify cached value is returned consistently
	assert.Equal(t, moderationConversationIDFromSeed("hello"), ResolveModerationConversationID(c4))
}

func TestResolveModerationConversationIDUsesStableHistorySeed(t *testing.T) {
	gin.SetMode(gin.TestMode)

	firstRequestBody := `{"model":"gpt-4o","messages":[{"role":"system","content":"assistant prompt"},{"role":"user","content":"first message"}]}`
	firstRecorder := httptest.NewRecorder()
	firstContext, _ := gin.CreateTestContext(firstRecorder)
	firstContext.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(firstRequestBody))
	firstStorage, err := common.CreateBodyStorage([]byte(firstRequestBody))
	require.NoError(t, err)
	firstContext.Set(common.KeyBodyStorage, firstStorage)
	t.Cleanup(func() { _ = firstStorage.Close() })
	common.SetContextKey(firstContext, common.RequestIdKey, "rikkahub-request-1")

	secondRequestBody := `{"model":"gpt-4o","messages":[{"role":"system","content":"assistant prompt"},{"role":"user","content":"first message"},{"role":"assistant","content":"first answer"},{"role":"user","content":"second message"}]}`
	secondRecorder := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondRecorder)
	secondContext.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(secondRequestBody))
	secondStorage, err := common.CreateBodyStorage([]byte(secondRequestBody))
	require.NoError(t, err)
	secondContext.Set(common.KeyBodyStorage, secondStorage)
	t.Cleanup(func() { _ = secondStorage.Close() })
	common.SetContextKey(secondContext, common.RequestIdKey, "rikkahub-request-2")

	firstConversationID := ResolveModerationConversationID(firstContext)
	secondConversationID := ResolveModerationConversationID(secondContext)
	require.NotEmpty(t, firstConversationID)
	assert.Equal(t, firstConversationID, secondConversationID)
	assert.Contains(t, firstConversationID, "conv_seed_")
	assert.NotEqual(t, "conv_rikkahub-request-1", firstConversationID)
	assert.NotEqual(t, "conv_rikkahub-request-2", secondConversationID)

	thirdRequestBody := `{"model":"gpt-4o","messages":[{"role":"system","content":"assistant prompt"},{"role":"user","content":"another conversation"}]}`
	thirdRecorder := httptest.NewRecorder()
	thirdContext, _ := gin.CreateTestContext(thirdRecorder)
	thirdContext.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(thirdRequestBody))
	thirdStorage, err := common.CreateBodyStorage([]byte(thirdRequestBody))
	require.NoError(t, err)
	thirdContext.Set(common.KeyBodyStorage, thirdStorage)
	t.Cleanup(func() { _ = thirdStorage.Close() })
	common.SetContextKey(thirdContext, common.RequestIdKey, "rikkahub-request-3")

	assert.NotEqual(t, firstConversationID, ResolveModerationConversationID(thirdContext))
}

func TestResolveModerationConversationIDForUserKeepsIdenticalInitialPromptsSeparate(t *testing.T) {
	requestBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"same opening prompt"}]}`

	firstRecorder := httptest.NewRecorder()
	firstContext, _ := gin.CreateTestContext(firstRecorder)
	firstContext.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(requestBody))
	firstStorage, err := common.CreateBodyStorage([]byte(requestBody))
	require.NoError(t, err)
	firstContext.Set(common.KeyBodyStorage, firstStorage)
	t.Cleanup(func() { _ = firstStorage.Close() })
	common.SetContextKey(firstContext, common.RequestIdKey, "rikkahub-identical-request-1")

	secondRecorder := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondRecorder)
	secondContext.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(requestBody))
	secondStorage, err := common.CreateBodyStorage([]byte(requestBody))
	require.NoError(t, err)
	secondContext.Set(common.KeyBodyStorage, secondStorage)
	t.Cleanup(func() { _ = secondStorage.Close() })
	common.SetContextKey(secondContext, common.RequestIdKey, "rikkahub-identical-request-2")

	firstConversationID := ResolveModerationConversationIDForUser(firstContext, 950002)
	secondConversationID := ResolveModerationConversationIDForUser(secondContext, 950002)
	assert.NotEqual(t, firstConversationID, secondConversationID)
	assert.Equal(t, "conv_rikkahub-identical-request-1", firstConversationID)
	assert.Equal(t, "conv_rikkahub-identical-request-2", secondConversationID)
}

func TestResolveModerationConversationIDMatchesHistoryForAllSupportedFormats(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.ModerationConversation{}, &model.ModerationTurn{}))

	tests := []struct {
		name      string
		body      string
		assistant string
	}{
		{
			name:      "openai chat completions",
			assistant: "distinctive assistant history for chat",
			body:      `{"model":"gpt-4o","messages":[{"role":"user","content":"first user message"},{"role":"assistant","content":"distinctive assistant history for chat"},{"role":"user","content":"current user message"}]}`,
		},
		{
			name:      "openai responses",
			assistant: "distinctive assistant history for responses",
			body:      `{"model":"gpt-4o","input":[{"type":"message","role":"user","content":"first user message"},{"type":"message","role":"assistant","content":"distinctive assistant history for responses"},{"type":"message","role":"user","content":"current user message"}]}`,
		},
		{
			name:      "gemini",
			assistant: "distinctive assistant history for gemini",
			body:      `{"model":"gemini-2.5-pro","contents":[{"role":"user","parts":[{"text":"first user message"}]},{"role":"model","parts":[{"text":"distinctive assistant history for gemini"}]},{"role":"user","parts":[{"text":"current user message"}]}]}`,
		},
		{
			name:      "gemini generateContentRequest wrapper",
			assistant: "distinctive assistant history for gemini wrapper",
			body:      `{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"first user message"}]},{"role":"model","parts":[{"text":"distinctive assistant history for gemini wrapper"}]},{"role":"user","parts":[{"text":"current user message"}]}]}}`,
		},
		{
			name:      "claude",
			assistant: "distinctive assistant history for claude",
			body:      `{"model":"claude-sonnet","messages":[{"role":"user","content":"first user message"},{"role":"assistant","content":"distinctive assistant history for claude"},{"role":"user","content":"current user message"}]}`,
		},
	}

	userID := 950003
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conversationKey := fmt.Sprintf("format-history-%d", time.Now().UnixNano())
			now := time.Now().Unix()
			conversation := &model.ModerationConversation{
				UserID:          userID,
				ConversationID:  conversationKey,
				Status:          model.ModerationConversationActive,
				FirstActivityAt: now,
				LastActivityAt:  now,
				ExpiresAt:       now + 3600,
			}
			require.NoError(t, model.DB.Create(conversation).Error)
			turn := &model.ModerationTurn{
				ConversationID:  conversation.ID,
				UserID:          userID,
				ConversationKey: conversationKey,
				RoundNumber:     1,
				UserPrompt:      "first user message",
				AssistantReply:  model.ModerationText(tt.assistant),
				ResponseStatus:  "success",
				ExpiresAt:       now + 3600,
			}
			require.NoError(t, model.DB.Create(turn).Error)
			t.Cleanup(func() {
				require.NoError(t, model.DB.Where("conversation_key = ?", conversationKey).Delete(&model.ModerationTurn{}).Error)
				require.NoError(t, model.DB.Where("conversation_id = ?", conversationKey).Delete(&model.ModerationConversation{}).Error)
			})

			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tt.body))
			storage, err := common.CreateBodyStorage([]byte(tt.body))
			require.NoError(t, err)
			ginContext.Set(common.KeyBodyStorage, storage)
			t.Cleanup(func() { _ = storage.Close() })
			common.SetContextKey(ginContext, common.RequestIdKey, "format-history-request")

			assert.Equal(t, conversationKey, ResolveModerationConversationIDForUser(ginContext, userID))
		})
	}
}

func TestResolveModerationConversationIDDoesNotGuessBetweenSplitConversations(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.ModerationConversation{}, &model.ModerationTurn{}))

	userID := 950004
	now := time.Now().Unix()
	blockedKey := fmt.Sprintf("split-blocked-%d", time.Now().UnixNano())
	activeKey := fmt.Sprintf("split-active-%d", time.Now().UnixNano())
	blockedConversation := &model.ModerationConversation{
		UserID:          userID,
		ConversationID:  blockedKey,
		Status:          model.ModerationConversationBlocked,
		FirstActivityAt: now,
		LastActivityAt:  now,
		ExpiresAt:       now + 3600,
	}
	activeConversation := &model.ModerationConversation{
		UserID:          userID,
		ConversationID:  activeKey,
		Status:          model.ModerationConversationActive,
		FirstActivityAt: now,
		LastActivityAt:  now,
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.DB.Create(blockedConversation).Error)
	require.NoError(t, model.DB.Create(activeConversation).Error)
	blockedTurn := &model.ModerationTurn{
		ConversationID:  blockedConversation.ID,
		UserID:          userID,
		ConversationKey: blockedKey,
		RoundNumber:     1,
		UserPrompt:      "first message",
		AssistantReply:  "distinctive blocked response from split history",
		ResponseStatus:  "success",
		ExpiresAt:       now + 3600,
	}
	activeTurn := &model.ModerationTurn{
		ConversationID:  activeConversation.ID,
		UserID:          userID,
		ConversationKey: activeKey,
		RoundNumber:     1,
		UserPrompt:      "first message\nsecond message",
		AssistantReply:  "distinctive active response from split history",
		ResponseStatus:  "success",
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.DB.Create(blockedTurn).Error)
	require.NoError(t, model.DB.Create(activeTurn).Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("conversation_key IN ?", []string{blockedKey, activeKey}).Delete(&model.ModerationTurn{}).Error)
		require.NoError(t, model.DB.Where("conversation_id IN ?", []string{blockedKey, activeKey}).Delete(&model.ModerationConversation{}).Error)
	})

	requestBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"first message"},{"role":"assistant","content":"distinctive blocked response from split history"},{"role":"user","content":"second message"},{"role":"assistant","content":"distinctive active response from split history"},{"role":"user","content":"third message"}]}`
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(requestBody))
	storage, err := common.CreateBodyStorage([]byte(requestBody))
	require.NoError(t, err)
	ginContext.Set(common.KeyBodyStorage, storage)
	t.Cleanup(func() { _ = storage.Close() })
	common.SetContextKey(ginContext, common.RequestIdKey, "split-history-request")

	resolvedKey := ResolveModerationConversationIDForUser(ginContext, userID)
	assert.NotEqual(t, blockedKey, resolvedKey)
	assert.NotEqual(t, activeKey, resolvedKey)
	assert.Equal(t, "conv_split-history-request", resolvedKey)
}

func TestResolveModerationConversationIDReusesConversationAfterHistoryTruncation(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.ModerationConversation{}, &model.ModerationTurn{}))

	userID := 950001
	conversationKey := fmt.Sprintf("rikkahub-history-%d", time.Now().UnixNano())
	now := time.Now().Unix()
	conversation := &model.ModerationConversation{
		UserID:          userID,
		ConversationID:  conversationKey,
		Status:          model.ModerationConversationActive,
		FirstActivityAt: now,
		LastActivityAt:  now,
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.DB.Create(conversation).Error)
	turn := &model.ModerationTurn{
		ConversationID:  conversation.ID,
		UserID:          userID,
		ConversationKey: conversationKey,
		RoundNumber:     1,
		UserPrompt:      "first message",
		AssistantReply:  "a distinctive first answer",
		ResponseStatus:  "success",
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.DB.Create(turn).Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Where("conversation_key = ?", conversationKey).Delete(&model.ModerationTurn{}).Error)
		require.NoError(t, model.DB.Where("conversation_id = ?", conversationKey).Delete(&model.ModerationConversation{}).Error)
	})

	// Simulate RikkaHub's context window dropping the first user message while
	// retaining the previous assistant response in the next request.
	requestBody := `{"model":"gpt-4o","messages":[{"role":"assistant","content":"a distinctive first answer"},{"role":"user","content":"second message"}]}`
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(requestBody))
	storage, err := common.CreateBodyStorage([]byte(requestBody))
	require.NoError(t, err)
	ginContext.Set(common.KeyBodyStorage, storage)
	t.Cleanup(func() { _ = storage.Close() })
	common.SetContextKey(ginContext, common.RequestIdKey, "rikkahub-history-request")

	assert.Equal(t, conversationKey, ResolveModerationConversationIDForUser(ginContext, userID))
}

func TestModerationDecisionCategories(t *testing.T) {
	result := openAIModerationResult{
		Flagged: true,
		Categories: map[string]bool{
			"harassment":             true,
			"harassment/threatening": true,
		},
		CategoryScores: map[string]float64{
			"harassment":             0.82,
			"harassment/threatening": 0.77,
		},
	}
	decision := moderationDecisionFromNativeResult(result)
	assert.Equal(t, "block", decision.Decision)
	assert.Equal(t, "high", decision.Severity)
	assert.Equal(t, "harassment", decision.ReasonCode)
	assert.Equal(t, []string{"harassment", "harassment/threatening"}, decision.Categories)
}

func TestFinalizeModerationChannelFilter(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(
		&model.ModerationConversation{},
		&model.ModerationTurn{},
		&model.ModerationJob{},
	))

	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	prevEnabled := common.OptionMap[setting.ContentModerationEnabledOption]
	prevChannels := common.OptionMap[setting.ContentModerationChannelsOption]
	common.OptionMap[setting.ContentModerationEnabledOption] = "true"
	common.OptionMap[setting.ContentModerationChannelsOption] = "2, 3"
	common.OptionMapRWMutex.Unlock()

	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap[setting.ContentModerationEnabledOption] = prevEnabled
		common.OptionMap[setting.ContentModerationChannelsOption] = prevChannels
		common.OptionMapRWMutex.Unlock()
	})

	userID := 920001
	convID := fmt.Sprintf("conv-filter-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		turnIDs := model.DB.Model(&model.ModerationTurn{}).Select("id").Where("user_id = ? AND conversation_key = ?", userID, convID)
		_ = model.DB.Where("turn_id IN (?)", turnIDs).Delete(&model.ModerationJob{}).Error
		_ = model.DB.Where("user_id = ? AND conversation_key = ?", userID, convID).Delete(&model.ModerationTurn{}).Error
		_ = model.DB.Where("user_id = ? AND conversation_id = ?", userID, convID).Delete(&model.ModerationConversation{}).Error
	})

	// 1. Channel 1 is NOT in [2, 3] -> should NOT be recorded
	rec1 := httptest.NewRecorder()
	c1, _ := gin.CreateTestContext(rec1)
	c1.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c1.Writer.Header().Set("Content-Type", "application/json")
	capture1 := NewModerationCapture(c1.Writer)
	_, _ = capture1.WriteString(`{"choices":[{"message":{"content":"response from channel 1"}}]}`)
	c1.Writer = capture1
	common.SetContextKey(c1, constant.ContextKeyModerationCapture, capture1)
	common.SetContextKey(c1, constant.ContextKeyModerationConversationID, convID)
	common.SetContextKey(c1, constant.ContextKeyModerationRequestContent, ModerationRequestContent{
		UserPrompt: "prompt 1",
	})
	info1 := &relaycommon.RelayInfo{
		UserId:          userID,
		RequestId:       "req-ch-1",
		OriginModelName: "gpt-4o",
		RelayFormat:     types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 1,
		},
	}
	FinalizeModeration(c1, info1, nil)

	var count1 int64
	require.NoError(t, model.DB.Model(&model.ModerationTurn{}).Where("user_id = ? AND request_id = ?", userID, "req-ch-1").Count(&count1).Error)
	assert.Equal(t, int64(0), count1, "turn for unmoderated channel 1 should not be recorded")

	// 2. Channel 2 IS in [2, 3] -> should be recorded with ChannelID = 2
	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	c2.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c2.Writer.Header().Set("Content-Type", "application/json")
	capture2 := NewModerationCapture(c2.Writer)
	_, _ = capture2.WriteString(`{"choices":[{"message":{"content":"response from channel 2"}}]}`)
	c2.Writer = capture2
	common.SetContextKey(c2, constant.ContextKeyModerationCapture, capture2)
	common.SetContextKey(c2, constant.ContextKeyModerationConversationID, convID)
	common.SetContextKey(c2, constant.ContextKeyModerationRequestContent, ModerationRequestContent{
		UserPrompt: "prompt 2",
	})
	info2 := &relaycommon.RelayInfo{
		UserId:          userID,
		RequestId:       "req-ch-2",
		OriginModelName: "gpt-4o",
		RelayFormat:     types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 2,
		},
	}
	FinalizeModeration(c2, info2, nil)

	var turn2 model.ModerationTurn
	err := model.DB.Where("user_id = ? AND request_id = ?", userID, "req-ch-2").First(&turn2).Error
	require.NoError(t, err, "turn for moderated channel 2 should be recorded")
	assert.Equal(t, 2, turn2.ChannelID)
	assert.Equal(t, "req-ch-2", turn2.RequestID)

	// 3. Scheme A: Channels setting is empty -> should NOT record for channel 2
	common.OptionMapRWMutex.Lock()
	common.OptionMap[setting.ContentModerationChannelsOption] = ""
	common.OptionMapRWMutex.Unlock()

	rec3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(rec3)
	c3.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c3.Writer.Header().Set("Content-Type", "application/json")
	capture3 := NewModerationCapture(c3.Writer)
	_, _ = capture3.WriteString(`{"choices":[{"message":{"content":"response from channel 2 with empty channels"}}]}`)
	c3.Writer = capture3
	common.SetContextKey(c3, constant.ContextKeyModerationCapture, capture3)
	common.SetContextKey(c3, constant.ContextKeyModerationConversationID, convID)
	common.SetContextKey(c3, constant.ContextKeyModerationRequestContent, ModerationRequestContent{
		UserPrompt: "prompt 3",
	})
	info3 := &relaycommon.RelayInfo{
		UserId:          userID,
		RequestId:       "req-ch-3",
		OriginModelName: "gpt-4o",
		RelayFormat:     types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 2,
		},
	}
	FinalizeModeration(c3, info3, nil)

	var count3 int64
	require.NoError(t, model.DB.Model(&model.ModerationTurn{}).Where("user_id = ? AND request_id = ?", userID, "req-ch-3").Count(&count3).Error)
	assert.Equal(t, int64(0), count3, "when channels is empty, no channel should be recorded (Scheme A)")
}

func TestFinalizeModerationUserWhitelist(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(
		&model.ModerationConversation{},
		&model.ModerationTurn{},
		&model.ModerationJob{},
	))

	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	prevEnabled := common.OptionMap[setting.ContentModerationEnabledOption]
	prevChannels := common.OptionMap[setting.ContentModerationChannelsOption]
	prevWhitelist := common.OptionMap[setting.ContentModerationUserWhitelistOption]
	common.OptionMap[setting.ContentModerationEnabledOption] = "true"
	common.OptionMap[setting.ContentModerationChannelsOption] = "1"
	common.OptionMap[setting.ContentModerationUserWhitelistOption] = "1, 930002"
	common.OptionMapRWMutex.Unlock()

	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap[setting.ContentModerationEnabledOption] = prevEnabled
		common.OptionMap[setting.ContentModerationChannelsOption] = prevChannels
		common.OptionMap[setting.ContentModerationUserWhitelistOption] = prevWhitelist
		common.OptionMapRWMutex.Unlock()
	})

	convID := fmt.Sprintf("conv-whitelist-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		turnIDs := model.DB.Model(&model.ModerationTurn{}).Select("id").Where("conversation_key = ?", convID)
		_ = model.DB.Where("turn_id IN (?)", turnIDs).Delete(&model.ModerationJob{}).Error
		_ = model.DB.Where("conversation_key = ?", convID).Delete(&model.ModerationTurn{}).Error
		_ = model.DB.Where("conversation_id = ?", convID).Delete(&model.ModerationConversation{}).Error
	})

	// 1. Root admin (ID: 1) is whitelisted -> should NOT be recorded even on moderated channel 1
	rec1 := httptest.NewRecorder()
	c1, _ := gin.CreateTestContext(rec1)
	c1.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c1.Writer.Header().Set("Content-Type", "application/json")
	capture1 := NewModerationCapture(c1.Writer)
	_, _ = capture1.WriteString(`{"choices":[{"message":{"content":"admin response"}}]}`)
	c1.Writer = capture1
	common.SetContextKey(c1, constant.ContextKeyModerationCapture, capture1)
	common.SetContextKey(c1, constant.ContextKeyModerationConversationID, convID)
	common.SetContextKey(c1, constant.ContextKeyModerationRequestContent, ModerationRequestContent{UserPrompt: "admin prompt"})
	info1 := &relaycommon.RelayInfo{
		UserId:          1,
		RequestId:       "req-whitelist-admin",
		OriginModelName: "gpt-4o",
		RelayFormat:     types.RelayFormatOpenAI,
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 1},
	}
	FinalizeModeration(c1, info1, nil)

	var count1 int64
	require.NoError(t, model.DB.Model(&model.ModerationTurn{}).Where("request_id = ?", "req-whitelist-admin").Count(&count1).Error)
	assert.Equal(t, int64(0), count1, "whitelisted root admin should not be recorded")

	// 2. User 930002 explicitly in whitelist -> should NOT be recorded
	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	c2.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c2.Writer.Header().Set("Content-Type", "application/json")
	capture2 := NewModerationCapture(c2.Writer)
	_, _ = capture2.WriteString(`{"choices":[{"message":{"content":"user 2 response"}}]}`)
	c2.Writer = capture2
	common.SetContextKey(c2, constant.ContextKeyModerationCapture, capture2)
	common.SetContextKey(c2, constant.ContextKeyModerationConversationID, convID)
	common.SetContextKey(c2, constant.ContextKeyModerationRequestContent, ModerationRequestContent{UserPrompt: "user 2 prompt"})
	info2 := &relaycommon.RelayInfo{
		UserId:          930002,
		RequestId:       "req-whitelist-user-2",
		OriginModelName: "gpt-4o",
		RelayFormat:     types.RelayFormatOpenAI,
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 1},
	}
	FinalizeModeration(c2, info2, nil)

	var count2 int64
	require.NoError(t, model.DB.Model(&model.ModerationTurn{}).Where("request_id = ?", "req-whitelist-user-2").Count(&count2).Error)
	assert.Equal(t, int64(0), count2, "whitelisted user 930002 should not be recorded")

	// 3. User 930003 NOT in whitelist -> SHOULD be recorded
	rec3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(rec3)
	c3.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c3.Writer.Header().Set("Content-Type", "application/json")
	capture3 := NewModerationCapture(c3.Writer)
	_, _ = capture3.WriteString(`{"choices":[{"message":{"content":"user 3 response"}}]}`)
	c3.Writer = capture3
	common.SetContextKey(c3, constant.ContextKeyModerationCapture, capture3)
	common.SetContextKey(c3, constant.ContextKeyModerationConversationID, convID)
	common.SetContextKey(c3, constant.ContextKeyModerationRequestContent, ModerationRequestContent{UserPrompt: "user 3 prompt"})
	info3 := &relaycommon.RelayInfo{
		UserId:          930003,
		RequestId:       "req-non-whitelist-user-3",
		OriginModelName: "gpt-4o",
		RelayFormat:     types.RelayFormatOpenAI,
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 1},
	}
	FinalizeModeration(c3, info3, nil)

	var count3 int64
	require.NoError(t, model.DB.Model(&model.ModerationTurn{}).Where("request_id = ?", "req-non-whitelist-user-3").Count(&count3).Error)
	assert.Equal(t, int64(1), count3, "non-whitelisted user 930003 should be recorded")
}

func TestModerationViolationRetentionWindow(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(
		&model.ModerationConversation{},
		&model.ModerationTurn{},
		&model.ModerationJob{},
		&model.ModerationViolation{},
		&model.ModerationNotification{},
		&model.ModerationTokenState{},
		&model.ModerationAccountState{},
		&model.ModerationAction{},
	))

	userID := 940001
	now := time.Now().Unix()

	// Create a violation within 7 days (created 3 days ago)
	recentViolation := model.ModerationViolation{
		UserID:         userID,
		ConversationID: "conv-recent",
		Actor:          "user",
		UserViolation:  true,
		Decision:       "block",
		Severity:       "high",
		Status:         model.ModerationViolationActive,
		CreatedAt:      now - 3*24*3600,
		ExpiresAt:      now + 4*24*3600,
	}
	// Create a violation older than 7 days (created 10 days ago)
	oldViolation := model.ModerationViolation{
		UserID:         userID,
		ConversationID: "conv-old",
		Actor:          "user",
		UserViolation:  true,
		Decision:       "block",
		Severity:       "high",
		Status:         model.ModerationViolationActive,
		CreatedAt:      now - 10*24*3600,
		ExpiresAt:      now - 3*24*3600,
	}

	require.NoError(t, model.DB.Create(&recentViolation).Error)
	require.NoError(t, model.DB.Create(&oldViolation).Error)
	t.Cleanup(func() {
		_ = model.DB.Where("user_id = ?", userID).Delete(&model.ModerationViolation{}).Error
	})

	// The active retention configuration is authoritative even for rows whose
	// original expiry was written under a longer configuration.
	staleConversation := &model.ModerationConversation{
		UserID:          userID,
		ConversationID:  "conv-stale-retention",
		Status:          model.ModerationConversationActive,
		FirstActivityAt: now - 2*24*3600,
		LastActivityAt:  now - 2*24*3600,
		ExpiresAt:       now + 4*24*3600,
	}
	require.NoError(t, model.DB.Create(staleConversation).Error)
	staleTurn := &model.ModerationTurn{
		ConversationID:  staleConversation.ID,
		UserID:          userID,
		ConversationKey: staleConversation.ConversationID,
		RoundNumber:     1,
		ResponseStatus:  "success",
		CreatedAt:       now - 2*24*3600,
		ExpiresAt:       now + 4*24*3600,
	}
	require.NoError(t, model.DB.Create(staleTurn).Error)
	require.NoError(t, model.DeleteExpiredModerationContent(now, 24*3600))
	var staleConversationCount int64
	require.NoError(t, model.DB.Model(&model.ModerationConversation{}).Where("id = ?", staleConversation.ID).Count(&staleConversationCount).Error)
	assert.Zero(t, staleConversationCount)
	var staleTurnCount int64
	require.NoError(t, model.DB.Model(&model.ModerationTurn{}).Where("id = ?", staleTurn.ID).Count(&staleTurnCount).Error)
	assert.Zero(t, staleTurnCount)

	// Default cutoff (7 days ago) should only count the recent one
	count, err := model.CountRecentUserModerationViolationsWithTx(model.DB, userID, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "7-day window should count only violations within the last 7 days")

	// DeleteExpiredModerationMetadata with 7 days retention
	// Create an old notification (created 8 days ago) and a recent one (created 2 days ago)
	oldNotification := model.ModerationNotification{
		ViolationID: oldViolation.ID,
		AlertType:   "violation",
		Recipient:   "admin@test.com",
		DedupeKey:   fmt.Sprintf("dedupe-old-%d", now),
		Status:      model.ModerationNotificationSent,
		CreatedAt:   now - 8*24*3600,
	}
	recentNotification := model.ModerationNotification{
		ViolationID: recentViolation.ID,
		AlertType:   "violation",
		Recipient:   "admin@test.com",
		DedupeKey:   fmt.Sprintf("dedupe-recent-%d", now),
		Status:      model.ModerationNotificationSent,
		CreatedAt:   now - 2*24*3600,
	}
	require.NoError(t, model.DB.Create(&oldNotification).Error)
	require.NoError(t, model.DB.Create(&recentNotification).Error)
	t.Cleanup(func() {
		_ = model.DB.Where("id IN (?)", []int64{oldNotification.ID, recentNotification.ID}).Delete(&model.ModerationNotification{}).Error
	})

	require.NoError(t, model.DeleteExpiredModerationMetadata(now, 7*24*3600))

	var notifCount int64
	require.NoError(t, model.DB.Model(&model.ModerationNotification{}).Where("id = ?", oldNotification.ID).Count(&notifCount).Error)
	assert.Equal(t, int64(0), notifCount, "notification older than 7 days should be deleted")

	require.NoError(t, model.DB.Model(&model.ModerationNotification{}).Where("id = ?", recentNotification.ID).Count(&notifCount).Error)
	assert.Equal(t, int64(1), notifCount, "recent notification should be kept")
}

func TestDeleteEncryptedModerationRecordsAutomaticallyDeletesEncryptedHistory(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(
		&model.ModerationConversation{},
		&model.ModerationTurn{},
		&model.ModerationJob{},
		&model.ModerationViolation{},
	))

	now := time.Now().Unix()
	encConvKey := "enc-conv-key"
	encConv := &model.ModerationConversation{
		UserID:          888801,
		ConversationID:  encConvKey,
		Status:          model.ModerationConversationActive,
		FirstActivityAt: now,
		LastActivityAt:  now,
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.DB.Create(encConv).Error)

	encTurn := &model.ModerationTurn{
		ConversationID:  encConv.ID,
		UserID:          encConv.UserID,
		ConversationKey: encConvKey,
		RoundNumber:     1,
		SystemPrompt:    "enc:v1:encrypted_system_prompt",
		UserPrompt:      "enc:v1:encrypted_user_prompt",
		AssistantReply:  "enc:v1:encrypted_assistant_reply",
		ResponseStatus:  "success",
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.DB.Create(encTurn).Error)

	encViolation := &model.ModerationViolation{
		UserID:         encConv.UserID,
		ConversationID: encConvKey,
		TurnID:         encTurn.ID,
		Actor:          "user",
		Status:         model.ModerationViolationActive,
		CreatedAt:      now,
		ExpiresAt:      now + 3600,
	}
	require.NoError(t, model.DB.Create(encViolation).Error)

	encJob := &model.ModerationJob{
		TurnID:          encTurn.ID,
		ConversationID:  encConv.ID,
		UserID:          encConv.UserID,
		Status:          model.ModerationJobSuccess,
		RequestPayload:  "enc:v1:encrypted_request",
		ResponsePayload: "enc:v1:encrypted_response",
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.DB.Create(encJob).Error)

	// Also create a normal plaintext record to ensure it is NOT deleted
	plainConvKey := "plain-conv-key"
	plainConv := &model.ModerationConversation{
		UserID:          888802,
		ConversationID:  plainConvKey,
		Status:          model.ModerationConversationActive,
		FirstActivityAt: now,
		LastActivityAt:  now,
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.DB.Create(plainConv).Error)
	plainTurn := &model.ModerationTurn{
		ConversationID:  plainConv.ID,
		UserID:          plainConv.UserID,
		ConversationKey: plainConvKey,
		RoundNumber:     1,
		SystemPrompt:    "plain system prompt",
		UserPrompt:      "plain user prompt",
		AssistantReply:  "plain assistant reply",
		ResponseStatus:  "success",
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.DB.Create(plainTurn).Error)
	plainViolation := &model.ModerationViolation{
		UserID:         plainConv.UserID,
		ConversationID: plainConvKey,
		TurnID:         plainTurn.ID,
		Actor:          "user",
		Status:         model.ModerationViolationActive,
		CreatedAt:      now,
		ExpiresAt:      now + 3600,
	}
	require.NoError(t, model.DB.Create(plainViolation).Error)

	// Call DeleteEncryptedModerationRecords
	require.NoError(t, model.DeleteEncryptedModerationRecords(model.DB))

	// Encrypted records must be completely deleted
	var encConvCount int64
	require.NoError(t, model.DB.Model(&model.ModerationConversation{}).Where("id = ?", encConv.ID).Count(&encConvCount).Error)
	assert.Zero(t, encConvCount, "encrypted conversation should be deleted")

	var encTurnCount int64
	require.NoError(t, model.DB.Model(&model.ModerationTurn{}).Where("id = ?", encTurn.ID).Count(&encTurnCount).Error)
	assert.Zero(t, encTurnCount, "encrypted turn should be deleted")

	var encViolationCount int64
	require.NoError(t, model.DB.Model(&model.ModerationViolation{}).Where("id = ?", encViolation.ID).Count(&encViolationCount).Error)
	assert.Zero(t, encViolationCount, "violation for encrypted turn should be deleted")

	var encJobCount int64
	require.NoError(t, model.DB.Model(&model.ModerationJob{}).Where("id = ?", encJob.ID).Count(&encJobCount).Error)
	assert.Zero(t, encJobCount, "encrypted job should be deleted")

	// Plaintext records must remain untouched
	var plainConvCount int64
	require.NoError(t, model.DB.Model(&model.ModerationConversation{}).Where("id = ?", plainConv.ID).Count(&plainConvCount).Error)
	assert.Equal(t, int64(1), plainConvCount, "plaintext conversation should remain")

	var plainTurnCount int64
	require.NoError(t, model.DB.Model(&model.ModerationTurn{}).Where("id = ?", plainTurn.ID).Count(&plainTurnCount).Error)
	assert.Equal(t, int64(1), plainTurnCount, "plaintext turn should remain")

	// Cleanup plaintext
	t.Cleanup(func() {
		_ = model.DB.Where("id = ?", plainConv.ID).Delete(&model.ModerationConversation{}).Error
		_ = model.DB.Where("id = ?", plainTurn.ID).Delete(&model.ModerationTurn{}).Error
		_ = model.DB.Where("id = ?", plainViolation.ID).Delete(&model.ModerationViolation{}).Error
	})
}

func TestUpdateUserRecordOnViolationUnarchives(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.ModerationUserRecord{}))

	userID := 987654
	now := time.Now().Unix()

	// Initial violation creates record
	updateUserRecordOnViolation(userID, now)
	var rec model.ModerationUserRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&rec).Error)
	assert.Equal(t, 1, rec.MaxViolationCount)
	assert.Equal(t, int64(0), rec.ArchivedAt)

	// Admin archives the record
	require.NoError(t, model.DB.Model(&rec).Update("archived_at", now).Error)
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&rec).Error)
	assert.Equal(t, now, rec.ArchivedAt)

	// New violation should unarchive the record
	updateUserRecordOnViolation(userID, now+100)
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&rec).Error)
	assert.Equal(t, 2, rec.MaxViolationCount)
	assert.Equal(t, int64(0), rec.ArchivedAt, "archived_at must be reset to 0 upon new violation")

	t.Cleanup(func() {
		_ = model.DB.Where("user_id = ?", userID).Delete(&model.ModerationUserRecord{}).Error
	})
}
