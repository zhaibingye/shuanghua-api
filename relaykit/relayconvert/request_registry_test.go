package relayconvert

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	sharedgemini "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/shared/gemini"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesConversionRejectsInvalidInputShape(t *testing.T) {
	for _, input := range []string{`{"role":"user","content":"hello"}`, `42`, `true`, `["hello"]`, `[null]`} {
		t.Run(input, func(t *testing.T) {
			for _, target := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude, types.RelayFormatGemini} {
				_, err := ConvertRequest(t.Context(), nil, target, &dto.OpenAIResponsesRequest{
					Model: "test-model", Input: []byte(input), MaxOutputTokens: lo.ToPtr(uint(1024)),
				})
				require.ErrorContains(t, err, "input", target)
			}
		})
	}
}

func TestConvertRequestOmitsUnreplayableHistoricalThinking(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request any
		raw     string
		targets []types.RelayFormat
	}{
		{
			name: "chat reasoning with tool continuation", request: &dto.GeneralOpenAIRequest{},
			raw: `{"model":"claude-sonnet-4-5","max_tokens":2048,"messages":[
				{"role":"user","content":"Check the weather"},
				{"role":"assistant","reasoning_content":"Look it up","content":"Checking","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"call_1","content":"sunny"}]}`,
			targets: []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAIResponses},
		},
		{
			name: "responses summary with tool continuation", request: &dto.OpenAIResponsesRequest{},
			raw: `{"model":"claude-sonnet-4-5","max_output_tokens":2048,"input":[
				{"role":"user","content":"Check the weather"},
				{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Look it up"}]},
				{"role":"assistant","content":"Checking"},
				{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_1","output":"sunny"}]}`,
			targets: []types.RelayFormat{types.RelayFormatClaude},
		},
		{
			name: "responses encrypted state without summary", request: &dto.OpenAIResponsesRequest{},
			raw: `{"model":"claude-sonnet-4-5","max_output_tokens":2048,"input":[
				{"role":"user","content":"Check the weather"},
				{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"},
				{"role":"assistant","content":"Checking"},
				{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_1","output":"sunny"}]}`,
			targets: []types.RelayFormat{types.RelayFormatClaude},
		},
		{
			name: "gemini thought with tool continuation", request: &dto.GeminiChatRequest{},
			raw: `{"generationConfig":{"maxOutputTokens":2048},"contents":[
				{"role":"user","parts":[{"text":"Check the weather"}]},
				{"role":"model","parts":[{"text":"Look it up","thought":true,"thoughtSignature":"opaque"},{"text":"Checking"},{"functionCall":{"id":"call_1","name":"lookup","args":{}}}]},
				{"role":"user","parts":[{"functionResponse":{"id":"call_1","name":"lookup","response":{"content":"sunny"}}}]}]}`,
			targets: []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAIResponses},
		},
		{
			name: "claude signed and redacted state to responses", request: &dto.ClaudeRequest{},
			raw: `{"model":"gpt-test","max_tokens":2048,"messages":[
				{"role":"user","content":"Check the weather"},
				{"role":"assistant","content":[{"type":"thinking","thinking":"Look it up","signature":"original-signature"},{"type":"redacted_thinking","data":"opaque"},{"type":"text","text":"Checking"},{"type":"tool_use","id":"call_1","name":"lookup","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"sunny"}]}]}`,
			targets: []types.RelayFormat{types.RelayFormatOpenAIResponses},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, kitutil.UnmarshalJsonStr(tc.raw, tc.request))
			before, err := kitutil.Marshal(tc.request)
			require.NoError(t, err)
			for _, target := range tc.targets {
				t.Run(string(target), func(t *testing.T) {
					info := &convmeta.Values{ChannelMetaAttached: true, UpstreamModelName: "claude-sonnet-4-5"}
					result, err := ConvertRequest(t.Context(), info, target, tc.request)
					require.NoError(t, err)
					var codes []string
					for _, diagnostic := range result.Diagnostics {
						codes = append(codes, diagnostic.Code)
					}
					assert.Contains(t, codes, "historical_thinking_omitted")
					var text, callID, resultID string
					switch request := result.Value.(type) {
					case *dto.ClaudeRequest:
						for _, message := range request.Messages {
							content, err := message.ParseContent()
							require.NoError(t, err)
							require.NotEmpty(t, content)
							for _, part := range content {
								assert.NotContains(t, []string{"thinking", "redacted_thinking"}, part.Type)
								if part.Type == "text" && message.Role == "assistant" {
									text += lo.FromPtr(part.Text)
								}
								if part.Type == "tool_use" {
									callID = part.Id
								}
								if part.Type == "tool_result" {
									resultID = part.ToolUseId
								}
							}
						}
					case *dto.OpenAIResponsesRequest:
						var items []map[string]any
						require.NoError(t, kitutil.Unmarshal(request.Input, &items))
						for _, item := range items {
							assert.NotEqual(t, "reasoning", item["type"])
							switch item["type"] {
							case "message":
								if item["role"] == "assistant" {
									text += inputContentText(t, item)
								}
							case "function_call":
								callID = item["call_id"].(string)
							case "function_call_output":
								resultID = item["call_id"].(string)
							}
						}
					default:
						t.Fatalf("unexpected request %T", result.Value)
					}
					assert.Equal(t, "Checking", text)
					assert.Equal(t, "call_1", callID)
					assert.Equal(t, callID, resultID)
				})
			}
			after, err := kitutil.Marshal(tc.request)
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(after))
		})
	}
}

func TestClaudeMessagesToOpenAIChatPreservesAssistantTextWithToolCalls(t *testing.T) {
	text := "I will look that up."
	request := dto.ClaudeRequest{
		Model: "claude-test",
		Messages: []dto.ClaudeMessage{
			{
				Role: "assistant",
				Content: []dto.ClaudeMediaMessage{
					{Type: "text", Text: &text},
					{
						Type:  "tool_use",
						Id:    "call_1",
						Name:  "lookup",
						Input: map[string]any{"query": "weather"},
					},
				},
			},
		},
	}

	result, err := ConvertRequest(
		context.Background(),
		&convmeta.Values{},
		types.RelayFormatOpenAI,
		&request,
	)
	require.NoError(t, err)

	converted, ok := result.Value.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Len(t, converted.Messages, 1)

	assistant := converted.Messages[0]
	assert.Equal(t, "assistant", assistant.Role)
	toolCalls := assistant.ParseToolCalls()
	require.Len(t, toolCalls, 1)
	assert.Equal(t, "call_1", toolCalls[0].ID)
	require.Len(t, assistant.ParseContent(), 1)
	assert.Equal(t, "text", assistant.ParseContent()[0].Type)
	assert.Equal(t, text, assistant.ParseContent()[0].Text)
}

func TestClaudeMessagesToOpenAIChatKeepsToolOnlyContentEmpty(t *testing.T) {
	request := dto.ClaudeRequest{
		Model: "claude-test",
		Messages: []dto.ClaudeMessage{
			{
				Role: "assistant",
				Content: []dto.ClaudeMediaMessage{
					{
						Type:  "tool_use",
						Id:    "call_1",
						Name:  "lookup",
						Input: map[string]any{"query": "weather"},
					},
				},
			},
		},
	}

	result, err := ConvertRequest(
		context.Background(),
		&convmeta.Values{},
		types.RelayFormatOpenAI,
		&request,
	)
	require.NoError(t, err)

	converted, ok := result.Value.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Len(t, converted.Messages, 1)

	assistant := converted.Messages[0]
	assert.Empty(t, assistant.ParseContent())
	toolCalls := assistant.ParseToolCalls()
	require.Len(t, toolCalls, 1)
	assert.Equal(t, "call_1", toolCalls[0].ID)
}

func TestRequestConverterRegistryListsSupportedTextConverters(t *testing.T) {
	tests := []struct {
		converter      string
		from           types.RelayFormat
		to             types.RelayFormat
		quality        RequestConverterQuality
		advancedCustom bool
	}{
		{converter: ConverterClaudeMessagesToOpenAIChat, from: types.RelayFormatClaude, to: types.RelayFormatOpenAI, quality: RequestConverterQualityFair, advancedCustom: true},
		{converter: ConverterGeminiContentToOpenAIChat, from: types.RelayFormatGemini, to: types.RelayFormatOpenAI, quality: RequestConverterQualityFair, advancedCustom: true},
		{converter: ConverterOpenAIChatToClaudeMessages, from: types.RelayFormatOpenAI, to: types.RelayFormatClaude, quality: RequestConverterQualityFair, advancedCustom: true},
		{converter: ConverterOpenAIChatToGeminiContent, from: types.RelayFormatOpenAI, to: types.RelayFormatGemini, quality: RequestConverterQualityFair, advancedCustom: true},
		{converter: ConverterOpenAIChatToOpenAIResponses, from: types.RelayFormatOpenAI, to: types.RelayFormatOpenAIResponses, quality: RequestConverterQualityGood, advancedCustom: true},
		{converter: ConverterOpenAIResponsesToOpenAIChat, from: types.RelayFormatOpenAIResponses, to: types.RelayFormatOpenAI, quality: RequestConverterQualityGood, advancedCustom: true},
		{
			converter:      requestConverterClaudeToGemini,
			from:           types.RelayFormatClaude,
			to:             types.RelayFormatGemini,
			quality:        RequestConverterQualityFair,
			advancedCustom: true,
		},
		{
			converter:      requestConverterClaudeToResponses,
			from:           types.RelayFormatClaude,
			to:             types.RelayFormatOpenAIResponses,
			quality:        RequestConverterQualityFair,
			advancedCustom: true,
		},
		{
			converter:      requestConverterGeminiToClaude,
			from:           types.RelayFormatGemini,
			to:             types.RelayFormatClaude,
			quality:        RequestConverterQualityFair,
			advancedCustom: true,
		},
		{
			converter:      requestConverterGeminiToResponses,
			from:           types.RelayFormatGemini,
			to:             types.RelayFormatOpenAIResponses,
			quality:        RequestConverterQualityFair,
			advancedCustom: true,
		},
		{
			converter:      requestConverterResponsesToClaude,
			from:           types.RelayFormatOpenAIResponses,
			to:             types.RelayFormatClaude,
			quality:        RequestConverterQualityFair,
			advancedCustom: true,
		},
		{
			converter:      ConverterOpenAIResponsesToGemini,
			from:           types.RelayFormatOpenAIResponses,
			to:             types.RelayFormatGemini,
			quality:        RequestConverterQualityFair,
			advancedCustom: true,
		},
	}

	require.Len(t, requestConverters, len(tests))

	for _, tt := range tests {
		t.Run(tt.converter, func(t *testing.T) {
			spec, ok := LookupRequestConverter(tt.converter)

			require.True(t, ok)
			assert.Equal(t, tt.converter, spec.ID)
			assert.Equal(t, tt.from, spec.From)
			assert.Equal(t, tt.to, spec.To)
			assert.Equal(t, tt.quality, spec.Quality)
			assert.Empty(t, spec.StepConverters)
			assert.NotNil(t, spec.Convert)
			assert.Equal(t, tt.advancedCustom, dto.IsAdvancedCustomConverterAllowed(tt.converter))
		})
	}
}

