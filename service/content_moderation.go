package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/cachex"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/samber/hot"
	"gorm.io/gorm"
)

const (
	moderationMaxRequestBytes     = 128 * 1024
	moderationExcerptMaxBytes     = 2048
	moderationMaxImages           = 4
	moderationMaxInlineDataBytes  = 2 * 1024 * 1024
	moderationAllowCacheTTL       = 24 * time.Hour
	moderationFingerprintCacheCap = 100_000
	moderationCleanupInterval     = time.Hour
)

var (
	ErrModerationUserPermissionDenied = errors.New("cannot manage moderation records for users with equal or higher role")
	ErrInvalidModerationUserRequest   = errors.New("invalid moderation user request")
	moderationResponseWriterKey       = constant.ContextKeyModerationCapture

	moderationFingerprintCache     *cachex.HybridCache[string]
	moderationFingerprintCacheOnce sync.Once
)

type ModerationRequestContent struct {
	UserPrompt string   `json:"user_prompt"`
	ImageURLs  []string `json:"image_urls,omitempty"`
}

func (c ModerationRequestContent) IsEmpty() bool {
	return strings.TrimSpace(c.UserPrompt) == "" && len(c.ImageURLs) == 0
}

type ModerationUserListItem struct {
	RecordID             int64  `json:"record_id"`
	UserID               int    `json:"user_id"`
	Username             string `json:"username"`
	DisplayName          string `json:"display_name"`
	Email                string `json:"email"`
	AccountStatus        int    `json:"account_status"`
	RecordStatus         string `json:"record_status"`
	ViolationCount       int    `json:"violation_count"`
	ActualViolationCount int    `json:"actual_violation_count"`
	MaxViolationCount    int    `json:"max_violation_count"`
	LastViolationAt      int64  `json:"last_violation_at"`
	Note                 string `json:"note"`
	ArchivedAt           int64  `json:"archived_at,omitempty"`
	CreatedAt            int64  `json:"created_at"`
	UpdatedAt            int64  `json:"updated_at"`
}

type ModerationUserDetail struct {
	User   ModerationUserListItem  `json:"user"`
	Events []model.ModerationEvent `json:"events"`
}

type openAIModerationImageURL struct {
	URL string `json:"url"`
}

type openAIModerationInputItem struct {
	Type     string                    `json:"type"`
	Text     string                    `json:"text,omitempty"`
	ImageURL *openAIModerationImageURL `json:"image_url,omitempty"`
}

type openAIModerationResult struct {
	Flagged                   bool                `json:"flagged"`
	Categories                map[string]bool     `json:"categories"`
	CategoryScores            map[string]float64  `json:"category_scores"`
	Scores                    map[string]float64  `json:"scores,omitempty"`
	CategoryAppliedInputTypes map[string][]string `json:"category_applied_input_types,omitempty"`
}

type openAIModerationResponse struct {
	ID      string                   `json:"id"`
	Model   string                   `json:"model"`
	Results []openAIModerationResult `json:"results"`
}

type moderationDecision struct {
	Decision   string   `json:"decision"`
	Actor      string   `json:"actor"`
	Severity   string   `json:"severity"`
	Categories []string `json:"categories"`
	Confidence float64  `json:"confidence"`
	ReasonCode string   `json:"reason_code"`
}

type ModerationCapture struct {
	gin.ResponseWriter
	buf     bytes.Buffer
	maxSize int
}

func NewModerationCapture(writer gin.ResponseWriter) *ModerationCapture {
	return &ModerationCapture{
		ResponseWriter: writer,
		maxSize:        moderationMaxRequestBytes,
	}
}

func (w *ModerationCapture) Write(data []byte) (int, error) {
	if w.buf.Len() < w.maxSize {
		remain := w.maxSize - w.buf.Len()
		if remain >= len(data) {
			w.buf.Write(data)
		} else {
			w.buf.Write(data[:remain])
		}
	}
	return w.ResponseWriter.Write(data)
}

func (w *ModerationCapture) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func (w *ModerationCapture) ReadFrom(reader io.Reader) (int64, error) {
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		var tee bytes.Buffer
		n, err := rf.ReadFrom(io.TeeReader(reader, &tee))
		data := tee.Bytes()
		if w.buf.Len() < w.maxSize {
			remain := w.maxSize - w.buf.Len()
			if remain >= len(data) {
				w.buf.Write(data)
			} else {
				w.buf.Write(data[:remain])
			}
		}
		return n, err
	}
	return io.Copy(w, reader)
}

func (w *ModerationCapture) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *ModerationCapture) Bytes() []byte {
	return w.buf.Bytes()
}

func BeginModerationCapture(c *gin.Context, _ dto.Request) (string, bool) {
	if c == nil {
		return "", false
	}
	capture := NewModerationCapture(c.Writer)
	c.Writer = capture
	common.SetContextKey(c, moderationResponseWriterKey, capture)
	return "", true
}

func IsModerationRequestSupported(request dto.Request) bool {
	switch request.(type) {
	case *dto.GeneralOpenAIRequest, *dto.OpenAIResponsesRequest, *dto.OpenAIResponsesCompactionRequest, *dto.ClaudeRequest, *dto.GeminiChatRequest:
		return true
	default:
		return false
	}
}

func GetModerationRequestContent(c *gin.Context) (ModerationRequestContent, bool) {
	if c == nil {
		return ModerationRequestContent{}, false
	}
	return common.GetContextKeyType[ModerationRequestContent](c, constant.ContextKeyModerationRequestContent)
}

func SetModerationRequestContent(c *gin.Context, request dto.Request) {
	if c == nil || request == nil {
		return
	}
	common.SetContextKey(c, constant.ContextKeyModerationRequestContent, extractLatestUserTurn(request))
}

func SetModerationRequestContentFromJSON(c *gin.Context, data []byte, request dto.Request) {
	if c == nil {
		return
	}
	content := extractLatestUserTurn(request)
	if content.IsEmpty() && len(data) > 0 {
		content = extractLatestUserTurnFromJSON(data)
	}
	common.SetContextKey(c, constant.ContextKeyModerationRequestContent, content)
}

func extractLatestUserTurn(request dto.Request) ModerationRequestContent {
	switch req := request.(type) {
	case *dto.GeneralOpenAIRequest:
		return latestOpenAIUserTurn(req)
	case *dto.ClaudeRequest:
		return latestClaudeUserTurn(req)
	case *dto.GeminiChatRequest:
		return latestGeminiUserTurn(req)
	case *dto.OpenAIResponsesRequest:
		return latestResponsesUserTurn(req)
	default:
		return ModerationRequestContent{}
	}
}

