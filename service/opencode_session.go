package service

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/pkg/conversationsession"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const openCodeSessionContextKey = "opencode_go_session"

var openCodeSessionCacheOnce sync.Once
var openCodeSessionCache *conversationsession.Cache

func getOpenCodeSessionCache() *conversationsession.Cache {
	openCodeSessionCacheOnce.Do(func() {
		openCodeSessionCache = conversationsession.NewCache(conversationsession.CacheConfig{
			Redis:        common.RDB,
			RedisEnabled: func() bool { return common.RedisEnabled && common.RDB != nil },
		})
	})
	return openCodeSessionCache
}

type openCodeRequestSession struct {
	prepared         bool
	explicit         string
	previousResponse string
	prefixes         []string
	byScope          map[string]string
	activeScope      string
	activeID         string
	responses        map[string]bool
}

func OpenCodeGoEnabled(info *relaycommon.RelayInfo) bool {
	return info != nil && info.ChannelMeta != nil && info.ChannelOtherSettings.OpenCodeGoCompat &&
		(info.ChannelType == constant.ChannelTypeOpenAI || info.ChannelType == constant.ChannelTypeAnthropic)
}

// ApplyOpenCodeSession runs after all header overrides, so an explicitly configured
// value keeps precedence. It only changes outbound headers, never request bodies,
// channel selection, API key selection, or the caller's dashboard login session.
func ApplyOpenCodeSession(c *gin.Context, info *relaycommon.RelayInfo, headers http.Header) {
	if !OpenCodeGoEnabled(info) || headers == nil {
		return
	}
	id := conversationsession.NormalizeID(headers.Get(conversationsession.Header))
	if c == nil || c.Request == nil || info.IsChannelTest {
		if id == "" {
			id = uuid.NewString()
		}
		headers.Set(conversationsession.Header, id)
		return
	}
	value, _ := c.Get(openCodeSessionContextKey)
	state, _ := value.(*openCodeRequestSession)
	if state == nil {
		state = &openCodeRequestSession{byScope: make(map[string]string), responses: make(map[string]bool)}
		c.Set(openCodeSessionContextKey, state)
	}
	// Deliberately exclude model names and upstream credentials: switching models or
	// rotating a key does not start a new conversation. Callers/upstreams stay isolated.
	scope := conversationsession.Digest(strconv.Itoa(info.UserId) + ":" + strconv.Itoa(info.TokenId) + ":" + strings.TrimRight(info.ChannelBaseUrl, "/"))
	if id == "" {
		id = state.byScope[scope]
	}
	if id == "" {
		if !state.prepared {
			state.prepare(c, info)
		}
		id = state.explicit
	}
	cache := getOpenCodeSessionCache()
	if id == "" && state.previousResponse != "" {
		var err error
		id, err = cache.ResponseSession(scope, state.previousResponse)
		if err != nil {
			logger.LogWarn(c, "OpenCode Go response-session cache unavailable; using local fallback")
		}
		if id == "" {
			// Without the earlier response, this is an unknown chain, not a fresh
			// message-history conversation. Never merge unrelated chains by their delta.
			id = conversationsession.StableID(scope + ":previous:" + state.previousResponse)
		}
	}
	if id == "" {
		var err error
		id, err = cache.Match(c.Request.Context(), scope, state.prefixes)
		if err != nil {
			logger.LogWarn(c, "OpenCode Go prefix cache unavailable; using local fallback")
		}
	}
	state.byScope[scope] = id
	state.activeScope = scope
	state.activeID = id
	headers.Set(conversationsession.Header, id)
}

func (s *openCodeRequestSession) prepare(c *gin.Context, info *relaycommon.RelayInfo) {
	s.prepared = true
	headers := make(http.Header)
	if len(info.RequestHeaders) > 0 {
		for key, value := range info.RequestHeaders {
			headers.Set(key, value)
		}
	} else if c.Request != nil {
		headers = c.Request.Header
	}
	s.explicit = conversationsession.ExplicitID(headers, nil)
	if s.explicit != "" {
		return
	}
	var payload []byte
	var err error
	if value, exists := c.Get(common.KeyBodyStorage); exists {
		if storage, ok := value.(common.BodyStorage); ok && storage.Size() <= conversationsession.MaxPayloadBytes {
			// An independent reader is essential: seeking the shared storage here would
			// corrupt passthrough/replayable upstream bodies and transport retries.
			var reader io.ReadCloser
			reader, err = storage.NewReader()
			if err == nil {
				payload, err = io.ReadAll(io.LimitReader(reader, conversationsession.MaxPayloadBytes+1))
				_ = reader.Close()
			}
		}
	} else if info.Request != nil {
		payload, err = common.Marshal(info.Request)
	}
	if err != nil {
		logger.LogWarn(c, "OpenCode Go conversation inspection failed; generating a request-scoped session")
		return
	}
	if len(payload) > conversationsession.MaxPayloadBytes {
		return
	}
	s.explicit = conversationsession.ExplicitID(headers, payload)
	if s.explicit == "" {
		s.previousResponse = conversationsession.PreviousResponseID(payload)
		if s.previousResponse == "" {
			s.prefixes = conversationsession.Prefixes(payload)
		}
	}
}

// RecordOpenCodeResponse must run before exposing a Responses ID to the caller,
// including response.created in SSE. This supports previous_response_id without
// buffering streams or attempting to reconstruct an upstream's conversation state.
func RecordOpenCodeResponse(c *gin.Context, info *relaycommon.RelayInfo, responseID string) {
	if c == nil || !OpenCodeGoEnabled(info) || conversationsession.NormalizeID(responseID) == "" {
		return
	}
	value, exists := c.Get(openCodeSessionContextKey)
	if !exists {
		return
	}
	state, ok := value.(*openCodeRequestSession)
	if !ok || state.activeID == "" {
		return
	}
	key := state.activeScope + ":" + responseID
	if state.responses[key] {
		return
	}
	if err := getOpenCodeSessionCache().BindResponse(state.activeScope, responseID, state.activeID); err != nil {
		logger.LogWarn(c, "OpenCode Go response-session cache write failed; retained a local alias")
	}
	state.responses[key] = true
}