func TestConvertRequestToTargetRecordsConversionChain(t *testing.T) {
	info := &convmeta.Values{
		ConversionChain: []types.RelayFormat{types.RelayFormatOpenAI},
	}
	req := &dto.GeneralOpenAIRequest{
		Model: "gpt-test",
		Messages: []dto.Message{
			{Role: "user", Content: "hello"},
		},
	}

	result, err := ConvertRequest(nil, info, types.RelayFormatOpenAIResponses, req)

	require.NoError(t, err)
	require.IsType(t, &dto.OpenAIResponsesRequest{}, result.Value)
	assert.Equal(t, types.RelayFormatOpenAI, result.From)
	assert.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), result.To)
	assert.Equal(t, ConverterOpenAIChatToOpenAIResponses, result.Converter)
	assert.Equal(t, RequestConverterQualityGood, result.Quality)
	assert.Equal(t, []RequestStep{
		{
			Converter: ConverterOpenAIChatToOpenAIResponses,
			From:      types.RelayFormatOpenAI,
			To:        types.RelayFormatOpenAIResponses,
		},
	}, result.Steps)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses}, info.ConversionChain)
}

func TestConvertRequestClaudeToResponsesUsesDirectPath(t *testing.T) {
	info := &convmeta.Values{
		ConversionChain: []types.RelayFormat{types.RelayFormatClaude},
	}
	req := &dto.ClaudeRequest{
		Model: "claude-test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}

	result, err := ConvertRequest(nil, info, types.RelayFormatOpenAIResponses, req)

	require.NoError(t, err)
	require.IsType(t, &dto.OpenAIResponsesRequest{}, result.Value)
	assert.Equal(t, types.RelayFormat(types.RelayFormatClaude), result.From)
	assert.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), result.To)
	assert.Equal(t, requestConverterClaudeToResponses, result.Converter)
	assert.Equal(t, RequestConverterQualityFair, result.Quality)
	assert.Equal(t, []RequestStep{
		{
			Converter: requestConverterClaudeToResponses,
			From:      types.RelayFormatClaude,
			To:        types.RelayFormatOpenAIResponses,
		},
	}, result.Steps)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAIResponses}, info.ConversionChain)
}

func TestConvertRequestClaudeToChatResolvesToolResultNames(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model: "claude-test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_result", ToolUseId: "call_1", Content: "before call"},
			}},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_use", Id: "call_1", Name: "first", Input: map[string]any{}},
			}},
			{Role: "user", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_result", ToolUseId: "call_1", Content: "after call"},
				{Type: "tool_result", ToolUseId: "missing", Content: "unknown"},
				{Type: "tool_result", ToolUseId: "call_1", Name: "explicit", Content: "named"},
			}},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_use", Id: "call_1", Name: "later", Input: map[string]any{}},
			}},
		},
	}

	result, err := ConvertRequestByID(nil, nil, ConverterClaudeMessagesToOpenAIChat, req)
	require.NoError(t, err)
	chatReq, ok := result.Value.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Len(t, chatReq.Messages, 6)
	for _, tt := range []struct {
		index int
		id    string
		name  string
	}{
		{0, "call_1", "first"},
		{2, "call_1", "first"},
		{3, "missing", ""},
		{4, "call_1", "explicit"},
	} {
		message := chatReq.Messages[tt.index]
		assert.Equal(t, "tool", message.Role)
		assert.Equal(t, tt.id, message.ToolCallId)
		require.NotNil(t, message.Name)
		assert.Equal(t, tt.name, *message.Name)
	}
	assert.Equal(t, "assistant", chatReq.Messages[1].Role)
	assert.Equal(t, "assistant", chatReq.Messages[5].Role)
}

func TestConvertRequestClaudeToResponsesPreservesMixedBlockOrder(t *testing.T) {
	info := &convmeta.Values{ConversionChain: []types.RelayFormat{types.RelayFormatClaude}}
	stream := true
	strict := true
	maxTokens := uint(4096)
	req := &dto.ClaudeRequest{
		Model:     "gpt-test",
		System:    []dto.ClaudeMediaMessage{{Type: "text", Text: kitutil.GetPointer("system ")}, {Type: "text", Text: kitutil.GetPointer("rules")}},
		MaxTokens: &maxTokens,
		Stream:    &stream,
		Tools: []dto.Tool{{
			Name:        "lookup",
			Description: "Look up a value",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
			Strict:      &strict,
		}},
		ToolChoice: dto.ClaudeToolChoice{Type: "tool", Name: "lookup", DisableParallelToolUse: true},
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: []dto.ClaudeMediaMessage{{Type: "text", Text: kitutil.GetPointer("question")}}},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{
				{Type: "text", Text: kitutil.GetPointer("before")},
				{Type: "tool_use", Id: "call_1", Name: "lookup", Input: map[string]any{"q": "x"}},
				{Type: "text", Text: kitutil.GetPointer("after")},
			}},
			{Role: "user", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_result", ToolUseId: "call_1", Content: "result"},
				{Type: "text", Text: kitutil.GetPointer("continue")},
			}},
		},
	}

	result, err := ConvertRequest(nil, info, types.RelayFormatOpenAIResponses, req)
	require.NoError(t, err)
	responsesReq := result.Value.(*dto.OpenAIResponsesRequest)
	assert.Equal(t, "gpt-test", responsesReq.Model)
	assert.Equal(t, maxTokens, *responsesReq.MaxOutputTokens)
	assert.True(t, *responsesReq.Stream)
	assert.JSONEq(t, `"system rules"`, string(responsesReq.Instructions))
	assert.JSONEq(t, `[{"type":"function","name":"lookup","description":"Look up a value","parameters":{"type":"object","properties":{"q":{"type":"string"}}},"strict":true}]`, string(responsesReq.Tools))
	assert.JSONEq(t, `{"type":"function","name":"lookup"}`, string(responsesReq.ToolChoice))
	assert.JSONEq(t, `false`, string(responsesReq.ParallelToolCalls))

	var input []map[string]any
	require.NoError(t, kitutil.Unmarshal(responsesReq.Input, &input))
	require.Len(t, input, 6)
	assert.Equal(t, "user", input[0]["role"])
	assert.Equal(t, "question", inputContentText(t, input[0]))
	assert.Equal(t, "assistant", input[1]["role"])
	assert.Equal(t, "before", inputContentText(t, input[1]))
	assert.Equal(t, "function_call", input[2]["type"])
	assert.Equal(t, "call_1", input[2]["call_id"])
	assert.Equal(t, "lookup", input[2]["name"])
	assert.JSONEq(t, `{"q":"x"}`, input[2]["arguments"].(string))
	assert.Equal(t, "assistant", input[3]["role"])
	assert.Equal(t, "after", inputContentText(t, input[3]))
	assert.Equal(t, "function_call_output", input[4]["type"])
	assert.Equal(t, "result", input[4]["output"])
	assert.Equal(t, "user", input[5]["role"])
	assert.Equal(t, "continue", inputContentText(t, input[5]))
}

func TestConvertRequestChatToResponsesPreservesImageDetail(t *testing.T) {
	for _, tc := range []struct {
		detail string
		want   string
	}{
		{"", "auto"}, {"auto", "auto"}, {"low", "low"}, {"high", "high"},
		{"original", "original"}, {" HIGH ", "high"}, {"bogus", "auto"},
	} {
		t.Run(tc.detail, func(t *testing.T) {
			request := &dto.GeneralOpenAIRequest{
				Model: "gpt-test",
				Messages: []dto.Message{{Role: "user", Content: []any{
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/a.png", "detail": tc.detail}},
				}}},
			}
			result, err := ConvertRequestByID(context.Background(), nil, ConverterOpenAIChatToOpenAIResponses, request)
			require.NoError(t, err)
			response, ok := result.Value.(*dto.OpenAIResponsesRequest)
			require.True(t, ok)
			var items []struct {
				Content []struct {
					Type     string `json:"type"`
					ImageURL string `json:"image_url"`
					Detail   string `json:"detail"`
				} `json:"content"`
			}
			require.NoError(t, kitutil.Unmarshal(response.Input, &items))
			require.Len(t, items, 1)
			require.Len(t, items[0].Content, 1)
			assert.Equal(t, "input_image", items[0].Content[0].Type)
			assert.Equal(t, "https://example.test/a.png", items[0].Content[0].ImageURL)
			assert.Equal(t, tc.want, items[0].Content[0].Detail)
		})
	}
}

func TestConvertRequestClaudeToResponsesDefaultsImageDetail(t *testing.T) {
	const imageData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+Xoy8AAAAASUVORK5CYII="
	req := &dto.ClaudeRequest{
		Model: "gpt-test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: []dto.ClaudeMediaMessage{{
				Type:   "image",
				Source: &dto.ClaudeMessageSource{Type: "url", Url: "https://example.test/a.png"},
			}}},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{{
				Type: "tool_use", Id: "call_1", Name: "screenshot", Input: map[string]any{},
			}}},
			{Role: "user", Content: []dto.ClaudeMediaMessage{{
				Type: "tool_result", ToolUseId: "call_1",
				Content: []dto.ClaudeMediaMessage{{
					Type:   "image",
					Source: &dto.ClaudeMessageSource{Type: "base64", MediaType: "image/png", Data: imageData},
				}},
			}}},
		},
	}

	result, err := ConvertRequestByID(context.Background(), nil, ConverterClaudeMessagesToOpenAIResponses, req)
	require.NoError(t, err)
	responsesReq, ok := result.Value.(*dto.OpenAIResponsesRequest)
	require.True(t, ok)
	assert.JSONEq(t, `[
		{"type":"message","role":"user","content":[
			{"type":"input_image","image_url":"https://example.test/a.png","detail":"auto"}
		]},
		{"type":"function_call","id":"fc_call_1","call_id":"call_1","name":"screenshot","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_1","output":[
			{"type":"input_image","image_url":"data:image/png;base64,`+imageData+`","detail":"auto"}
		]}
	]`, string(responsesReq.Input))
}

func TestConvertRequestClaudeToResponsesDropsIncompatibleContextManagement(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model: "gpt-test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
		ContextManagement: mustRawMessage(t, map[string]any{
			"edits": []map[string]any{{"type": "clear_tool_uses_20250919"}},
		}),
	}

	result, err := ConvertRequest(nil, nil, types.RelayFormatOpenAIResponses, req)

	require.NoError(t, err)
	responsesReq, ok := result.Value.(*dto.OpenAIResponsesRequest)
	require.True(t, ok)
	assert.Empty(t, responsesReq.ContextManagement)
}

func TestConvertRequestClaudeAdaptiveThinkingPreservesEffort(t *testing.T) {
	tests := []struct {
		name         string
		outputConfig []byte
		wantEffort   string
	}{
		{name: "adaptive default", wantEffort: "high"},
		{name: "explicit low", outputConfig: mustRawMessage(t, map[string]any{"effort": "low"}), wantEffort: "low"},
		{name: "explicit xhigh", outputConfig: mustRawMessage(t, map[string]any{"effort": "xhigh"}), wantEffort: "xhigh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &convmeta.Values{
				OriginModelName: "gpt-5.6-sol",
				ConversionChain: []types.RelayFormat{types.RelayFormatClaude},
			}
			req := &dto.ClaudeRequest{
				Model:        "gpt-5.6-sol",
				OutputConfig: tt.outputConfig,
				Thinking:     &dto.Thinking{Type: "adaptive", Display: "summarized"},
				Messages: []dto.ClaudeMessage{
					{Role: "user", Content: "hello"},
				},
			}

			result, err := ConvertRequest(nil, info, types.RelayFormatOpenAIResponses, req)

			require.NoError(t, err)
			responsesReq, ok := result.Value.(*dto.OpenAIResponsesRequest)
			require.True(t, ok)
			require.NotNil(t, responsesReq.Reasoning)
			assert.Equal(t, tt.wantEffort, responsesReq.Reasoning.Effort)
			assert.Equal(t, "detailed", responsesReq.Reasoning.Summary)
			assert.Equal(t, tt.wantEffort, info.GetReasoningEffort())
		})
	}
}