func latestOpenAIUserTurn(req *dto.GeneralOpenAIRequest) ModerationRequestContent {
	if req == nil {
		return ModerationRequestContent{}
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if !strings.EqualFold(msg.Role, "user") {
			continue
		}
		return ModerationRequestContent{
			UserPrompt: openAIMessageText(msg),
			ImageURLs:  limitImageURLs(extractOpenAIImages(msg)),
		}
	}
	if req.Prompt != nil {
		switch v := req.Prompt.(type) {
		case string:
			return ModerationRequestContent{UserPrompt: v}
		case []any:
			var texts []string
			for _, item := range v {
				if str, ok := item.(string); ok && strings.TrimSpace(str) != "" {
					texts = append(texts, str)
				}
			}
			if len(texts) > 0 {
				return ModerationRequestContent{UserPrompt: strings.Join(texts, "\n")}
			}
		case []string:
			if len(v) > 0 {
				return ModerationRequestContent{UserPrompt: strings.Join(v, "\n")}
			}
		default:
			str := fmt.Sprintf("%v", req.Prompt)
			if strings.TrimSpace(str) != "" {
				return ModerationRequestContent{UserPrompt: str}
			}
		}
	}
	return ModerationRequestContent{}
}

func latestClaudeUserTurn(req *dto.ClaudeRequest) ModerationRequestContent {
	if req == nil {
		return ModerationRequestContent{}
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if msg.Role != "" && !strings.EqualFold(msg.Role, "user") {
			continue
		}
		return ModerationRequestContent{
			UserPrompt: claudeMessageText(msg),
			ImageURLs:  limitImageURLs(extractClaudeImages(msg)),
		}
	}
	return ModerationRequestContent{}
}

func latestGeminiUserTurn(req *dto.GeminiChatRequest) ModerationRequestContent {
	if req == nil {
		return ModerationRequestContent{}
	}
	for i := len(req.Contents) - 1; i >= 0; i-- {
		content := req.Contents[i]
		if content.Role != "" && !strings.EqualFold(content.Role, "user") {
			continue
		}
		var text strings.Builder
		var imageURLs []string
		for _, part := range content.Parts {
			if part.Text != "" {
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(part.Text)
			}
			if part.InlineData != nil && part.InlineData.Data != "" {
				if len(part.InlineData.Data) <= moderationMaxInlineDataBytes {
					imageURLs = append(imageURLs, fmt.Sprintf("data:%s;base64,%s", part.InlineData.MimeType, part.InlineData.Data))
				}
			}
		}
		return ModerationRequestContent{
			UserPrompt: text.String(),
			ImageURLs:  limitImageURLs(imageURLs),
		}
	}
	return ModerationRequestContent{}
}

func latestResponsesUserTurn(req *dto.OpenAIResponsesRequest) ModerationRequestContent {
	if req == nil || len(req.Input) == 0 {
		return ModerationRequestContent{}
	}
	var asString string
	if err := common.Unmarshal(req.Input, &asString); err == nil && strings.TrimSpace(asString) != "" {
		return ModerationRequestContent{UserPrompt: asString}
	}
	return latestUserTurnFromInputItems(req.Input)
}

func latestUserTurnFromInputItems(raw []byte) ModerationRequestContent {
	var items []map[string]any
	if err := common.Unmarshal(raw, &items); err != nil {
		return ModerationRequestContent{}
	}
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		role, _ := item["role"].(string)
		itemType, _ := item["type"].(string)
		if role != "" && !strings.EqualFold(role, "user") {
			continue
		}
		if itemType != "" && itemType != "message" && itemType != "input_text" && itemType != "text" && itemType != "input_image" {
			if role == "" {
				continue
			}
		}
		text, images := collectInputItemContent(item)
		if strings.TrimSpace(text) == "" && len(images) == 0 {
			continue
		}
		return ModerationRequestContent{UserPrompt: text, ImageURLs: limitImageURLs(images)}
	}
	return ModerationRequestContent{}
}

func collectInputItemContent(item map[string]any) (string, []string) {
	var text strings.Builder
	var images []string
	appendText := func(value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		if text.Len() > 0 {
			text.WriteString("\n")
		}
		text.WriteString(value)
	}
	if value, ok := item["content"].(string); ok {
		appendText(value)
	}
	if value, ok := item["text"].(string); ok {
		appendText(value)
	}
	if imgURL, ok := item["image_url"].(string); ok && strings.TrimSpace(imgURL) != "" {
		images = append(images, strings.TrimSpace(imgURL))
	}
	contentArr, _ := item["content"].([]any)
	for _, part := range contentArr {
		partMap, ok := part.(map[string]any)
		if !ok {
			continue
		}
		partType, _ := partMap["type"].(string)
		if txt, ok := partMap["text"].(string); ok {
			appendText(txt)
		}
		if partType == "input_image" || partType == "image_url" {
			if imgURL, ok := partMap["image_url"].(string); ok && strings.TrimSpace(imgURL) != "" {
				images = append(images, strings.TrimSpace(imgURL))
			}
		}
	}
	return text.String(), images
}

func extractLatestUserTurnFromJSON(data []byte) ModerationRequestContent {
	var payload map[string]any
	if err := common.Unmarshal(data, &payload); err != nil {
		return ModerationRequestContent{}
	}
	if req, ok := payload["generateContentRequest"].(map[string]any); ok {
		payload = req
	} else if req, ok := payload["generate_content_request"].(map[string]any); ok {
		payload = req
	}
	if contents, ok := payload["contents"].([]any); ok {
		for i := len(contents) - 1; i >= 0; i-- {
			cmap, ok := contents[i].(map[string]any)
			if !ok {
				continue
			}
			role, _ := cmap["role"].(string)
			if role != "" && !strings.EqualFold(role, "user") {
				continue
			}
			var text strings.Builder
			if parts, ok := cmap["parts"].([]any); ok {
				for _, p := range parts {
					if pmap, ok := p.(map[string]any); ok {
						if txt, ok := pmap["text"].(string); ok && txt != "" {
							if text.Len() > 0 {
								text.WriteString("\n")
							}
							text.WriteString(txt)
						}
					}
				}
			}
			if text.Len() > 0 {
				return ModerationRequestContent{UserPrompt: text.String()}
			}
		}
	}
	if msgs, ok := payload["messages"].([]any); ok {
		for i := len(msgs) - 1; i >= 0; i-- {
			mmap, ok := msgs[i].(map[string]any)
			if !ok {
				continue
			}
			role, _ := mmap["role"].(string)
			if !strings.EqualFold(role, "user") {
				continue
			}
			if content, ok := mmap["content"].(string); ok && strings.TrimSpace(content) != "" {
				return ModerationRequestContent{UserPrompt: content}
			}
		}
	}
	if raw, err := common.Marshal(payload["input"]); err == nil && len(raw) > 0 && string(raw) != "null" {
		return latestUserTurnFromInputItems(raw)
	}
	return ModerationRequestContent{}
}

