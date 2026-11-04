package relayconvert

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/ir/project"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheControlsAcrossOpenAIProtocols(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request any
		raw     string
		target  types.RelayFormat
	}{
		{"chat to responses", &dto.GeneralOpenAIRequest{}, `{"model":"test","messages":[{"role":"user","content":"hello"}]}`, types.RelayFormatOpenAIResponses},
		{"responses to chat", &dto.OpenAIResponsesRequest{}, `{"model":"test","input":"hello"}`, types.RelayFormatOpenAI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw map[string]any
			require.NoError(t, kitutil.UnmarshalJsonStr(tc.raw, &raw))
			raw["prompt_cache_key"] = "tenant/stable-prefix"
			raw["prompt_cache_retention"] = "24h"
			raw["cache_salt"] = "tenant-isolation"
			encoded, err := kitutil.Marshal(raw)
			require.NoError(t, err)
			require.NoError(t, kitutil.Unmarshal(encoded, tc.request))
			before, err := kitutil.Marshal(tc.request)
			require.NoError(t, err)
			result, err := ConvertRequest(t.Context(), nil, tc.target, tc.request)
			require.NoError(t, err)
			encoded, err = kitutil.Marshal(result.Value)
			require.NoError(t, err)
			var converted map[string]any
			require.NoError(t, kitutil.Unmarshal(encoded, &converted))
			for _, field := range []string{"prompt_cache_key", "prompt_cache_retention", "cache_salt"} {
				assert.Equal(t, raw[field], converted[field], field)
			}
			after, err := kitutil.Marshal(tc.request)
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(after), "conversion must not modify the source")
			// A request without explicit controls must keep implicit provider caching.
			// Unmarshal into a fresh request: omitted JSON fields do not clear a Go struct.
			if tc.target == types.RelayFormatOpenAIResponses {
				tc.request = &dto.GeneralOpenAIRequest{}
			} else {
				tc.request = &dto.OpenAIResponsesRequest{}
			}
			require.NoError(t, kitutil.UnmarshalJsonStr(tc.raw, tc.request))
			result, err = ConvertRequest(t.Context(), nil, tc.target, tc.request)
			require.NoError(t, err)
			encoded, err = kitutil.Marshal(result.Value)
			require.NoError(t, err)
			converted = nil
			require.NoError(t, kitutil.Unmarshal(encoded, &converted))
			for _, field := range []string{"prompt_cache_key", "prompt_cache_retention", "cache_salt"} {
				assert.NotContains(t, converted, field)
			}
		})
	}
}

func TestUnsupportedCacheFieldsAreSilentlyDropped(t *testing.T) {
	for _, tc := range []struct {
		name    string
		from    types.RelayFormat
		request any
		raw     string
		targets []types.RelayFormat
		fields  []string
	}{
		{
			name: "Gemini cached content", from: types.RelayFormatGemini, request: &dto.GeminiChatRequest{},
			raw:     `{"cachedContent":"cachedContents/opaque-context","generationConfig":{"maxOutputTokens":2048},"contents":[{"role":"user","parts":[{"text":"inline prompt"}]}]}`,
			targets: []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, types.RelayFormatClaude}, fields: []string{"cachedContent"},
		},
		{
			name: "Responses cache policies", from: types.RelayFormatOpenAIResponses, request: &dto.OpenAIResponsesRequest{},
			raw:     `{"model":"test","input":"inline prompt","max_output_tokens":2048,"prompt_cache_options":{"mode":"explicit","prewarm":true,"ttl":"30m"}}`,
			targets: []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude, types.RelayFormatGemini}, fields: []string{"prompt_cache_options"},
		},
		{
			name: "Chat cache salt", from: types.RelayFormatOpenAI, request: &dto.GeneralOpenAIRequest{},
			raw:     `{"model":"test","messages":[{"role":"user","content":"inline prompt"}],"max_tokens":2048,"cache_salt":"tenant"}`,
			targets: []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatGemini}, fields: []string{"cache_salt"},
		},
		{
			name: "Responses cache salt", from: types.RelayFormatOpenAIResponses, request: &dto.OpenAIResponsesRequest{},
			raw:     `{"model":"test","input":"inline prompt","max_output_tokens":2048,"cache_salt":"tenant"}`,
			targets: []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatGemini}, fields: []string{"cache_salt"},
		},
		{
			name: "Non-string Responses cache key", from: types.RelayFormatOpenAIResponses, request: &dto.OpenAIResponsesRequest{},
			raw:     `{"model":"test","input":"inline prompt","max_output_tokens":2048,"prompt_cache_key":{"unsupported":true}}`,
			targets: []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude, types.RelayFormatGemini}, fields: []string{"prompt_cache_key"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, kitutil.UnmarshalJsonStr(tc.raw, tc.request))
			before, err := kitutil.Marshal(tc.request)
			require.NoError(t, err)
			for _, target := range tc.targets {
				info := &convmeta.Values{ChannelMetaAttached: true, UpstreamModelName: "test", Options: &convmeta.Options{ToolLossPolicy: types.ConversionLossPolicyStrict}}
				result, err := ConvertRequest(t.Context(), info, target, tc.request)
				require.NoError(t, err, target)
				encoded, err := kitutil.Marshal(result.Value)
				require.NoError(t, err)
				assert.Contains(t, string(encoded), "inline prompt")
				for _, field := range tc.fields {
					assert.NotContains(t, string(encoded), `"`+field+`"`, target)
					for _, diagnostic := range result.Diagnostics {
						assert.NotContains(t, diagnostic.Path, field, "cache omission must remain silent")
					}
				}
			}
			result, err := ConvertRequest(t.Context(), nil, tc.from, tc.request)
			require.NoError(t, err)
			assert.Same(t, tc.request, result.Value)
			after, err := kitutil.Marshal(tc.request)
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(after), "omission must not change the source request")
		})
	}
	request := &dto.GeminiChatRequest{CachedContent: "cachedContents/opaque-context", Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{{Text: "inline prompt"}}}}}
	result, err := ConvertRequestVia(t.Context(), nil, request, types.RelayFormatOpenAI, types.RelayFormatGemini)
	require.NoError(t, err)
	assert.Empty(t, result.Value.(*dto.GeminiChatRequest).CachedContent)
	assert.Equal(t, "cachedContents/opaque-context", request.CachedContent)
}

