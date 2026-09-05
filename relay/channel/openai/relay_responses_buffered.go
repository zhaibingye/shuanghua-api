package openai

import (
	"bufio"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// OaiResponsesBufferedStreamHandler consumes an upstream Responses SSE stream
// and returns one normal Responses JSON document. CLIProxyAPI streams Codex
// requests internally even when its caller requested a non-streaming response.
func OaiResponsesBufferedStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	accumulator := relayconvert.NewResponsesBufferedAccumulator()
	var finalResponse *dto.OpenAIResponsesResponse
	var createdResponse *dto.OpenAIResponsesResponse
	scanner := helper.NewStreamScanner(resp.Body)
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 5 || line[:5] != "data:" {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}

		var event dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &event); err != nil {
			logger.LogError(c, "failed to unmarshal buffered Codex Responses event: "+err.Error())
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		accumulator.ProcessEvent(&event)
		if event.Type == "response.created" && event.Response != nil {
			createdResponse = event.Response
		}
		switch event.Type {
		case "response.completed", "response.done", "response.incomplete":
			finalResponse = event.Response
			if event.Type == "response.incomplete" {
				if finalResponse == nil {
					finalResponse = &dto.OpenAIResponsesResponse{}
				}
				if len(finalResponse.Status) == 0 {
					finalResponse.Status = []byte(`"incomplete"`)
				}
			}
		case "response.failed", "response.error":
			if event.Response != nil {
				if oaiErr := event.Response.GetOpenAIError(); oaiErr != nil && oaiErr.Type != "" {
					return nil, types.WithOpenAIError(*oaiErr, http.StatusInternalServerError)
				}
			}
			return nil, types.NewOpenAIError(fmt.Errorf("responses stream error: %s", event.Type), types.ErrorCodeBadResponse, http.StatusInternalServerError)
		}
		if finalResponse != nil {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	if finalResponse == nil {
		finalResponse = &dto.OpenAIResponsesResponse{
			ID:        helper.GetResponseID(c),
			CreatedAt: int(time.Now().Unix()),
			Model:     info.UpstreamModelName,
			Status:    []byte(`"completed"`),
		}
	}
	if createdResponse != nil {
		if finalResponse.ID == "" {
			finalResponse.ID = createdResponse.ID
		}
		if finalResponse.Model == "" {
			finalResponse.Model = createdResponse.Model
		}
		if finalResponse.CreatedAt == 0 {
			finalResponse.CreatedAt = createdResponse.CreatedAt
		}
	}
	accumulator.SupplementResponseOutput(finalResponse)

	for _, output := range finalResponse.Output {
		switch output.Type {
		case dto.BuildInCallWebSearchCall:
			info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
		case dto.BuildInCallFileSearchCall:
			info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
		case dto.BuildInCallFunctionCall:
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, output.Name)
		}
	}
	usage := relayconvert.UsageFromResponsesUsage(finalResponse.Usage)
	responseBody, err := common.Marshal(finalResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	service.IOCopyBytesGracefully(c, resp, responseBody)
	return usage, nil
}