func openAIMessageText(message dto.Message) string {
	if message.IsStringContent() {
		return message.StringContent()
	}
	var builder strings.Builder
	for _, item := range message.ParseContent() {
		if item.Type == dto.ContentTypeText || item.Type == "" {
			builder.WriteString(item.Text)
		}
	}
	return builder.String()
}

func extractOpenAIImages(message dto.Message) []string {
	if message.IsStringContent() {
		return nil
	}
	var imageURLs []string
	for _, item := range message.ParseContent() {
		if item.Type == dto.ContentTypeImageURL {
			if img := item.GetImageMedia(); img != nil && strings.TrimSpace(img.Url) != "" {
				imageURLs = append(imageURLs, strings.TrimSpace(img.Url))
			}
		}
	}
	return imageURLs
}

func claudeMessageText(message dto.ClaudeMessage) string {
	if message.IsStringContent() {
		return message.GetStringContent()
	}
	media, err := message.ParseContent()
	if err != nil {
		return ""
	}
	var builder strings.Builder
	for _, item := range media {
		if item.Type == "text" || item.Type == "" {
			if t := item.GetText(); t != "" {
				builder.WriteString(t)
			} else {
				builder.WriteString(item.GetStringContent())
			}
		}
	}
	return builder.String()
}

func extractClaudeImages(message dto.ClaudeMessage) []string {
	if message.IsStringContent() {
		return nil
	}
	media, err := message.ParseContent()
	if err != nil {
		return nil
	}
	var imageURLs []string
	for _, item := range media {
		if item.Type == "image" && item.Source != nil && item.Source.Type == "base64" {
			if strData, ok := item.Source.Data.(string); ok && strData != "" {
				if len(strData) <= moderationMaxInlineDataBytes {
					imageURLs = append(imageURLs, fmt.Sprintf("data:%s;base64,%s", item.Source.MediaType, strData))
				}
			}
		}
	}
	return imageURLs
}

func limitImageURLs(urls []string) []string {
	if len(urls) <= moderationMaxImages {
		return urls
	}
	return urls[:moderationMaxImages]
}

func ExtractModerationAssistantText(data []byte, contentType string, _ types.RelayFormat) string {
	if len(data) == 0 {
		return ""
	}
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") || bytes.Contains(data, []byte("data:")) {
		var builder strings.Builder
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 64*1024), moderationMaxRequestBytes)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
				continue
			}
			var item map[string]any
			if err := common.Unmarshal(payload, &item); err == nil {
				builder.WriteString(assistantTextFromPayload(item))
				continue
			}
			var items []map[string]any
			if err := common.Unmarshal(payload, &items); err == nil {
				for _, it := range items {
					builder.WriteString(assistantTextFromPayload(it))
				}
			}
		}
		return builder.String()
	}
	var item map[string]any
	if err := common.Unmarshal(data, &item); err == nil {
		return assistantTextFromPayload(item)
	}
	var items []map[string]any
	if err := common.Unmarshal(data, &items); err == nil {
		var builder strings.Builder
		for _, it := range items {
			builder.WriteString(assistantTextFromPayload(it))
		}
		return builder.String()
	}
	return ""
}

func assistantTextFromPayload(item map[string]any) string {
	if choices, ok := item["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if delta, ok := choice["delta"].(map[string]any); ok {
				if content, ok := delta["content"].(string); ok {
					return content
				}
			}
			if msg, ok := choice["message"].(map[string]any); ok {
				if content, ok := msg["content"].(string); ok {
					return content
				}
			}
		}
	}
	// Anthropic Claude streaming delta: {"type": "content_block_delta", "delta": {"type": "text_delta", "text": "..."}}
	if deltaMap, ok := item["delta"].(map[string]any); ok {
		if text, ok := deltaMap["text"].(string); ok && text != "" {
			return text
		}
	}
	// OpenAI Responses streaming delta or string delta: {"type": "response.text.delta", "delta": "..."}
	if delta, ok := item["delta"].(string); ok {
		return delta
	}
	// Google Gemini candidates: {"candidates": [{"content": {"parts": [{"text": "..."}]}}]}
	if candidates, ok := item["candidates"].([]any); ok && len(candidates) > 0 {
		if cand, ok := candidates[0].(map[string]any); ok {
			if content, ok := cand["content"].(map[string]any); ok {
				if parts, ok := content["parts"].([]any); ok {
					var builder strings.Builder
					for _, p := range parts {
						if part, ok := p.(map[string]any); ok {
							if text, ok := part["text"].(string); ok {
								builder.WriteString(text)
							}
						}
					}
					return builder.String()
				}
			}
		}
	}
	// Anthropic Claude non-streaming content: {"content": [{"type": "text", "text": "..."}]}
	if contentArr, ok := item["content"].([]any); ok {
		var builder strings.Builder
		for _, c := range contentArr {
			if cmap, ok := c.(map[string]any); ok {
				if text, ok := cmap["text"].(string); ok {
					builder.WriteString(text)
				}
			}
		}
		if builder.Len() > 0 {
			return builder.String()
		}
	}
	// OpenAI Responses non-streaming output: {"output": [{"type": "message", "content": [{"type": "text", "text": "..."}]}]}
	if outputArr, ok := item["output"].([]any); ok && len(outputArr) > 0 {
		var builder strings.Builder
		for _, out := range outputArr {
			if outMap, ok := out.(map[string]any); ok {
				if contentArr, ok := outMap["content"].([]any); ok {
					for _, c := range contentArr {
						if cMap, ok := c.(map[string]any); ok {
							if text, ok := cMap["text"].(string); ok && text != "" {
								builder.WriteString(text)
							}
						}
					}
				}
			}
		}
		if builder.Len() > 0 {
			return builder.String()
		}
	}
	return ""
}

func ValidateContentModerationURL(rawURL string) error {
	endpoint := strings.TrimSpace(rawURL)
	if endpoint == "" {
		return nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("content moderation API URL must be an absolute HTTP(S) URL without credentials or fragments")
	}
	for key := range parsed.Query() {
		normalizedKey := strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(key))
		switch normalizedKey {
		case "key", "apikey", "token", "accesstoken", "secret", "apisecret":
			return errors.New("content moderation API URL must not contain credential query parameters")
		}
	}
	return nil
}