func TestConvertRequestReasoningDefaultsAndEquivalentLevels(t *testing.T) {
	formats := []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, types.RelayFormatClaude, types.RelayFormatGemini}
	for _, source := range formats {
		for _, target := range formats {
			for _, effort := range []string{"", "minimal", "low", "medium", "high", "xhigh", "max"} {
				if source == target {
					continue
				}
				t.Run(string(source)+"/"+string(target)+"/"+effort, func(t *testing.T) {
					model := "proxy-model"
					if target == types.RelayFormatClaude {
						model = "claude-sonnet-4-6"
					}
					if target == types.RelayFormatGemini {
						model = "gemini-3-pro-preview"
					}
					req := reasoningConversionRequest(t, source, model, effort)
					before, err := kitutil.Marshal(req)
					require.NoError(t, err)
					// These source models have different native defaults. Neither
					// may override the format converter's default when fields are absent.
					origin := "claude-opus-5-5"
					if source == types.RelayFormatGemini {
						origin = "gemini-2.5-flash-lite"
					}
					info := &convmeta.Values{OriginModelName: origin, UpstreamModelName: model, ChannelMetaAttached: true}
					result, err := ConvertRequest(t.Context(), info, target, req)
					require.NoError(t, err)
					want := effort
					if want == "" {
						want = "xhigh"
					}
					if target == types.RelayFormatGemini && (want == "xhigh" || want == "max") {
						want = "high"
					}
					assertConvertedReasoningEffort(t, result.Value, want)
					assert.Equal(t, want, info.GetReasoningEffort())
					after, err := kitutil.Marshal(req)
					require.NoError(t, err)
					assert.JSONEq(t, string(before), string(after))
				})
			}
		}
	}
}

func TestConvertRequestReasoningSurvivesIntermediateFormats(t *testing.T) {
	for _, tc := range []struct {
		source types.RelayFormat
		path   []types.RelayFormat
		effort string
		want   string
	}{
		{types.RelayFormatOpenAI, []types.RelayFormat{types.RelayFormatGemini, types.RelayFormatOpenAIResponses}, "", "xhigh"},
		{types.RelayFormatOpenAIResponses, []types.RelayFormat{types.RelayFormatGemini, types.RelayFormatClaude}, "", "xhigh"},
		{types.RelayFormatGemini, []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAIResponses}, "", "xhigh"},
		{types.RelayFormatClaude, []types.RelayFormat{types.RelayFormatGemini, types.RelayFormatOpenAI}, "", "xhigh"},
		{types.RelayFormatOpenAI, []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatGemini}, "", "high"},
		{types.RelayFormatOpenAI, []types.RelayFormat{types.RelayFormatGemini, types.RelayFormatOpenAIResponses}, "xhigh", "xhigh"},
		{types.RelayFormatOpenAIResponses, []types.RelayFormat{types.RelayFormatGemini, types.RelayFormatOpenAI}, "max", "max"},
		{types.RelayFormatClaude, []types.RelayFormat{types.RelayFormatGemini, types.RelayFormatOpenAIResponses}, "minimal", "minimal"},
	} {
		t.Run(string(tc.source)+"/"+string(tc.path[0])+"/"+string(tc.path[1])+"/"+tc.effort, func(t *testing.T) {
			req := reasoningConversionRequest(t, tc.source, "proxy-model", tc.effort)
			info := &convmeta.Values{UpstreamModelName: "gemini-2.5-pro", ChannelMetaAttached: true}
			result, err := ConvertRequestVia(t.Context(), info, req, tc.path...)
			require.NoError(t, err)
			if tc.path[len(tc.path)-1] == types.RelayFormatGemini {
				converted := result.Value.(*dto.GeminiChatRequest)
				require.NotNil(t, converted.GenerationConfig.ThinkingConfig)
				assert.Equal(t, lo.ToPtr(24576), converted.GenerationConfig.ThinkingConfig.ThinkingBudget)
			} else {
				assertConvertedReasoningEffort(t, result.Value, tc.want)
			}
			assert.Equal(t, tc.want, info.GetReasoningEffort())
		})
	}
}

func TestConvertRequestReasoningExplicitDisableAndBudget(t *testing.T) {
	for _, source := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, types.RelayFormatClaude, types.RelayFormatGemini} {
		for _, target := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, types.RelayFormatClaude, types.RelayFormatGemini} {
			if source == target {
				continue
			}
			t.Run(string(source)+"/"+string(target), func(t *testing.T) {
				info := &convmeta.Values{UpstreamModelName: "gemini-2.5-flash", ChannelMetaAttached: true}
				result, err := ConvertRequest(t.Context(), info, target, reasoningConversionRequest(t, source, "proxy-model", "none"))
				require.NoError(t, err)
				if target == types.RelayFormatGemini {
					converted := result.Value.(*dto.GeminiChatRequest)
					require.NotNil(t, converted.GenerationConfig.ThinkingConfig)
					assert.Equal(t, lo.ToPtr(0), converted.GenerationConfig.ThinkingConfig.ThinkingBudget)
				} else {
					assertConvertedReasoningEffort(t, result.Value, "none")
				}
				assert.Equal(t, "none", info.GetReasoningEffort())
			})
		}
	}
	t.Run("exact budget survives a level-based intermediate", func(t *testing.T) {
		req := reasoningConversionRequest(t, types.RelayFormatClaude, "proxy-model", "").(*dto.ClaudeRequest)
		req.Thinking = &dto.Thinking{Type: "enabled", BudgetTokens: lo.ToPtr(4096)}
		info := &convmeta.Values{UpstreamModelName: "gemini-3-flash-preview", ChannelMetaAttached: true}
		result, err := ConvertRequestVia(t.Context(), info, req, types.RelayFormatGemini, types.RelayFormatOpenAIResponses)
		require.NoError(t, err)
		converted := result.Value.(*dto.OpenAIResponsesRequest)
		assertConvertedReasoningEffort(t, converted, "xhigh")
		require.NotNil(t, converted.ReasoningConversion)
		assert.Equal(t, lo.ToPtr(4096), converted.ReasoningConversion.BudgetTokens)
	})
	for _, tc := range []struct {
		name, model, want string
		source, target    types.RelayFormat
		budget            int
		level             string
	}{
		{"Chat budget to Argon", "gemini-4-argon", "HIGH", types.RelayFormatOpenAI, types.RelayFormatGemini, 2048, ""},
		{"Responses budget to adaptive Claude", "claude-opus-4-7", "xhigh", types.RelayFormatOpenAIResponses, types.RelayFormatClaude, 2048, ""},
		{"Responses budget to Claude 4.6", "claude-sonnet-4-6", "high", types.RelayFormatOpenAIResponses, types.RelayFormatClaude, 2048, ""},
		{"Gemini level wins over zero", "gpt-5.4", "low", types.RelayFormatGemini, types.RelayFormatOpenAI, 0, "LOW"},
		{"Claude level wins over budget", "gpt-5.4", "medium", types.RelayFormatClaude, types.RelayFormatOpenAIResponses, 2048, "medium"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := reasoningConversionRequest(t, tc.source, tc.model, tc.level)
			budget := mustRawMessage(t, tc.budget)
			switch value := req.(type) {
			case *dto.GeneralOpenAIRequest:
				value.ThinkingBudget = budget
			case *dto.OpenAIResponsesRequest:
				value.ThinkingBudget = budget
			case *dto.GeminiChatRequest:
				value.GenerationConfig.ThinkingConfig.ThinkingBudget = &tc.budget
			case *dto.ClaudeRequest:
				value.Thinking.Type = "enabled"
				value.Thinking.BudgetTokens = &tc.budget
			}
			info := &convmeta.Values{UpstreamModelName: tc.model, ChannelMetaAttached: true}
			result, err := ConvertRequest(t.Context(), info, tc.target, req)
			require.NoError(t, err)
			assertConvertedReasoningEffort(t, result.Value, tc.want)
			wire, err := kitutil.Marshal(result.Value)
			require.NoError(t, err)
			assert.NotContains(t, string(wire), `"thinkingBudget"`)
			assert.NotContains(t, string(wire), `"budget_tokens"`)
			assert.NotContains(t, string(wire), `"thinking_budget"`)
		})
	}
	t.Run("converter ID uses format defaults", func(t *testing.T) {
		req := reasoningConversionRequest(t, types.RelayFormatOpenAI, "proxy-model", "")
		result, err := ConvertRequestByID(t.Context(), nil, ConverterOpenAIChatToClaudeMessages, req)
		require.NoError(t, err)
		assertConvertedReasoningEffort(t, result.Value, "xhigh")
	})
	t.Run("same protocol keeps absent controls", func(t *testing.T) {
		for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, types.RelayFormatClaude, types.RelayFormatGemini} {
			req := reasoningConversionRequest(t, format, "proxy-model", "")
			before, err := kitutil.Marshal(req)
			require.NoError(t, err)
			result, err := ConvertRequest(t.Context(), nil, format, req)
			require.NoError(t, err)
			assert.Same(t, req, result.Value)
			after, err := kitutil.Marshal(result.Value)
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(after))
		}
	})
	for _, effort := range []string{"", "xhigh", "max"} {
		t.Run("gemini budget mapping/"+effort, func(t *testing.T) {
			info := &convmeta.Values{UpstreamModelName: "gemini-2.5-pro", ChannelMetaAttached: true}
			result, err := ConvertRequest(t.Context(), info, types.RelayFormatGemini, reasoningConversionRequest(t, types.RelayFormatOpenAI, "gemini-2.5-pro", effort))
			require.NoError(t, err)
			converted := result.Value.(*dto.GeminiChatRequest)
			require.NotNil(t, converted.GenerationConfig.ThinkingConfig)
			assert.Equal(t, lo.ToPtr(24576), converted.GenerationConfig.ThinkingConfig.ThinkingBudget)
		})
	}
	for _, level := range []string{"medium", "xhigh", "max"} {
		t.Run("google extra body/"+level, func(t *testing.T) {
			req := reasoningConversionRequest(t, types.RelayFormatOpenAI, "proxy-model", "").(*dto.GeneralOpenAIRequest)
			req.ExtraBody = mustRawMessage(t, map[string]any{"google": map[string]any{"thinking_config": map[string]any{"thinking_level": level}}})
			info := &convmeta.Values{UpstreamModelName: "gemini-3-pro-preview", ChannelMetaAttached: true}
			result, err := ConvertRequest(t.Context(), info, types.RelayFormatGemini, req)
			require.NoError(t, err)
			want := level
			if level == "xhigh" || level == "max" {
				want = "high"
			}
			assertConvertedReasoningEffort(t, result.Value, want)
			for _, path := range [][]types.RelayFormat{{types.RelayFormatOpenAIResponses}, {types.RelayFormatGemini, types.RelayFormatOpenAIResponses}} {
				result, err := ConvertRequestVia(t.Context(), info, req, path...)
				require.NoError(t, err)
				assertConvertedReasoningEffort(t, result.Value, level)
			}
		})
	}
}

