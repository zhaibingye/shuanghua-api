package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/copier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiContentSurvivesRequestRelay(t *testing.T) {
	const functionHistory = `{"role":"model","parts":[
		{"functionCall":{"id":"call_a","name":"lookup","args":{"count":0}},"thoughtSignature":"c2lnbmF0dXJl"},
		{"functionCall":{"id":"call_b","name":"lookup","args":{"enabled":false}}}
	]},{"role":"user","parts":[
		{"functionResponse":{"id":"call_b","name":"lookup","response":{"result":false}}},
		{"functionResponse":{"id":"call_a","name":"lookup","response":{"result":0}}}
	]}`
	const userTurn = `{"role":"user","parts":[{"text":"Look up the results."}]}`
	const omittedHistoryTurn = `{"role":"user","parts":[{"text":"Earlier conversation context was omitted."}]}`
	tests := []struct {
		name  string
		input string
		want  string
		extra string
	}{
		{
			name:  "function history",
			input: "[" + userTurn + "," + functionHistory + "]",
		},
		{
			name:  "clipped function history",
			input: "[" + functionHistory + "," + userTurn + "]",
			want:  "[" + omittedHistoryTurn + "," + functionHistory + "," + userTurn + "]",
		},
		{
			name:  "function history after cached content",
			input: "[" + functionHistory + "]",
			extra: `,"cachedContent":"cachedContents/history"`,
		},
		{
			name:  "function history with a continuation token",
			input: "[" + functionHistory + "]",
			extra: `,"continuationToken":"previous-turn"`,
		},
		{
			name: "snake case function history",
			input: "[" + userTurn + `,{"role":"model","parts":[{"function_call":{"id":"call_a","name":"lookup","args":{}},"thought_signature":"c2lnbmF0dXJl"}]},
				{"role":"user","parts":[{"function_response":{"id":"call_a","name":"lookup","response":{"result":"ok"}}}]}]`,
			want: "[" + userTurn + `,{"role":"model","parts":[{"functionCall":{"id":"call_a","name":"lookup","args":{}},"thoughtSignature":"c2lnbmF0dXJl"}]},
				{"role":"user","parts":[{"functionResponse":{"id":"call_a","name":"lookup","response":{"result":"ok"}}}]}]`,
		},
		{
			name: "server side tool history",
			input: `[{"role":"model","parts":[
				{"toolCall":{"id":"search_1","toolType":"GOOGLE_SEARCH_WEB","args":{"queries":["weather"]}},"thoughtSignature":"c2lnbmF0dXJl"},
				{"toolResponse":{"id":"search_1","toolType":"GOOGLE_SEARCH_WEB","response":{"results":[{"title":"forecast","score":0}]}}},
				{"text":"Found the forecast"}
			]}]`,
		},
		{
			name: "snake case server side tool history",
			input: `[{"role":"model","parts":[
				{"tool_call":{"id":"search_1","tool_type":"GOOGLE_SEARCH_WEB","args":{"query":"weather"}},"thought_signature":"c2lnbmF0dXJl"},
				{"tool_response":{"id":"search_1","tool_type":"GOOGLE_SEARCH_WEB","response":{"result":false}}}
			]}]`,
			want: `[{"role":"model","parts":[
				{"toolCall":{"id":"search_1","tool_type":"GOOGLE_SEARCH_WEB","args":{"query":"weather"}},"thoughtSignature":"c2lnbmF0dXJl"},
				{"toolResponse":{"id":"search_1","tool_type":"GOOGLE_SEARCH_WEB","response":{"result":false}}}
			]}]`,
		},
		{
			name:  "signed empty text",
			input: `[{"role":"model","parts":[{"text":"","thoughtSignature":"c2lnbmF0dXJl"},{"text":"answer"}]}]`,
		},
		{
			name:  "empty text",
			input: `[{"role":"user","parts":[{"text":""},{"text":"hello"}]}]`,
		},
		{
			name:  "absent text remains absent",
			input: `[{"role":"model","parts":[{}, {"thoughtSignature":"c2lnbmF0dXJl"}]}]`,
		},
		{
			name:  "null text is not a data variant",
			input: `[{"role":"user","parts":[{"text":null}]}]`,
			want:  `[{"role":"user","parts":[{}]}]`,
		},
		{
			name: "media and code history",
			input: `[{"role":"user","parts":[
				{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}},
				{"fileData":{"mimeType":"video/mp4","fileUri":"https://example.com/video.mp4"},"videoMetadata":{"startOffset":"0s"},"mediaResolution":{"level":"MEDIA_RESOLUTION_LOW"}}
			]},{"role":"model","parts":[
				{"executableCode":{"language":"PYTHON","code":"print(1)"}},
				{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1"}}
			]}]`,
		},
		{
			name: "snake case media and code history",
			input: `[{"role":"user","parts":[
				{"inline_data":{"mime_type":"image/png","data":"aW1hZ2U="}},
				{"file_data":{"mime_type":"video/mp4","file_uri":"https://example.com/video.mp4"},"video_metadata":{"startOffset":"0s"},"media_resolution":{"level":"MEDIA_RESOLUTION_LOW"}}
			]},{"role":"model","parts":[
				{"executable_code":{"language":"PYTHON","code":"print(1)"}},
				{"code_execution_result":{"outcome":"OUTCOME_OK","output":"1"}}
			]}]`,
			want: `[{"role":"user","parts":[
				{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}},
				{"fileData":{"mimeType":"video/mp4","fileUri":"https://example.com/video.mp4"},"videoMetadata":{"startOffset":"0s"},"mediaResolution":{"level":"MEDIA_RESOLUTION_LOW"}}
			]},{"role":"model","parts":[
				{"executableCode":{"language":"PYTHON","code":"print(1)"}},
				{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1"}}
			]}]`,
		},
	}

	for _, apiType := range []int{constant.APITypeGemini, constant.APITypeNewAPI} {
		adaptor := GetAdaptor(apiType)
		t.Run(adaptor.GetChannelName(), func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					want := tt.want
					if want == "" {
						want = tt.input
					}
					var source dto.GeminiChatRequest
					require.NoError(t, common.Unmarshal([]byte(`{"contents":`+tt.input+tt.extra+`}`), &source))
					request, err := common.DeepCopy(&source)
					require.NoError(t, err)
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-2.5-flash"}}
					converted, err := adaptor.ConvertGeminiRequest(c, info, request)
					require.NoError(t, err)
					encoded, err := common.Marshal(converted)
					require.NoError(t, err)
					assert.JSONEq(t, want, gjson.GetBytes(encoded, "contents").Raw)

					// Replaying the history on another turn must remain lossless.
					var replay dto.GeminiChatRequest
					require.NoError(t, common.Unmarshal(encoded, &replay))
					replayedRequest, err := adaptor.ConvertGeminiRequest(c, info, &replay)
					require.NoError(t, err)
					replayed, err := common.Marshal(replayedRequest)
					require.NoError(t, err)
					assert.JSONEq(t, want, gjson.GetBytes(replayed, "contents").Raw)
				})
			}
		})
	}
}

