package conversationsession

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExplicitConversationSignals(t *testing.T) {
	for _, tt := range []struct {
		name    string
		headers map[string]string
		body    string
		want    string
	}{
		{"OpenCode header wins", map[string]string{Header: "ses-open", "Session_id": "codex"}, `{"session_id":"body"}`, "ses-open"},
		{"Claude header", map[string]string{"X-Claude-Code-Session-Id": "claude"}, "", "claude"},
		{"Codex thread outranks execution session", map[string]string{"Thread_id": "child", "Session_id": "parent"}, "", "child"},
		{"generic header", map[string]string{"X-Session-ID": "chat"}, "", "chat"},
		{"OpenCode affinity", map[string]string{"X-Session-Affinity": "ses-affinity"}, "", "ses-affinity"},
		{"pi header", map[string]string{"X-Pi-Session-Id": "pi-session"}, "", "pi-session"},
		{"metadata session", nil, `{"metadata":{"sessionID":"ses-body"}}`, "ses-body"},
		{"Responses conversation object", nil, `{"conversation":{"id":"conv-one"}}`, "conv-one"},
		{"Responses conversation string", nil, `{"conversation":"conv-two"}`, "conv-two"},
		{"Claude JSON user metadata", nil, `{"metadata":{"user_id":"{\"device_id\":\"private\",\"session_id\":\"claude-chat\"}"}}`, "claude-chat"},
		{"Claude legacy metadata", nil, `{"metadata":{"user_id":"user_private_account_private_session_01234567-1234-1234-1234-012345678901"}}`, "01234567-1234-1234-1234-012345678901"},
		{"Codex prompt cache key", map[string]string{"User-Agent": "codex_cli_rs/1.0"}, `{"prompt_cache_key":"codex-chat"}`, "codex-chat"},
		{"user identity is not conversation", nil, `{"user":"alice","metadata":{"user_id":"alice"}}`, ""},
		{"request identity is not conversation", map[string]string{"X-Request-Id": "request", "X-Client-Request-Id": "request"}, `{"id":"request"}`, ""},
		{"shared prompt cache key is not conversation", nil, `{"prompt_cache_key":"shared-prompt"}`, ""},
		{"previous response is not conversation ID", nil, `{"previous_response_id":"resp-1"}`, ""},
		{"header injection falls back to valid signal", map[string]string{Header: "bad\r\nX-Key:secret"}, `{"session_id":"safe"}`, "safe"},
		{"oversized ID ignored", map[string]string{Header: strings.Repeat("x", 257)}, `{}`, ""},
		{"body ID must be string", nil, `{"session_id":123}`, ""},
		{"invalid JSON ignored", nil, `{"session_id":"partial"`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			for name, value := range tt.headers {
				headers.Set(name, value)
			}
			assert.Equal(t, tt.want, ExplicitID(headers, []byte(tt.body)))
		})
	}
}