func reasoningConversionRequest(t *testing.T, format types.RelayFormat, model, effort string) any {
	t.Helper()
	switch format {
	case types.RelayFormatOpenAI:
		return &dto.GeneralOpenAIRequest{Model: model, MaxTokens: lo.ToPtr(uint(8192)), Messages: []dto.Message{{Role: "user", Content: "hello"}}, ReasoningEffort: effort}
	case types.RelayFormatOpenAIResponses:
		req := &dto.OpenAIResponsesRequest{Model: model, MaxOutputTokens: lo.ToPtr(uint(8192)), Input: []byte(`"hello"`)}
		if effort != "" {
			req.Reasoning = &dto.Reasoning{Effort: effort}
		}
		return req
	case types.RelayFormatClaude:
		req := &dto.ClaudeRequest{Model: model, MaxTokens: lo.ToPtr(uint(8192)), Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}}}
		if effort == "none" {
			req.Thinking = &dto.Thinking{Type: "disabled"}
		} else if effort != "" {
			req.Thinking = &dto.Thinking{Type: "adaptive"}
			req.OutputConfig = mustRawMessage(t, map[string]any{"effort": effort})
		}
		return req
	case types.RelayFormatGemini:
		req := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{{Text: "hello"}}}}, GenerationConfig: dto.GeminiChatGenerationConfig{MaxOutputTokens: lo.ToPtr(uint(8192))}}
		if effort == "none" {
			req.GenerationConfig.ThinkingConfig = &dto.GeminiThinkingConfig{ThinkingBudget: lo.ToPtr(0)}
		} else if effort != "" {
			req.GenerationConfig.ThinkingConfig = &dto.GeminiThinkingConfig{ThinkingLevel: effort}
		}
		return req
	default:
		t.Fatalf("unsupported test format %s", format)
		return nil
	}
}

func assertConvertedReasoningEffort(t *testing.T, request any, effort string) {
	t.Helper()
	switch req := request.(type) {
	case *dto.GeneralOpenAIRequest:
		assert.Equal(t, effort, req.ReasoningEffort)
	case *dto.OpenAIResponsesRequest:
		require.NotNil(t, req.Reasoning)
		assert.Equal(t, effort, req.Reasoning.Effort)
	case *dto.ClaudeRequest:
		require.NotNil(t, req.Thinking)
		if effort == "none" {
			assert.Equal(t, "disabled", req.Thinking.Type)
			return
		}
		assert.Contains(t, []string{"adaptive", "enabled"}, req.Thinking.Type)
		assert.Equal(t, effort, req.GetEfforts())
	case *dto.GeminiChatRequest:
		require.NotNil(t, req.GenerationConfig.ThinkingConfig)
		assert.Equal(t, effort, req.GenerationConfig.ThinkingConfig.ThinkingLevel)
	default:
		t.Fatalf("unsupported converted request %T", request)
	}
}

func TestGeminiThinkingLevelCaseInsensitiveAcrossPaths(t *testing.T) {
	newRequest := func(level string) *dto.GeminiChatRequest {
		return &dto.GeminiChatRequest{
			Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{{Text: "hello"}}}},
			GenerationConfig: dto.GeminiChatGenerationConfig{
				ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingLevel: level},
			},
		}
	}

	t.Run("native passthrough records canonical effort without rewriting wire value", func(t *testing.T) {
		info := &convmeta.Values{OriginModelName: "gemini-3.7-flash", UpstreamModelName: "gemini-3.7-flash"}
		req := newRequest(" MEDIUM ")
		require.NoError(t, ApplyGeminiThinkingConfigChecked(req, info))
		assert.Equal(t, "medium", info.GetReasoningEffort())
		assert.Equal(t, " MEDIUM ", req.GenerationConfig.ThinkingConfig.ThinkingLevel)
	})

	t.Run("native passthrough keeps unknown level as sent", func(t *testing.T) {
		info := &convmeta.Values{OriginModelName: "gemini-3.7-flash", UpstreamModelName: "gemini-3.7-flash"}
		req := newRequest("ULTRA")
		require.NoError(t, ApplyGeminiThinkingConfigChecked(req, info))
		assert.Equal(t, "ULTRA", info.GetReasoningEffort())
	})

	t.Run("suffix state canonicalizes uppercase level against normalized effort", func(t *testing.T) {
		info := &convmeta.Values{
			OriginModelName:     "gemini-3.7-flash-thinking-medium",
			UpstreamModelName:   "gemini-3.7-flash",
			ChannelMetaAttached: true,
			ReasoningConversion: &dto.ReasoningConversionState{Mode: "enabled", Effort: "medium"},
		}
		req := newRequest("MEDIUM")
		require.NoError(t, ApplyGeminiThinkingConfigChecked(req, info))
		assert.Equal(t, "medium", info.GetReasoningEffort())
		assert.Equal(t, "medium", req.GenerationConfig.ThinkingConfig.ThinkingLevel)
	})

	t.Run("gemini to openai conversion accepts uppercase level", func(t *testing.T) {
		info := &convmeta.Values{
			OriginModelName:   "gemini-3.7-flash",
			UpstreamModelName: "gemini-3.7-flash",
			ConversionChain:   []types.RelayFormat{types.RelayFormatGemini},
		}
		result, err := ConvertRequest(nil, info, types.RelayFormatOpenAI, newRequest("MEDIUM"))
		require.NoError(t, err)
		openaiReq, ok := result.Value.(*dto.GeneralOpenAIRequest)
		require.True(t, ok)
		assert.Equal(t, "medium", openaiReq.ReasoningEffort)
		assert.Equal(t, "medium", info.GetReasoningEffort())
	})

	t.Run("gemini to openai conversion preserves the supplied level", func(t *testing.T) {
		info := &convmeta.Values{
			OriginModelName:   "gemini-3-pro-preview",
			UpstreamModelName: "gemini-3-pro-preview",
			ConversionChain:   []types.RelayFormat{types.RelayFormatGemini},
		}
		result, err := ConvertRequest(nil, info, types.RelayFormatOpenAI, newRequest("MINIMAL"))
		require.NoError(t, err)
		openaiReq, ok := result.Value.(*dto.GeneralOpenAIRequest)
		require.True(t, ok)
		assert.Equal(t, "minimal", openaiReq.ReasoningEffort)
		assert.Equal(t, "minimal", info.GetReasoningEffort())
		codes := make([]string, 0, len(result.Diagnostics))
		for _, diagnostic := range result.Diagnostics {
			codes = append(codes, diagnostic.Code)
		}
		assert.NotContains(t, codes, "gemini_level_adjusted")
	})
}

func TestConvertRequestViaExecutesExplicitPath(t *testing.T) {
	info := &convmeta.Values{
		ConversionChain: []types.RelayFormat{types.RelayFormatOpenAI},
	}
	req := &dto.GeneralOpenAIRequest{
		Model: "gpt-test",
		Messages: []dto.Message{
			{Role: "user", Content: "hello"},
		},
	}

	result, err := ConvertRequestVia(nil, info, req, types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses)

	require.NoError(t, err)
	require.IsType(t, &dto.OpenAIResponsesRequest{}, result.Value)
	assert.Equal(t, []RequestStep{
		{
			Converter: ConverterOpenAIChatToOpenAIResponses,
			From:      types.RelayFormatOpenAI,
			To:        types.RelayFormatOpenAIResponses,
		},
	}, result.Steps)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses}, info.ConversionChain)
}

// customToolInputs are custom_tool_call inputs that must reach the upstream
// as the raw string, including ones that happen to look like JSON.
var customToolInputs = []struct {
	name  string
	input string
}{
	{"json object", `{"a":1}`},
	{"json array", `[1,2]`},
	{"plain text", "*** Begin Patch\n*** End Patch"},
}

func customToolHistoryRequest(t *testing.T, items ...map[string]any) *dto.OpenAIResponsesRequest {
	t.Helper()
	input := append([]map[string]any{{"role": "user", "content": "apply the patch"}}, items...)
	return &dto.OpenAIResponsesRequest{
		Model:           "model-test",
		MaxOutputTokens: lo.ToPtr(uint(64)),
		Input:           mustRawMessage(t, input),
		Tools:           mustRawMessage(t, []map[string]any{{"type": "custom", "name": "apply_patch"}}),
	}
}

func TestConvertRequestResponsesToClaudeKeepsCustomToolHistory(t *testing.T) {
	for _, tc := range customToolInputs {
		t.Run(tc.name, func(t *testing.T) {
			req := customToolHistoryRequest(t,
				map[string]any{"type": "custom_tool_call", "call_id": "call_custom", "name": "apply_patch", "input": tc.input},
				map[string]any{"type": "custom_tool_call_output", "call_id": "call_custom", "output": "ok"},
			)

			result, err := ConvertRequest(nil, &convmeta.Values{}, types.RelayFormatClaude, req)
			require.NoError(t, err)
			claudeReq, ok := result.Value.(*dto.ClaudeRequest)
			require.True(t, ok)
			require.Len(t, claudeReq.Messages, 3)

			assert.Equal(t, "assistant", claudeReq.Messages[1].Role)
			toolUse, err := claudeReq.Messages[1].ParseContent()
			require.NoError(t, err)
			assert.Equal(t, []dto.ClaudeMediaMessage{{
				Type:  "tool_use",
				Id:    "call_custom",
				Name:  "apply_patch",
				Input: map[string]any{"input": tc.input},
			}}, toolUse)

			assert.Equal(t, "user", claudeReq.Messages[2].Role)
			toolResult, err := claudeReq.Messages[2].ParseContent()
			require.NoError(t, err)
			assert.Equal(t, []dto.ClaudeMediaMessage{{Type: "tool_result", ToolUseId: "call_custom", Content: "ok"}}, toolResult)
		})
	}

	t.Run("missing name", func(t *testing.T) {
		req := customToolHistoryRequest(t, map[string]any{"type": "custom_tool_call", "call_id": "call_custom", "input": "ls"})
		_, err := ConvertRequest(nil, &convmeta.Values{}, types.RelayFormatClaude, req)
		require.ErrorContains(t, err, "custom_tool_call item is missing name")
	})
}

