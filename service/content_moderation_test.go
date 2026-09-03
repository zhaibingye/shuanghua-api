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
		if index < 3 {
			assert.True(t, turn.ReviewRequired)
		}
	}
}

func TestReviewModerationTurnSupportsResponsesAndGeminiProviders(t *testing.T) {
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
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "review_data")
		writer.Header().Set("Content-Type", "application/json")
		decisionJSON := `{"decision":"allow","actor":"none","severity":"none","categories":[],"confidence":0,"reason_code":"safe"}`
		var response any
		if request.Header.Get("x-goog-api-key") != "" {
			assert.Equal(t, "gemini-key", request.Header.Get("x-goog-api-key"))
			response = map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": decisionJSON}}}}}}
		} else {
			assert.Equal(t, "Bearer responses-key", request.Header.Get("Authorization"))
			response = map[string]any{"output_text": decisionJSON}
		}
		responseBody, err := common.Marshal(response)
		require.NoError(t, err)
		_, err = writer.Write(responseBody)
		require.NoError(t, err)
	}))
	defer server.Close()
	httpClient = server.Client()
	ssrfProtectedHTTPClient = nil

	turn := &model.ModerationTurn{
		SystemPrompt:   model.ModerationText("system"),
		UserPrompt:     model.ModerationText("user"),
		AssistantReply: model.ModerationText("assistant"),
		ResponseStatus: "success",
	}
	responsesDecision, _, err := reviewModerationTurn(context.Background(), turn, setting.ContentModerationSetting{
		Provider:       "responses",
		BaseURL:        server.URL,
		APIKey:         "responses-key",
		Model:          "moderation-model",
		TimeoutSeconds: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, "allow", responsesDecision.Decision)

	geminiDecision, _, err := reviewModerationTurn(context.Background(), turn, setting.ContentModerationSetting{
		Provider:       "gemini",
		BaseURL:        server.URL,
		APIKey:         "gemini-key",
		Model:          "moderation-model",
		TimeoutSeconds: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, "allow", geminiDecision.Decision)
}

func TestApplyModerationDecisionNotifiesAssistantForMixedActorConfidence(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(
		&model.ModerationConversation{},
		&model.ModerationTurn{},
		&model.ModerationViolation{},
		&model.ModerationNotification{},
	))

	conversationKey := fmt.Sprintf("moderation-mixed-actor-%d", time.Now().UnixNano())
	now := time.Now().Unix()
	conversation := &model.ModerationConversation{
		UserID:          910001,
		ConversationID:  conversationKey,
		Status:          model.ModerationConversationActive,
		FirstActivityAt: now,
		LastActivityAt:  now,
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.DB.Create(conversation).Error)
	turn := &model.ModerationTurn{
		ConversationID:  conversation.ID,
		UserID:          conversation.UserID,
		ConversationKey: conversationKey,
		RoundNumber:     1,
		ResponseStatus:  "success",
		ExpiresAt:       now + 3600,
	}
	require.NoError(t, model.DB.Create(turn).Error)
	t.Cleanup(func() {
		violationIDs := model.DB.Model(&model.ModerationViolation{}).
			Select("id").Where("conversation_id = ?", conversationKey)
		require.NoError(t, model.DB.Where("violation_id IN (?)", violationIDs).Delete(&model.ModerationNotification{}).Error)
		require.NoError(t, model.DB.Where("conversation_id = ?", conversationKey).Delete(&model.ModerationViolation{}).Error)
		require.NoError(t, model.DB.Delete(turn).Error)
		require.NoError(t, model.DB.Delete(conversation).Error)
	})

	err := applyModerationDecision(context.Background(), turn, moderationDecision{
		Decision:   "block",
		Actor:      "both",
		Severity:   "high",
		Categories: []string{"violence"},
		Confidence: 0.8,
		ReasonCode: "assistant_risk",
	})
	require.NoError(t, err)
	var violations []model.ModerationViolation
	require.NoError(t, model.DB.Where("conversation_id = ?", conversationKey).Find(&violations).Error)
	require.Len(t, violations, 1)
	assert.False(t, violations[0].UserViolation)
	assert.Equal(t, "both", violations[0].Actor)
	var storedConversation model.ModerationConversation
	require.NoError(t, model.DB.First(&storedConversation, conversation.ID).Error)
	assert.Equal(t, model.ModerationConversationBlocked, storedConversation.Status)
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

func TestModerationReviewPlanAlwaysReviewsFirstThreeRounds(t *testing.T) {
	for round := 1; round <= 3; round++ {
		review, trigger := moderationReviewPlan(1, round, "", "ordinary request", "ordinary answer", "request-id")
		require.True(t, review)
		assert.Equal(t, "initial_rounds", trigger)
	}
}

func TestValidateModerationDecisionRejectsUnsafeConfidenceValues(t *testing.T) {
	decision := &moderationDecision{
		Decision:   "block",
		Actor:      "user",
		Severity:   "high",
		Confidence: 1.1,
	}
	assert.Error(t, validateModerationDecision(decision))
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

func TestReviewModerationTurnUsesCustomPolicyPrompt(t *testing.T) {
	var capturedInstructions string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, common.Unmarshal(body, &payload))
		if inst, ok := payload["instructions"].(string); ok {
			capturedInstructions = inst
		} else if sysInst, ok := payload["systemInstruction"].(map[string]any); ok {
			if parts, ok := sysInst["parts"].([]any); ok && len(parts) > 0 {
				if partMap, ok := parts[0].(map[string]any); ok {
					capturedInstructions, _ = partMap["text"].(string)
				}
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		decisionJSON := `{"decision":"allow","actor":"none","severity":"none","categories":[],"confidence":0,"reason_code":"safe"}`
		response := map[string]any{"output_text": decisionJSON}
		responseBody, err := common.Marshal(response)
		require.NoError(t, err)
		_, err = writer.Write(responseBody)
		require.NoError(t, err)
	}))
	defer server.Close()

	originalHTTPClient := httpClient
	t.Cleanup(func() {
		httpClient = originalHTTPClient
	})
	httpClient = server.Client()

	turn := &model.ModerationTurn{
		SystemPrompt:   model.ModerationText("system"),
		UserPrompt:     model.ModerationText("user"),
		AssistantReply: model.ModerationText("assistant"),
		ResponseStatus: "success",
	}

	// 1. Custom policy prompt
	customPrompt := "Custom moderation classifier instructions."
	_, _, err := reviewModerationTurn(context.Background(), turn, setting.ContentModerationSetting{
		Provider:       "responses",
		BaseURL:        server.URL,
		APIKey:         "responses-key",
		Model:          "moderation-model",
		PolicyPrompt:   customPrompt,
		TimeoutSeconds: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, customPrompt, capturedInstructions)

	// 2. Empty policy prompt falls back to DefaultContentModerationPolicyPrompt
	_, _, err = reviewModerationTurn(context.Background(), turn, setting.ContentModerationSetting{
		Provider:       "responses",
		BaseURL:        server.URL,
		APIKey:         "responses-key",
		Model:          "moderation-model",
		PolicyPrompt:   "",
		TimeoutSeconds: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, setting.DefaultContentModerationPolicyPrompt, capturedInstructions)
}
