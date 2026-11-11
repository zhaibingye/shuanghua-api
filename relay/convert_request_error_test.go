package relay

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	kitreasoning "github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type imageReservation struct {
	held, limit int
}

func TestReasoningBudgetsInNativeUpstreamRequests(t *testing.T) {
	service.InitHttpClient()
	received := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			received <- body
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"fixture upstream failure"}}`)
	}))
	t.Cleanup(upstream.Close)

	for _, tc := range []struct {
		name, model, controls, wantEffort, wantBudget string
		format                                        types.RelayFormat
		passThrough                                   bool
	}{
		{"Argon budget", "gemini-4-argon", `"thinkingBudget":8192,"includeThoughts":false`, "HIGH", "", types.RelayFormatGemini, false},
		{"Argon explicit level wins", "gemini-4-argon", `"thinkingBudget":0,"thinkingLevel":"LOW"`, "LOW", "", types.RelayFormatGemini, false},
		{"Argon zero budget uses lowest level", "gemini-4-argon", `"thinkingBudget":0`, "LOW", "", types.RelayFormatGemini, false},
		{"Argon dynamic budget uses native default", "gemini-4-argon", `"thinkingBudget":-1`, "", "", types.RelayFormatGemini, false},
		{"Gemini 2.5 budget", "gemini-2.5-flash", `"thinkingBudget":8192`, "", "8192", types.RelayFormatGemini, false},
		{"Gemini body passthrough", "gemini-4-argon", `"thinkingBudget":8192`, "", "8192", types.RelayFormatGemini, true},
		{"Chat budget", "gpt-5.4", `"thinking_budget":8192`, "xhigh", "", types.RelayFormatOpenAI, false},
		{"Chat nested budget and explicit effort", "gpt-5.4", `"reasoning":{"max_tokens":8192,"effort":"low"}`, "low", "", types.RelayFormatOpenAI, false},
		{"Chat older effort", "gpt-5", `"thinking_budget":8192`, "high", "", types.RelayFormatOpenAI, false},
		{"Responses budget preserves summary", "gpt-5.4", `"thinking_budget":8192,"reasoning":{"summary":"auto"}`, "xhigh", "", types.RelayFormatOpenAIResponses, false},
		{"Responses explicit off wins", "gpt-5.4", `"thinking_budget":8192,"reasoning":{"effort":"none"}`, "none", "", types.RelayFormatOpenAIResponses, false},
		{"Claude budget", "claude-opus-4-7", `"thinking":{"type":"enabled","budget_tokens":8192}`, "xhigh", "", types.RelayFormatClaude, false},
		{"Claude explicit effort wins", "claude-opus-4-7", `"thinking":{"type":"enabled","budget_tokens":8192},"output_config":{"effort":"low"}`, "low", "", types.RelayFormatClaude, false},
		{"Claude 4.6 falls back to high", "claude-sonnet-4-6", `"thinking":{"type":"enabled","budget_tokens":8192}`, "high", "", types.RelayFormatClaude, false},
		{"Claude manual budget", "claude-opus-4-5", `"thinking":{"type":"enabled","budget_tokens":8192}`, "", "8192", types.RelayFormatClaude, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channelType := constant.ChannelTypeOpenAI
			var path, body, effortPath, budgetPath, outputPath string
			var handle func(*gin.Context, *relaycommon.RelayInfo) *types.NewAPIError
			switch tc.format {
			case types.RelayFormatGemini:
				channelType = constant.ChannelTypeGemini
				path = "/v1beta/models/alias:streamGenerateContent"
				body = `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":16384,"thinkingConfig":{` + tc.controls + `}}}`
				effortPath, budgetPath, outputPath = "generationConfig.thinkingConfig.thinkingLevel", "generationConfig.thinkingConfig.thinkingBudget", "generationConfig.maxOutputTokens"
				handle = GeminiHelper
			case types.RelayFormatOpenAI:
				path = "/v1/chat/completions"
				body = `{"model":"alias","messages":[{"role":"user","content":"hello"}],"max_completion_tokens":16384,` + tc.controls + `}`
				effortPath, budgetPath, outputPath = "reasoning_effort", "thinking_budget", "max_completion_tokens"
				handle = TextHelper
			case types.RelayFormatOpenAIResponses:
				path = "/v1/responses"
				body = `{"model":"alias","input":"hello","max_output_tokens":16384,` + tc.controls + `}`
				effortPath, budgetPath, outputPath = "reasoning.effort", "thinking_budget", "max_output_tokens"
				handle = ResponsesHelper
			case types.RelayFormatClaude:
				channelType = constant.ChannelTypeAnthropic
				path = "/v1/messages"
				body = `{"model":"alias","messages":[{"role":"user","content":"hello"}],"max_tokens":16384,` + tc.controls + `}`
				effortPath, budgetPath, outputPath = "output_config.effort", "thinking.budget_tokens", "max_tokens"
				handle = ClaudeHelper
			}
			require.NotNil(t, handle)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "alias")
			common.SetContextKey(c, constant.ContextKeyChannelType, channelType)
			common.SetContextKey(c, constant.ContextKeyChannelModelMapping, `{"alias":"`+tc.model+`"}`)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
			common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: tc.passThrough})
			request, err := helper.GetAndValidateRequest(c, tc.format)
			require.NoError(t, err)
			info, err := relaycommon.GenRelayInfo(c, tc.format, request, nil)
			require.NoError(t, err)
			apiErr := handle(c, info)
			require.NotNil(t, apiErr)
			require.Equal(t, http.StatusBadGateway, apiErr.StatusCode, "%v", apiErr)
			require.Len(t, received, 1)
			outbound := <-received
			assert.Equal(t, tc.wantEffort, gjson.GetBytes(outbound, effortPath).Str)
			assert.Equal(t, tc.wantBudget, gjson.GetBytes(outbound, budgetPath).Raw)
			assert.Equal(t, int64(16384), gjson.GetBytes(outbound, outputPath).Int())
			assert.False(t, gjson.GetBytes(outbound, "reasoning.max_tokens").Exists())
			if tc.format == types.RelayFormatClaude && tc.wantEffort != "" {
				assert.Equal(t, "adaptive", gjson.GetBytes(outbound, "thinking.type").Str)
			}
			if strings.Contains(tc.controls, `"summary":"auto"`) {
				assert.Equal(t, "auto", gjson.GetBytes(outbound, "reasoning.summary").Str)
			}
			if strings.Contains(tc.controls, `"includeThoughts":false`) {
				assert.Equal(t, "false", gjson.GetBytes(outbound, "generationConfig.thinkingConfig.includeThoughts").Raw)
			}
			if tc.passThrough {
				assert.Equal(t, body, string(outbound))
			}
		})
	}
}