func appendModerationEndpointSuffix(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	path := strings.TrimRight(parsed.Path, "/")
	if path == "" {
		parsed.Path = "/v1/moderations"
	} else if strings.HasSuffix(path, "/v1") {
		parsed.Path = path + "/moderations"
	} else if !strings.HasSuffix(path, "/moderations") {
		parsed.Path = path + "/moderations"
	}
	return parsed.String()
}

func moderationEndpoint(config setting.ContentModerationSetting) (string, error) {
	endpoint := strings.TrimSpace(config.BaseURL)
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}
	if endpoint = appendModerationEndpointSuffix(endpoint); endpoint == "" {
		return "", errors.New("content moderation API URL is empty")
	}
	if err := ValidateContentModerationURL(endpoint); err != nil {
		return "", err
	}
	return endpoint, nil
}

func executeOpenAIModerationCall(ctx context.Context, config setting.ContentModerationSetting, inputVal any) (openAIModerationResult, []byte, error) {
	var emptyResult openAIModerationResult
	endpoint, err := moderationEndpoint(config)
	if err != nil {
		return emptyResult, nil, err
	}

	modelName := strings.TrimSpace(config.Model)
	if modelName == "" {
		modelName = setting.DefaultContentModerationModel
	}

	reqPayload := map[string]any{
		"model": modelName,
		"input": inputVal,
	}
	reqBody, err := common.Marshal(reqPayload)
	if err != nil {
		return emptyResult, nil, fmt.Errorf("marshal moderation request: %w", err)
	}

	timeout := time.Duration(config.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	client := moderationHTTPClient(timeout)

	maxRetries := config.MaxRetries
	if maxRetries < 1 {
		maxRetries = 1
	} else if maxRetries > 5 {
		maxRetries = 5
	}

	keys := config.ResolvedAPIKeys()
	start := 0
	if len(keys) > 1 {
		start = nextModerationAPIKeyIndex(len(keys))
	}

	var respBytes []byte
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		apiKey := ""
		if len(keys) > 0 {
			apiKey = keys[(start+attempt)%len(keys)]
		}
		status, body, doErr := postOpenAIModeration(ctx, client, timeout, endpoint, apiKey, reqBody)
		respBytes = body
		if doErr != nil {
			lastErr = doErr
			continue
		}
		if isRetryableModerationStatus(status) {
			lastErr = fmt.Errorf("moderation upstream error (status %d): %s", status, string(respBytes))
			continue
		}
		if status != http.StatusOK {
			return emptyResult, respBytes, fmt.Errorf("moderation upstream rejected request (status %d): %s", status, string(respBytes))
		}

		var decoded openAIModerationResponse
		if err := common.Unmarshal(respBytes, &decoded); err != nil {
			return emptyResult, respBytes, fmt.Errorf("decode moderation response: %w", err)
		}
		if len(decoded.Results) == 0 {
			return emptyResult, respBytes, errors.New("moderation provider returned no results")
		}

		merged := decoded.Results[0]
		mergedCategories := make(map[string]bool, len(merged.Categories))
		for k, v := range merged.Categories {
			mergedCategories[k] = v
		}
		mergedScores := make(map[string]float64)
		srcScores := merged.CategoryScores
		if srcScores == nil {
			srcScores = merged.Scores
		}
		for k, v := range srcScores {
			mergedScores[k] = v
		}
		merged.Categories = mergedCategories
		merged.CategoryScores = mergedScores

		for i := 1; i < len(decoded.Results); i++ {
			r := decoded.Results[i]
			if r.Flagged {
				merged.Flagged = true
			}
			rScores := r.CategoryScores
			if rScores == nil {
				rScores = r.Scores
			}
			for cat, flagged := range r.Categories {
				if flagged {
					merged.Categories[cat] = true
				}
			}
			for cat, score := range rScores {
				if score > merged.CategoryScores[cat] {
					merged.CategoryScores[cat] = score
				}
			}
		}
		return merged, respBytes, nil
	}

	return emptyResult, respBytes, fmt.Errorf("moderation request failed after %d attempts: %w", maxRetries, lastErr)
}

func sanitizeModerationText(s string) string {
	s = strings.TrimSpace(s)
	const maxLen = 25000
	if len(s) > maxLen {
		end := maxLen
		for end > 0 && !utf8.RuneStart(s[end]) {
			end--
		}
		return s[:end]
	}
	return s
}

func truncateUTF8Bytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

func moderationExcerpt(text string, imageCount int) string {
	excerpt := truncateUTF8Bytes(strings.TrimSpace(text), moderationExcerptMaxBytes)
	if imageCount <= 0 {
		return excerpt
	}
	suffix := fmt.Sprintf("\n[%d images not saved]", imageCount)
	if len(excerpt)+len(suffix) <= moderationExcerptMaxBytes {
		return excerpt + suffix
	}
	keep := moderationExcerptMaxBytes - len(suffix)
	if keep < 0 {
		return truncateUTF8Bytes(suffix, moderationExcerptMaxBytes)
	}
	return truncateUTF8Bytes(excerpt, keep) + suffix
}

func callNativeModeration(ctx context.Context, config setting.ContentModerationSetting, inputs ...string) (openAIModerationResult, []byte, error) {
	var validInputs []string
	for _, in := range inputs {
		in = sanitizeModerationText(in)
		if in != "" {
			validInputs = append(validInputs, in)
		}
	}
	if len(validInputs) == 0 {
		return openAIModerationResult{}, nil, errors.New("moderation input is empty")
	}
	var inputVal any = validInputs
	if len(validInputs) == 1 {
		inputVal = validInputs[0]
	}
	return executeOpenAIModerationCall(ctx, config, inputVal)
}

func callModerationContent(ctx context.Context, config setting.ContentModerationSetting, content ModerationRequestContent) (openAIModerationResult, []byte, error) {
	modelName := strings.TrimSpace(config.Model)
	if modelName == "" {
		modelName = setting.DefaultContentModerationModel
	}
	isOmni := strings.Contains(strings.ToLower(modelName), "omni") || modelName == setting.DefaultContentModerationModel
	if len(content.ImageURLs) > 0 && isOmni {
		var items []openAIModerationInputItem
		if u := sanitizeModerationText(content.UserPrompt); u != "" {
			items = append(items, openAIModerationInputItem{Type: "text", Text: u})
		}
		for _, imgURL := range content.ImageURLs {
			if strings.TrimSpace(imgURL) != "" {
				items = append(items, openAIModerationInputItem{
					Type:     "image_url",
					ImageURL: &openAIModerationImageURL{URL: imgURL},
				})
			}
		}
		if len(items) > 0 {
			return executeOpenAIModerationCall(ctx, config, items)
		}
	}
	if u := sanitizeModerationText(content.UserPrompt); u != "" {
		return callNativeModeration(ctx, config, u)
	}
	return openAIModerationResult{Flagged: false}, nil, nil
}

