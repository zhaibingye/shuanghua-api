package conversationsession

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

const MaxTurns = 1024

// Prefixes returns rolling message hashes, starting only after a non-system turn.
// It reads the inbound protocol, before any provider conversion or parameter override.
// Oversized/unsupported histories return nil rather than silently dropping a suffix
// and conflating conversations that differ beyond a resource limit.
func Prefixes(payload []byte) []string {
	if len(payload) == 0 || len(payload) > MaxPayloadBytes || !gjson.ValidBytes(payload) {
		return nil
	}
	root := gjson.ParseBytes(payload)
	var history historyFingerprint
	for _, field := range []string{"system", "instructions", "systemInstruction.parts", "system_instruction.parts"} {
		if value := root.Get(field); value.Exists() {
			history.append("system", value, gjson.Result{})
		}
	}
	switch {
	case root.Get("messages").IsArray():
		root.Get("messages").ForEach(func(_, message gjson.Result) bool {
			history.append(message.Get("role").String(), message.Get("content"), message)
			return !history.invalid
		})
	case root.Get("contents").IsArray():
		root.Get("contents").ForEach(func(_, message gjson.Result) bool {
			history.append(message.Get("role").String(), message.Get("parts"), message)
			return !history.invalid
		})
	case root.Get("input").Type == gjson.String:
		history.append("user", root.Get("input"), gjson.Result{})
	case root.Get("input").IsArray():
		root.Get("input").ForEach(func(_, item gjson.Result) bool {
			switch item.Get("type").String() {
			case "reasoning":
				return true
			case "function_call":
				history.append("assistant", item, gjson.Result{})
			case "function_call_output":
				history.append("tool", item, gjson.Result{})
			default:
				history.append(item.Get("role").String(), item.Get("content"), item)
			}
			return !history.invalid
		})
	}
	if history.invalid {
		return nil
	}
	return history.prefixes
}

type historyFingerprint struct {
	previous [32]byte
	prefixes []string
	turns    int
	eligible bool
	invalid  bool
}

func (h *historyFingerprint) append(role string, content, message gjson.Result) {
	if h.invalid {
		return
	}
	switch role {
	case "developer":
		role = "system"
	case "model":
		role = "assistant"
	case "function":
		role = "tool"
	}
	if role != "system" && role != "user" && role != "assistant" && role != "tool" {
		h.invalid = true
		return
	}
	h.turns++
	if h.turns > MaxTurns {
		h.invalid = true
		return
	}
	fingerprint := sha256.New()
	writeField(fingerprint, role)
	parts := 0
	meaningful := false
	valid := true
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			parts++
			if parts > 256 {
				valid = false
				return false
			}
			if isReasoningPart(part) {
				return true
			}
			meaningful = true
			valid = fingerprintPart(fingerprint, part)
			return valid
		})
	} else if content.Exists() && content.Type != gjson.Null && !isReasoningPart(content) {
		meaningful = true
		valid = fingerprintPart(fingerprint, content)
	}
	for _, field := range []string{"tool_calls", "function_call", "tool_call_id", "name"} {
		if value := message.Get(field); value.Exists() {
			meaningful = true
			writeField(fingerprint, field)
			budget := 16384
			valid = valid && fingerprintJSON(fingerprint, value, 0, &budget)
		}
	}
	if !valid {
		h.invalid = true
		return
	}
	if !meaningful {
		return
	}
	rolling := sha256.New()
	_, _ = rolling.Write(h.previous[:])
	_, _ = rolling.Write(fingerprint.Sum(nil))
	copy(h.previous[:], rolling.Sum(nil))
	h.eligible = h.eligible || role != "system"
	if h.eligible {
		h.prefixes = append(h.prefixes, hex.EncodeToString(h.previous[:]))
	}
}

func isReasoningPart(part gjson.Result) bool {
	typ := part.Get("type").String()
	return typ == "thinking" || typ == "redacted_thinking" || typ == "reasoning" || part.Get("thought").Bool()
}

func fingerprintPart(h hash.Hash, part gjson.Result) bool {
	if part.Type == gjson.String {
		writeField(h, "text")
		writeField(h, strings.ReplaceAll(part.String(), "\r\n", "\n"))
		return true
	}
	typ := part.Get("type").String()
	text := part.Get("text")
	if text.Type == gjson.String && (typ == "" || typ == "text" || typ == "input_text" || typ == "output_text") {
		writeField(h, "text")
		writeField(h, strings.ReplaceAll(text.String(), "\r\n", "\n"))
		return true
	}
	writeField(h, "structured")
	// cache_control is transport/cache metadata on content blocks, not conversation
	// content. Do not remove identically named properties inside tool arguments.
	if part.IsObject() {
		fields := make(map[string]gjson.Result)
		var names []string
		part.ForEach(func(key, value gjson.Result) bool {
			if key.String() != "cache_control" {
				names = append(names, key.String())
				fields[key.String()] = value
			}
			return true
		})
		sort.Strings(names)
		budget := 16384
		for _, name := range names {
			writeField(h, name)
			if !fingerprintJSON(h, fields[name], 0, &budget) {
				return false
			}
		}
		return true
	}
	budget := 16384
	return fingerprintJSON(h, part, 0, &budget)
}

// Hash JSON structurally without float64 conversion, preserving tool arguments
// larger than 2^53 and distinguishing tool IDs, media and nested values.
func fingerprintJSON(h hash.Hash, value gjson.Result, depth int, budget *int) bool {
	*budget--
	if depth > 64 || *budget < 0 {
		return false
	}
	if value.IsObject() {
		writeField(h, "object")
		fields := make(map[string]gjson.Result)
		var names []string
		value.ForEach(func(key, child gjson.Result) bool {
			names = append(names, key.String())
			fields[key.String()] = child
			return true
		})
		if len(names) > *budget {
			return false
		}
		sort.Strings(names)
		for _, name := range names {
			writeField(h, name)
			if !fingerprintJSON(h, fields[name], depth+1, budget) {
				return false
			}
		}
		writeField(h, "end-object")
		return true
	}
	if value.IsArray() {
		writeField(h, "array")
		valid := true
		value.ForEach(func(_, child gjson.Result) bool {
			valid = fingerprintJSON(h, child, depth+1, budget)
			return valid
		})
		writeField(h, "end-array")
		return valid
	}
	writeField(h, strconv.Itoa(int(value.Type)))
	if value.Type == gjson.String {
		writeField(h, value.String())
	} else {
		writeField(h, value.Raw)
	}
	return true
}

func writeField(h hash.Hash, value string) {
	_, _ = io.WriteString(h, strconv.Itoa(len(value))+":")
	_, _ = io.WriteString(h, value)
}
