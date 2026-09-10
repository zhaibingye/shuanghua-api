package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/conversationsession"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeGoUnsavedModelPreview(t *testing.T) {
	received := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		_, _ = w.Write([]byte(`{"data":[{"id":"preview-model"}]}`))
	}))
	defer server.Close()
	request := fetchModelsRequest{
		Type:             constant.ChannelTypeOpenAI,
		BaseURL:          common.GetPointer(server.URL),
		Key:              "create-key",
		OpenCodeGoCompat: common.GetPointer(true),
	}
	body, err := common.Marshal(request)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/fetch_models", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	FetchModels(c)
	var response struct {
		Success bool     `json:"success"`
		Message string   `json:"message"`
		Data    []string `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success, response.Message)
	assert.Equal(t, []string{"preview-model"}, response.Data)
	header := <-received
	assert.NotEmpty(t, header.Get(conversationsession.Header))
	assert.Equal(t, "Bearer create-key", header.Get("Authorization"))
}

func TestOpenCodeGoEditPreviewDoesNotPersistUnsavedSettings(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	for _, enabled := range []bool{false, true} {
		saved := &model.Channel{Type: constant.ChannelTypeOpenAI, Key: "saved-key", Name: "saved", Models: "old"}
		saved.SetOtherSettings(dto.ChannelOtherSettings{OpenCodeGoCompat: enabled})
		require.NoError(t, db.Create(saved).Error)
		preview, err := buildModelPreviewChannel(fetchModelsRequest{
			ChannelID:        saved.Id,
			Type:             saved.Type,
			Key:              "untrusted-replacement",
			OpenCodeGoCompat: common.GetPointer(!enabled),
		})
		require.NoError(t, err)
		header, err := buildFetchModelsHeaders(preview, preview.Key)
		require.NoError(t, err)
		assert.Equal(t, "Bearer saved-key", header.Get("Authorization"))
		assert.Equal(t, !enabled, header.Get(conversationsession.Header) != "")
		reloaded, err := model.GetChannelById(saved.Id, true)
		require.NoError(t, err)
		assert.Equal(t, enabled, reloaded.GetOtherSettings().OpenCodeGoCompat)
		_, err = buildModelPreviewChannel(fetchModelsRequest{ChannelID: saved.Id, Type: constant.ChannelTypeAnthropic})
		require.Error(t, err, "a preview must not silently use credentials for another channel type")
	}
}

func TestOpenCodeGoModelDiscoveryAndManagementHeaders(t *testing.T) {
	for _, kind := range []int{constant.ChannelTypeOpenAI, constant.ChannelTypeAnthropic} {
		received := make(chan http.Header, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received <- r.Header.Clone()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
		}))
		t.Cleanup(server.Close)
		channel := &model.Channel{Type: kind, BaseURL: common.GetPointer(server.URL), Key: "test-key"}
		channel.SetOtherSettings(dto.ChannelOtherSettings{OpenCodeGoCompat: true})
		models, err := fetchChannelUpstreamModelIDs(channel)
		require.NoError(t, err)
		assert.Equal(t, []string{"test-model"}, models)
		headers := <-received
		require.NotEmpty(t, headers.Get(conversationsession.Header))
		if kind == constant.ChannelTypeOpenAI {
			assert.Equal(t, "Bearer test-key", headers.Get("Authorization"))
		} else {
			assert.Equal(t, "test-key", headers.Get("x-api-key"))
		}
		channel.HeaderOverride = common.GetPointer(`{"X-Opencode-Session":"admin-management"}`)
		_, err = GetResponseBody(http.MethodGet, server.URL, channel, GetAuthHeader(channel.Key))
		require.NoError(t, err)
		assert.Equal(t, "admin-management", (<-received).Get(conversationsession.Header))
		channel.HeaderOverride = nil
		channel.SetOtherSettings(dto.ChannelOtherSettings{})
		headers, err = buildFetchModelsHeaders(channel, channel.Key)
		require.NoError(t, err)
		assert.Empty(t, headers.Get(conversationsession.Header))
	}
}