func TestConvertRequestResponsesToGeminiKeepsCustomToolHistory(t *testing.T) {
	info := func() *convmeta.Values {
		return &convmeta.Values{Options: &convmeta.Options{Gemini: convmeta.GeminiOptions{FunctionCallThoughtSignatureEnabled: true}}}
	}
	for _, tc := range customToolInputs {
		t.Run(tc.name, func(t *testing.T) {
			// Older Codex history may answer a custom call with function_call_output.
			req := customToolHistoryRequest(t,
				map[string]any{"type": "custom_tool_call", "call_id": "call_custom", "name": "apply_patch", "input": tc.input},
				map[string]any{"type": "custom_tool_call_output", "call_id": "call_custom", "output": "ok"},
				map[string]any{"type": "function_call_output", "call_id": "call_custom", "output": "legacy"},
			)

			result, err := ConvertRequest(nil, info(), types.RelayFormatGemini, req)
			require.NoError(t, err)
			geminiReq, ok := result.Value.(*dto.GeminiChatRequest)
			require.True(t, ok)
			tools := geminiReq.GetTools()
			require.Len(t, tools, 1)
			functions, err := kitutil.Any2Type[[]dto.FunctionRequest](tools[0].FunctionDeclarations)
			require.NoError(t, err)
			require.Len(t, functions, 1)
			assert.Equal(t, "apply_patch", functions[0].Name)
			require.Len(t, geminiReq.Contents, 3)

			model := geminiReq.Contents[1]
			assert.Equal(t, "model", model.Role)
			require.Len(t, model.Parts, 1)
			assert.Equal(t, &dto.FunctionCall{
				ID:           "call_custom",
				FunctionName: "apply_patch",
				Arguments:    map[string]any{"input": tc.input},
			}, model.Parts[0].FunctionCall)
			var thoughtSignature string
			require.NoError(t, kitutil.Unmarshal(model.Parts[0].ThoughtSignature, &thoughtSignature))
			assert.Equal(t, sharedgemini.ThoughtSignatureBypassValue, thoughtSignature)

			user := geminiReq.Contents[2]
			assert.Equal(t, "user", user.Role)
			require.Len(t, user.Parts, 2)
			for index, output := range []string{"ok", "legacy"} {
				assert.Equal(t, &dto.GeminiFunctionResponse{
					Name:     "apply_patch",
					Response: map[string]any{"content": output},
					ID:       []byte(`"call_custom"`),
				}, user.Parts[index].FunctionResponse)
			}
		})
	}

	t.Run("missing name", func(t *testing.T) {
		req := customToolHistoryRequest(t, map[string]any{"type": "custom_tool_call", "call_id": "call_custom", "input": "ls"})
		_, err := ConvertRequest(nil, info(), types.RelayFormatGemini, req)
		require.ErrorContains(t, err, "custom_tool_call item is missing name")
	})
}