func TestRequestDeepCopyResponses(t *testing.T) {
	t.Run("nil source", func(t *testing.T) {
		clone, err := common.DeepCopy[dto.OpenAIResponsesRequest](nil)
		require.Error(t, err)
		assert.Nil(t, clone)
	})

	for _, raw := range []json.RawMessage{nil, {}, []byte(`null`), []byte(`false`), []byte(`0`), []byte(`[]`), []byte(`{"x":1}`)} {
		t.Run(fmt.Sprintf("raw_%q_nil_%t", raw, raw == nil), func(t *testing.T) {
			src := &dto.OpenAIResponsesRequest{}
			populateResponsesCloneFixture(t, reflect.ValueOf(src).Elem(), raw)
			want := &dto.OpenAIResponsesRequest{}
			populateResponsesCloneFixture(t, reflect.ValueOf(want).Elem(), raw)
			clone, err := common.DeepCopy(src)
			require.NoError(t, err)
			assert.Equal(t, src, clone, "including internal state and explicit zero pointers")

			// Mutate every reachable field, including nested pointers and raw bytes.
			// A later channel attempt must still see the original request.
			populateResponsesCloneFixture(t, reflect.ValueOf(clone).Elem(), []byte(`true`))
			assert.Equal(t, want, src)
			retry, err := common.DeepCopy(src)
			require.NoError(t, err)
			assert.Equal(t, want, retry)
		})
	}

	t.Run("absent optional fields", func(t *testing.T) {
		src := &dto.OpenAIResponsesRequest{Model: "gpt-4.1", Input: []byte(`"hello"`)}
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)
		assert.Equal(t, src, clone)
		clone.Input[1] = 'H'
		assert.Equal(t, json.RawMessage(`"hello"`), src.Input)
	})

	t.Run("large input", func(t *testing.T) {
		input := bytes.Repeat([]byte("a"), 1<<20)
		input[0], input[len(input)-1] = '"', '"'
		src := &dto.OpenAIResponsesRequest{Model: "gpt-4.1", Input: input}
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)
		assert.Equal(t, src, clone)
		clone.Input[len(input)/2] = 'b'
		assert.Equal(t, byte('a'), src.Input[len(input)/2])
	})

	t.Run("model mapping on successive channel attempts", func(t *testing.T) {
		src := &dto.OpenAIResponsesRequest{Model: "client-model", Input: []byte(`"hello"`)}
		for _, model := range []string{"first-channel-model", "retry-channel-model"} {
			request, err := common.DeepCopy(src)
			require.NoError(t, err)
			assert.Equal(t, json.RawMessage(`"hello"`), request.Input)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set("model_mapping", fmt.Sprintf(`{"client-model":%q}`, model))
			info := &relaycommon.RelayInfo{OriginModelName: src.Model}
			require.NoError(t, helper.ModelMappedHelper(c, info, request))
			assert.Equal(t, model, request.Model)
			assert.Equal(t, "client-model", src.Model)
			request.Input[1] = 'H'
			assert.Equal(t, json.RawMessage(`"hello"`), src.Input)
		}
	})
}

