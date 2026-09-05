package plugins_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	pluginadaptor "github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func loadDoubaoPlugin(t *testing.T) *jsplugin.LoadedPlugin {
	t.Helper()
	source, err := builtinplugins.Source("doubao")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "doubao"})
	require.NoError(t, err)
	return plugin
}

func doubaoObject(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := common.Marshal(value)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, common.Unmarshal(data, &result))
	return result
}

func TestDoubaoGenerationAndBillingUseTheSameResolutionPolicy(t *testing.T) {
	plugin := loadDoubaoPlugin(t)
	for _, tc := range []struct {
		name, key, requested, source, target string
		tokens, enhancementSeconds           float64
	}{
		{"ordinary", "ark-key", "1080p", "1080p", "none", 243000, 0},
		{"enhance 480 to 720", "ark-key|media-key", "480p", "480p", "720p", 48038, 5},
		{"enhance 480 to 1080", "ark-key|media-key", "720p", "480p", "1080p", 48038, 5},
		{"enhance 720 to 1080", `{"ark_api_key":"ark-key","mediakit_api_key":"media-key"}`, "1080p", "720p", "1080p", 108000, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := map[string]any{
				"baseUrl": "https://ark.example/", "apiKey": tc.key,
				"model": "storefront-alias", "upstreamModel": "doubao-seedance-2-0-260128",
				"requestBody": map[string]any{"prompt": "sunrise", "seconds": 5, "resolution": tc.requested, "seed": 0, "generate_audio": false},
			}
			value, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", ctx)
			require.NoError(t, err)
			descriptor := doubaoObject(t, value)
			body := descriptor["body"].(map[string]any)
			assert.Equal(t, tc.source, body["resolution"])
			assert.Equal(t, "doubao-seedance-2-0-260128", body["model"])
			assert.Equal(t, float64(0), body["seed"])
			assert.Equal(t, false, body["generate_audio"])
			assert.Equal(t, "Bearer ark-key", descriptor["headers"].(map[string]any)["Authorization"])
			assert.Equal(t, "https://ark.example/api/v3/contents/generations/tasks", descriptor["url"])
			assert.NotContains(t, descriptor, "model", "the client model alias must remain host-owned")

			value, err = plugin.Engine.Call(t.Context(), "extractUsage", ctx)
			require.NoError(t, err)
			facts := doubaoObject(t, value)
			assert.Equal(t, map[string]any{"tokens": tc.tokens, "resolution": tc.source, "video_input": "none", "enhancement_seconds": tc.enhancementSeconds, "enhancement_resolution": tc.target}, facts)
		})
	}
}