func moderationDecisionFromNativeResult(result openAIModerationResult) moderationDecision {
	scores := result.CategoryScores
	if scores == nil {
		scores = result.Scores
	}
	categories := make([]string, 0)
	maxScore := 0.0
	for category, flagged := range result.Categories {
		if flagged {
			categories = append(categories, category)
		}
		if scores != nil {
			if s, ok := scores[category]; ok && s > maxScore {
				maxScore = s
			}
		}
	}
	sort.Strings(categories)
	severity := "none"
	if maxScore >= 0.9 {
		severity = "critical"
	} else if maxScore >= 0.75 {
		severity = "high"
	} else if maxScore >= 0.5 {
		severity = "medium"
	} else if len(categories) > 0 {
		severity = "low"
	}
	decision := "allow"
	reasonCode := ""
	if result.Flagged || len(categories) > 0 {
		decision = "block"
		if len(categories) > 0 {
			reasonCode = categories[0]
		} else {
			reasonCode = "flagged"
		}
	}
	return moderationDecision{
		Decision:   decision,
		Actor:      model.ModerationEventActorUser,
		Severity:   severity,
		Categories: categories,
		Confidence: maxScore,
		ReasonCode: reasonCode,
	}
}

func moderationContentFingerprint(content ModerationRequestContent) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(content.UserPrompt))
	builder.WriteByte(0)
	for _, img := range content.ImageURLs {
		builder.WriteString(strings.TrimSpace(img))
		builder.WriteByte(0)
	}
	return hex.EncodeToString(common.Sha256Raw([]byte(builder.String())))
}

func getModerationFingerprintCache() *cachex.HybridCache[string] {
	moderationFingerprintCacheOnce.Do(func() {
		moderationFingerprintCache = cachex.NewHybridCache[string](cachex.HybridCacheConfig[string]{
			Namespace: cachex.Namespace("moderation_fp_v1"),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.StringCodec{},
			Memory: func() *hot.HotCache[string, string] {
				return hot.NewHotCache[string, string](hot.LRU, moderationFingerprintCacheCap).
					WithTTL(moderationAllowCacheTTL).
					WithJanitor().
					Build()
			},
		})
	})
	return moderationFingerprintCache
}

func lookupModerationFingerprint(fp string) string {
	if fp == "" {
		return ""
	}
	value, found, err := getModerationFingerprintCache().Get(fp)
	if err != nil || !found {
		return ""
	}
	return value
}

func storeModerationFingerprint(fp, decision string, ttl time.Duration) {
	if fp == "" || decision == "" || ttl <= 0 {
		return
	}
	if err := getModerationFingerprintCache().SetWithTTL(fp, decision, ttl); err != nil {
		common.SysError("failed to cache content moderation fingerprint: " + err.Error())
	}
}

func resetModerationFingerprintCache() {
	if moderationFingerprintCache == nil {
		return
	}
	_ = moderationFingerprintCache.Purge()
}

func PreflightModerationRequest(ctx context.Context, content ModerationRequestContent, config setting.ContentModerationSetting) error {
	if !config.PreflightEnabled || !config.HasAPIKey() || strings.TrimSpace(config.Model) == "" {
		return nil
	}
	if content.IsEmpty() {
		return nil
	}
	fp := moderationContentFingerprint(content)
	switch lookupModerationFingerprint(fp) {
	case "allow":
		return nil
	case "block":
		return model.ErrModerationBlocked
	}

	result, _, err := callModerationContent(ctx, config, content)
	if err != nil {
		if config.FailureMode == "open" {
			return nil
		}
		return fmt.Errorf("content moderation preflight unavailable: %w", err)
	}
	if !result.Flagged {
		storeModerationFingerprint(fp, "allow", moderationAllowCacheTTL)
		return nil
	}

	decision := moderationDecisionFromNativeResult(result)
	if ginCtx, ok := ctx.(*gin.Context); ok {
		recordModerationEventFromContext(ginCtx, content, "", decision, model.ModerationEventSourcePreflight, config)
	}
	storeModerationFingerprint(fp, "block", config.GetViolationRetentionDuration())
	return model.ErrModerationBlocked
}

func FinalizeModeration(c *gin.Context, info *relaycommon.RelayInfo, relayErr *types.NewAPIError) {
	if c == nil || info == nil {
		return
	}
	if relayErr != nil && relayErr.GetErrorCode() == types.ErrorCodeContentModerationBlocked {
		return
	}
	config := setting.GetContentModerationSetting()
	if !config.Enabled || !config.PostflightEnabled || config.IsUserWhitelisted(info.UserId) {
		return
	}
	channelID := moderationChannelID(c, info)
	if !config.ShouldModerateChannel(channelID) {
		return
	}
	if !config.HasAPIKey() || strings.TrimSpace(config.Model) == "" {
		return
	}
	capture, _ := common.GetContextKeyType[*ModerationCapture](c, moderationResponseWriterKey)
	if capture == nil {
		return
	}
	assistantReply := ExtractModerationAssistantText(capture.Bytes(), c.Writer.Header().Get("Content-Type"), info.RelayFormat)
	if strings.TrimSpace(assistantReply) == "" {
		return
	}
	content, _ := GetModerationRequestContent(c)
	go func(reply string, userContent ModerationRequestContent, cfg setting.ContentModerationSetting, uid, chID int, requestID, modelName, relayFormat string) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
		defer cancel()
		result, _, err := callNativeModeration(ctx, cfg, reply)
		if err != nil || !result.Flagged {
			return
		}
		decision := moderationDecisionFromNativeResult(result)
		decision.Actor = model.ModerationEventActorAssistant
		now := common.GetTimestamp()
		expiresAt := now + int64(cfg.GetViolationRetentionDuration().Seconds())
		_ = persistModerationEvent(model.ModerationEvent{
			UserID:           uid,
			RequestID:        requestID,
			ChannelID:        chID,
			Model:            modelName,
			RelayFormat:      relayFormat,
			Source:           model.ModerationEventSourcePostflight,
			Actor:            model.ModerationEventActorAssistant,
			Decision:         decision.Decision,
			Severity:         decision.Severity,
			Categories:       marshalModerationCategories(decision.Categories),
			Confidence:       decision.Confidence,
			ReasonCode:       decision.ReasonCode,
			UserExcerpt:      moderationExcerpt(userContent.UserPrompt, len(userContent.ImageURLs)),
			AssistantExcerpt: moderationExcerpt(reply, 0),
			ImageCount:       len(userContent.ImageURLs),
			Status:           model.ModerationEventActive,
			CreatedAt:        now,
			ExpiresAt:        expiresAt,
		}, false)
	}(assistantReply, content, config, info.UserId, channelID, info.RequestId, info.OriginModelName, string(info.RelayFormat))
}