// Populate all mutable fields so newly added request fields also exercise the
// retry-isolation contract, rather than silently escaping a hand-written list.
func populateResponsesCloneFixture(t *testing.T, value reflect.Value, raw json.RawMessage) {
	t.Helper()
	switch value.Kind() {
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			populateResponsesCloneFixture(t, value.Field(i), raw)
		}
	case reflect.Pointer:
		if value.IsNil() {
			value.Set(reflect.New(value.Type().Elem()))
		}
		populateResponsesCloneFixture(t, value.Elem(), raw)
	case reflect.Slice:
		require.Equal(t, reflect.TypeFor[json.RawMessage](), value.Type())
		if value.Len() > 0 {
			value.Index(0).SetUint('!')
		}
		value.Set(reflect.ValueOf(json.RawMessage(bytes.Clone(raw))))
	case reflect.String:
		value.SetString(value.String() + "gpt-4.1")
	case reflect.Bool:
		value.SetBool(bytes.Equal(raw, []byte(`true`)))
	case reflect.Int:
		if bytes.Equal(raw, []byte(`true`)) {
			value.SetInt(1)
		}
	case reflect.Uint:
		if bytes.Equal(raw, []byte(`true`)) {
			value.SetUint(1)
		}
	case reflect.Float64:
		if bytes.Equal(raw, []byte(`true`)) {
			value.SetFloat(1)
		}
	default:
		require.FailNow(t, "extend clone fixture for new field type", "%s", value.Type())
	}
}

func TestRequestDeepCopyRawMutation(t *testing.T) {
	t.Run("edits before cloning are included", func(t *testing.T) {
		src := &dto.OpenAIResponsesRequest{Input: json.RawMessage(`"hello"`)}
		src.Input[1] = 'H'
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)
		assert.Equal(t, json.RawMessage(`"Hello"`), clone.Input)
	})

	t.Run("edits after cloning are isolated in both directions", func(t *testing.T) {
		input := make(json.RawMessage, len(`"hello"`), 32)
		copy(input, `"hello"`)
		src := &dto.OpenAIResponsesRequest{Input: input}
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)

		clone.Input[1] = 'H'
		assert.Equal(t, json.RawMessage(`"hello"`), src.Input)
		src.Input[2] = 'A'
		assert.Equal(t, json.RawMessage(`"Hello"`), clone.Input)
		clone.Input = append(clone.Input, ' ')
		src.Input = append(src.Input, '\n')
		assert.Equal(t, json.RawMessage("\"Hello\" "), clone.Input)
		assert.Equal(t, json.RawMessage("\"hAllo\"\n"), src.Input)
		clone.Input = json.RawMessage(`"replacement"`)
		assert.Equal(t, json.RawMessage("\"hAllo\"\n"), src.Input)
	})

	t.Run("raw map and slice entries retain nil and empty values", func(t *testing.T) {
		src := &struct {
			Fields map[string]json.RawMessage
			Items  []json.RawMessage
		}{
			Fields: map[string]json.RawMessage{"absent": nil, "empty": {}, "value": []byte(`false`)},
			Items:  []json.RawMessage{nil, {}, []byte(`0`)},
		}
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)
		assert.Equal(t, src, clone)
		clone.Fields["value"][0] = '!'
		clone.Items[2][0] = '1'
		delete(clone.Fields, "absent")
		assert.Contains(t, src.Fields, "absent")
		assert.Equal(t, json.RawMessage(`false`), src.Fields["value"])
		assert.Equal(t, json.RawMessage(`0`), src.Items[2])
	})
}

