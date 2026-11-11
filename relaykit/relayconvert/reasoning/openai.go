package reasoning

import (
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func openAIThinkingBudget(rawBudget, enabled []byte) *int {
	if len(rawBudget) == 0 {
		return nil
	}
	var budget *int
	if err := kitutil.Unmarshal(rawBudget, &budget); err != nil {
		return nil
	}
	if budget != nil && strings.TrimSpace(string(enabled)) == "false" {
		*budget = 0
	}
	return budget
}

// OpenAIOutputEffort resolves the effort sent in Chat or Responses. The
// original intent can still carry an exact budget through conversion pivots.
func OpenAIOutputEffort(model string, intent Intent) Effort {
	if intent.BudgetTokens == nil || *intent.BudgetTokens <= 0 || intent.Effort != "" || intent.Mode == ModeDisabled {
		return EffectiveEffort(intent)
	}
	model = strings.TrimPrefix(strings.ToLower(model), "openai/")
	switch {
	case strings.HasPrefix(model, "gpt-4"), strings.HasPrefix(model, "gpt-3.5"), strings.Contains(model, "-chat-latest"):
		return ""
	case dto.IsOpenAIReasoningOModel(model), model == "gpt-5", strings.HasPrefix(model, "gpt-5-"),
		model == "gpt-5.1", strings.HasPrefix(model, "gpt-5.1-20"), model == "gpt-5.1-codex", model == "gpt-5.1-codex-mini":
		return EffortHigh
	default:
		return EffortXHigh
	}
}

// NormalizeOpenAIChatBudget handles budget extensions on native Chat
// requests. Qwen uses its own native budget field.
func NormalizeOpenAIChatBudget(req *dto.GeneralOpenAIRequest) error {
	if dto.IsQwenThinkingBudgetModel(req.Model) || len(req.ThinkingBudget) == 0 && !gjson.GetBytes(req.Reasoning, "max_tokens").Exists() {
		return nil
	}
	intent, _, err := FromOpenAIChat(req)
	if err != nil {
		return err
	}
	explicitEffort := req.ReasoningEffort != "" || gjson.GetBytes(req.Reasoning, "effort").Str != "" || gjson.GetBytes(req.Reasoning, "enabled").Type == gjson.False
	if !explicitEffort && (intent.BudgetTokens == nil || *intent.BudgetTokens <= 0) {
		return nil
	}
	if req.ReasoningEffort == "" {
		req.ReasoningEffort = string(OpenAIOutputEffort(req.Model, intent))
	}
	req.ThinkingBudget = nil
	if gjson.GetBytes(req.Reasoning, "max_tokens").Exists() {
		req.Reasoning, err = sjson.DeleteBytes(req.Reasoning, "max_tokens")
		if string(req.Reasoning) == "{}" {
			req.Reasoning = nil
		}
	}
	return err
}

// NormalizeOpenAIResponsesBudget handles the native Responses budget
// extension without rewriting other reasoning fields such as summary.
func NormalizeOpenAIResponsesBudget(req *dto.OpenAIResponsesRequest) {
	if dto.IsQwenThinkingBudgetModel(req.Model) {
		return
	}
	budget := openAIThinkingBudget(req.ThinkingBudget, req.EnableThinking)
	if budget == nil || *budget <= 0 || req.Reasoning != nil && req.Reasoning.Effort != "" {
		return
	}
	effort := OpenAIOutputEffort(req.Model, Intent{BudgetTokens: budget})
	if effort != "" {
		config := dto.Reasoning{Effort: string(effort)}
		if req.Reasoning != nil {
			config = *req.Reasoning
			config.Effort = string(effort)
		}
		req.Reasoning = &config
	}
	req.ThinkingBudget = nil
}
