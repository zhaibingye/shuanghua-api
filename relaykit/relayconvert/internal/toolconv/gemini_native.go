package toolconv

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
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
	root := ojson.ParseBytes(request.Tools)
	tools := root.Array()
	if root.IsObject() {
		tools = []ojson.Result{root}
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
			payload, err = ojson.Delete(payload, key)
			if err != nil {
				return nil, err
			}
		}
		if payload != tool.Raw {
			diagnostic := geminiSearchWithFunctionsOmitted(index)
			diagnostic.From = types.RelayFormatGemini
			diagnostic.To = types.RelayFormatGemini
			diagnostics = append(diagnostics, diagnostic)
			if len(ojson.Parse(payload).Map()) == 0 {
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

func geminiNativeFunctionTool(tool ojson.Result) bool {
	for _, key := range []string{"functionDeclarations", "function_declarations"} {
		declarations := tool.Get(key)
		if declarations.IsArray() && slices.ContainsFunc(declarations.Array(), func(declaration ojson.Result) bool {
			name := declaration.Get("name")
			return name.Type == ojson.String && strings.TrimSpace(name.Str) != ""
		}) {
			return true
		}
	}
	return false
}