func TestConvertRequestToGeminiToolCombination(t *testing.T) {
	function := map[string]any{
		"type": "function", "name": "lookup",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}},
	}
	search := map[string]any{"type": "web_search"}
	chatWithSearch := &dto.GeneralOpenAIRequest{
		Model: "gemini-3-flash-preview", Messages: []dto.Message{{Role: "user", Content: "hello"}},
		Tools:            []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}}},
		WebSearchOptions: &dto.WebSearchOptions{},
		ToolChoice:       "auto",
	}
	chatSearchOnly := &dto.GeneralOpenAIRequest{
		Model: "gemini-3-flash-preview", Messages: []dto.Message{{Role: "user", Content: "hello"}},
		WebSearchOptions: &dto.WebSearchOptions{},
	}
	for _, tc := range []struct {
		name       string
		request    any
		tools      []map[string]any
		choice     any
		options    *convmeta.Options
		wantTools  []string
		wantConfig string
	}{
		{
			name: "default choice", tools: []map[string]any{search, function},
			wantTools:  []string{"functionDeclarations"},
			wantConfig: `null`,
		},
		{
			name: "auto choice", tools: []map[string]any{search, function}, choice: "auto",
			wantTools:  []string{"functionDeclarations"},
			wantConfig: `{"functionCallingConfig":{"mode":"AUTO"}}`,
		},
		{
			name: "required choice", tools: []map[string]any{search, function}, choice: "required",
			wantTools:  []string{"functionDeclarations"},
			wantConfig: `{"functionCallingConfig":{"mode":"ANY"}}`,
		},
		{
			name: "named choice", tools: []map[string]any{search, function}, choice: map[string]any{"type": "function", "name": "lookup"},
			wantTools:  []string{"functionDeclarations"},
			wantConfig: `{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["lookup"]}}`,
		},
		{
			name: "none choice", tools: []map[string]any{search, function}, choice: "none",
			wantTools:  []string{"functionDeclarations"},
			wantConfig: `{"functionCallingConfig":{"mode":"NONE"}}`,
		},
		{
			name: "preview search and custom tool", tools: []map[string]any{{"type": "web_search_preview"}, {"type": "custom", "name": "apply_patch"}}, choice: "auto",
			wantTools:  []string{"functionDeclarations"},
			wantConfig: `{"functionCallingConfig":{"mode":"AUTO"}}`,
		},
		{
			name: "forced search omitted", tools: []map[string]any{search, function}, choice: map[string]any{"type": "web_search"},
			wantTools: []string{"functionDeclarations"}, wantConfig: `null`,
		},
		{
			name: "functions only", tools: []map[string]any{function}, choice: "auto",
			wantTools: []string{"functionDeclarations"}, wantConfig: `{"functionCallingConfig":{"mode":"AUTO"}}`,
		},
		{
			name: "search only", tools: []map[string]any{search},
			wantTools: []string{"googleSearch"}, wantConfig: `null`,
		},
		{
			name: "search only with required choice", tools: []map[string]any{search}, choice: "required",
			wantTools: []string{"googleSearch"}, wantConfig: `null`,
		},
		{
			name: "search with unsupported hosted tool", tools: []map[string]any{search, {"type": "file_search"}},
			wantTools: []string{"googleSearch"}, wantConfig: `null`,
		},
		{
			name: "search with invalid custom tool", tools: []map[string]any{search, {"type": "custom"}},
			wantTools: []string{"googleSearch"}, wantConfig: `null`,
		},
		{
			name: "chat search and function", request: chatWithSearch,
			wantTools:  []string{"functionDeclarations"},
			wantConfig: `{"functionCallingConfig":{"mode":"AUTO"}}`,
		},
		{
			name: "chat search only", request: chatSearchOnly,
			wantTools: []string{"googleSearch"}, wantConfig: `null`,
		},
		{
			name: "chat pseudo search before function",
			request: &dto.GeneralOpenAIRequest{
				Model: "gemini-3-flash-preview", Messages: []dto.Message{{Role: "user", Content: "hello"}},
				Tools: []dto.ToolCallRequest{
					{Type: "function", Function: dto.FunctionRequest{Name: "googleSearch"}},
					{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}},
				},
			},
			wantTools: []string{"functionDeclarations"}, wantConfig: `null`,
		},
		{
			name: "chat pseudo search only",
			request: &dto.GeneralOpenAIRequest{
				Model: "gemini-3-flash-preview", Messages: []dto.Message{{Role: "user", Content: "hello"}},
				Tools: []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{Name: "googleSearch"}}},
			},
			wantTools: []string{"googleSearch"}, wantConfig: `null`,
		},
		{
			name: "host omits chat search", request: chatSearchOnly,
			options: &convmeta.Options{WebSearch: func(convmeta.WebSearchCall) (*convmeta.WebSearchEncoding, error) {
				return &convmeta.WebSearchEncoding{Omitted: true}, nil
			}},
			wantConfig: `null`,
		},
		{
			name: "host retrieval configuration", request: chatSearchOnly,
			options: &convmeta.Options{WebSearch: func(call convmeta.WebSearchCall) (*convmeta.WebSearchEncoding, error) {
				call.Gemini.ToolConfig = &dto.ToolConfig{RetrievalConfig: &dto.RetrievalConfig{LanguageCode: "en"}}
				return &convmeta.WebSearchEncoding{Tool: map[string]any{"googleSearch": map[string]any{}}}, nil
			}},
			wantTools:  []string{"googleSearch"},
			wantConfig: `{"retrievalConfig":{"languageCode":"en"}}`,
		},
		{
			name: "chat code execution and function",
			request: &dto.GeneralOpenAIRequest{
				Model: "gemini-3-flash-preview", Messages: []dto.Message{{Role: "user", Content: "hello"}},
				Tools: []dto.ToolCallRequest{
					{Type: "function", Function: dto.FunctionRequest{Name: "codeExecution"}},
					{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}},
				},
				ToolChoice: "auto",
			},
			wantTools:  []string{"codeExecution", "functionDeclarations"},
			wantConfig: `{"includeServerSideToolInvocations":true,"functionCallingConfig":{"mode":"VALIDATED"}}`,
		},
		{
			name: "chat URL context and function",
			request: &dto.GeneralOpenAIRequest{
				Model: "gemini-3-flash-preview", Messages: []dto.Message{{Role: "user", Content: "hello"}},
				Tools: []dto.ToolCallRequest{
					{Type: "function", Function: dto.FunctionRequest{Name: "urlContext"}},
					{Type: "function", Function: dto.FunctionRequest{Name: "lookup"}},
				},
				ToolChoice: "auto",
			},
			wantTools:  []string{"urlContext", "functionDeclarations"},
			wantConfig: `{"includeServerSideToolInvocations":true,"functionCallingConfig":{"mode":"VALIDATED"}}`,
		},
		{
			name: "Claude search and function",
			request: &dto.ClaudeRequest{
				Model: "gemini-3-flash-preview", Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
				Tools: []map[string]any{
					{"type": "web_search_20250305", "name": "web_search"},
					{"name": "lookup", "input_schema": map[string]any{"type": "object"}},
				},
				ToolChoice: map[string]any{"type": "auto"},
			},
			wantTools:  []string{"functionDeclarations"},
			wantConfig: `{"functionCallingConfig":{"mode":"AUTO"}}`,
		},
		{
			name: "Claude search only",
			request: &dto.ClaudeRequest{
				Model: "gemini-3-flash-preview", Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
				Tools: []map[string]any{{"type": "web_search_20250305", "name": "web_search"}},
			},
			wantTools: []string{"googleSearch"}, wantConfig: `null`,
		},
		{
			name: "native Gemini mixed tools preserved",
			request: &dto.GeminiChatRequest{
				Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{{Text: "hello"}}}},
				Tools: mustRawMessage(t, []map[string]any{
					{"googleSearch": map[string]any{}},
					{"functionDeclarations": []map[string]any{{"name": "lookup"}}},
				}),
				ToolConfig: &dto.ToolConfig{
					FunctionCallingConfig:            &dto.FunctionCallingConfig{Mode: "AUTO"},
					IncludeServerSideToolInvocations: lo.ToPtr(false),
				},
			},
			wantTools:  []string{"googleSearch", "functionDeclarations"},
			wantConfig: `{"functionCallingConfig":{"mode":"AUTO"},"includeServerSideToolInvocations":false}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := tc.request
			if request == nil {
				req := &dto.OpenAIResponsesRequest{
					Model: "gemini-3-flash-preview", Input: mustRawMessage(t, "hello"), Tools: mustRawMessage(t, tc.tools),
				}
				if tc.choice != nil {
					req.ToolChoice = mustRawMessage(t, tc.choice)
				}
				request = req
			}
			result, err := ConvertRequest(context.Background(), &convmeta.Values{Options: tc.options}, types.RelayFormatGemini, request)
			require.NoError(t, err)
			converted, ok := result.Value.(*dto.GeminiChatRequest)
			require.True(t, ok)
			var tools []map[string]any
			if len(tc.wantTools) == 0 {
				assert.Empty(t, converted.Tools)
			} else {
				require.NoError(t, kitutil.Unmarshal(converted.Tools, &tools))
			}
			require.Len(t, tools, len(tc.wantTools))
			for i, key := range tc.wantTools {
				assert.Contains(t, tools[i], key)
			}
			config, err := kitutil.Marshal(converted.ToolConfig)
			require.NoError(t, err)
			assert.JSONEq(t, tc.wantConfig, string(config))
		})
	}
}

func TestResponsesToGeminiOmitsBuiltInWebSearch(t *testing.T) {
	tools := []map[string]any{
		{"type": "function", "name": "eval_javascript", "parameters": map[string]any{
			"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}}, "required": []string{"code"},
		}},
		{"type": "function", "name": "get_time_info", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"type": "function", "name": "web_search"},
	}
	for _, toolType := range []string{"web_search", "web_search_2025_08_26", "web_search_preview", "web_search_preview_2025_03_11"} {
		tools = append(tools, map[string]any{"type": toolType})
	}
	req := &dto.OpenAIResponsesRequest{
		Model: "gemini-test", Input: mustRawMessage(t, "hello"), Tools: mustRawMessage(t, tools),
	}
	originalTools := string(req.Tools)
	for _, policy := range []types.ConversionLossPolicy{"", types.ConversionLossPolicySafe, types.ConversionLossPolicyStrict} {
		t.Run(string(policy), func(t *testing.T) {
			info := &convmeta.Values{Options: &convmeta.Options{
				ToolLossPolicy: policy,
				WebSearch: func(convmeta.WebSearchCall) (*convmeta.WebSearchEncoding, error) {
					return &convmeta.WebSearchEncoding{Tool: map[string]any{"googleSearch": map[string]any{}}}, nil
				},
			}}
			result, err := ConvertRequest(t.Context(), info, types.RelayFormatGemini, req)
			require.NotNil(t, result)
			assert.Equal(t, originalTools, string(req.Tools), "filtering must not mutate the inbound request")
			require.Len(t, result.Diagnostics, 4)
			for _, diagnostic := range result.Diagnostics {
				assert.Equal(t, "hosted_web_search_omitted", diagnostic.Code)
				assert.Equal(t, types.ConversionDiagnosticError, diagnostic.Severity)
			}
			if policy != "" {
				var loss *types.ConversionLossError
				require.ErrorAs(t, err, &loss)
				return
			}
			require.NoError(t, err)
			converted, ok := result.Value.(*dto.GeminiChatRequest)
			require.True(t, ok)
			assert.JSONEq(t, `[{"functionDeclarations":[
				{"name":"eval_javascript","parameters":{"type":"OBJECT","properties":{"code":{"type":"STRING"}},"required":["code"]}},
				{"name":"get_time_info","parameters":null},
				{"name":"web_search","parameters":null}
			]}]`, string(converted.Tools))
			assert.Nil(t, converted.ToolConfig)
		})
	}
}

func TestConvertRequestResponsesToGeminiUsesDirectConverter(t *testing.T) {
	info := &convmeta.Values{
		Options:             &convmeta.Options{Gemini: convmeta.GeminiOptions{FunctionCallThoughtSignatureEnabled: true}},
		ConversionChain:     []types.RelayFormat{types.RelayFormatOpenAIResponses},
		ChannelMetaAttached: true,
		UpstreamModelName:   "gemini-test",
	}
	maxOutputTokens := uint(256)
	req := &dto.OpenAIResponsesRequest{
		Model:           "gemini-test",
		Instructions:    mustRawMessage(t, "system rules"),
		MaxOutputTokens: &maxOutputTokens,
		Input: mustRawMessage(t, []map[string]any{
			{
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": "I will call."},
				},
			},
			{
				"type":      "function_call",
				"call_id":   "call_1",
				"name":      "lookup",
				"arguments": map[string]any{"q": "x"},
			},
			{
				"type":    "function_call_output",
				"call_id": "call_1",
				"output":  map[string]any{"ok": true},
			},
		}),
		Tools: mustRawMessage(t, []map[string]any{
			{
				"type":        "function",
				"name":        "lookup",
				"description": "Lookup data",
				"parameters": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"propertyNames":        map[string]any{"pattern": "^[a-z]+$"},
					"properties": map[string]any{
						"q": map[string]any{
							"type":             "string",
							"exclusiveMinimum": 0,
						},
						"filters": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type":                 "object",
								"additionalProperties": true,
								"properties": map[string]any{
									"name": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			},
		}),
		Text: mustRawMessage(t, map[string]any{
			"format": map[string]any{
				"type":   "json_schema",
				"name":   "answer",
				"schema": map[string]any{"type": "object"},
			},
		}),
	}

	result, err := ConvertRequest(nil, info, types.RelayFormatGemini, req)

	require.NoError(t, err)
	geminiReq, ok := result.Value.(*dto.GeminiChatRequest)
	require.True(t, ok)
	assert.Equal(t, ConverterOpenAIResponsesToGemini, result.Converter)
	assert.Equal(t, []RequestStep{
		{
			Converter: ConverterOpenAIResponsesToGemini,
			From:      types.RelayFormatOpenAIResponses,
			To:        types.RelayFormatGemini,
		},
	}, result.Steps)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatOpenAIResponses, types.RelayFormatGemini}, info.ConversionChain)

	require.NotNil(t, geminiReq.SystemInstructions)
	require.Len(t, geminiReq.SystemInstructions.Parts, 1)
	assert.Equal(t, "system rules", geminiReq.SystemInstructions.Parts[0].Text)
	assert.Equal(t, "application/json", geminiReq.GenerationConfig.ResponseMimeType)
	assert.Equal(t, maxOutputTokens, *geminiReq.GenerationConfig.MaxOutputTokens)

	tools := geminiReq.GetTools()
	require.Len(t, tools, 1)
	functions, err := kitutil.Any2Type[[]dto.FunctionRequest](tools[0].FunctionDeclarations)
	require.NoError(t, err)
	require.Len(t, functions, 1)
	assert.Equal(t, "lookup", functions[0].Name)
	var params map[string]any
	require.NoError(t, kitutil.Unmarshal(functions[0].Parameters, &params))
	assert.Equal(t, "OBJECT", params["type"])
	assert.NotContains(t, params, "additionalProperties")
	assert.NotContains(t, params, "propertyNames")
	properties, ok := params["properties"].(map[string]any)
	require.True(t, ok)
	queryParam, ok := properties["q"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "STRING", queryParam["type"])
	assert.NotContains(t, queryParam, "exclusiveMinimum")
	filterParam, ok := properties["filters"].(map[string]any)
	require.True(t, ok)
	filterItems, ok := filterParam["items"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, filterItems, "additionalProperties")

	require.Len(t, geminiReq.Contents, 2)
	assert.Equal(t, "model", geminiReq.Contents[0].Role)
	require.Len(t, geminiReq.Contents[0].Parts, 2)
	functionCall := geminiReq.Contents[0].Parts[1].FunctionCall
	require.NotNil(t, functionCall)
	assert.Equal(t, "lookup", functionCall.FunctionName)
	assert.Equal(t, map[string]any{"q": "x"}, functionCall.Arguments)
	var thoughtSignature string
	require.NoError(t, kitutil.Unmarshal(geminiReq.Contents[0].Parts[1].ThoughtSignature, &thoughtSignature))
	assert.Equal(t, sharedgemini.ThoughtSignatureBypassValue, thoughtSignature)
	assert.Equal(t, "I will call.", geminiReq.Contents[0].Parts[0].Text)

	assert.Equal(t, "user", geminiReq.Contents[1].Role)
	require.Len(t, geminiReq.Contents[1].Parts, 1)
	functionResponse := geminiReq.Contents[1].Parts[0].FunctionResponse
	require.NotNil(t, functionResponse)
	assert.Equal(t, "lookup", functionResponse.Name)
	assert.Equal(t, true, functionResponse.Response["ok"])
	assert.Empty(t, geminiReq.Contents[1].Parts[0].ThoughtSignature)
}

func TestConvertRequestToGeminiNullableToolSchema(t *testing.T) {
	const parameters = `{"type":"object","properties":{
		"threadId":{"type":"string","minLength":1},
		"projectId":{"anyOf":[{"type":"string","minLength":1},{"type":"null"}],"description":"Destination project"},
		"nested":{"type":"array","items":{"type":["integer","null"],"minimum":0}},
		"choice":{"oneOf":[{"type":"null"},{"type":"string","enum":["a","b"]}]},
		"nullOnly":{"type":"null"},
		"example":{"type":"object","default":{"type":"null","anyOf":[null]},"properties":{"type":{"type":"string","maxLength":9007199254740993}}}
	},"required":["threadId","projectId"]}`
	const want = `{"type":"OBJECT","properties":{
		"threadId":{"type":"STRING","minLength":"1"},
		"projectId":{"type":"STRING","nullable":true,"minLength":"1","description":"Destination project"},
		"nested":{"type":"ARRAY","items":{"type":"INTEGER","nullable":true,"minimum":0}},
		"choice":{"type":"STRING","nullable":true,"enum":["a","b"]},
		"nullOnly":{"type":"NULL"},
		"example":{"type":"OBJECT","default":{"type":"null","anyOf":[null]},"properties":{"type":{"type":"STRING","maxLength":"9007199254740993"}}}
	},"required":["threadId","projectId"],"propertyOrdering":["threadId","projectId","nested","choice","nullOnly","example"]}`
	for _, tc := range []struct {
		name    string
		request any
		body    string
	}{
		{
			name: "Responses", request: &dto.OpenAIResponsesRequest{},
			body: `{"model":"gemini-4-carbon","input":[{"type":"function_call","call_id":"call_1","name":"move","arguments":"{\"threadId\":\"t\",\"projectId\":null}"}],"tools":[{"type":"function","name":"move","parameters":` + parameters + `}]}`,
		},
		{
			name: "Chat", request: &dto.GeneralOpenAIRequest{},
			body: `{"model":"gemini-4-carbon","messages":[{"role":"user","content":"OK"}],"tools":[{"type":"function","function":{"name":"move","parameters":` +
				parameters + `}}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":` + parameters + `}}}`,
		},
		{
			name: "Claude", request: &dto.ClaudeRequest{},
			body: `{"model":"gemini-4-carbon","max_tokens":64,"messages":[{"role":"user","content":"OK"}],"tools":[{"name":"move","input_schema":` + parameters + `}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, kitutil.UnmarshalJsonStr(tc.body, tc.request))
			info := &convmeta.Values{ChannelMetaAttached: true, UpstreamModelName: "gemini-4-carbon",
				Options: &convmeta.Options{Gemini: convmeta.GeminiOptions{SupportsJSONSchema: func(string) bool { return true }}},
			}
			result, err := ConvertRequest(t.Context(), info, types.RelayFormatGemini, tc.request)
			require.NoError(t, err)
			assert.Empty(t, result.Diagnostics)
			req := result.Value.(*dto.GeminiChatRequest)
			assert.JSONEq(t, `[{"functionDeclarations":[{"name":"move","parameters":`+want+`}]}]`, string(req.Tools))
			if tc.name == "Responses" {
				require.NotEmpty(t, req.Contents)
				require.NotEmpty(t, req.Contents[0].Parts)
				require.NotNil(t, req.Contents[0].Parts[0].FunctionCall)
				assert.Equal(t, map[string]any{"threadId": "t", "projectId": nil}, req.Contents[0].Parts[0].FunctionCall.Arguments)
			}
			if tc.name == "Chat" {
				config, err := kitutil.Marshal(req.GenerationConfig)
				require.NoError(t, err)
				assert.JSONEq(t, want, string(kitutil.ObjectMember(config, "responseSchema")))
			}
		})
	}
}

func TestConvertRequestToGeminiSchemaFieldSelection(t *testing.T) {
	const nullable = `{"type":"object","properties":{"value":{"anyOf":[{"type":"string","minLength":1},{"type":"null"}]}},"required":["value"]}`
	const typedNullable = `{"type":"OBJECT","properties":{"value":{"type":"STRING","nullable":true,"minLength":"1"}},"required":["value"]}`
	const closed = `{"type":"object","additionalProperties":false,"properties":{"value":{"anyOf":[{"type":"string","minLength":1},{"type":"null"}]}},"required":["value"]}`
	const references = `{"type":"object","properties":{"z":{"$ref":"#/$defs/value"},"a":{"type":"string"}},"$defs":{"value":{"type":"array","items":{"$ref":"#/$defs/value"}}}}`
	for _, tc := range []struct {
		name           string
		schema         string
		legacy         bool
		raw            bool
		wantDiagnostic string
	}{
		{name: "typed nullable on v1beta", schema: nullable},
		{name: "typed nullable on v1", schema: nullable, legacy: true},
		{name: "preserve references on v1beta", schema: references, raw: true},
		{name: "preserve closed nullable objects on v1beta", schema: closed, raw: true},
		{name: "report closed object constraint on v1", schema: closed, legacy: true, wantDiagnostic: "additionalProperties"},
		{name: "preserve additional property schema on v1beta", schema: `{"type":"object","properties":{"nested":{"type":"object","additionalProperties":{"type":["string","null"]}}}}`, raw: true},
		{name: "preserve multiple concrete types", schema: `{"type":"object","properties":{"value":{"type":["string","integer"]}}}`, raw: true},
		{name: "preserve multi-branch nullable union", schema: `{"type":"object","properties":{"value":{"anyOf":[{"type":"string"},{"type":"integer"},{"type":"null"}]}}}`, raw: true},
		{name: "preserve constraints on the whole union", schema: `{"type":"object","properties":{"value":{"anyOf":[{"type":"string"},{"type":"null"}],"enum":["a"]}}}`, raw: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &dto.OpenAIResponsesRequest{}
			body := `{"model":"alias","input":"OK","tools":[{"type":"function","name":"lookup","parameters":` + tc.schema +
				`}],"text":{"format":{"type":"json_schema","name":"answer","schema":` + tc.schema + `}}}`
			require.NoError(t, kitutil.UnmarshalJsonStr(body, req))
			model := "gemini-v1beta"
			if tc.legacy {
				model = "gemini-v1"
			}
			info := &convmeta.Values{ChannelMetaAttached: true, UpstreamModelName: model,
				Options: &convmeta.Options{Gemini: convmeta.GeminiOptions{
					SupportsJSONSchema: func(model string) bool { return model == "gemini-v1beta" },
				}},
			}
			result, err := ConvertRequest(t.Context(), info, types.RelayFormatGemini, req)
			require.NoError(t, err)
			if tc.wantDiagnostic == "" {
				assert.Empty(t, result.Diagnostics)
			} else {
				require.Len(t, result.Diagnostics, 2)
				assert.Equal(t, "text.format.schema", result.Diagnostics[0].Path)
				assert.Equal(t, "tools[0].parameters", result.Diagnostics[1].Path)
				for _, diagnostic := range result.Diagnostics {
					assert.Equal(t, "gemini_schema_keyword_dropped", diagnostic.Code)
					assert.Contains(t, diagnostic.Message, tc.wantDiagnostic)
				}
			}
			gemini := result.Value.(*dto.GeminiChatRequest)
			config, err := kitutil.Marshal(gemini.GenerationConfig)
			require.NoError(t, err)
			assert.Equal(t, "application/json", gemini.GenerationConfig.ResponseMimeType)
			if tc.raw {
				assert.Equal(t, `[{"functionDeclarations":[{"name":"lookup","parametersJsonSchema":`+tc.schema+`}]}]`, string(gemini.Tools))
				assert.Contains(t, string(config), `"responseJsonSchema":`+tc.schema)
				assert.NotContains(t, string(config), `"responseSchema"`)
			} else {
				assert.JSONEq(t, `[{"functionDeclarations":[{"name":"lookup","parameters":`+typedNullable+`}]}]`, string(gemini.Tools))
				assert.JSONEq(t, typedNullable, string(kitutil.ObjectMember(config, "responseSchema")))
				assert.NotContains(t, string(config), `"responseJsonSchema"`)
			}
		})
	}
}

func TestConvertRequestResponsesToGeminiSkipsThoughtSignatureWhenDisabled(t *testing.T) {
	info := &convmeta.Values{
		Options:             &convmeta.Options{Gemini: convmeta.GeminiOptions{FunctionCallThoughtSignatureEnabled: false}},
		ConversionChain:     []types.RelayFormat{types.RelayFormatOpenAIResponses},
		ChannelMetaAttached: true,
		UpstreamModelName:   "gemini-test",
	}
	req := &dto.OpenAIResponsesRequest{
		Model: "gemini-test",
		Input: mustRawMessage(t, []map[string]any{
			{
				"type":      "function_call",
				"call_id":   "call_1",
				"name":      "lookup",
				"arguments": map[string]any{"q": "x"},
			},
		}),
		Tools: mustRawMessage(t, []map[string]any{
			{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}},
		}),
	}

	result, err := ConvertRequest(nil, info, types.RelayFormatGemini, req)

	require.NoError(t, err)
	geminiReq, ok := result.Value.(*dto.GeminiChatRequest)
	require.True(t, ok)
	require.Len(t, geminiReq.Contents, 1)
	require.Len(t, geminiReq.Contents[0].Parts, 1)
	require.NotNil(t, geminiReq.Contents[0].Parts[0].FunctionCall)
	assert.Empty(t, geminiReq.Contents[0].Parts[0].ThoughtSignature)
}

func TestConvertRequestOpenAIChatToGeminiImageConfig(t *testing.T) {
	tests := []struct {
		name   string
		fields string
		want   string
	}{
		{
			name: "omitted",
		},
		{
			name:   "top level 4K",
			fields: `,"imageConfig":{"imageSize":"4K"}`,
			want:   `{"imageSize":"4K"}`,
		},
		{
			name:   "top level aspect ratio",
			fields: `,"imageConfig":{"imageSize":"4K","aspectRatio":"16:9"}`,
			want:   `{"imageSize":"4K","aspectRatio":"16:9"}`,
		},
		{
			name:   "google extra body",
			fields: `,"extra_body":{"google":{"image_config":{"image_size":"4K","aspect_ratio":"16:9"}}}`,
			want:   `{"imageSize":"4K","aspectRatio":"16:9"}`,
		},
		{
			name:   "google extra body takes precedence",
			fields: `,"imageConfig":{"imageSize":"1K"},"extra_body":{"google":{"image_config":{"image_size":"4K"}}}`,
			want:   `{"imageSize":"4K"}`,
		},
		{
			name:   "empty google image config keeps top level config",
			fields: `,"imageConfig":{"imageSize":"4K"},"extra_body":{"google":{"image_config":{}}}`,
			want:   `{"imageSize":"4K"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"model":"gemini-3.1-flash-image-preview","temperature":0.7,"top_p":1,"messages":[{"role":"user","content":[{"type":"text","text":"Remove the distant car"}]}]` + tt.fields + `}`
			var request dto.GeneralOpenAIRequest
			require.NoError(t, kitutil.Unmarshal([]byte(raw), &request))

			body, err := kitutil.Marshal(request)
			require.NoError(t, err)
			var copiedRequest dto.GeneralOpenAIRequest
			require.NoError(t, kitutil.Unmarshal(body, &copiedRequest))

			result, err := ConvertRequest(context.Background(), nil, types.RelayFormatGemini, &copiedRequest)
			require.NoError(t, err)
			body, err = kitutil.Marshal(result.Value)
			require.NoError(t, err)
			var upstream map[string]json.RawMessage
			require.NoError(t, kitutil.Unmarshal(body, &upstream))
			var generationConfig map[string]json.RawMessage
			require.NoError(t, kitutil.Unmarshal(upstream["generationConfig"], &generationConfig))

			assert.NotContains(t, upstream, "imageConfig")
			if tt.want == "" {
				assert.NotContains(t, generationConfig, "imageConfig")
				return
			}
			require.Contains(t, generationConfig, "imageConfig")
			assert.JSONEq(t, tt.want, string(generationConfig["imageConfig"]))
		})
	}
}

func TestConvertRequestOpenAIChatToGeminiAddsThoughtSignatureForAdvancedCustom(t *testing.T) {
	assistantMessage := dto.Message{Role: "assistant", Content: ""}
	assistantMessage.SetToolCalls([]dto.ToolCallRequest{
		{
			ID:   "call_1",
			Type: "function",
			Function: dto.FunctionRequest{
				Name:      "lookup",
				Arguments: `{"q":"x"}`,
			},
		},
	})
	info := &convmeta.Values{
		Options:             &convmeta.Options{Gemini: convmeta.GeminiOptions{FunctionCallThoughtSignatureEnabled: true}},
		ConversionChain:     []types.RelayFormat{types.RelayFormatOpenAI},
		ChannelMetaAttached: true,
		ChannelType:         58, // advanced-custom in the host
		UpstreamModelName:   "gemini-test",
	}
	req := &dto.GeneralOpenAIRequest{
		Model: "gemini-test",
		Messages: []dto.Message{
			{Role: "user", Content: "hi"},
			assistantMessage,
			{Role: "tool", ToolCallId: "call_1", Content: `{"ok":true}`},
		},
		Tools: []dto.ToolCallRequest{
			{
				Type: "function",
				Function: dto.FunctionRequest{
					Name:       "lookup",
					Parameters: json.RawMessage(`{"type":"object"}`),
				},
			},
		},
	}

	result, err := ConvertRequest(nil, info, types.RelayFormatGemini, req)

	require.NoError(t, err)
	geminiReq, ok := result.Value.(*dto.GeminiChatRequest)
	require.True(t, ok)
	require.Len(t, geminiReq.Contents, 3)
	assert.Equal(t, "model", geminiReq.Contents[1].Role)
	require.Len(t, geminiReq.Contents[1].Parts, 1)
	require.NotNil(t, geminiReq.Contents[1].Parts[0].FunctionCall)
	var thoughtSignature string
	require.NoError(t, kitutil.Unmarshal(geminiReq.Contents[1].Parts[0].ThoughtSignature, &thoughtSignature))
	assert.Equal(t, sharedgemini.ThoughtSignatureBypassValue, thoughtSignature)
}

func TestConvertRequestViaResponsesToGeminiStillUsesDirectSteps(t *testing.T) {
	info := &convmeta.Values{
		ConversionChain:     []types.RelayFormat{types.RelayFormatOpenAIResponses},
		ChannelMetaAttached: true,
		UpstreamModelName:   "gemini-test",
	}
	req := &dto.OpenAIResponsesRequest{
		Model: "gemini-test",
		Input: mustRawMessage(t, []map[string]any{
			{
				"role":    "user",
				"content": "hello",
			},
		}),
	}

	result, err := ConvertRequestVia(nil, info, req, types.RelayFormatOpenAI, types.RelayFormatGemini)

	require.NoError(t, err)
	require.IsType(t, &dto.GeminiChatRequest{}, result.Value)
	assert.Equal(t, ConverterOpenAIResponsesToOpenAIChat+","+ConverterOpenAIChatToGeminiContent, result.Converter)
	assert.Equal(t, []RequestStep{
		{
			Converter: ConverterOpenAIResponsesToOpenAIChat,
			From:      types.RelayFormatOpenAIResponses,
			To:        types.RelayFormatOpenAI,
		},
		{
			Converter: ConverterOpenAIChatToGeminiContent,
			From:      types.RelayFormatOpenAI,
			To:        types.RelayFormatGemini,
		},
	}, result.Steps)
}

func TestConvertRequestByIDDeduplicatesConversionChain(t *testing.T) {
	info := &convmeta.Values{
		ConversionChain: []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses},
	}
	req := &dto.GeneralOpenAIRequest{
		Model: "gpt-test",
		Messages: []dto.Message{
			{Role: "user", Content: "hello"},
		},
	}

	result, err := ConvertRequestByID(nil, info, ConverterOpenAIChatToOpenAIResponses, req)

	require.NoError(t, err)
	require.IsType(t, &dto.OpenAIResponsesRequest{}, result.Value)
	require.Len(t, result.Steps, 1)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses}, info.ConversionChain)
}

func TestConvertRequestByIDExecutesDirectClaudeToResponsesConverter(t *testing.T) {
	info := &convmeta.Values{
		ConversionChain: []types.RelayFormat{types.RelayFormatClaude},
	}
	req := &dto.ClaudeRequest{
		Model: "claude-test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}

	result, err := ConvertRequestByID(nil, info, requestConverterClaudeToResponses, req)

	require.NoError(t, err)
	require.IsType(t, &dto.OpenAIResponsesRequest{}, result.Value)
	assert.Equal(t, requestConverterClaudeToResponses, result.Converter)
	assert.Equal(t, RequestConverterQualityFair, result.Quality)
	assert.Equal(t, []RequestStep{
		{
			Converter: requestConverterClaudeToResponses,
			From:      types.RelayFormatClaude,
			To:        types.RelayFormatOpenAIResponses,
		},
	}, result.Steps)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAIResponses}, info.ConversionChain)
}

func TestConvertRequestRejectsUnsupportedConverterAndNilRequest(t *testing.T) {
	_, err := ConvertRequestByID(nil, &convmeta.Values{}, "missing_converter", &dto.GeneralOpenAIRequest{Model: "gpt-test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not registered")

	_, err = ConvertRequest(nil, &convmeta.Values{}, types.RelayFormatOpenAIResponses, (*dto.GeneralOpenAIRequest)(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request is nil")
}

func TestConvertRequestByIDRejectsWrongSourceFormat(t *testing.T) {
	_, err := ConvertRequestByID(
		nil,
		&convmeta.Values{},
		ConverterOpenAIChatToOpenAIResponses,
		&dto.ClaudeRequest{Model: "claude-test"},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "expects openai request")
}

func TestConvertRequestRejectsUnregisteredExplicitPath(t *testing.T) {
	_, err := ConvertRequest(
		nil,
		&convmeta.Values{},
		types.RelayFormatEmbedding,
		&dto.ClaudeRequest{Model: "claude-test"},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "from claude to embedding is not registered")
}

func mustRawMessage(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := kitutil.Marshal(value)
	require.NoError(t, err)
	return raw
}

func inputContentText(t *testing.T, item map[string]any) string {
	t.Helper()
	content, ok := item["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	part, ok := content[0].(map[string]any)
	require.True(t, ok)
	text, ok := part["text"].(string)
	require.True(t, ok)
	return text
}

func TestConvertRequestRecordsResponsesCustomToolsForEachTarget(t *testing.T) {
	codexRequest := func(tools ...map[string]any) *dto.OpenAIResponsesRequest {
		return &dto.OpenAIResponsesRequest{
			Model:           "model-test",
			MaxOutputTokens: lo.ToPtr(uint(64)),
			Input:           mustMarshalRequestJSON(t, "hello"),
			Tools:           mustMarshalRequestJSON(t, tools),
		}
	}
	execTool := map[string]any{"type": "custom", "name": "exec", "description": "Run code", "format": map[string]any{"type": "text"}}
	waitTool := map[string]any{"type": "function", "name": "wait", "parameters": map[string]any{"type": "object"}}

	for _, target := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude, types.RelayFormatGemini} {
		t.Run(string(target), func(t *testing.T) {
			info := &convmeta.Values{}
			_, err := ConvertRequest(nil, info, target, codexRequest(execTool, waitTool))
			require.NoError(t, err)
			require.NotNil(t, info.ResponsesTools)
			assert.True(t, info.ResponsesTools.IsCustomTool("exec"))
			assert.False(t, info.ResponsesTools.IsCustomTool("wait"))

			_, err = ConvertRequest(nil, info, target, codexRequest(waitTool))
			require.NoError(t, err)
			assert.Nil(t, info.ResponsesTools, "a retry without custom tools must not keep the previous record")
		})
	}
}

func mustMarshalRequestJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := kitutil.Marshal(value)
	require.NoError(t, err)
	return raw
}

func TestIRRequestPreservesExplicitZeroAndFalseAcrossProtocols(t *testing.T) {
	sources := []struct {
		format  types.RelayFormat
		raw     string
		request any
	}{
		{types.RelayFormatOpenAI, `{"model":"test","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"none","max_tokens":0,"temperature":0,"top_p":0,"stream":false}`, &dto.GeneralOpenAIRequest{}},
		{types.RelayFormatClaude, `{"model":"test","messages":[{"role":"user","content":"hello"}],"max_tokens":0,"temperature":0,"top_p":0,"stream":false}`, &dto.ClaudeRequest{}},
		{types.RelayFormatOpenAIResponses, `{"model":"test","input":"hello","reasoning":{"effort":"none"},"max_output_tokens":0,"temperature":0,"top_p":0,"stream":false}`, &dto.OpenAIResponsesRequest{}},
		{types.RelayFormatGemini, `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":0},"maxOutputTokens":0,"temperature":0,"topP":0}}`, &dto.GeminiChatRequest{}},
	}
	for _, source := range sources {
		require.NoError(t, kitutil.UnmarshalJsonStr(source.raw, source.request))
		for _, target := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude, types.RelayFormatOpenAIResponses, types.RelayFormatGemini} {
			if source.format == target {
				continue
			}
			t.Run(string(source.format)+"_to_"+string(target), func(t *testing.T) {
				result, err := ConvertRequest(context.Background(), &convmeta.Values{UpstreamModelName: "test"}, target, source.request)
				require.NoError(t, err)
				var maxTokens *uint
				var temperature, topP *float64
				switch value := result.Value.(type) {
				case *dto.GeneralOpenAIRequest:
					maxTokens, temperature, topP = value.MaxCompletionTokens, value.Temperature, value.TopP
					if maxTokens == nil {
						maxTokens = value.MaxTokens
					}
					if source.format != types.RelayFormatGemini {
						require.NotNil(t, value.Stream)
						assert.False(t, *value.Stream)
					}
				case *dto.ClaudeRequest:
					maxTokens, temperature, topP = value.MaxTokens, value.Temperature, value.TopP
					if source.format != types.RelayFormatGemini {
						require.NotNil(t, value.Stream)
						assert.False(t, *value.Stream)
					}
				case *dto.OpenAIResponsesRequest:
					maxTokens, temperature, topP = value.MaxOutputTokens, value.Temperature, value.TopP
					if source.format != types.RelayFormatGemini {
						require.NotNil(t, value.Stream)
						assert.False(t, *value.Stream)
					}
				case *dto.GeminiChatRequest:
					maxTokens, temperature, topP = value.GenerationConfig.MaxOutputTokens, value.GenerationConfig.Temperature, value.GenerationConfig.TopP
				}
				require.NotNil(t, maxTokens)
				assert.Zero(t, *maxTokens)
				require.NotNil(t, temperature)
				assert.Zero(t, *temperature)
				require.NotNil(t, topP)
				assert.Zero(t, *topP)
			})
		}
	}
}

