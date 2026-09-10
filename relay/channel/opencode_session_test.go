package channel_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/conversationsession"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openCodeRelayFixture(t *testing.T, baseURL, body string) (*gin.Context, *relaycommon.RelayInfo, common.BodyStorage) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	storage, err := common.CreateBodyStorage([]byte(body))
	require.NoError(t, err)
	c.Set(common.KeyBodyStorage, storage)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	info := &relaycommon.RelayInfo{
		UserId: 1, TokenId: 2, DisablePing: true,
		RequestURLPath: "/v1/responses", RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeOpenAI, ChannelBaseUrl: baseURL, ApiKey: "upstream-test-key",
			ChannelOtherSettings: dto.ChannelOtherSettings{OpenCodeGoCompat: true},
		},
	}
	return c, info, storage
}

func TestOpenCodeOutboundHeadersAndBodyReplay(t *testing.T) {
	for _, provider := range []struct {
		name    string
		kind    int
		adaptor channel.Adaptor
	}{
		{"OpenAI", constant.ChannelTypeOpenAI, &openai.Adaptor{}},
		{"Anthropic", constant.ChannelTypeAnthropic, &claude.Adaptor{}},
	} {
		t.Run(provider.name, func(t *testing.T) {
			type observedRequest struct {
				headers http.Header
				body    string
				err     error
			}
			received := make(chan observedRequest, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				received <- observedRequest{r.Header.Clone(), string(body), err}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{}`)
			}))
			defer upstream.Close()
			payload := `{"metadata":{"session_id":"conversation-1"},"messages":[{"role":"user","content":"hello"}]}`
			c, info, storage := openCodeRelayFixture(t, upstream.URL, payload)
			info.ChannelType = provider.kind
			provider.adaptor.Init(info)
			resp, err := channel.DoApiRequest(provider.adaptor, c, info, common.NewReplayableBodyReader(storage))
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			request := <-received
			require.NoError(t, request.err)
			assert.Equal(t, "conversation-1", request.headers.Get(conversationsession.Header))
			assert.Equal(t, payload, request.body, "session inspection must not seek, consume, or rewrite passthrough bodies")
			assert.Empty(t, c.Request.Header.Get(conversationsession.Header), "only outbound headers change")
			if provider.kind == constant.ChannelTypeOpenAI {
				assert.Equal(t, "Bearer upstream-test-key", request.headers.Get("Authorization"))
			} else {
				assert.Equal(t, "upstream-test-key", request.headers.Get("x-api-key"))
			}
			require.NotNil(t, resp.Request.GetBody)
			replayed, err := resp.Request.GetBody()
			require.NoError(t, err)
			replay, err := io.ReadAll(replayed)
			require.NoError(t, err)
			require.NoError(t, replayed.Close())
			assert.Equal(t, payload, string(replay))

			info.HeadersOverride = map[string]any{"x-opencode-session": "administrator-session"}
			resp, err = channel.DoApiRequest(provider.adaptor, c, info, strings.NewReader(`{"messages":[]}`))
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			request = <-received
			assert.Equal(t, "administrator-session", request.headers.Get(conversationsession.Header))
			assert.Equal(t, `{"messages":[]}`, request.body)
		})
	}
}

func TestOpenCodeSessionGatingRetriesAndScope(t *testing.T) {
	payload := `{"messages":[{"role":"user","content":"hello"}]}`
	c, info, _ := openCodeRelayFixture(t, t.Name(), payload)
	info.ChannelOtherSettings.OpenCodeGoCompat = false
	headers := make(http.Header)
	service.ApplyOpenCodeSession(c, info, headers)
	assert.Empty(t, headers)
	info.ChannelOtherSettings.OpenCodeGoCompat = true
	info.ChannelType = constant.ChannelTypeAzure
	service.ApplyOpenCodeSession(c, info, headers)
	assert.Empty(t, headers, "OpenAI-compatible adaptors must not enable other channel types")
	info.ChannelType = constant.ChannelTypeOpenAI
	service.ApplyOpenCodeSession(c, info, headers)
	first := headers.Get(conversationsession.Header)
	require.NotEmpty(t, first)
	info.UpstreamModelName = "other-model"
	info.ApiKey = "rotated-key"
	info.ChannelId = 99
	headers = make(http.Header)
	service.ApplyOpenCodeSession(c, info, headers)
	assert.Equal(t, first, headers.Get(conversationsession.Header))

	next, nextInfo, _ := openCodeRelayFixture(t, t.Name(), `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"continue"}]}`)
	nextInfo.ChannelType = constant.ChannelTypeAnthropic
	headers = make(http.Header)
	service.ApplyOpenCodeSession(next, nextInfo, headers)
	assert.Equal(t, first, headers.Get(conversationsession.Header), "matching history survives model/protocol and channel changes at the same upstream")
	for _, identity := range []struct{ user, token int }{{3, 2}, {1, 4}} {
		other, otherInfo, _ := openCodeRelayFixture(t, t.Name(), payload)
		otherInfo.UserId, otherInfo.TokenId = identity.user, identity.token
		headers = make(http.Header)
		service.ApplyOpenCodeSession(other, otherInfo, headers)
		assert.NotEqual(t, first, headers.Get(conversationsession.Header))
	}
}

func TestOpenCodeNativeResponsesIncrementalChain(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	for _, stream := range []bool{false, true} {
		name := "JSON"
		if stream {
			name = "SSE"
		}
		t.Run(name, func(t *testing.T) {
			c, info, _ := openCodeRelayFixture(t, t.Name(), `{"input":"hello"}`)
			info.IsStream = stream
			headers := make(http.Header)
			service.ApplyOpenCodeSession(c, info, headers)
			first := headers.Get(conversationsession.Header)
			require.NotEmpty(t, first)
			body := `{"id":"resp-one","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
			if stream {
				// Only created exposes the ID: the alias must be recorded before the
				// completed event, which is not guaranteed to repeat it.
				body = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-one\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\ndata: [DONE]\n\n"
			}
			response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
			if stream {
				_, apiErr := openai.OaiResponsesStreamHandler(c, info, response)
				require.Nil(t, apiErr)
			} else {
				_, apiErr := openai.OaiResponsesHandler(c, info, response)
				require.Nil(t, apiErr)
			}
			next, nextInfo, _ := openCodeRelayFixture(t, t.Name(), `{"previous_response_id":"resp-one","input":"continue"}`)
			headers = make(http.Header)
			service.ApplyOpenCodeSession(next, nextInfo, headers)
			assert.Equal(t, first, headers.Get(conversationsession.Header))
			service.RecordOpenCodeResponse(next, nextInfo, "resp-two")
			third, thirdInfo, _ := openCodeRelayFixture(t, t.Name(), `{"previous_response_id":"resp-two","input":"finish"}`)
			headers = make(http.Header)
			service.ApplyOpenCodeSession(third, thirdInfo, headers)
			assert.Equal(t, first, headers.Get(conversationsession.Header))

			explicit, explicitInfo, _ := openCodeRelayFixture(t, t.Name(), `{"previous_response_id":"resp-two","session_id":"explicit-chat","input":"continue"}`)
			headers = make(http.Header)
			service.ApplyOpenCodeSession(explicit, explicitInfo, headers)
			assert.Equal(t, "explicit-chat", headers.Get(conversationsession.Header))
		})
	}
}

