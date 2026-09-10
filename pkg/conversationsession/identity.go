// Package conversationsession identifies relay conversations without retaining message text
// or making channel/credential routing decisions.
package conversationsession

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const Header = "X-Opencode-Session"

// MaxPayloadBytes bounds optional history inspection, not the relay's request size.
const MaxPayloadBytes = 8 << 20

var claudeSessionSuffix = regexp.MustCompile(`_session_([a-zA-Z0-9-]+)$`)

// NormalizeID accepts bounded printable ASCII suitable for an HTTP header. Invalid
// client signals are ignored, never allowed to break an otherwise valid request.
func NormalizeID(value string) string {
	if len(value) > 256 {
		return ""
	}
	for _, ch := range value {
		if ch < 32 || ch > 126 {
			return ""
		}
	}
	return strings.TrimSpace(value)
}

// ExplicitID deliberately excludes generic request IDs and bare user/account IDs.
// A prompt_cache_key is authoritative only for Codex, which uses it per conversation.
func ExplicitID(headers http.Header, payload []byte) string {
	for _, name := range []string{
		Header, "X-Claude-Code-Session-Id", "X-Pi-Session-Id",
		"X-Session-Affinity", "Thread-Id", "Thread_id", "X-Thread-Id",
		"Session-Id", "Session_id", "X-Session-Id", "X-Conversation-Id",
	} {
		if id := NormalizeID(headers.Get(name)); id != "" {
			return id
		}
	}
	if len(payload) == 0 || len(payload) > MaxPayloadBytes || !gjson.ValidBytes(payload) {
		return ""
	}
	root := gjson.ParseBytes(payload)
	for _, path := range []string{
		"session_id", "sessionId", "sessionID",
		"metadata.session_id", "metadata.sessionId", "metadata.sessionID",
		"conversation.id", "conversation", "conversation_id", "conversationId",
		"metadata.conversation_id", "thread_id", "threadId",
		"extra_body.session_id", "extra_body.conversation_id",
	} {
		if value := root.Get(path); value.Type == gjson.String {
			if id := NormalizeID(value.String()); id != "" {
				return id
			}
		}
	}
	user := root.Get("metadata.user_id")
	if user.Type == gjson.String {
		value := user.String()
		if gjson.Valid(value) {
			if id := gjson.Get(value, "session_id"); id.Type == gjson.String {
				if sessionID := NormalizeID(id.String()); sessionID != "" {
					return sessionID
				}
			}
		}
		if match := claudeSessionSuffix.FindStringSubmatch(value); len(match) == 2 {
			if id := NormalizeID(match[1]); id != "" {
				return id
			}
		}
	}
	client := strings.ToLower(headers.Get("User-Agent") + " " + headers.Get("Originator"))
	if strings.Contains(client, "codex") {
		if value := root.Get("prompt_cache_key"); value.Type == gjson.String {
			return NormalizeID(value.String())
		}
	}
	return ""
}

// StableID projects a namespaced fingerprint into an opaque UUID. Cold roots and
// the first divergent prefix have deterministic identities, including after restart.
func StableID(value string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("new-api:conversation-session:v1:"+value)).String()
}

func PreviousResponseID(payload []byte) string {
	value := gjson.GetBytes(payload, "previous_response_id")
	if value.Type != gjson.String {
		return ""
	}
	return NormalizeID(value.String())
}