func TestGeminiToOpenAIChatPreservesTextAlongsideToolCalls(t *testing.T) {
	const text = "I will check the weather."
	call := dto.GeminiPart{FunctionCall: &dto.FunctionCall{
		ID: "call_weather", FunctionName: "get_weather", Arguments: map[string]any{"city": "Paris"},
	}}
	for _, tt := range []struct {
		name      string
		parts     []dto.GeminiPart
		wantText  []string
		wantCalls int
	}{
		{name: "text and call", parts: []dto.GeminiPart{{Text: text}, call}, wantText: []string{text}, wantCalls: 1},
		{name: "text around call", parts: []dto.GeminiPart{{Text: text}, call, {Text: "Please wait."}}, wantText: []string{text, "Please wait."}, wantCalls: 1},
		{name: "call only", parts: []dto.GeminiPart{call}, wantCalls: 1},
		{name: "text only", parts: []dto.GeminiPart{{Text: text}}, wantText: []string{text}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{Role: "model", Parts: tt.parts}}}
			result, err := ConvertRequest(context.Background(), &convmeta.Values{
				UpstreamModelName: "gpt-4o", ChannelMetaAttached: true,
			}, types.RelayFormatOpenAI, request)
			require.NoError(t, err)
			converted, ok := result.Value.(*dto.GeneralOpenAIRequest)
			require.True(t, ok)
			// Verify the JSON sent upstream, including custom DTO serialization.
			body, err := kitutil.Marshal(converted)
			require.NoError(t, err)
			var wire dto.GeneralOpenAIRequest
			require.NoError(t, kitutil.Unmarshal(body, &wire))
			require.Len(t, wire.Messages, 1)
			assistant := wire.Messages[0]
			assert.Equal(t, "assistant", assistant.Role)
			content := assistant.ParseContent()
			require.Len(t, content, len(tt.wantText))
			for i, want := range tt.wantText {
				assert.Equal(t, "text", content[i].Type)
				assert.Equal(t, want, content[i].Text)
			}
			calls := assistant.ParseToolCalls()
			require.Len(t, calls, tt.wantCalls)
			if tt.wantCalls > 0 {
				assert.Equal(t, "call_weather", calls[0].ID)
				assert.Equal(t, "get_weather", calls[0].Function.Name)
				assert.JSONEq(t, `{"city":"Paris"}`, calls[0].Function.Arguments)
			}
		})
	}
}