func TestOpenCodeUnknownPreviousResponsesNeverMergeIdenticalDeltas(t *testing.T) {
	first, firstInfo, _ := openCodeRelayFixture(t, t.Name(), `{"previous_response_id":"missing-one","input":"continue"}`)
	second, secondInfo, _ := openCodeRelayFixture(t, t.Name(), `{"previous_response_id":"missing-two","input":"continue"}`)
	left, right := make(http.Header), make(http.Header)
	service.ApplyOpenCodeSession(first, firstInfo, left)
	service.ApplyOpenCodeSession(second, secondInfo, right)
	require.NotEmpty(t, left.Get(conversationsession.Header))
	assert.NotEqual(t, left.Get(conversationsession.Header), right.Get(conversationsession.Header))
}

func TestOpenCodeUnidentifiableRequestsAndChannelTestsStillHaveHeaders(t *testing.T) {
	c, info, _ := openCodeRelayFixture(t, t.Name(), `{}`)
	headers := make(http.Header)
	service.ApplyOpenCodeSession(c, info, headers)
	id := headers.Get(conversationsession.Header)
	require.NotEmpty(t, id)
	headers = make(http.Header)
	service.ApplyOpenCodeSession(c, info, headers)
	assert.Equal(t, id, headers.Get(conversationsession.Header), "an unidentifiable request keeps its ID across retries")
	info.IsChannelTest = true
	testHeaders := make(http.Header)
	service.ApplyOpenCodeSession(c, info, testHeaders)
	require.NotEmpty(t, testHeaders.Get(conversationsession.Header))
	assert.NotEqual(t, id, testHeaders.Get(conversationsession.Header), "admin tests do not join customer conversations")
}