func moderationChannelID(c *gin.Context, info *relaycommon.RelayInfo) int {
	channelID := 0
	if info != nil {
		channelID = info.GetChannelID()
	}
	if channelID <= 0 && c != nil {
		channelID = common.GetContextKeyInt(c, constant.ContextKeyChannelId)
	}
	if channelID <= 0 && c != nil {
		channelID = c.GetInt("channel_id")
	}
	return channelID
}

func marshalModerationCategories(categories []string) string {
	if len(categories) == 0 {
		return "[]"
	}
	raw, err := common.Marshal(categories)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func recordModerationEventFromContext(c *gin.Context, content ModerationRequestContent, assistantReply string, decision moderationDecision, source string, config setting.ContentModerationSetting) {
	if c == nil {
		return
	}
	userID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	if userID <= 0 {
		return
	}
	channelID := common.GetContextKeyInt(c, constant.ContextKeyChannelId)
	if channelID <= 0 {
		channelID = c.GetInt("channel_id")
	}
	now := common.GetTimestamp()
	expiresAt := now + int64(config.GetViolationRetentionDuration().Seconds())
	fp := moderationContentFingerprint(content)
	modelName := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)
	if modelName == "" {
		modelName = config.Model
	}
	relayFormat := c.GetString("relay_format")
	if relayFormat == "" {
		relayFormat = string(types.RelayFormatOpenAI)
	}
	event := model.ModerationEvent{
		UserID:             userID,
		RequestID:          c.GetString(common.RequestIdKey),
		ChannelID:          channelID,
		Model:              modelName,
		RelayFormat:        relayFormat,
		Source:             source,
		Actor:              decision.Actor,
		Decision:           decision.Decision,
		Severity:           decision.Severity,
		Categories:         marshalModerationCategories(decision.Categories),
		Confidence:         decision.Confidence,
		ReasonCode:         decision.ReasonCode,
		UserExcerpt:        moderationExcerpt(content.UserPrompt, len(content.ImageURLs)),
		AssistantExcerpt:   moderationExcerpt(assistantReply, 0),
		ContentFingerprint: fp,
		ImageCount:         len(content.ImageURLs),
		Status:             model.ModerationEventActive,
		CreatedAt:          now,
		ExpiresAt:          expiresAt,
	}
	if err := persistModerationEvent(event, decision.Actor == model.ModerationEventActorUser); err != nil {
		common.SysError(fmt.Sprintf("failed to record content moderation event: %v", err))
	}
}

func persistModerationEvent(event model.ModerationEvent, userViolation bool) error {
	if model.DB == nil {
		return errors.New("database is not initialized")
	}
	if event.UserID <= 0 {
		return errors.New("invalid moderation event")
	}
	if event.Status == "" {
		event.Status = model.ModerationEventActive
	}
	if event.CreatedAt <= 0 {
		event.CreatedAt = common.GetTimestamp()
	}
	if err := model.DB.Create(&event).Error; err != nil {
		return err
	}
	if !userViolation {
		return nil
	}
	updateUserRecordOnViolation(event.UserID, event.CreatedAt)
	maybeAutoDisableUser(event.UserID, event.CreatedAt)
	return nil
}

func updateUserRecordOnViolation(userID int, now int64) {
	if userID <= 0 {
		return
	}
	username, displayName, email := snapshotModerationUser(userID)
	var record model.ModerationUserRecord
	err := model.DB.Where("user_id = ?", userID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		record = model.ModerationUserRecord{
			UserID:              userID,
			MaxViolationCount:   1,
			LastViolationAt:     now,
			UsernameSnapshot:    username,
			DisplayNameSnapshot: displayName,
			EmailSnapshot:       email,
			CreatedAt:           now,
			UpdatedAt:           now,
		}
		_ = model.DB.Create(&record).Error
		return
	}
	if err != nil {
		return
	}
	updates := map[string]any{
		"max_violation_count": record.MaxViolationCount + 1,
		"last_violation_at":   now,
		"archived_at":         0,
		"updated_at":          now,
	}
	if username != "" {
		updates["username_snapshot"] = username
	}
	if displayName != "" {
		updates["display_name_snapshot"] = displayName
	}
	if email != "" {
		updates["email_snapshot"] = email
	}
	_ = model.DB.Model(&record).Updates(updates).Error
}

func snapshotModerationUser(userID int) (username, displayName, email string) {
	var user model.User
	if err := model.DB.Select("username", "display_name", "email").First(&user, userID).Error; err != nil {
		return "", "", ""
	}
	return user.Username, user.DisplayName, user.Email
}

func maybeAutoDisableUser(userID int, now int64) {
	threshold := setting.GetContentModerationSetting().AutoDisableViolations
	if threshold <= 0 || userID <= 0 {
		return
	}
	cutoff := now - int64(setting.GetContentModerationSetting().GetViolationRetentionDuration().Seconds())
	count, err := model.CountRecentUserModerationEvents(userID, cutoff)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to count moderation events for auto-disable: %v", err))
		return
	}
	record, err := model.GetModerationUserRecord(userID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		common.SysError(fmt.Sprintf("failed to get user record for auto-disable: %v", err))
		return
	}
	recVal := model.ModerationUserRecord{}
	if record != nil {
		recVal = *record
	}
	effectiveCount := effectiveViolationCount(userID, int(count), recVal, cutoff, now)
	if effectiveCount < threshold {
		return
	}
	if _, err := model.DisableUserAndTokensForModeration(userID, now); err != nil {
		common.SysError(fmt.Sprintf("failed to auto-disable user %d after content moderation: %v", userID, err))
	}
}

func effectiveViolationCount(userID int, actualCount int, record model.ModerationUserRecord, cutoff, now int64) int {
	if !record.OverrideActive {
		return actualCount
	}
	newCount, err := model.CountUserModerationEventsAfter(userID, record.OverrideAt, cutoff, now)
	if err != nil {
		return record.ViolationCountOverride
	}
	return record.ViolationCountOverride + int(newCount)
}