func TestConvertRequestPreservesSchemaPropertyOrder(t *testing.T) {
	const schema = `{"type":"object","properties":{"reasoning":{"type":"string"},"answer":{"type":"string"}}}`
	for _, tc := range []struct {
		name   string
		target types.RelayFormat
		input  any
		body   string
	}{
		{
			name: "Chat structured output and tools to Responses", target: types.RelayFormatOpenAIResponses,
			input: &dto.GeneralOpenAIRequest{},
			body:  `{"model":"test","messages":[{"role":"user","content":"hello"}],"response_format":{"type":"json_schema","json_schema":{"name":"result","schema":` + schema + `}},"tools":[{"type":"function","function":{"name":"lookup","parameters":` + schema + `}}]}`,
		},
		{
			name: "Responses tools to Claude", target: types.RelayFormatClaude,
			input: &dto.OpenAIResponsesRequest{},
			body:  `{"model":"test","input":"hello","max_output_tokens":1024,"tools":[{"type":"function","name":"lookup","parameters":` + schema + `}]}`,
		},
		{
			name: "Claude tools to Chat", target: types.RelayFormatOpenAI,
			input: &dto.ClaudeRequest{},
			body:  `{"model":"test","messages":[{"role":"user","content":"hello"}],"max_tokens":1024,"tools":[{"name":"lookup","input_schema":` + schema + `}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, kitutil.UnmarshalJsonStr(tc.body, tc.input))
			result, err := ConvertRequest(t.Context(), &convmeta.Values{UpstreamModelName: "test"}, tc.target, tc.input)
			require.NoError(t, err)
			body, err := kitutil.Marshal(result.Value)
			require.NoError(t, err)
			assert.Contains(t, string(body), schema)
			if tc.target == types.RelayFormatOpenAIResponses {
				assert.Contains(t, string(body), `"schema":`+schema)
				assert.Contains(t, string(body), `"parameters":`+schema)
			}
		})
	}
}