func TestDoubaoRejectsUnsafeRequestQuantitiesAndCredentials(t *testing.T) {
	plugin := loadDoubaoPlugin(t)
	for _, tc := range []struct {
		name, key string
		body      map[string]any
	}{
		{"huge duration", "ark", map[string]any{"duration": "18446744073686646784"}},
		{"metadata duration bypass", "ark", map[string]any{"seconds": 5, "metadata": map[string]any{"duration": 3601}}},
		{"metadata frames bypass", "ark", map[string]any{"metadata": map[string]any{"frames": 86401}}},
		{"fractional frames", "ark", map[string]any{"frames": 2.5}},
		{"negative duration", "ark", map[string]any{"duration": -1}},
		{"fractional duration", "ark", map[string]any{"seconds": 5.5}},
		{"boolean duration", "ark", map[string]any{"seconds": true}},
		{"frames and duration conflict", "ark", map[string]any{"seconds": 5, "frames": 120}},
		{"metadata count bypass", "ark", map[string]any{"metadata": map[string]any{"n": 129}}},
		{"prompt flags bypass", "ark", map[string]any{"prompt": "sunrise --duration 999999"}},
		{"metadata prompt flags bypass", "ark", map[string]any{"metadata": map[string]any{"content": []any{map[string]any{"type": "text", "text": "sunrise --rs 4k"}}}}},
		{"unsupported resolution", "ark|media", map[string]any{"resolution": "4k"}},
		{"malformed resolution", "ark", map[string]any{"resolution": "oops"}},
		{"missing media key", "ark|", nil},
		{"invalid json credentials", `{"ark_api_key":`, nil},
		{"credential header injection", "ark\r\nheader|media", nil},
		{"unapproved MediaKit host", `{"ark_api_key":"ark","mediakit_api_key":"media","mediakit_base_url":"https://unapproved.example"}`, nil},
		{"client callback cannot report final result", "ark|media", map[string]any{"callback_url": "https://client.example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"prompt": "sunrise"}
			for key, value := range tc.body {
				body[key] = value
			}
			ctx := map[string]any{"model": "doubao-seedance-2-0-260128", "apiKey": tc.key, "baseUrl": "https://ark.example", "requestBody": body}
			_, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", ctx)
			require.Error(t, err)
			_, err = plugin.Engine.Call(t.Context(), "extractUsage", ctx)
			require.Error(t, err, "billing must not accept a body the driver rejects")
			request, _ := gin.CreateTestContext(httptest.NewRecorder())
			request.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
			request.Set("task_request", body)
			info := &relaycommon.RelayInfo{OriginModelName: "doubao-seedance-2-0-260128", ChannelMeta: &relaycommon.ChannelMeta{ApiKey: tc.key, ChannelBaseUrl: "https://ark.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
			adaptor := pluginadaptor.New(plugin)
			adaptor.Init(info)
			taskErr := adaptor.ValidateRequestAndSetAction(request, info)
			require.NotNil(t, taskErr)
			assert.Equal(t, http.StatusBadRequest, taskErr.StatusCode)
		})
	}
}

func TestDoubaoVideoProtocolPreservesReferencesAndExplicitZeros(t *testing.T) {
	plugin := loadDoubaoPlugin(t)
	for _, body := range []map[string]any{
		{"kind": "json", "value": map[string]any{"prompt": "sunrise", "input_reference": "https://cdn.example/frame.png", "frames": 121, "seed": 0, "generate_audio": false}},
		{"kind": "multipart", "fields": map[string][]string{"prompt": {"sunrise"}, "input_reference": {"https://cdn.example/frame.png"}, "frames": {"121"}, "seed": {"0"}, "generate_audio": {"false"}}},
	} {
		value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"body": body, "model": "doubao-seedance-2-0-260128"})
		require.NoError(t, err)
		intent := doubaoObject(t, value)
		ctx := map[string]any{"baseUrl": "https://ark.example", "apiKey": "ark", "model": intent["model"], "requestBody": intent["requestBody"]}
		value, err = plugin.Engine.Call(t.Context(), "buildSubmitRequest", ctx)
		require.NoError(t, err)
		upstream := doubaoObject(t, value)["body"].(map[string]any)
		assert.NotContains(t, upstream, "duration")
		assert.Equal(t, float64(121), upstream["frames"])
		assert.Equal(t, float64(0), upstream["seed"])
		assert.Equal(t, false, upstream["generate_audio"])
		assert.Contains(t, upstream["content"], map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://cdn.example/frame.png"}})
		value, err = plugin.Engine.Call(t.Context(), "extractUsage", ctx)
		require.NoError(t, err)
		assert.Equal(t, float64(108900), doubaoObject(t, value)["tokens"], "fractional seconds from frames must not be truncated")
	}
}

func TestDoubaoMediaKitPublishesOnlyEnhancedArtifacts(t *testing.T) {
	plugin := loadDoubaoPlugin(t)
	source := map[string]any{"status": "succeeded", "content": map[string]any{"video_url": "https://cdn.example/source.mp4"}}
	value, err := plugin.Engine.CallPath(t.Context(), "native", []string{"taskStatus"}, map[string]any{}, map[string]any{"task_id": "task_public", "status": "IN_PROGRESS", "data": source})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"id": "task_public", "status": "running"}, doubaoObject(t, value))
	value, err = plugin.Engine.Call(t.Context(), "listArtifacts", map[string]any{"status": "IN_PROGRESS", "data": source})
	require.NoError(t, err)
	assert.Empty(t, value)

	data := map[string]any{"task_id": "private-media-id", "status": "completed", "result": map[string]any{"video_url": "https://cdn.example/enhanced.mp4"}}
	value, err = plugin.Engine.CallPath(t.Context(), "native", []string{"taskStatus"}, map[string]any{}, map[string]any{"task_id": "task_public", "status": "SUCCESS", "data": data})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"id": "task_public", "status": "succeeded", "content": map[string]any{"video_url": "https://cdn.example/enhanced.mp4"}}, doubaoObject(t, value))
	value, err = plugin.Engine.Call(t.Context(), "listArtifacts", map[string]any{"status": "SUCCESS", "data": data})
	require.NoError(t, err)
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"key":"video","type":"video","mimeType":"video/mp4"}]`, string(encoded))
	value, err = plugin.Engine.Call(t.Context(), "buildContentRequest", map[string]any{"data": data, "artifactKey": "video", "clientRequest": map[string]any{"method": "HEAD"}})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"url": "https://cdn.example/enhanced.mp4", "method": "HEAD", "credentialless": true}, doubaoObject(t, value))
}

func TestDoubaoMediaKitRejectsUnknownOrMissingOutputs(t *testing.T) {
	plugin := loadDoubaoPlugin(t)
	for _, tc := range []struct {
		phase, status string
		body          map[string]any
	}{
		{"generation", "FAILURE", map[string]any{"status": "succeeded"}},
		{"enhancement_submit", "UNKNOWN", map[string]any{"success": true}},
		{"enhancement", "UNKNOWN", map[string]any{"status": "new-vendor-status"}},
		{"enhancement", "FAILURE", map[string]any{"status": "completed", "result": map[string]any{"thumbnail_url": "https://cdn.example/thumb.jpg"}}},
	} {
		value, err := plugin.Engine.Call(t.Context(), "parseTaskResult", map[string]any{"state": map[string]any{"version": 1, "phase": tc.phase}}, tc.body, map[string]any{"status": 200})
		require.NoError(t, err)
		assert.Equal(t, tc.status, doubaoObject(t, value)["status"])
	}
}

func TestDoubaoLegacyTasksAndZeroTokenUsageRemainCompatible(t *testing.T) {
	plugin := loadDoubaoPlugin(t)
	body := map[string]any{"status": "succeeded", "usage": map[string]any{"completion_tokens": 0, "total_tokens": 123}, "content": map[string]any{"video_url": "https://cdn.example/source.mp4"}}
	ctx := map[string]any{"apiKey": "ark|media", "baseUrl": "https://ark.example", "taskId": "legacy-ark-id"}
	value, err := plugin.Engine.Call(t.Context(), "parseTaskResult", ctx, body)
	require.NoError(t, err)
	result := doubaoObject(t, value)
	assert.Equal(t, "SUCCESS", result["status"], "changing credentials must not add a stage to an old plain Ark task")
	assert.NotContains(t, result, "state")
	value, err = plugin.Engine.Call(t.Context(), "extractUsageOnComplete", ctx, result, body)
	require.NoError(t, err)
	assert.Equal(t, float64(0), doubaoObject(t, value)["tokens"], "explicit zero must not fall back to total tokens")
}

func TestDoubaoMediaKitPollingPersistsStagesAndSettlesOnlyFinalOutput(t *testing.T) {
	for _, terminal := range []string{"completed", "failed"} {
		t.Run(terminal, func(t *testing.T) {
			plugin := loadDoubaoPlugin(t)
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			require.NoError(t, db.AutoMigrate(&model.Task{}, &model.Channel{}, &model.User{}, &model.Token{}, &model.Log{}))
			previousDB, previousLogDB := model.DB, model.LOG_DB
			previousRedis, previousMemory, previousBatch := common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled
			previousFactory := service.GetTaskAdaptorFunc
			previousDBType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
			model.DB, model.LOG_DB = db, db
			common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled = false, false, false
			common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
			service.GetTaskAdaptorFunc = func(_ constant.TaskPlatform) service.TaskPollingAdaptor { return pluginadaptor.New(plugin) }
			t.Cleanup(func() {
				model.DB, model.LOG_DB = previousDB, previousLogDB
				common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled = previousRedis, previousMemory, previousBatch
				common.SetDatabaseTypes(previousDBType, previousLogType)
				service.GetTaskAdaptorFunc = previousFactory
			})

			var enhancementRequests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v3/contents/generations/tasks":
					assert.Equal(t, "Bearer ark-key", r.Header.Get("Authorization"))
					var body map[string]any
					if assert.NoError(t, common.DecodeJson(r.Body, &body)) {
						assert.Equal(t, "480p", body["resolution"])
					}
					_, _ = io.WriteString(w, `{"id":"ark-private-id"}`)
				case "/api/v3/contents/generations/tasks/ark-private-id":
					assert.Equal(t, "Bearer ark-key", r.Header.Get("Authorization"))
					_, _ = io.WriteString(w, `{"id":"ark-private-id","status":"succeeded","duration":4,"resolution":"480p","usage":{"completion_tokens":40000},"content":{"video_url":"https://cdn.example/source.mp4"}}`)
				case "/api/v1/tools/enhance-video":
					assert.Equal(t, http.MethodPost, r.Method)
					assert.Equal(t, "Bearer media-key", r.Header.Get("Authorization"))
					var body map[string]any
					if assert.NoError(t, common.DecodeJson(r.Body, &body)) {
						enhancementRequests = append(enhancementRequests, body)
					}
					if len(enhancementRequests) == 1 {
						w.WriteHeader(http.StatusServiceUnavailable) // provider accepted, but response was lost
						return
					}
					_, _ = io.WriteString(w, `{"success":true,"task_id":"media-private-id"}`)
				case "/api/v1/tasks/media-private-id":
					assert.Equal(t, "Bearer media-key", r.Header.Get("Authorization"))
					if terminal == "failed" {
						_, _ = io.WriteString(w, `{"status":"failed","success":false}`)
						return
					}
					_, _ = io.WriteString(w, `{"status":"completed","result":{"video_url":"https://cdn.example/enhanced.mp4"}}`)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			keys, err := common.Marshal(map[string]string{"ark_api_key": "ark-key", "mediakit_api_key": "media-key", "mediakit_base_url": server.URL})
			require.NoError(t, err)
			ch := &model.Channel{Type: constant.ChannelTypeTaskPlugin, Key: string(keys), BaseURL: &server.URL, Status: common.ChannelStatusEnabled, UsedQuota: 300000}
			require.NoError(t, db.Create(ch).Error)
			user := &model.User{Username: "mediakit-user", Quota: 700000, UsedQuota: 300000}
			require.NoError(t, db.Create(user).Error)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
			ctx.Set("task_request", map[string]any{"model": "doubao-seedance-2-0-260128", "prompt": "sunrise", "seconds": 5, "resolution": "720p"})
			info := &relaycommon.RelayInfo{OriginModelName: "doubao-seedance-2-0-260128", ChannelMeta: &relaycommon.ChannelMeta{ApiKey: string(keys), ChannelBaseUrl: server.URL}, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"}}
			adaptor := pluginadaptor.New(plugin)
			adaptor.Init(info)
			require.Nil(t, adaptor.ValidateRequestAndSetAction(ctx, info))
			facts, err := adaptor.ExtractUsageFactsValidated(ctx, info)
			require.NoError(t, err)
			requestBody, err := adaptor.BuildRequestBody(ctx, info)
			require.NoError(t, err)
			resp, err := adaptor.DoRequest(ctx, info, requestBody)
			require.NoError(t, err)
			submitted, taskErr := adaptor.ParseResponse(ctx, resp, info)
			require.NoError(t, resp.Body.Close())
			require.Nil(t, taskErr)
			task := &model.Task{TaskID: "task_public", Platform: "doubao", ChannelId: ch.Id, UserId: user.Id, Quota: 300000, Status: model.TaskStatusSubmitted, Data: submitted.TaskData,
				Properties: model.Properties{OriginModelName: info.OriginModelName},
				PrivateData: model.TaskPrivateData{UpstreamTaskID: submitted.UpstreamTaskID, PluginState: submitted.PluginState, BillingContext: &model.TaskBillingContext{GroupRatio: 1, TieredSnapshot: &billingexpr.BillingSnapshot{
					ExprString:  `tier("enhanced", u("tokens") * 10 / 1000000 + u("enhancement_seconds") * 0.01)`,
					ExprVersion: 1, TaskUsageBilling: true, GroupRatio: 1, QuotaPerUnit: 500000, UsageFacts: facts,
				}}},
			}
			require.NoError(t, db.Create(task).Error)

			// Each pass reloads persisted state and obtains a fresh adaptor, as after restart.
			for _, phase := range []string{"generation completes", "enhancement retry", "enhancement acknowledged", "enhancement finishes"} {
				require.NoError(t, db.First(task, task.ID).Error)
				err = service.UpdateVideoTasks(context.Background(), "doubao", map[int][]string{ch.Id: {submitted.UpstreamTaskID}}, map[string]*model.Task{submitted.UpstreamTaskID: task})
				require.NoError(t, err)
				require.NoError(t, db.First(task, task.ID).Error)
				assert.Equal(t, "ark-private-id", task.GetUpstreamTaskID(), "phase state must not mutate upstream identity")
				if phase != "enhancement finishes" {
					assert.EqualValues(t, model.TaskStatusInProgress, task.Status)
					assert.Equal(t, 300000, task.Quota, "generation success must not settle or refund")
					assert.Empty(t, task.GetResultURL(), "unenhanced output must not be published")
				}
			}
			require.Len(t, enhancementRequests, 2)
			assert.Equal(t, enhancementRequests[0], enhancementRequests[1], "retries must use a stable vendor idempotency key")
			assert.Equal(t, "https://cdn.example/source.mp4", enhancementRequests[0]["video_url"])
			assert.Equal(t, "1080p", enhancementRequests[0]["resolution"])
			require.NoError(t, db.First(user, user.Id).Error)
			if terminal == "failed" {
				assert.EqualValues(t, model.TaskStatusFailure, task.Status)
				assert.Zero(t, task.Quota)
				assert.Equal(t, 1000000, user.Quota)
			} else {
				assert.EqualValues(t, model.TaskStatusSuccess, task.Status)
				assert.Equal(t, "https://cdn.example/enhanced.mp4", task.GetResultURL())
				assert.Equal(t, 220000, task.Quota, "actual tokens and source duration replace both estimates")
				assert.Equal(t, 780000, user.Quota)
			}
			// A stale process losing the terminal CAS must not credit or charge twice.
			stale := *task
			stale.Status = model.TaskStatusInProgress
			require.NoError(t, service.UpdateVideoTasks(t.Context(), "doubao", map[int][]string{ch.Id: {submitted.UpstreamTaskID}}, map[string]*model.Task{submitted.UpstreamTaskID: &stale}))
			var after model.User
			require.NoError(t, db.First(&after, user.Id).Error)
			assert.Equal(t, user.Quota, after.Quota)
		})
	}
}