func ListModerationUsers(status string, userID, limit, offset int) ([]ModerationUserListItem, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	now := common.GetTimestamp()
	cutoff := now - int64(setting.GetContentModerationSetting().GetViolationRetentionDuration().Seconds())

	type eventAgg struct {
		UserID int
		Count  int
		LastAt int64
	}
	var aggs []eventAgg
	vQuery := model.DB.Model(&model.ModerationEvent{}).
		Select("user_id, count(*) as count, max(created_at) as last_at").
		Where("actor = ? AND status = ? AND created_at >= ? AND expires_at > ?", model.ModerationEventActorUser, model.ModerationEventActive, cutoff, now).
		Group("user_id")
	if userID > 0 {
		vQuery = vQuery.Where("user_id = ?", userID)
	}
	if err := vQuery.Scan(&aggs).Error; err != nil {
		return nil, 0, err
	}
	vMap := make(map[int]eventAgg, len(aggs))
	for _, a := range aggs {
		vMap[a.UserID] = a
	}

	var records []model.ModerationUserRecord
	rQuery := model.DB.Model(&model.ModerationUserRecord{})
	if userID > 0 {
		rQuery = rQuery.Where("user_id = ?", userID)
	}
	_ = rQuery.Find(&records).Error
	rMap := make(map[int]model.ModerationUserRecord, len(records))
	for _, r := range records {
		rMap[r.UserID] = r
	}

	allUserIDsMap := make(map[int]struct{})
	for uid := range vMap {
		allUserIDsMap[uid] = struct{}{}
	}
	for uid := range rMap {
		allUserIDsMap[uid] = struct{}{}
	}
	if userID > 0 {
		allUserIDsMap[userID] = struct{}{}
	}
	var allUserIDs []int
	for uid := range allUserIDsMap {
		allUserIDs = append(allUserIDs, uid)
	}
	slices.Sort(allUserIDs)

	var filteredUserIDs []int
	for _, uid := range allUserIDs {
		agg := vMap[uid]
		rec, hasRec := rMap[uid]
		effectiveCount := effectiveViolationCount(uid, agg.Count, rec, cutoff, now)
		switch status {
		case "active":
			if effectiveCount > 0 {
				filteredUserIDs = append(filteredUserIDs, uid)
			}
		case "history":
			if hasRec && (rec.ArchivedAt > 0 || (rec.OverrideActive && effectiveCount == 0)) {
				filteredUserIDs = append(filteredUserIDs, uid)
			}
		default:
			filteredUserIDs = append(filteredUserIDs, uid)
		}
	}

	total := int64(len(filteredUserIDs))
	if offset >= len(filteredUserIDs) {
		return []ModerationUserListItem{}, total, nil
	}
	end := offset + limit
	if end > len(filteredUserIDs) {
		end = len(filteredUserIDs)
	}
	pageIDs := filteredUserIDs[offset:end]

	var users []model.User
	if len(pageIDs) > 0 {
		_ = model.DB.Where("id IN ?", pageIDs).Find(&users).Error
	}
	userMap := make(map[int]model.User, len(users))
	for _, u := range users {
		userMap[u.Id] = u
	}

	items := make([]ModerationUserListItem, 0, len(pageIDs))
	for _, uid := range pageIDs {
		u := userMap[uid]
		agg := vMap[uid]
		rec := rMap[uid]
		effectiveCount := effectiveViolationCount(uid, agg.Count, rec, cutoff, now)
		lastAt := agg.LastAt
		if rec.LastViolationAt > lastAt {
			lastAt = rec.LastViolationAt
		}
		recStatus := "active"
		archivedAt := rec.ArchivedAt
		if effectiveCount == 0 && (rec.ArchivedAt > 0 || rec.OverrideActive) {
			recStatus = "history"
			if archivedAt == 0 {
				archivedAt = rec.UpdatedAt
			}
		}
		username := u.Username
		displayName := u.DisplayName
		email := u.Email
		if username == "" {
			username = rec.UsernameSnapshot
		}
		if displayName == "" {
			displayName = rec.DisplayNameSnapshot
		}
		if email == "" {
			email = rec.EmailSnapshot
		}
		items = append(items, ModerationUserListItem{
			RecordID:             rec.ID,
			UserID:               uid,
			Username:             username,
			DisplayName:          displayName,
			Email:                email,
			AccountStatus:        u.Status,
			RecordStatus:         recStatus,
			ViolationCount:       effectiveCount,
			ActualViolationCount: agg.Count,
			MaxViolationCount:    rec.MaxViolationCount,
			LastViolationAt:      lastAt,
			Note:                 rec.Note,
			ArchivedAt:           archivedAt,
			CreatedAt:            rec.CreatedAt,
			UpdatedAt:            rec.UpdatedAt,
		})
	}
	return items, total, nil
}

func GetModerationUserDetail(userID int) (*ModerationUserDetail, error) {
	if userID <= 0 {
		return nil, errors.New("invalid moderation user")
	}
	var user model.User
	userErr := model.DB.First(&user, userID).Error
	if userErr != nil && !errors.Is(userErr, gorm.ErrRecordNotFound) {
		return nil, userErr
	}
	var record model.ModerationUserRecord
	recordErr := model.DB.Where("user_id = ?", userID).First(&record).Error
	if recordErr != nil && !errors.Is(recordErr, gorm.ErrRecordNotFound) {
		return nil, recordErr
	}
	if errors.Is(userErr, gorm.ErrRecordNotFound) && errors.Is(recordErr, gorm.ErrRecordNotFound) {
		return nil, gorm.ErrRecordNotFound
	}
	now := common.GetTimestamp()
	cutoff := now - int64(setting.GetContentModerationSetting().GetViolationRetentionDuration().Seconds())

	actualCount, _ := model.CountRecentUserModerationEvents(userID, cutoff)
	effectiveCount := effectiveViolationCount(userID, int(actualCount), record, cutoff, now)

	var events []model.ModerationEvent
	_ = model.DB.Where("user_id = ? AND created_at >= ? AND expires_at > ?", userID, cutoff, now).
		Order("id desc").Limit(50).Find(&events).Error

	recStatus := "active"
	if record.ArchivedAt > 0 || (effectiveCount == 0 && record.OverrideActive) {
		recStatus = "history"
	}
	username := user.Username
	if username == "" {
		username = record.UsernameSnapshot
	}
	displayName := user.DisplayName
	if displayName == "" {
		displayName = record.DisplayNameSnapshot
	}
	email := user.Email
	if email == "" {
		email = record.EmailSnapshot
	}
	accountStatus := user.Status
	resolvedUserID := user.Id
	if resolvedUserID <= 0 {
		resolvedUserID = userID
	}
	return &ModerationUserDetail{
		User: ModerationUserListItem{
			RecordID:             record.ID,
			UserID:               resolvedUserID,
			Username:             username,
			DisplayName:          displayName,
			Email:                email,
			AccountStatus:        accountStatus,
			RecordStatus:         recStatus,
			ViolationCount:       effectiveCount,
			ActualViolationCount: int(actualCount),
			MaxViolationCount:    record.MaxViolationCount,
			LastViolationAt:      record.LastViolationAt,
			Note:                 record.Note,
			ArchivedAt:           record.ArchivedAt,
			CreatedAt:            record.CreatedAt,
			UpdatedAt:            record.UpdatedAt,
		},
		Events: events,
	}, nil
}

