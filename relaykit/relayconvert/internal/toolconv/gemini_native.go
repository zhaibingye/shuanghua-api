package toolconv

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func geminiSearchWithFunctionsOmitted(index int) types.ConversionDiagnostic {
	return semanticLoss(
		fmt.Sprintf("tools[%d]", index),
		"hosted_web_search_omitted",
		"built-in web search is omitted from Gemini requests with function tools",
	)
}

// FilterNativeGeminiSearch applies the function/search policy directly to
// native tool JSON, retaining other keys in each tool group.
func FilterNativeGeminiSearch(request *dto.GeminiChatRequest, options *convmeta.Options) ([]types.ConversionDiagnostic, error) {
	if request == nil || len(request.Tools) == 0 {
		return nil, nil
	}
	root := gjson.ParseBytes(request.Tools)
	tools := root.Array()
	if root.IsObject() {
		tools = []gjson.Result{root}
	}
	if !slices.ContainsFunc(tools, geminiNativeFunctionTool) {
		return nil, nil
	}

	filtered := make([]json.RawMessage, 0, len(tools))
	var diagnostics []types.ConversionDiagnostic
	for index, tool := range tools {
		payload := tool.Raw
		for _, key := range []string{
			"googleSearch", "google_search", "googleSearchRetrieval", "google_search_retrieval", "enterpriseWebSearch", "enterprise_web_search",
		} {
			if !tool.Get(key).Exists() {
				continue
			}
			var err error
			payload, err = sjson.Delete(payload, key)
			if err != nil {
				return nil, err
			}
		}
		if payload != tool.Raw {
			diagnostic := geminiSearchWithFunctionsOmitted(index)
			diagnostic.From = types.RelayFormatGemini
			diagnostic.To = types.RelayFormatGemini
			diagnostics = append(diagnostics, diagnostic)
			if len(gjson.Parse(payload).Map()) == 0 {
				continue
			}
		}
		filtered = append(filtered, json.RawMessage(payload))
	}
	if len(diagnostics) == 0 {
		return nil, nil
	}
	if err := types.RejectConversionLoss(options.EffectiveToolLossPolicy(), diagnostics); err != nil {
		return diagnostics, err
	}
	if root.IsObject() {
		request.Tools = filtered[0]
		return diagnostics, nil
	}
	encoded, err := kitutil.Marshal(filtered)
	if err != nil {
		return diagnostics, err
	}
	request.Tools = encoded
	return diagnostics, nil
}

func geminiNativeFunctionTool(tool gjson.Result) bool {
	for _, key := range []string{"functionDeclarations", "function_declarations"} {
		declarations := tool.Get(key)
		if declarations.IsArray() && slices.ContainsFunc(declarations.Array(), func(declaration gjson.Result) bool {
			name := declaration.Get("name")
			return name.Type == gjson.String && strings.TrimSpace(name.Str) != ""
		}) {
			return true
		}
	}
	return false
}