func TestClaudeCacheBreakpointsSurviveProjection(t *testing.T) {
	var request dto.ClaudeRequest
	require.NoError(t, kitutil.UnmarshalJsonStr(`{"model":"test","max_tokens":2048,
		"cache_control":{"type":"ephemeral","ttl":"1h"},
		"system":[{"type":"text","text":"Stable instructions","cache_control":{"type":"ephemeral","ttl":"1h"}}],
		"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"key":"x"},"cache_control":{"type":"ephemeral","ttl":"5m"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"value","cache_control":{"type":"ephemeral","ttl":"5m"}}],"cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`, &request))
	normalized, err := project.FromRequest(types.RelayFormatClaude, &request)
	require.NoError(t, err)
	converted, err := project.ToRequest(types.RelayFormatClaude, normalized)
	require.NoError(t, err)
	before, err := kitutil.Marshal(&request)
	require.NoError(t, err)
	after, err := kitutil.Marshal(converted)
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after))
	// The registry extracts tools before projecting through intermediate protocols.
	// Their original cache breakpoints must still be present at a Claude target.
	request.Messages = []dto.ClaudeMessage{{Role: "user", Content: "Use lookup"}}
	request.System = nil
	request.CacheControl = nil
	require.NoError(t, kitutil.UnmarshalJsonStr(`[{"name":"lookup","input_schema":{"type":"object","properties":{}},"cache_control":{"type":"ephemeral","ttl":"1h"}},
		{"type":"web_search_20250305","name":"web_search","cache_control":{"type":"ephemeral","ttl":"5m"}}]`, &request.Tools))
	result, err := ConvertRequestVia(t.Context(), nil, &request, types.RelayFormatOpenAI, types.RelayFormatClaude)
	require.NoError(t, err)
	claude := result.Value.(*dto.ClaudeRequest)
	encoded, err := kitutil.Marshal(claude.Tools)
	require.NoError(t, err)
	var tools []map[string]any
	require.NoError(t, kitutil.Unmarshal(encoded, &tools))
	require.Len(t, tools, 2)
	assert.Equal(t, map[string]any{"type": "ephemeral", "ttl": "1h"}, tools[0]["cache_control"])
	assert.Equal(t, map[string]any{"type": "ephemeral", "ttl": "5m"}, tools[1]["cache_control"])
}