func TestNativeGeminiSearchAndFunctionTools(t *testing.T) {
	service.InitHttpClient()
	received := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			received <- body
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"fixture upstream failure"}}`)
	}))
	t.Cleanup(upstream.Close)

	const functions = `[{"functionDeclarations":[{"name":"lookup","parametersJsonSchema":{"type":"object"}}]}]`
	const mixed = `[{"googleSearch":{}},{"google_search_retrieval":{}},{"enterpriseWebSearch":{}},{"functionDeclarations":[{"name":"lookup","parametersJsonSchema":{"type":"object"}}]}]`
	const combined = `{"google_search":{},"function_declarations":[{"name":"lookup"}],"urlContext":{},"vendorTool":{"value":9007199254740993}}`
	const combinedWithoutSearch = `{"function_declarations":[{"name":"lookup"}],"urlContext":{},"vendorTool":{"value":9007199254740993}}`
	const searchOnly = `[{"googleSearch":{"excludeDomains":["example.test"]}},{"function_declarations":[]},{"codeExecution":{}}]`
	const skillPrompt = "<role>assistant</role>\n<available_skills>\nUse the lookup tool.\n</available_skills>"
	const cleanSkillPrompt = "<role>assistant</role>\n\nUse the lookup tool.\n"
	const clientToolConfig = `,"toolConfig":{"function_calling_config":{"mode":"ANY","allowed_function_names":["lookup"],"stream_function_call_arguments":false,"clientOnly":true}}`
	const nativeToolConfig = `{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["lookup"],"streamFunctionCallArguments":false}}`
	for _, tc := range []struct {
		name, tools, want string
		channelType       int
		policy            types.ConversionLossPolicy
		passthrough       bool
	}{
		{name: "function tools suppress preceding searches", tools: mixed, want: functions},
		{name: "mixed native object retains its other fields", tools: combined, want: combinedWithoutSearch},
		{name: "search and hosted code execution stay enabled", tools: searchOnly, want: searchOnly},
		{name: "functions only", tools: functions, want: functions},
		{name: "safe policy rejects removal", tools: mixed, policy: types.ConversionLossPolicySafe},
		{name: "strict policy rejects removal", tools: mixed, policy: types.ConversionLossPolicyStrict},
		{name: "request body passthrough", tools: mixed, want: mixed, passthrough: true},
		{name: "native format on another channel", tools: mixed, want: mixed, channelType: constant.ChannelTypeNewAPI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt, err := common.Marshal(skillPrompt)
			require.NoError(t, err)
			toolConfig := clientToolConfig
			if tc.tools == searchOnly {
				toolConfig = ""
			}
			body := `{"contents":[{"role":"user","parts":[{"text":"<available_skills>literal user message</available_skills>"}]}],` +
				`"system_instruction":{"parts":[{"text":` + string(prompt) + `}]},"tools":` + tc.tools + toolConfig + `}`
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-4-argon:streamGenerateContent?alt=sse", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			channelType := tc.channelType
			if channelType == 0 {
				channelType = constant.ChannelTypeGemini
			}
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "gemini-4-argon")
			common.SetContextKey(c, constant.ContextKeyChannelType, channelType)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
			common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: tc.passthrough})
			common.SetContextKey(c, constant.ContextKeyChannelOtherSetting, dto.ChannelOtherSettings{ToolLossPolicy: string(tc.policy)})
			request, err := helper.GetAndValidateGeminiRequest(c)
			require.NoError(t, err)
			info := relaycommon.GenRelayInfoGemini(c, request)
			apiErr := GeminiHelper(c, info)
			require.NotNil(t, apiErr)
			if tc.want == "" {
				assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
				assert.Empty(t, received)
				assert.True(t, hasHostDiagnosticCode(info.ConversionDiagnostics(), "hosted_web_search_omitted"))
				return
			}
			require.Equal(t, http.StatusBadGateway, apiErr.StatusCode, "%v", apiErr)
			require.Len(t, received, 1)
			outbound := <-received
			gotTools := gjson.GetBytes(outbound, "tools").Raw
			assert.JSONEq(t, tc.want, gotTools)
			assert.Equal(t, tc.want != tc.tools, hasHostDiagnosticCode(info.ConversionDiagnostics(), "hosted_web_search_omitted"))
			if tc.tools == combined {
				assert.Equal(t, "9007199254740993", gjson.Get(gotTools, "vendorTool.value").Raw)
			}
			if tc.passthrough {
				assert.Equal(t, body, string(outbound))
				return
			}
			wantPrompt := skillPrompt
			if channelType == constant.ChannelTypeGemini {
				wantPrompt = cleanSkillPrompt
			}
			assert.Equal(t, wantPrompt, gjson.GetBytes(outbound, "systemInstruction.parts.0.text").String())
			assert.Equal(t, "<available_skills>literal user message</available_skills>", gjson.GetBytes(outbound, "contents.0.parts.0.text").String())
			if toolConfig != "" {
				assert.JSONEq(t, nativeToolConfig, gjson.GetBytes(outbound, "toolConfig").Raw)
			}
		})
	}
}

func (s *imageReservation) Reserve(quota int) error {
	if quota > s.limit {
		return errors.New("insufficient image quota")
	}
	s.held = max(s.held, quota)
	return nil
}
func (s *imageReservation) GetPreConsumedQuota() int { return s.held }
func (*imageReservation) Settle(int) error           { return nil }
func (*imageReservation) Refund(*gin.Context)        {}
func (*imageReservation) NeedsRefund() bool          { return false }

func (s *imageReservation) ReserveBeforeSubmit(quota int) error { return s.Reserve(quota) }

func TestImageRequestReservesFinalQuantityBeforeUpstream(t *testing.T) {
	service.InitHttpClient()
	for _, tc := range []struct {
		name, body                                    string
		tiered, passThrough, insufficient, retryReset bool
		override                                      any
		count, status                                 int
	}{
		{name: "top-level count", body: `{"model":"gpt-image-2","n":2,"parameters":{}}`, count: 2},
		{name: "legacy channel quantity override", body: `{"model":"gpt-image-2","n":1}`, override: 4, count: 4},
		{name: "expression channel quantity override", body: `{"model":"gpt-image-2","n":1}`, override: 4, count: 4, tiered: true},
		{name: "pass-through quantity", body: `{"model":"gpt-image-2","n":2,"parameters":{}}`, count: 2, passThrough: true},
		{name: "oversized override rejected", body: `{"model":"gpt-image-2","n":1}`, override: 129, status: http.StatusBadRequest},
		{name: "insufficient reservation blocks upstream", body: `{"model":"gpt-image-2","n":1}`, override: 4, count: 4, insufficient: true, status: http.StatusForbidden},
		{name: "retry drops the previous attempt's quantity", body: `{"model":"gpt-image-2","n":1}`, count: 1, retryReset: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			received := make(chan []byte, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err == nil {
					received <- body
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `{"error":{"message":"fixture upstream failure","type":"upstream_error"}}`)
			}))
			t.Cleanup(upstream.Close)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
			common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: tc.passThrough})
			if tc.override != nil {
				common.SetContextKey(c, constant.ContextKeyChannelParamOverride, map[string]any{"operations": []any{map[string]any{"path": "n", "mode": "set", "value": tc.override}}})
			}
			request, err := helper.GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesGenerations)
			require.NoError(t, err)
			reservation := &imageReservation{held: 20000, limit: 500000}
			if tc.insufficient {
				reservation.limit = reservation.held
			}
			info := &relaycommon.RelayInfo{Request: request, OriginModelName: "gpt-image-2", RelayMode: relayconstant.RelayModeImagesGenerations,
				RequestURLPath: c.Request.URL.Path, Billing: reservation,
				PriceData: hosttypes.PriceData{UsePrice: true, ModelPrice: 0.04, GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}},
			}
			if tc.tiered {
				expr := `tier("image", fixed(0.04)) * image_count`
				info.TieredBillingSnapshot = &billingexpr.BillingSnapshot{BillingMode: "tiered_expr", ExprString: expr, ExprHash: billingexpr.ExprHashString(expr), GroupRatio: 1, QuotaPerUnit: common.QuotaPerUnit, EstimatedImageCount: common.GetPointer(1)}
				info.BillingRequestInput = &billingexpr.RequestInput{Body: []byte(tc.body), ImageCount: common.GetPointer(1)}
				info.PriceData.UsePrice = false
			}
			if tc.retryReset {
				reservation.held = 80000
				info.PriceData.AddOtherRatio("n", 4)
				info.BillingImageCount = common.GetPointer(3)
			}
			apiErr := ImageHelper(c, info)
			require.NotNil(t, apiErr)
			if tc.status != 0 {
				assert.Equal(t, tc.status, apiErr.StatusCode)
				assert.Empty(t, received, "no upstream submission before quantity validation and reservation")
				return
			}
			assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
			require.Len(t, received, 1)
			body := <-received
			assert.Equal(t, int64(tc.count), gjson.GetBytes(body, "n").Int())
			assert.Equal(t, tc.count*20000, info.PriceData.QuotaToPreConsume)
			assert.GreaterOrEqual(t, reservation.held, info.PriceData.QuotaToPreConsume)
			assert.Nil(t, info.BillingImageCount)
			if tc.tiered {
				assert.Equal(t, tc.body, string(info.BillingRequestInput.Body))
				assert.Equal(t, 1, *info.BillingRequestInput.ImageCount)
			}
		})
	}
}

func TestOptInSafeToolLossRejectedAsBadRequestWithAdminDiagnostics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-4o",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-4o",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				ToolLossPolicy: string(types.ConversionLossPolicySafe),
			},
		},
	}

	tools, err := common.Marshal([]map[string]any{{"codeExecution": map[string]any{}}})
	require.NoError(t, err)
	req := &dto.GeminiChatRequest{
		Contents: []dto.GeminiChatContent{
			{Role: "user", Parts: []dto.GeminiPart{{Text: "run this"}}},
		},
		Tools: tools,
	}

	result, convErr := service.ConvertRequest(c, info, types.RelayFormatOpenAI, req)
	require.Error(t, convErr)
	var loss *types.ConversionLossError
	require.ErrorAs(t, convErr, &loss)
	require.NotEmpty(t, loss.Diagnostics)
	require.NotNil(t, result)

	apiErr := newConvertRequestFailedError(c, info, convErr)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeConvertRequestFailed, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))

	diagnostics := info.ConversionDiagnostics()
	require.NotEmpty(t, diagnostics)
	assert.True(t, hasHostDiagnosticCode(diagnostics, "unsupported_hosted_tool"))

	other := service.GenerateTextOtherInfo(c, info, 1, 1, 1, 0, 0, 0, 1)
	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, adminInfo, "conversion_diagnostics")
}

func TestUnknownModelModifierIsBadRequestWithoutRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName: "m@thinkin:on",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "m@thinkin:on",
		},
	}
	err := helper.ApplyReasoningModelSuffix(c, info)
	require.Error(t, err)
	require.True(t, kitreasoning.IsClientError(err))
	assert.Contains(t, err.Error(), `unsupported model modifier "thinkin"`)
	assert.Contains(t, err.Error(), "re:")

	apiErr := newConvertRequestFailedError(c, info, err)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeConvertRequestFailed, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))
}

func hasHostDiagnosticCode(diagnostics []types.ConversionDiagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func TestGeminiThinkingControlsConvertBestEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := model_setting.GetGeminiSettings()
	originalAdapter := settings.ThinkingAdapterEnabled
	t.Cleanup(func() { settings.ThinkingAdapterEnabled = originalAdapter })

	t.Run("openai thinking_budget on gemini 3 becomes thinkingLevel", func(t *testing.T) {
		settings.ThinkingAdapterEnabled = false
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req := &dto.GeneralOpenAIRequest{
			Model:     "gemini-3.1-pro-preview-thinking",
			Messages:  []dto.Message{{Role: "user", Content: "hello"}},
			ExtraBody: []byte(`{"google":{"thinking_config":{"thinking_budget":8192}}}`),
		}
		info := &relaycommon.RelayInfo{
			RelayFormat:     types.RelayFormatOpenAI,
			OriginModelName: "gemini-3.1-pro-preview-thinking",
			Request:         req,
			ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.1-pro-preview"},
		}
		require.NoError(t, helper.ApplyReasoningModelSuffix(c, info, req))

		result, err := service.ConvertRequest(c, info, types.RelayFormatGemini, req)
		require.NoError(t, err)
		converted, ok := result.Value.(*dto.GeminiChatRequest)
		require.True(t, ok)
		require.NotNil(t, converted.GenerationConfig.ThinkingConfig)
		assert.Equal(t, "high", converted.GenerationConfig.ThinkingConfig.ThinkingLevel)
		assert.Nil(t, converted.GenerationConfig.ThinkingConfig.ThinkingBudget)
		assert.True(t, hasHostDiagnosticCode(info.ConversionDiagnostics(), "gemini_budget_to_level"))
	})

	t.Run("thinking alias with native thinking_level keeps the level", func(t *testing.T) {
		settings.ThinkingAdapterEnabled = true
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req := &dto.GeneralOpenAIRequest{
			Model:     "gemini-3.1-pro-preview-thinking",
			Messages:  []dto.Message{{Role: "user", Content: "hello"}},
			ExtraBody: []byte(`{"google":{"thinking_config":{"thinking_level":"low"}}}`),
		}
		info := &relaycommon.RelayInfo{
			RelayFormat:     types.RelayFormatOpenAI,
			OriginModelName: "gemini-3.1-pro-preview-thinking",
			Request:         req,
			ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.1-pro-preview-thinking"},
		}
		require.NoError(t, helper.ApplyReasoningModelSuffix(c, info, req))
		assert.Equal(t, "gemini-3.1-pro-preview", info.UpstreamModelName)

		result, err := service.ConvertRequest(c, info, types.RelayFormatGemini, req)
		require.NoError(t, err)
		converted, ok := result.Value.(*dto.GeminiChatRequest)
		require.True(t, ok)
		require.NotNil(t, converted.GenerationConfig.ThinkingConfig)
		assert.Equal(t, "low", converted.GenerationConfig.ThinkingConfig.ThinkingLevel)
		assert.Nil(t, converted.GenerationConfig.ThinkingConfig.ThinkingBudget)
	})

	t.Run("gemini thinkingBudget converts to openai reasoning_effort", func(t *testing.T) {
		settings.ThinkingAdapterEnabled = false
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.1-pro-preview:generateContent", nil)
		budget := 8192
		req := &dto.GeminiChatRequest{
			Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{{Text: "hello"}}}},
			GenerationConfig: dto.GeminiChatGenerationConfig{
				ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingBudget: &budget},
			},
		}
		info := &relaycommon.RelayInfo{
			RelayFormat:     types.RelayFormatGemini,
			OriginModelName: "gemini-3.1-pro-preview",
			Request:         req,
			ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-5"},
		}

		result, err := service.ConvertRequest(c, info, types.RelayFormatOpenAI, req)
		require.NoError(t, err)
		converted, ok := result.Value.(*dto.GeneralOpenAIRequest)
		require.True(t, ok)
		assert.Equal(t, "high", converted.ReasoningEffort)
		require.NotNil(t, converted.ReasoningConversion)
		assert.Equal(t, &budget, converted.ReasoningConversion.BudgetTokens)
	})
}

// An Ali image model the alibaba task plugin does not claim is rejected by the
// adaptor with a classified 400 that skips retries. ImageHelper must surface
// that classification unchanged instead of wrapping it into a retryable
// conversion failure that other channels would then be asked to serve.
func TestImageHelperKeepsAdaptorClassifiedConvertError(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"wanx-style-repaint-v1","prompt":"a cat"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeAli)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://dashscope.invalid")
	request, err := helper.GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesGenerations)
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{Request: request, OriginModelName: "wanx-style-repaint-v1", RelayMode: relayconstant.RelayModeImagesGenerations,
		RequestURLPath: c.Request.URL.Path, Billing: &imageReservation{limit: 500000},
		PriceData: hosttypes.PriceData{UsePrice: true, ModelPrice: 0.04, GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}},
	}

	apiErr := ImageHelper(c, info)

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeInvalidRequest, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr), "other channels must not be asked to serve the same name")
	assert.Contains(t, apiErr.Error(), "not served by the alibaba task plugin")
}
