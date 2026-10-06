package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/plugins"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestForkNativeVideoReceiptsUsePublicIDs(t *testing.T) {
	for _, tc := range []struct{ plugin, path, idField string }{
		{"xai", "/v1/videos/generations", "request_id"},
		{"xai", "/v1/videos/edits", "request_id"},
		{"xai", "/v1/videos/extensions", "request_id"},
		{"doubao", "/api/v3/contents/generations/tasks", "id"},
		{"doubao-mediakit", "/api/v3/contents/generations/tasks", "id"},
	} {
		t.Run(tc.plugin+tc.path, func(t *testing.T) {
			source, err := plugins.Source(tc.plugin)
			require.NoError(t, err)
			plugin, err := pluginruntime.CompilePlugin(source, pluginruntime.Options{Key: tc.plugin})
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			c.Set(pluginruntime.ContextKeyPinnedEndpoint, pluginruntime.PinnedEndpoint{Plugin: plugin, Protocol: "openai_video", Operation: pluginruntime.HostProtocolOperation{Name: "create"}})
			task := &model.Task{TaskID: "task_public", Status: model.TaskStatusSubmitted, Properties: model.Properties{OriginModelName: "client-model"}, PrivateData: model.TaskPrivateData{UpstreamTaskID: "provider-private"}}
			task.SetData(map[string]any{tc.idField: "provider-private", "status": "queued"})
			presentTaskSubmission(c, &taskSubmissionOutcome{Task: task, RelayInfo: &relaycommon.RelayInfo{OriginModelName: "client-model"}})
			require.Equal(t, http.StatusOK, recorder.Code)
			var body map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &body))
			assert.Equal(t, "task_public", body[tc.idField])
			assert.NotContains(t, body, "object")
			assert.NotContains(t, recorder.Body.String(), "provider-private")
		})
	}
}