func TestRequestDeepCopyOtherProtocols(t *testing.T) {
	t.Run("chat nested fields remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"gpt-4.1","max_tokens":0,"stream":false,"thinking_budget":128,
			"messages":[{"role":"assistant","content":[{"type":"text","text":"hello"}],"tool_calls":[{"id":"call_1"}]}],
			"response_format":{"type":"json_schema","json_schema":{"strict":false}}
		}`, func(clone *dto.GeneralOpenAIRequest) {
			clone.Messages[0].ToolCalls[0] = '!'
			clone.Messages[0].Content.([]any)[0].(map[string]any)["text"] = "changed"
			clone.ResponseFormat.JsonSchema[0] = '!'
			*clone.MaxTokens = 1
			*clone.Stream = true
		})
	})

	t.Run("claude dynamic tools and raw fields remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"claude-sonnet-4-5","max_tokens":1024,"temperature":0,
			"tools":[{"name":"lookup","input_schema":{"type":"object"}}],
			"metadata":{"user_id":"test"},"messages":[{"role":"user","content":"hello"}]
		}`, func(clone *dto.ClaudeRequest) {
			clone.Metadata[0] = '!'
			clone.Tools.(json.RawMessage)[0] = '!'
			clone.Messages[0].Content = "changed"
			*clone.Temperature = 1
		})
	})

	t.Run("gemini nested parts and configuration remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"contents":[{"role":"model","parts":[{"text":"hello","thoughtSignature":"signature","inlineData":{"mimeType":"text/plain","data":"aGk="}}]}],
			"tools":[{"googleSearch":{}}],"generationConfig":{"responseJsonSchema":{"type":"object"}}
		}`, func(clone *dto.GeminiChatRequest) {
			clone.Contents[0].Parts[0].ThoughtSignature[1] = '!'
			clone.Contents[0].Parts[0].InlineData.Data = "changed"
			clone.Tools[0] = '!'
			clone.GenerationConfig.ResponseJsonSchema[0] = '!'
		})
	})

	t.Run("image extra parameters remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"gpt-image-1","prompt":"a cat","n":1,"watermark":false,
			"image":["https://example.com/image.png"],"parameters":{"seed":0}
		}`, func(clone *dto.ImageRequest) {
			clone.Image[0] = '!'
			clone.Extra["parameters"][0] = '!'
			clone.Extra["new_field"] = json.RawMessage(`true`)
			*clone.N = 2
			*clone.Watermark = true
		})
	})

	t.Run("audio raw parameters and speed remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"tts-1","input":"hello","voice":"alloy","speed":1,"metadata":{"tag":"test"},"ref_audio":"sample"
		}`, func(clone *dto.AudioRequest) {
			clone.Metadata[0] = '!'
			clone.RefAudio[1] = '!'
			*clone.Speed = 2
		})
	})

	t.Run("embedding dynamic input and dimensions remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{"model":"text-embedding-3-small","input":[[1,2],[3]],"dimensions":256}`,
			func(clone *dto.EmbeddingRequest) {
				clone.Input.([]any)[0].([]any)[0] = float64(9)
				*clone.Dimensions = 512
			})
	})

	t.Run("rerank documents and options remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"rerank-v3.5","query":"hello","documents":["first","second"],"top_n":1,"return_documents":false
		}`, func(clone *dto.RerankRequest) {
			clone.Documents[0] = "changed"
			*clone.TopN = 2
			*clone.ReturnDocuments = true
		})
	})
}

// A channel attempt may mutate its clone without changing the request used by
// later attempts. Decode the expected request independently to avoid aliases.
func assertRequestCloneIsolation[T any](t *testing.T, body string, mutate func(*T)) {
	t.Helper()
	var src, want T
	input := []byte(body)
	require.NoError(t, common.UnmarshalNoCopy(input, &src))
	require.NoError(t, common.UnmarshalJsonStr(body, &want))
	clone, err := common.DeepCopy(&src)
	require.NoError(t, err)
	require.Equal(t, &want, clone)
	wantJSON, err := common.Marshal(&want)
	require.NoError(t, err)
	cloneJSON, err := common.Marshal(clone)
	require.NoError(t, err)
	assert.Equal(t, string(wantJSON), string(cloneJSON), "preserve the outbound JSON")
	mutate(clone)
	assert.Equal(t, want, src, "a channel attempt must not mutate the original request")
	assert.Equal(t, body, string(input), "borrowed input must remain unchanged after attempt edits")
	retry, err := common.DeepCopy(&src)
	require.NoError(t, err)
	assert.Equal(t, &want, retry)
}

func TestBorrowedResponsesRequestRetryBodies(t *testing.T) {
	previous := common.GetDiskCacheConfig()
	t.Cleanup(func() { common.SetDiskCacheConfig(previous) })
	settings := model_setting.GetGlobalSettings()
	previousPassthrough := settings.PassThroughRequestEnabled
	settings.PassThroughRequestEnabled = false
	t.Cleanup(func() { settings.PassThroughRequestEnabled = previousPassthrough })
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			common.SetDiskCacheConfig(common.DiskCacheConfig{Enabled: disk, ThresholdMB: 0, MaxSizeMB: 16, Path: t.TempDir()})
			const original = `{"model":"client-model","input":"original input","metadata":{"key":"original"}}`
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(original))
			c.Request.Header.Set("Content-Type", "application/json")
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "client-model")
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
			req, err := helper.GetAndValidateResponsesRequest(c)
			require.NoError(t, err)
			info := relaycommon.GenRelayInfoResponses(c, req)
			var attempts []common.ReplayableBody
			for _, model := range []string{"first-channel", "retry-channel"} {
				c.Set("model_mapping", fmt.Sprintf(`{"client-model":%q}`, model))
				common.SetContextKey(c, constant.ContextKeyChannelParamOverride, map[string]any{
					"operations": []any{
						map[string]any{"path": "input", "mode": "set", "value": model + " input"},
						map[string]any{"path": "metadata.key", "mode": "set", "value": model},
					},
				})
				_, body, closer, apiErr := PrepareResponsesRequest(c, info, req)
				require.Nil(t, apiErr)
				t.Cleanup(func() { require.NoError(t, closer.Close()) })
				attempts = append(attempts, body)
			}
			originalStorage, err := common.GetBodyStorage(c)
			require.NoError(t, err)
			originalBytes, err := originalStorage.Bytes()
			require.NoError(t, err)
			assert.Equal(t, original, string(originalBytes))
			common.CleanupBodyStorage(c)
			const replacement = `{"model":"replacement-model","input":"replacement input"}`
			storage, err := common.CreateBodyStorage([]byte(replacement))
			require.NoError(t, err)
			c.Set(common.KeyBodyStorage, storage)
			for i, model := range []string{"first-channel", "retry-channel"} {
				reader, err := attempts[i].NewReader()
				require.NoError(t, err)
				wire, err := io.ReadAll(reader)
				require.NoError(t, err)
				require.NoError(t, reader.Close())
				var got dto.OpenAIResponsesRequest
				require.NoError(t, common.Unmarshal(wire, &got))
				assert.Equal(t, model, got.Model)
				assert.JSONEq(t, fmt.Sprintf(`%q`, model+" input"), string(got.Input))
				assert.JSONEq(t, fmt.Sprintf(`{"key":%q}`, model), string(got.Metadata))
			}
			assert.Equal(t, "client-model", info.OriginModelName)
			assert.Equal(t, json.RawMessage(`"original input"`), req.Input)
			assert.Equal(t, json.RawMessage(`{"key":"original"}`), req.Metadata)
			next, err := helper.GetAndValidateResponsesRequest(c)
			require.NoError(t, err)
			assert.Equal(t, "replacement-model", next.Model)
			assert.Equal(t, json.RawMessage(`"replacement input"`), next.Input)
		})
	}
}

func BenchmarkRequestDeepCopy(b *testing.B) {
	for _, size := range []int{256, 4 << 10, 1 << 20, 10 << 20, 40 << 20} {
		input := bytes.Repeat([]byte("a"), size)
		input[0], input[len(input)-1] = '"', '"'
		src := &dto.OpenAIResponsesRequest{Model: "gpt-4.1", Input: input}
		for _, method := range []struct {
			name string
			copy func(*dto.OpenAIResponsesRequest) (*dto.OpenAIResponsesRequest, error)
		}{
			{"reflect", func(src *dto.OpenAIResponsesRequest) (*dto.OpenAIResponsesRequest, error) {
				var dst dto.OpenAIResponsesRequest
				err := copier.CopyWithOption(&dst, src, copier.Option{DeepCopy: true, IgnoreEmpty: true})
				return &dst, err
			}},
			{"raw_bytes", common.DeepCopy[dto.OpenAIResponsesRequest]},
		} {
			b.Run(fmt.Sprintf("%dB/%s", size, method.name), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(size))
				for b.Loop() {
					clone, err := method.copy(src)
					require.NoError(b, err)
					require.Len(b, clone.Input, size)
				}
			})
		}
	}
}