func TestCacheUsageConversionMatrix(t *testing.T) {
	// Every source reports the same total input, cache read, and output. Claude
	// splits uncached input and writes out; the other formats include them.
	for _, source := range []struct {
		name     string
		format   types.RelayFormat
		response any
		raw      string
	}{
		{"chat", types.RelayFormatOpenAI, &dto.OpenAITextResponse{}, `{"id":"chat_1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":12,"total_tokens":112,"prompt_tokens_details":{"cached_tokens":60,"cache_write_tokens":20},"completion_tokens_details":{"reasoning_tokens":3}}}`},
		{"responses", types.RelayFormatOpenAIResponses, &dto.OpenAIResponsesResponse{}, `{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":100,"output_tokens":12,"total_tokens":112,"input_tokens_details":{"cached_tokens":60,"cache_write_tokens":20},"output_tokens_details":{"reasoning_tokens":3}}}`},
		{"claude", types.RelayFormatClaude, &dto.ClaudeResponse{}, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":20,"output_tokens":12,"cache_read_input_tokens":60,"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":5,"ephemeral_1h_input_tokens":15}}}`},
		{"gemini", types.RelayFormatGemini, &dto.GeminiChatResponse{}, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":9,"thoughtsTokenCount":3,"cachedContentTokenCount":60,"totalTokenCount":112}}`},
	} {
		for _, target := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, types.RelayFormatClaude, types.RelayFormatGemini} {
			t.Run(source.name+" to "+string(target), func(t *testing.T) {
				require.NoError(t, kitutil.UnmarshalJsonStr(source.raw, source.response))
				result, err := ConvertResponse(t.Context(), nil, target, source.response)
				require.NoError(t, err)
				require.NotNil(t, result.Usage)
				assert.Equal(t, 100, result.Usage.PromptTokens)
				assert.Equal(t, 60, result.Usage.PromptTokensDetails.CachedTokens)
				assert.Equal(t, 12, result.Usage.CompletionTokens)
				assert.Equal(t, 112, result.Usage.TotalTokens)
				switch value := result.Value.(type) {
				case *dto.GeminiChatResponse:
					assert.Equal(t, 60, value.UsageMetadata.CachedContentTokenCount)
					assert.Equal(t, 100, value.UsageMetadata.PromptTokenCount)
					assert.Equal(t, 112, value.UsageMetadata.TotalTokenCount)
					assert.Equal(t, 12, value.UsageMetadata.CandidatesTokenCount+value.UsageMetadata.ThoughtsTokenCount)
				case *dto.ClaudeResponse:
					require.NotNil(t, value.Usage)
					assert.Equal(t, 60, value.Usage.CacheReadInputTokens)
					assert.Equal(t, 100, value.Usage.InputTokens+value.Usage.CacheReadInputTokens+value.Usage.GetCacheCreationTotalTokens())
				}
				if source.format == target {
					return
				}
				// Exercise usage arriving only in a terminal stream envelope, as it
				// does after a cache hit on all four native protocols.
				var chunk any
				switch source.format {
				case types.RelayFormatOpenAI:
					chunk = &dto.ChatCompletionsStreamResponse{Usage: &source.response.(*dto.OpenAITextResponse).Usage}
				case types.RelayFormatOpenAIResponses:
					chunk = &dto.ResponsesStreamResponse{Type: "response.completed", Response: source.response.(*dto.OpenAIResponsesResponse)}
				case types.RelayFormatClaude:
					chunk = &dto.ClaudeResponse{Type: "message_delta", Usage: source.response.(*dto.ClaudeResponse).Usage, Delta: &dto.ClaudeMediaMessage{StopReason: kitutil.GetPointer("end_turn")}}
				default:
					chunk = source.response
				}
				state, err := NewResponseStreamState(source.format, target, ResponseStreamOptions{IncludeUsage: true, EmitSequenceNumber: true})
				require.NoError(t, err)
				info := &convmeta.Values{}
				results, err := ConvertStreamResponseChunk(t.Context(), info, state, chunk)
				require.NoError(t, err)
				final, err := FinalizeStreamResponse(t.Context(), info, state)
				require.NoError(t, err)
				results = append(results, final...)
				require.NotNil(t, state.Usage())
				assert.Equal(t, 60, state.Usage().PromptTokensDetails.CachedTokens)
				assert.Equal(t, 100, state.Usage().PromptTokens)
				if target == types.RelayFormatGemini {
					require.NotEmpty(t, results)
					var last *dto.GeminiChatResponse
					for _, result := range results {
						if value, ok := result.Value.(*dto.GeminiChatResponse); ok && value.HasUsageMetadata {
							last = value
						}
					}
					require.NotNil(t, last)
					assert.Equal(t, 60, last.UsageMetadata.CachedContentTokenCount)
					assert.Equal(t, 12, last.UsageMetadata.CandidatesTokenCount+last.UsageMetadata.ThoughtsTokenCount)
				}
			})
		}
	}
}

func TestChatCacheDetailSnapshotsAreIndependent(t *testing.T) {
	var source dto.Usage
	require.NoError(t, kitutil.UnmarshalJsonStr(`{"prompt_tokens":100,"completion_tokens":12,"prompt_tokens_details":{"cached_tokens":60,"cache_write_tokens":20,"cached_tokens_details":{"image_tokens":0,"text_tokens":60}}}`, &source))
	result := UsageFromChatUsage(&source)
	require.NotNil(t, result.InputTokensDetails)
	require.NotNil(t, result.PromptTokensDetails.CachedTokensDetails)
	require.NotNil(t, result.InputTokensDetails.CachedTokensDetails)
	*result.PromptTokensDetails.CachedTokensDetails.TextTokens = 1
	*result.InputTokensDetails.CachedTokensDetails.ImageTokens = 2
	assert.Equal(t, 60, *source.PromptTokensDetails.CachedTokensDetails.TextTokens)
	assert.Zero(t, *source.PromptTokensDetails.CachedTokensDetails.ImageTokens)
	assert.Equal(t, 60, *result.InputTokensDetails.CachedTokensDetails.TextTokens)
	assert.Zero(t, *result.PromptTokensDetails.CachedTokensDetails.ImageTokens)
	require.NotNil(t, result.BillingUsage)
	assert.Equal(t, 60, result.BillingUsage.OpenAIUsage.PromptTokensDetails.CachedTokens)
	source = dto.Usage{PromptTokens: 100, PromptCacheHitTokens: 60}
	result = UsageFromChatUsage(&source)
	assert.Equal(t, 60, result.PromptTokensDetails.CachedTokens)
	require.NotNil(t, result.InputTokensDetails)
	assert.Equal(t, 60, result.InputTokensDetails.CachedTokens)
}