func UpdateModerationUserRecord(userID, adminID, adminRole, violationCount int, note string) error {
	if userID <= 0 {
		return errors.New("invalid user id")
	}
	var user model.User
	if err := model.DB.First(&user, userID).Error; err != nil {
		return err
	}
	if err := validateModerationUserMutation(&user, adminID, adminRole); err != nil {
		return err
	}
	if violationCount < 0 {
		return errors.New("violation count must not be negative")
	}
	now := common.GetTimestamp()
	var archivedAt int64
	if violationCount == 0 {
		archivedAt = now
	}
	var record model.ModerationUserRecord
	err := model.DB.Where("user_id = ?", userID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		record = model.ModerationUserRecord{
			UserID:                 userID,
			ViolationCountOverride: violationCount,
			OverrideActive:         true,
			OverrideAt:             now,
			ArchivedAt:             archivedAt,
			Note:                   note,
			UsernameSnapshot:       user.Username,
			DisplayNameSnapshot:    user.DisplayName,
			EmailSnapshot:          user.Email,
			CreatedAt:              now,
			UpdatedAt:              now,
		}
		return model.DB.Create(&record).Error
	}
	if err != nil {
		return err
	}
	return model.DB.Model(&record).Updates(map[string]any{
		"violation_count_override": violationCount,
		"override_active":          true,
		"override_at":              now,
		"archived_at":              archivedAt,
		"note":                     note,
		"updated_at":               now,
	}).Error
}

func validateModerationUserMutation(user *model.User, adminID, adminRole int) error {
	if user == nil {
		return errors.New("user not found")
	}
	if adminRole <= common.RoleAdminUser && user.Role >= common.RoleAdminUser {
		return ErrModerationUserPermissionDenied
	}
	if user.Role >= common.RoleRootUser {
		return ErrModerationUserPermissionDenied
	}
	return nil
}

func SetModerationUserAccountStatus(userID, adminID, adminRole int, enabled bool, reason string) error {
	if userID <= 0 {
		return errors.New("invalid user id")
	}
	var user model.User
	if err := model.DB.First(&user, userID).Error; err != nil {
		return err
	}
	if err := validateModerationUserMutation(&user, adminID, adminRole); err != nil {
		return err
	}
	status := common.UserStatusEnabled
	if !enabled {
		status = common.UserStatusDisabled
	}
	now := common.GetTimestamp()
	if err := model.SetUserAccountStatusForModeration(userID, status, now); err != nil {
		return err
	}
	action := model.ModerationAction{
		AdminID: adminID,
		UserID:  userID,
		Action:  "account_status_change",
		Reason:  reason,
	}
	_ = model.DB.Create(&action).Error
	return nil
}

func DeleteModerationUserHistory(userID, adminID, adminRole int) error {
	if userID <= 0 {
		return errors.New("invalid user id")
	}
	var user model.User
	err := model.DB.First(&user, userID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil {
		if err := validateModerationUserMutation(&user, adminID, adminRole); err != nil {
			return err
		}
	}
	now := common.GetTimestamp()
	cutoff := now - int64(setting.GetContentModerationSetting().GetViolationRetentionDuration().Seconds())
	return model.DeleteModerationUserHistoryIfArchived(userID, cutoff, now)
}

func RestoreUserAfterModeration(userID, adminID int, reason string) error {
	now := common.GetTimestamp()
	_, err := model.RestoreUserAndTokensAfterModeration(userID, now)
	if err != nil {
		return err
	}
	_ = model.DB.Model(&model.ModerationUserRecord{}).
		Where("user_id = ?", userID).
		Updates(map[string]any{
			"violation_count_override": 0,
			"override_active":          true,
			"override_at":              now,
			"archived_at":              now,
			"updated_at":               now,
		}).Error
	action := model.ModerationAction{
		AdminID: adminID,
		UserID:  userID,
		Action:  "restore_user",
		Reason:  reason,
	}
	_ = model.DB.Create(&action).Error
	return nil
}

func ResolveModerationEvent(eventID, adminID int64, status, reason string) error {
	if eventID <= 0 {
		return errors.New("invalid event id")
	}
	if status != model.ModerationEventFalsePositive && status != model.ModerationEventReversed {
		return errors.New("invalid event resolution status")
	}
	var event model.ModerationEvent
	if err := model.DB.First(&event, eventID).Error; err != nil {
		return err
	}
	now := common.GetTimestamp()
	if err := model.DB.Model(&event).Updates(map[string]any{
		"status":          status,
		"resolved_at":     now,
		"resolved_by":     int(adminID),
		"resolution_note": reason,
	}).Error; err != nil {
		return err
	}
	if event.ContentFingerprint != "" {
		storeModerationFingerprint(event.ContentFingerprint, "allow", moderationAllowCacheTTL)
	}
	action := model.ModerationAction{
		AdminID: int(adminID),
		UserID:  event.UserID,
		EventID: event.ID,
		Action:  "resolve_event",
		Reason:  reason,
	}
	_ = model.DB.Create(&action).Error
	return nil
}

func CleanupContentModerationData() error {
	config := setting.GetContentModerationSetting()
	now := common.GetTimestamp()
	if err := model.DropLegacyModerationTables(model.DB); err != nil {
		return err
	}
	return model.DeleteExpiredModerationData(now, int64(config.GetViolationRetentionDuration().Seconds()))
}

func StartContentModerationCleanup() {
	if !common.IsMasterNode {
		return
	}
	go func() {
		if err := CleanupContentModerationData(); err != nil {
			common.SysError("failed to clean up content moderation data: " + err.Error())
		}
		ticker := time.NewTicker(moderationCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			if err := CleanupContentModerationData(); err != nil {
				common.SysError("failed to clean up content moderation data: " + err.Error())
			}
		}
	}()
}
