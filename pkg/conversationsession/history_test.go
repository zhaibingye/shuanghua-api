package conversationsession

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHistoryGrowthAndProtocolShapes(t *testing.T) {
	for _, tt := range []struct{ name, first, next string }{
		{"Chat", `{"model":"one","messages":[{"role":"system","content":"help"},{"role":"user","content":"hello"}]}`, `{"model":"two","stream":true,"max_tokens":100,"messages":[{"role":"system","content":"help"},{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]},{"role":"assistant","content":"hi"},{"role":"user","content":"continue"}]}`},
		{"Claude", `{"system":"help","messages":[{"role":"user","content":"hello"}]}`, `{"system":[{"type":"text","text":"help"}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]},{"role":"assistant","content":"hi"},{"role":"user","content":"continue"}]}`},
		{"Responses", `{"instructions":"help","input":"hello"}`, `{"instructions":"help","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"reasoning","id":"rs-private"},{"role":"assistant","content":[{"type":"output_text","text":"hi"}]},{"role":"user","content":"continue"}]}`},
		{"Gemini", `{"systemInstruction":{"parts":[{"text":"help"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`, `{"systemInstruction":{"parts":[{"text":"help"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"hi"}]},{"role":"user","parts":[{"text":"continue"}]}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			first, next := Prefixes([]byte(tt.first)), Prefixes([]byte(tt.next))
			require.Len(t, first, 1)
			require.Len(t, next, 3)
			assert.Equal(t, first, next[:1])
		})
	}
}

func TestHistoryKeepsToolsMediaAndPreciseNumbersDistinct(t *testing.T) {
	for _, tt := range []struct {
		name, left, right string
		equal             bool
	}{
		{"tool argument key order", `{"role":"assistant","content":[{"type":"tool_use","id":"call-a","name":"lookup","input":{"a":1,"b":2}}]}`, `{"role":"assistant","content":[{"input":{"b":2,"a":1},"name":"lookup","id":"call-a","type":"tool_use"}]}`, true},
		{"large integer arguments", `{"role":"assistant","content":[{"type":"tool_use","id":"call-a","name":"lookup","input":{"id":9007199254740992}}]}`, `{"role":"assistant","content":[{"type":"tool_use","id":"call-a","name":"lookup","input":{"id":9007199254740993}}]}`, false},
		{"tool result linkage", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-a","content":"OK"}]}`, `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-b","content":"OK"}]}`, false},
		{"tool cache_control is an argument", `{"role":"assistant","content":[{"type":"tool_use","id":"call-a","name":"lookup","input":{"cache_control":1}}]}`, `{"role":"assistant","content":[{"type":"tool_use","id":"call-a","name":"lookup","input":{"cache_control":2}}]}`, false},
		{"images with same caption", `{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}`, `{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAB"}}]}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			left := Prefixes([]byte(`{"messages":[` + tt.left + `]}`))
			right := Prefixes([]byte(`{"messages":[` + tt.right + `]}`))
			require.NotEmpty(t, left)
			require.NotEmpty(t, right)
			if tt.equal {
				assert.Equal(t, left, right)
			} else {
				assert.NotEqual(t, left, right)
			}
		})
	}
}

func TestHistoryIgnoresReasoningOnlyTurns(t *testing.T) {
	plain := Prefixes([]byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`))
	withReasoning := Prefixes([]byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"thinking","thinking":"private reasoning","signature":"rotating"}]},{"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque"},{"type":"text","text":"hi"}]}]}`))
	require.Len(t, plain, 2)
	assert.Equal(t, plain, withReasoning, "opaque reasoning must not introduce an empty logical turn")
}

func TestHistoryDoesNotMatchInstructionsOrTruncatedInputs(t *testing.T) {
	for _, payload := range []string{
		`{"messages":[{"role":"system","content":"shared instructions"}]}`,
		`{"system":"shared instructions","messages":[]}`,
		`{"messages":[{"role":"user","content":"partial"}`,
		`{"messages":[` + strings.Repeat(`{"role":"user","content":"hi"},`, MaxTurns) + `{"role":"user","content":"distinct suffix"}]}`,
		`{"messages":[{"role":"user","content":[` + strings.Repeat(`{"type":"text","text":"x"},`, 256) + `{"type":"text","text":"distinct suffix"}]}]}`,
		`{"messages":[{"role":"user","content":"` + strings.Repeat("x", MaxPayloadBytes) + `"}]}`,
	} {
		assert.Empty(t, Prefixes([]byte(payload)))
	}
}
