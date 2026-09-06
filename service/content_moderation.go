package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	moderationMaxRequestBytes = 128 * 1024
	moderationHighConfidence  = 0.9
)

var (
	ErrModerationUserPermissionDenied = errors.New("cannot manage moderation records for users with equal or higher role")
	ErrInvalidModerationUserRequest   = errors.New("invalid moderation user request")
	moderationResponseWriterKey       = constant.ContextKeyModerationCapture
)

type ModerationRequestContent struct {
	SystemPrompt string   `json:"system_prompt"`
	UserPrompt   string   `json:"user_prompt"`
	ImageURLs    []string `json:"image_urls,omitempty"`
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
	User          ModerationUserListItem         `json:"user"`
	Conversations []model.ModerationConversation `json:"conversations"`
	Violations    []model.ModerationViolation    `json:"violations"`
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

// ModerationCapture buffers response output so post-flight moderation and audit logging can inspect it.
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

func BeginModerationCapture(c *gin.Context, request dto.Request) (string, bool) {
	if c == nil {
		return "", false
	}
	capture := NewModerationCapture(c.Writer)
	c.Writer = capture
	common.SetContextKey(c, moderationResponseWriterKey, capture)
	return "", true
}

func moderationConversationIDFromSeed(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return ""
	}
	originalLength := len(seed)
	if len(seed) > 2048 {
		prefixLength := 1024
		suffixLength := 2048 - prefixLength - 1
		seed = seed[:prefixLength] + "\x00" + seed[len(seed)-suffixLength:]
	}
	digest := common.GenerateHMAC(seed + "\x00" + strconv.Itoa(originalLength))
	return "conv_seed_" + digest
}

func moderationRequestConversationFallback(c *gin.Context) string {
	reqID := c.GetString(common.RequestIdKey)
	if reqID == "" {
		reqID = common.GetUUID()
	}
	return "conv_" + reqID
}

type moderationConversationEvidence struct {
	UserTexts      []string
	AssistantTexts []string
}

func extractModerationConversationEvidence(data []byte) moderationConversationEvidence {
	var evidence moderationConversationEvidence
	if len(data) == 0 {
		return evidence
	}
	var payload map[string]any
	if err := common.Unmarshal(data, &payload); err != nil {
		return evidence
	}
	if req, ok := payload["generateContentRequest"].(map[string]any); ok {
		payload = req
	} else if req, ok := payload["generate_content_request"].(map[string]any); ok {
		payload = req
	}
	if contents, ok := payload["contents"].([]any); ok {
		for _, c := range contents {
			if cmap, ok := c.(map[string]any); ok {
				role, _ := cmap["role"].(string)
				if parts, ok := cmap["parts"].([]any); ok {
					for _, p := range parts {
						if pmap, ok := p.(map[string]any); ok {
							if txt, ok := pmap["text"].(string); ok && txt != "" {
								if role == "model" || role == "assistant" {
									evidence.AssistantTexts = append(evidence.AssistantTexts, txt)
								} else {
									evidence.UserTexts = append(evidence.UserTexts, txt)
								}
							}
						}
					}
				}
			}
		}
	}
	if msgs, ok := payload["messages"].([]any); ok {
		for _, m := range msgs {
			if mmap, ok := m.(map[string]any); ok {
				role, _ := mmap["role"].(string)
				if content, ok := mmap["content"].(string); ok && content != "" {
					if role == "assistant" {
						evidence.AssistantTexts = append(evidence.AssistantTexts, content)
					} else if role == "user" {
						evidence.UserTexts = append(evidence.UserTexts, content)
					}
				}
			}
		}
	}
	if inputArr, ok := payload["input"].([]any); ok {
		for _, item := range inputArr {
			if imap, ok := item.(map[string]any); ok {
				role, _ := imap["role"].(string)
				content, _ := imap["content"].(string)
				if role == "assistant" {
					evidence.AssistantTexts = append(evidence.AssistantTexts, content)
				} else if role == "user" {
					evidence.UserTexts = append(evidence.UserTexts, content)
				}
			}
		}
	}
	return evidence
}

func resolveModerationConversationIDFromJSON(data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	var payload map[string]any
	if err := common.Unmarshal(data, &payload); err != nil {
		return "", false
	}
	for _, key := range []string{"conversation_id", "conversationId", "session_id", "sessionId", "thread_id"} {
		if val, ok := payload[key].(string); ok && strings.TrimSpace(val) != "" {
			return strings.TrimSpace(val), true
		}
	}
	if msgs, ok := payload["messages"].([]any); ok {
		for _, m := range msgs {
			if mmap, ok := m.(map[string]any); ok {
				if role, _ := mmap["role"].(string); role == "user" {
					if content, ok := mmap["content"].(string); ok && strings.TrimSpace(content) != "" {
						return moderationConversationIDFromSeed(strings.TrimSpace(content)), false
					}
				}
			}
		}
	}
	return "", false
}

func ResolveModerationConversationIDForUser(c *gin.Context, userID int) string {
	conversationID := ResolveModerationConversationID(c)
	if c == nil || userID <= 0 || common.GetContextKeyBool(c, constant.ContextKeyModerationConversationIDExplicit) {
		return conversationID
	}

	var evidence moderationConversationEvidence
	if ev, ok := common.GetContextKeyType[moderationConversationEvidence](c, constant.ContextKeyModerationConversationEvidence); ok {
		evidence = ev
	} else if bs, err := common.GetBodyStorage(c); err == nil && bs != nil {
		if data, err := bs.Bytes(); err == nil {
			evidence = extractModerationConversationEvidence(data)
			common.SetContextKey(c, constant.ContextKeyModerationConversationEvidence, evidence)
		}
	}

	if len(evidence.UserTexts) == 0 && len(evidence.AssistantTexts) == 0 {
		return conversationID
	}

	isContentSeed := strings.HasPrefix(conversationID, "conv_seed_")
	if isContentSeed && len(evidence.AssistantTexts) == 0 && len(evidence.UserTexts) <= 1 {
		return cacheModerationConversationID(c, moderationRequestConversationFallback(c))
	}

	now := common.GetTimestamp()
	var turns []model.ModerationTurn
	_ = model.DB.Where("user_id = ? AND expires_at > ?", userID, now).
		Order("created_at desc, id desc").Limit(50).Find(&turns).Error

	matchedConvKeys := make(map[string]bool)
	for _, turn := range turns {
		userPromptText := string(turn.UserPrompt)
		if dec, err := DecryptModerationStoredText(userPromptText); err == nil {
			userPromptText = dec
		}
		assistantReplyText := string(turn.AssistantReply)
		if dec, err := DecryptModerationStoredText(assistantReplyText); err == nil {
			assistantReplyText = dec
		}
		for _, u := range evidence.UserTexts {
			if userPromptText == u {
				matchedConvKeys[turn.ConversationKey] = true
			}
		}
		for _, a := range evidence.AssistantTexts {
			if assistantReplyText == a {
				matchedConvKeys[turn.ConversationKey] = true
			}
		}
	}
	if len(matchedConvKeys) == 1 {
		for k := range matchedConvKeys {
			return cacheModerationConversationID(c, k)
		}
	}
	if len(matchedConvKeys) > 1 {
		return cacheModerationConversationID(c, moderationRequestConversationFallback(c))
	}

	return conversationID
}

func ResolveModerationConversationID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if existing := common.GetContextKeyString(c, constant.ContextKeyModerationConversationID); existing != "" {
		return existing
	}
	if headerID := strings.TrimSpace(c.GetHeader("X-Conversation-ID")); headerID != "" {
		common.SetContextKey(c, constant.ContextKeyModerationConversationIDExplicit, true)
		return cacheModerationConversationID(c, headerID)
	}
	if headerID := strings.TrimSpace(c.GetHeader("Conversation-ID")); headerID != "" {
		common.SetContextKey(c, constant.ContextKeyModerationConversationIDExplicit, true)
		return cacheModerationConversationID(c, headerID)
	}
	if queryID := strings.TrimSpace(c.Query("conversation_id")); queryID != "" {
		common.SetContextKey(c, constant.ContextKeyModerationConversationIDExplicit, true)
		return cacheModerationConversationID(c, queryID)
	}
	if storage, ok := c.Get(common.KeyBodyStorage); ok {
		if bs, ok := storage.(common.BodyStorage); ok {
			if data, err := bs.Bytes(); err == nil && len(data) > 0 {
				if id, explicit := resolveModerationConversationIDFromJSON(data); id != "" {
					if explicit {
						common.SetContextKey(c, constant.ContextKeyModerationConversationIDExplicit, true)
					}
					return cacheModerationConversationID(c, id)
				}
			}
		}
	}
	if bs, err := common.GetBodyStorage(c); err == nil && bs != nil {
		if data, err := bs.Bytes(); err == nil && len(data) > 0 {
			if id, explicit := resolveModerationConversationIDFromJSON(data); id != "" {
				if explicit {
					common.SetContextKey(c, constant.ContextKeyModerationConversationIDExplicit, true)
				}
				return cacheModerationConversationID(c, id)
			}
		}
	}
	userID := common.GetContextKeyInt(c, constant.ContextKeyUserId)
	reqID := c.GetString(common.RequestIdKey)
	if reqID == "" {
		reqID = common.GetUUID()
	}
	convID := fmt.Sprintf("u%d-%s", userID, reqID)
	return cacheModerationConversationID(c, convID)
}

func cacheModerationConversationID(c *gin.Context, conversationID string) string {
	if c != nil && conversationID != "" {
		common.SetContextKey(c, constant.ContextKeyModerationConversationID, conversationID)
	}
	return conversationID
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
	content := extractModerationRequestContent(request)
	if existing, ok := GetModerationRequestContent(c); ok {
		if content.SystemPrompt == "" {
			content.SystemPrompt = existing.SystemPrompt
		}
		if content.UserPrompt == "" {
			content.UserPrompt = existing.UserPrompt
		}
		if len(content.ImageURLs) == 0 {
			content.ImageURLs = existing.ImageURLs
		}
	}
	common.SetContextKey(c, constant.ContextKeyModerationRequestContent, content)
}

func SetModerationRequestContentFromJSON(c *gin.Context, data []byte, request dto.Request) {
	if c == nil {
		return
	}
	var content ModerationRequestContent
	if len(data) > 0 {
		content = extractModerationRequestContentFromJSON(data)
	}
	if request != nil {
		reqContent := extractModerationRequestContent(request)
		if reqContent.SystemPrompt != "" {
			content.SystemPrompt = reqContent.SystemPrompt
		}
		if reqContent.UserPrompt != "" {
			content.UserPrompt = reqContent.UserPrompt
		}
		if len(reqContent.ImageURLs) > 0 {
			content.ImageURLs = append(content.ImageURLs, reqContent.ImageURLs...)
		}
	}
	if existing, ok := GetModerationRequestContent(c); ok {
		if content.SystemPrompt == "" {
			content.SystemPrompt = existing.SystemPrompt
		}
		if content.UserPrompt == "" {
			content.UserPrompt = existing.UserPrompt
		}
		if len(content.ImageURLs) == 0 {
			content.ImageURLs = existing.ImageURLs
		}
	}
	common.SetContextKey(c, constant.ContextKeyModerationRequestContent, content)
}

func extractModerationRequestContentFromJSON(data []byte) ModerationRequestContent {
	var payload map[string]any
	if err := common.Unmarshal(data, &payload); err != nil {
		return ModerationRequestContent{}
	}
	if req, ok := payload["generateContentRequest"].(map[string]any); ok {
		payload = req
	} else if req, ok := payload["generate_content_request"].(map[string]any); ok {
		payload = req
	}
	var systemPrompt, userPrompt strings.Builder

	if sysInst, ok := payload["systemInstruction"].(map[string]any); ok {
		if parts, ok := sysInst["parts"].([]any); ok {
			for _, p := range parts {
				if pmap, ok := p.(map[string]any); ok {
					if txt, ok := pmap["text"].(string); ok && txt != "" {
						if systemPrompt.Len() > 0 {
							systemPrompt.WriteString("\n")
						}
						systemPrompt.WriteString(txt)
					}
				}
			}
		}
	}

	if contents, ok := payload["contents"].([]any); ok {
		for _, c := range contents {
			if cmap, ok := c.(map[string]any); ok {
				role, _ := cmap["role"].(string)
				if role == "user" {
					if parts, ok := cmap["parts"].([]any); ok {
						for _, p := range parts {
							if pmap, ok := p.(map[string]any); ok {
								if txt, ok := pmap["text"].(string); ok && txt != "" {
									if userPrompt.Len() > 0 {
										userPrompt.WriteString("\n")
									}
									userPrompt.WriteString(txt)
								}
							}
						}
					}
				}
			}
		}
	}

	if inputArr, ok := payload["input"].([]any); ok {
		for _, item := range inputArr {
			if imap, ok := item.(map[string]any); ok {
				role, _ := imap["role"].(string)
				content, _ := imap["content"].(string)
				if role == "system" {
					if systemPrompt.Len() > 0 {
						systemPrompt.WriteString("\n")
					}
					systemPrompt.WriteString(content)
				} else if role == "user" {
					if userPrompt.Len() > 0 {
						userPrompt.WriteString("\n")
					}
					userPrompt.WriteString(content)
				}
			}
		}
	}

	if msgs, ok := payload["messages"].([]any); ok {
		for _, m := range msgs {
			if mmap, ok := m.(map[string]any); ok {
				role, _ := mmap["role"].(string)
				if content, ok := mmap["content"].(string); ok && content != "" {
					if role == "system" {
						if systemPrompt.Len() > 0 {
							systemPrompt.WriteString("\n")
						}
						systemPrompt.WriteString(content)
					} else if role == "user" {
						if userPrompt.Len() > 0 {
							userPrompt.WriteString("\n")
						}
						userPrompt.WriteString(content)
					}
				}
			}
		}
	}

	return ModerationRequestContent{
		SystemPrompt: systemPrompt.String(),
		UserPrompt:   userPrompt.String(),
	}
}

func mediaPlaceholder(mediaType string) string {
	switch mediaType {
	case dto.ContentTypeImageURL, "input_image":
		return "[image content not saved]"
	case dto.ContentTypeInputAudio:
		return "[audio content not saved]"
	case dto.ContentTypeVideoUrl, "input_video":
		return "[video content not saved]"
	case dto.ContentTypeFile, "input_file":
		return "[file content not saved]"
	default:
		return "[media content not saved]"
	}
}

func openAIMessageText(message dto.Message) string {
	if message.IsStringContent() {
		return message.StringContent()
	}
	var builder strings.Builder
	for _, item := range message.ParseContent() {
		if item.Type == dto.ContentTypeText || item.Type == "" {
			builder.WriteString(item.Text)
		} else {
			builder.WriteString(mediaPlaceholder(item.Type))
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
		} else {
			builder.WriteString(mediaPlaceholder(item.Type))
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
				imageURLs = append(imageURLs, fmt.Sprintf("data:%s;base64,%s", item.Source.MediaType, strData))
			}
		}
	}
	return imageURLs
}

func extractModerationRequestContent(request dto.Request) ModerationRequestContent {
	var systemPrompt, userPrompt strings.Builder
	var imageURLs []string
	switch req := request.(type) {
	case *dto.GeneralOpenAIRequest:
		for _, msg := range req.Messages {
			content := openAIMessageText(msg)
			if imgs := extractOpenAIImages(msg); len(imgs) > 0 {
				imageURLs = append(imageURLs, imgs...)
			}
			if msg.Role == "system" {
				if systemPrompt.Len() > 0 {
					systemPrompt.WriteString("\n")
				}
				systemPrompt.WriteString(content)
			} else {
				if userPrompt.Len() > 0 {
					userPrompt.WriteString("\n")
				}
				userPrompt.WriteString(content)
			}
		}
		if req.Prompt != nil {
			if str, ok := req.Prompt.(string); ok && str != "" {
				if userPrompt.Len() > 0 {
					userPrompt.WriteString("\n")
				}
				userPrompt.WriteString(str)
			}
		}
	case *dto.ClaudeRequest:
		if req.System != nil {
			if str, ok := req.System.(string); ok {
				systemPrompt.WriteString(str)
			}
		}
		for _, msg := range req.Messages {
			content := claudeMessageText(msg)
			if imgs := extractClaudeImages(msg); len(imgs) > 0 {
				imageURLs = append(imageURLs, imgs...)
			}
			if userPrompt.Len() > 0 {
				userPrompt.WriteString("\n")
			}
			userPrompt.WriteString(content)
		}
	case *dto.GeminiChatRequest:
		if req.SystemInstructions != nil {
			for _, part := range req.SystemInstructions.Parts {
				if part.Text != "" {
					if systemPrompt.Len() > 0 {
						systemPrompt.WriteString("\n")
					}
					systemPrompt.WriteString(part.Text)
				} else {
					if systemPrompt.Len() > 0 {
						systemPrompt.WriteString("\n")
					}
					systemPrompt.WriteString(mediaPlaceholder("file"))
				}
			}
		}
		for _, c := range req.Contents {
			for _, part := range c.Parts {
				if part.Text != "" {
					if userPrompt.Len() > 0 {
						userPrompt.WriteString("\n")
					}
					userPrompt.WriteString(part.Text)
				} else {
					if part.InlineData != nil && part.InlineData.Data != "" {
						imageURLs = append(imageURLs, fmt.Sprintf("data:%s;base64,%s", part.InlineData.MimeType, part.InlineData.Data))
					}
					if userPrompt.Len() > 0 {
						userPrompt.WriteString("\n")
					}
					userPrompt.WriteString(mediaPlaceholder("file"))
				}
			}
		}
	case *dto.OpenAIResponsesRequest:
		if len(req.Instructions) > 0 {
			var parsed []map[string]any
			if err := common.Unmarshal(req.Instructions, &parsed); err == nil {
				for _, item := range parsed {
					t, _ := item["type"].(string)
					if t == "input_text" || t == "text" {
						txt, _ := item["text"].(string)
						if systemPrompt.Len() > 0 {
							systemPrompt.WriteString("\n")
						}
						systemPrompt.WriteString(txt)
					} else {
						if systemPrompt.Len() > 0 {
							systemPrompt.WriteString("\n")
						}
						systemPrompt.WriteString(mediaPlaceholder(t))
					}
				}
			} else {
				systemPrompt.Write(req.Instructions)
			}
		}
		if len(req.Input) > 0 {
			var parsed []map[string]any
			if err := common.Unmarshal(req.Input, &parsed); err == nil {
				for _, item := range parsed {
					t, _ := item["type"].(string)
					if t == "input_text" || t == "text" {
						txt, _ := item["text"].(string)
						if userPrompt.Len() > 0 {
							userPrompt.WriteString("\n")
						}
						userPrompt.WriteString(txt)
					} else {
						if t == "input_image" {
							if imgURL, ok := item["image_url"].(string); ok && imgURL != "" {
								imageURLs = append(imageURLs, imgURL)
							}
						}
						if userPrompt.Len() > 0 {
							userPrompt.WriteString("\n")
						}
						userPrompt.WriteString(mediaPlaceholder(t))
					}
				}
			} else {
				userPrompt.Write(req.Input)
			}
		}
	}
	return ModerationRequestContent{
		SystemPrompt: systemPrompt.String(),
		UserPrompt:   userPrompt.String(),
		ImageURLs:    imageURLs,
	}
}

func ExtractModerationAssistantText(data []byte, contentType string, relayFormat types.RelayFormat) string {
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
			if err := common.Unmarshal(payload, &item); err != nil {
				continue
			}
			if choices, ok := item["choices"].([]any); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]any); ok {
					if delta, ok := choice["delta"].(map[string]any); ok {
						if content, ok := delta["content"].(string); ok {
							builder.WriteString(content)
						}
					}
				}
			}
			if delta, ok := item["delta"].(string); ok {
				builder.WriteString(delta)
			}
			if candidates, ok := item["candidates"].([]any); ok && len(candidates) > 0 {
				if cand, ok := candidates[0].(map[string]any); ok {
					if content, ok := cand["content"].(map[string]any); ok {
						if parts, ok := content["parts"].([]any); ok {
							for _, p := range parts {
								if part, ok := p.(map[string]any); ok {
									if text, ok := part["text"].(string); ok {
										builder.WriteString(text)
									}
								}
							}
						}
					}
				}
			}
		}
		return builder.String()
	}

	var item map[string]any
	if err := common.Unmarshal(data, &item); err != nil {
		return ""
	}
	if choices, ok := item["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if msg, ok := choice["message"].(map[string]any); ok {
				if content, ok := msg["content"].(string); ok {
					return content
				}
			}
		}
	}
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
	if contentArr, ok := item["content"].([]any); ok {
		var builder strings.Builder
		for _, c := range contentArr {
			if cmap, ok := c.(map[string]any); ok {
				if text, ok := cmap["text"].(string); ok {
					builder.WriteString(text)
				}
			}
		}
		return builder.String()
	}
	return ""
}

func moderationResponseStatus(info *relaycommon.RelayInfo, relayErr *types.NewAPIError, hasReply bool) string {
	if relayErr == nil {
		if info != nil && info.IsStream && info.StreamStatus != nil && !info.StreamStatus.IsNormalEnd() {
			if info.StreamStatus.EndReason != "" {
				return string(info.StreamStatus.EndReason)
			}
			if hasReply {
				return "partial"
			}
			return "failed"
		}
		return "success"
	}
	if info != nil && info.StreamStatus != nil && !info.StreamStatus.IsNormalEnd() && info.StreamStatus.EndReason != "" {
		return string(info.StreamStatus.EndReason)
	}
	if hasReply {
		return "partial"
	}
	return "failed"
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

// executeOpenAIModerationCall executes the HTTP request to OpenAI Moderations API.
// Reference: https://developers.openai.com/api/reference/resources/moderations/methods/create
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

	client := httpClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	maxRetries := config.MaxRetries
	if maxRetries < 1 {
		maxRetries = 1
	} else if maxRetries > 5 {
		maxRetries = 5
	}

	var respBytes []byte
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		req, reqErr := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
		if reqErr != nil {
			cancel()
			return emptyResult, nil, reqErr
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey := strings.TrimSpace(config.APIKey); apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, doErr := client.Do(req)
		if doErr != nil {
			cancel()
			lastErr = doErr
			continue
		}
		respBytes, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("moderation upstream error (status %d): %s", resp.StatusCode, string(respBytes))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return emptyResult, respBytes, fmt.Errorf("moderation upstream rejected request (status %d): %s", resp.StatusCode, string(respBytes))
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

// callNativeModeration executes the OpenAI Moderations API call with string inputs.
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

// callModerationContent executes the OpenAI Moderations API call with structured request content.
// Supports multi-modal image inputs when omni moderation model is selected.
func callModerationContent(ctx context.Context, config setting.ContentModerationSetting, content ModerationRequestContent) (openAIModerationResult, []byte, error) {
	modelName := strings.TrimSpace(config.Model)
	if modelName == "" {
		modelName = setting.DefaultContentModerationModel
	}

	isOmni := strings.Contains(strings.ToLower(modelName), "omni") || modelName == setting.DefaultContentModerationModel

	if len(content.ImageURLs) > 0 && isOmni {
		var items []openAIModerationInputItem
		if s := sanitizeModerationText(content.SystemPrompt); s != "" {
			items = append(items, openAIModerationInputItem{Type: "text", Text: s})
		}
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

	var inputs []string
	if s := sanitizeModerationText(content.SystemPrompt); s != "" {
		inputs = append(inputs, s)
	}
	if u := sanitizeModerationText(content.UserPrompt); u != "" {
		inputs = append(inputs, u)
	}
	if len(inputs) == 0 {
		return openAIModerationResult{}, nil, errors.New("moderation input is empty")
	}
	return callNativeModeration(ctx, config, inputs...)
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
		Actor:      "user",
		Severity:   severity,
		Categories: categories,
		Confidence: maxScore,
		ReasonCode: reasonCode,
	}
}

func PreflightModerationRequest(ctx context.Context, content ModerationRequestContent, config setting.ContentModerationSetting) error {
	if !config.PreflightEnabled || strings.TrimSpace(config.APIKey) == "" || strings.TrimSpace(config.Model) == "" {
		return nil
	}
	if strings.TrimSpace(content.SystemPrompt) == "" && strings.TrimSpace(content.UserPrompt) == "" && len(content.ImageURLs) == 0 {
		return nil
	}

	result, _, err := callModerationContent(ctx, config, content)
	if err != nil {
		if config.FailureMode == "open" {
			return nil
		}
		return fmt.Errorf("content moderation preflight unavailable: %w", err)
	}

	if result.Flagged {
		if ginCtx, ok := ctx.(*gin.Context); ok {
			userID := common.GetContextKeyInt(ginCtx, constant.ContextKeyUserId)
			conversationID := common.GetContextKeyString(ginCtx, constant.ContextKeyModerationConversationID)
			channelID := common.GetContextKeyInt(ginCtx, constant.ContextKeyChannelId)
			if channelID <= 0 {
				channelID = ginCtx.GetInt("channel_id")
			}
			decision := moderationDecisionFromNativeResult(result)
			recordViolationFromPreflight(ginCtx, userID, conversationID, channelID, content, decision)
		}
		return model.ErrModerationConversationBlocked
	}
	return nil
}

func recordViolationFromPreflight(c *gin.Context, userID int, conversationID string, channelID int, content ModerationRequestContent, decision moderationDecision) {
	if userID <= 0 {
		return
	}
	now := common.GetTimestamp()
	retention := setting.GetContentModerationSetting().GetViolationRetentionDuration()
	expiresAt := now + int64(retention.Seconds())

	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var conv model.ModerationConversation
		err := model.LockModerationConversation(tx, userID, conversationID, &conv)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			conv = model.ModerationConversation{
				UserID:          userID,
				ConversationID:  conversationID,
				Status:          model.ModerationConversationBlocked,
				FirstActivityAt: now,
				LastActivityAt:  now,
				ExpiresAt:       expiresAt,
				BlockedAt:       now,
				BlockedReason:   "preflight content moderation blocked",
			}
			if createErr := tx.Create(&conv).Error; createErr != nil {
				return createErr
			}
		} else if err != nil {
			return err
		} else {
			if updateErr := tx.Model(&conv).Updates(map[string]any{
				"status":           model.ModerationConversationBlocked,
				"last_activity_at": now,
				"blocked_at":       now,
				"blocked_reason":   "preflight content moderation blocked",
			}).Error; updateErr != nil {
				return updateErr
			}
		}

		var maxRound int
		row := tx.Model(&model.ModerationTurn{}).
			Where("user_id = ? AND conversation_key = ?", userID, conversationID).
			Select("COALESCE(MAX(round_number), 0)").Row()
		if rowErr := row.Scan(&maxRound); rowErr != nil {
			return rowErr
		}

		turn := model.ModerationTurn{
			ConversationID:  conv.ID,
			UserID:          userID,
			ConversationKey: conversationID,
			RoundNumber:     maxRound + 1,
			ChannelID:       channelID,
			RequestID:       c.GetString(common.RequestIdKey),
			SystemPrompt:    model.ModerationText(content.SystemPrompt),
			UserPrompt:      model.ModerationText(content.UserPrompt),
			ResponseStatus:  "blocked",
			RelayFormat:     c.GetString("relay_format"),
			Model:           setting.GetContentModerationSetting().Model,
			ReviewRequired:  true,
			ExpiresAt:       expiresAt,
		}
		if createTurnErr := tx.Create(&turn).Error; createTurnErr != nil {
			return createTurnErr
		}

		categoriesJSON, _ := common.Marshal(decision.Categories)
		return recordModerationViolationWithTx(tx, &turn, decision, true, string(categoriesJSON), now, expiresAt)
	})
	if err != nil {
		common.SysError(fmt.Sprintf("failed to record preflight violation: %v", err))
	}
}

func recordModerationViolation(turn *model.ModerationTurn, decision moderationDecision, userViolation bool, categories string, now, expiresAt int64) error {
	return recordModerationViolationWithTx(model.DB, turn, decision, userViolation, categories, now, expiresAt)
}

func recordModerationViolationWithTx(tx *gorm.DB, turn *model.ModerationTurn, decision moderationDecision, userViolation bool, categories string, now, expiresAt int64) error {
	if tx == nil || turn == nil || turn.UserID <= 0 {
		return errors.New("invalid moderation turn")
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	if expiresAt <= 0 {
		expiresAt = now + int64(setting.GetContentModerationSetting().GetViolationRetentionDuration().Seconds())
	}
	violation := model.ModerationViolation{
		UserID:         turn.UserID,
		ConversationID: turn.ConversationKey,
		TurnID:         turn.ID,
		Actor:          decision.Actor,
		UserViolation:  userViolation,
		Decision:       decision.Decision,
		Severity:       decision.Severity,
		Categories:     categories,
		Confidence:     decision.Confidence,
		ReasonCode:     decision.ReasonCode,
		Status:         model.ModerationViolationActive,
		CreatedAt:      now,
		ExpiresAt:      expiresAt,
	}
	if err := tx.Create(&violation).Error; err != nil {
		return err
	}

	if userViolation {
		updateUserRecordOnViolationWithTx(tx, turn.UserID, now)
	}
	return nil
}

func updateUserRecordOnViolation(userID int, now int64) {
	updateUserRecordOnViolationWithTx(model.DB, userID, now)
}

func updateUserRecordOnViolationWithTx(tx *gorm.DB, userID int, now int64) {
	if tx == nil || userID <= 0 {
		return
	}
	var record model.ModerationUserRecord
	err := tx.Where("user_id = ?", userID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		record = model.ModerationUserRecord{
			UserID:            userID,
			MaxViolationCount: 1,
			LastViolationAt:   now,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		_ = tx.Create(&record).Error
		return
	}
	if err == nil {
		_ = tx.Model(&record).Updates(map[string]any{
			"max_violation_count": record.MaxViolationCount + 1,
			"last_violation_at":   now,
			"archived_at":         0,
			"updated_at":          now,
		}).Error
	}
}

func persistModerationTurn(turn *model.ModerationTurn) error {
	if turn == nil || turn.UserID <= 0 || turn.ConversationKey == "" {
		return errors.New("invalid moderation turn")
	}

	for attempt := 0; attempt < 10; attempt++ {
		err := model.DB.Transaction(func(tx *gorm.DB) error {
			var conv model.ModerationConversation
			err := model.LockModerationConversation(tx, turn.UserID, turn.ConversationKey, &conv)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				now := common.GetTimestamp()
				conv = model.ModerationConversation{
					UserID:          turn.UserID,
					ConversationID:  turn.ConversationKey,
					Status:          model.ModerationConversationActive,
					FirstActivityAt: now,
					LastActivityAt:  now,
					ExpiresAt:       turn.ExpiresAt,
				}
				if createErr := tx.Create(&conv).Error; createErr != nil {
					return createErr
				}
			} else if err != nil {
				return err
			}

			turn.ConversationID = conv.ID

			var maxRound int
			row := tx.Model(&model.ModerationTurn{}).
				Where("user_id = ? AND conversation_key = ?", turn.UserID, turn.ConversationKey).
				Select("COALESCE(MAX(round_number), 0)").Row()
			if rowErr := row.Scan(&maxRound); rowErr != nil {
				return rowErr
			}
			turn.RoundNumber = maxRound + 1

			if err := tx.Create(turn).Error; err != nil {
				return err
			}

			return tx.Model(&conv).Updates(map[string]any{
				"last_activity_at": common.GetTimestamp(),
				"expires_at":       turn.ExpiresAt,
			}).Error
		})

		if err == nil {
			return nil
		}
		if !isRetryableDBErr(err) {
			return err
		}
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}
	return errors.New("failed to persist moderation turn after retries")
}

func isRetryableDBErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "busy") ||
		strings.Contains(msg, "deadlock") ||
		strings.Contains(msg, "lock wait timeout")
}

func FinalizeModeration(c *gin.Context, info *relaycommon.RelayInfo, relayErr *types.NewAPIError) {
	if c == nil || info == nil {
		return
	}
	if relayErr != nil && relayErr.GetErrorCode() == types.ErrorCodeContentModerationBlocked {
		return
	}

	moderationSetting := setting.GetContentModerationSetting()
	if !moderationSetting.Enabled || moderationSetting.IsUserWhitelisted(info.UserId) {
		return
	}

	channelID := 0
	if info.ChannelMeta != nil {
		channelID = info.ChannelMeta.ChannelId
	}
	if channelID <= 0 {
		channelID = info.ChannelId
	}
	if channelID <= 0 {
		channelID = common.GetContextKeyInt(c, constant.ContextKeyChannelId)
	}
	if !moderationSetting.ShouldModerateChannel(channelID) {
		return
	}

	content, _ := common.GetContextKeyType[ModerationRequestContent](c, constant.ContextKeyModerationRequestContent)
	capture, _ := common.GetContextKeyType[*ModerationCapture](c, moderationResponseWriterKey)

	assistantReply := ""
	if capture != nil {
		assistantReply = ExtractModerationAssistantText(capture.Bytes(), c.Writer.Header().Get("Content-Type"), info.RelayFormat)
	}

	responseStatus := moderationResponseStatus(info, relayErr, len(assistantReply) > 0)
	conversationID := common.GetContextKeyString(c, constant.ContextKeyModerationConversationID)
	if conversationID == "" {
		conversationID = ResolveModerationConversationID(c)
	}
	if conversationID == "" {
		return
	}

	now := common.GetTimestamp()
	retention := moderationSetting.GetViolationRetentionDuration()
	expiresAt := now + int64(retention.Seconds())

	turn := model.ModerationTurn{
		UserID:          info.UserId,
		ConversationKey: conversationID,
		ChannelID:       channelID,
		RequestID:       info.RequestId,
		SystemPrompt:    model.ModerationText(content.SystemPrompt),
		UserPrompt:      model.ModerationText(content.UserPrompt),
		AssistantReply:  model.ModerationText(assistantReply),
		ResponseStatus:  responseStatus,
		RelayFormat:     string(info.RelayFormat),
		Model:           info.OriginModelName,
		ExpiresAt:       expiresAt,
	}

	_ = persistModerationTurn(&turn)

	if assistantReply != "" && moderationSetting.APIKey != "" {
		go func(turnCopy model.ModerationTurn, reply string, cfg setting.ContentModerationSetting, uid int, convID string, nowTs, expTs int64) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
			defer cancel()
			result, _, err := callNativeModeration(ctx, cfg, reply)
			if err == nil && result.Flagged {
				decision := moderationDecisionFromNativeResult(result)
				decision.Actor = "assistant"
				catJSON, _ := common.Marshal(decision.Categories)
				_ = recordModerationViolation(&turnCopy, decision, false, string(catJSON), nowTs, expTs)
				_ = model.DB.Model(&model.ModerationConversation{}).
					Where("user_id = ? AND conversation_id = ?", uid, convID).
					Updates(map[string]any{
						"status":         model.ModerationConversationBlocked,
						"blocked_at":     nowTs,
						"blocked_reason": "assistant response flagged by moderation",
					}).Error
			}
		}(turn, assistantReply, moderationSetting, info.UserId, conversationID, now, expiresAt)
	}
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

	type violationAgg struct {
		UserID int
		Count  int
		LastAt int64
	}
	var aggs []violationAgg
	vQuery := model.DB.Model(&model.ModerationViolation{}).
		Select("user_id, count(distinct conversation_id) as count, max(created_at) as last_at").
		Where("user_violation = ? AND status = ? AND created_at >= ? AND expires_at > ?", true, model.ModerationViolationActive, cutoff, now).
		Group("user_id")
	if userID > 0 {
		vQuery = vQuery.Where("user_id = ?", userID)
	}
	if err := vQuery.Scan(&aggs).Error; err != nil {
		return nil, 0, err
	}

	vMap := make(map[int]violationAgg)
	for _, a := range aggs {
		vMap[a.UserID] = a
	}

	var records []model.ModerationUserRecord
	rQuery := model.DB.Model(&model.ModerationUserRecord{})
	if userID > 0 {
		rQuery = rQuery.Where("user_id = ?", userID)
	}
	_ = rQuery.Find(&records).Error
	rMap := make(map[int]model.ModerationUserRecord)
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
		effectiveCount := agg.Count
		if hasRec && rec.OverrideActive {
			var snapshot []string
			_ = common.Unmarshal([]byte(rec.ViolationConversationSnapshot), &snapshot)
			known := make(map[string]bool, len(snapshot))
			for _, k := range snapshot {
				known[k] = true
			}
			var userConvIDs []string
			_ = model.DB.Model(&model.ModerationViolation{}).
				Where("user_id = ? AND user_violation = ? AND status = ? AND created_at >= ? AND expires_at > ?", uid, true, model.ModerationViolationActive, cutoff, now).
				Distinct().Pluck("conversation_id", &userConvIDs).Error
			newCount := 0
			for _, cid := range userConvIDs {
				if !known[cid] {
					newCount++
				}
			}
			effectiveCount = rec.ViolationCountOverride + newCount
		}
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
	userMap := make(map[int]model.User)
	for _, u := range users {
		userMap[u.Id] = u
	}

	items := make([]ModerationUserListItem, 0, len(pageIDs))
	for _, uid := range pageIDs {
		u := userMap[uid]
		agg := vMap[uid]
		rec := rMap[uid]
		effectiveCount := agg.Count
		if rec.OverrideActive {
			var snapshot []string
			_ = common.Unmarshal([]byte(rec.ViolationConversationSnapshot), &snapshot)
			known := make(map[string]bool, len(snapshot))
			for _, k := range snapshot {
				known[k] = true
			}
			var userConvIDs []string
			_ = model.DB.Model(&model.ModerationViolation{}).
				Where("user_id = ? AND user_violation = ? AND status = ? AND created_at >= ? AND expires_at > ?", uid, true, model.ModerationViolationActive, cutoff, now).
				Distinct().Pluck("conversation_id", &userConvIDs).Error
			newCount := 0
			for _, cid := range userConvIDs {
				if !known[cid] {
					newCount++
				}
			}
			effectiveCount = rec.ViolationCountOverride + newCount
		}
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
		items = append(items, ModerationUserListItem{
			RecordID:             rec.ID,
			UserID:               uid,
			Username:             u.Username,
			DisplayName:          u.DisplayName,
			Email:                u.Email,
			AccountStatus:        u.Status,
			RecordStatus:         recStatus,
			ViolationCount:       effectiveCount,
			ActualViolationCount: agg.Count,
			MaxViolationCount:    rec.MaxViolationCount,
			LastViolationAt:      lastAt,
			Note:                 rec.Note,
			ArchivedAt:           rec.ArchivedAt,
			CreatedAt:            rec.CreatedAt,
			UpdatedAt:            rec.UpdatedAt,
		})
	}
	return items, total, nil
}

func GetModerationUserDetail(userID int, conversationMode string) (*ModerationUserDetail, error) {
	if userID <= 0 {
		return nil, errors.New("invalid moderation user")
	}
	var user model.User
	if err := model.DB.First(&user, userID).Error; err != nil {
		return nil, err
	}

	now := common.GetTimestamp()
	cutoff := now - int64(setting.GetContentModerationSetting().GetViolationRetentionDuration().Seconds())

	var actualCount int64
	_ = model.DB.Model(&model.ModerationViolation{}).
		Where("user_id = ? AND user_violation = ? AND status = ? AND created_at >= ? AND expires_at > ?", userID, true, model.ModerationViolationActive, cutoff, now).
		Distinct("conversation_id").Count(&actualCount).Error

	var record model.ModerationUserRecord
	_ = model.DB.Where("user_id = ?", userID).First(&record).Error

	effectiveCount := int(actualCount)
	if record.OverrideActive {
		var snapshot []string
		_ = common.Unmarshal([]byte(record.ViolationConversationSnapshot), &snapshot)
		known := make(map[string]bool, len(snapshot))
		for _, k := range snapshot {
			known[k] = true
		}
		var userConvIDs []string
		_ = model.DB.Model(&model.ModerationViolation{}).
			Where("user_id = ? AND user_violation = ? AND status = ? AND created_at >= ? AND expires_at > ?", userID, true, model.ModerationViolationActive, cutoff, now).
			Distinct().Pluck("conversation_id", &userConvIDs).Error
		newCount := 0
		for _, cid := range userConvIDs {
			if !known[cid] {
				newCount++
			}
		}
		effectiveCount = record.ViolationCountOverride + newCount
	}

	var violations []model.ModerationViolation
	_ = model.DB.Where("user_id = ? AND user_violation = ? AND created_at >= ? AND expires_at > ?", userID, true, cutoff, now).
		Order("id desc").Limit(50).Find(&violations).Error

	var conversations []model.ModerationConversation
	convQuery := model.DB.Where("user_id = ? AND expires_at > ?", userID, now)
	if conversationMode == "violations" {
		var violationConvIDs []string
		_ = model.DB.Model(&model.ModerationViolation{}).
			Where("user_id = ? AND created_at >= ? AND expires_at > ?", userID, cutoff, now).
			Distinct().Pluck("conversation_id", &violationConvIDs).Error
		if len(violationConvIDs) > 0 {
			convQuery = convQuery.Where("conversation_id IN ?", violationConvIDs)
		} else {
			convQuery = convQuery.Where("1 = 0")
		}
	}
	_ = convQuery.Order("last_activity_at desc").Limit(50).Find(&conversations).Error

	recStatus := "active"
	if record.ArchivedAt > 0 {
		recStatus = "history"
	}
	userItem := ModerationUserListItem{
		RecordID:             record.ID,
		UserID:               user.Id,
		Username:             user.Username,
		DisplayName:          user.DisplayName,
		Email:                user.Email,
		AccountStatus:        user.Status,
		RecordStatus:         recStatus,
		ViolationCount:       effectiveCount,
		ActualViolationCount: int(actualCount),
		MaxViolationCount:    record.MaxViolationCount,
		LastViolationAt:      record.LastViolationAt,
		Note:                 record.Note,
		ArchivedAt:           record.ArchivedAt,
		CreatedAt:            record.CreatedAt,
		UpdatedAt:            record.UpdatedAt,
	}

	return &ModerationUserDetail{
		User:          userItem,
		Violations:    violations,
		Conversations: conversations,
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
	cutoff := now - int64(setting.GetContentModerationSetting().GetViolationRetentionDuration().Seconds())
	var currentConvIDs []string
	_ = model.DB.Model(&model.ModerationViolation{}).
		Where("user_id = ? AND user_violation = ? AND status = ? AND created_at >= ? AND expires_at > ?", userID, true, model.ModerationViolationActive, cutoff, now).
		Distinct().Pluck("conversation_id", &currentConvIDs).Error
	snapshotBytes, _ := common.Marshal(currentConvIDs)

	var archivedAt int64
	if violationCount == 0 {
		archivedAt = now
	}
	var record model.ModerationUserRecord
	err := model.DB.Where("user_id = ?", userID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		record = model.ModerationUserRecord{
			UserID:                        userID,
			ViolationCountOverride:        violationCount,
			OverrideActive:                true,
			ViolationConversationSnapshot: model.ModerationText(string(snapshotBytes)),
			ArchivedAt:                    archivedAt,
			Note:                          note,
			CreatedAt:                     now,
			UpdatedAt:                     now,
		}
		return model.DB.Create(&record).Error
	}
	if err != nil {
		return err
	}
	return model.DB.Model(&record).Updates(map[string]any{
		"violation_count_override":        violationCount,
		"override_active":                 true,
		"violation_conversation_snapshot": model.ModerationText(string(snapshotBytes)),
		"archived_at":                     archivedAt,
		"note":                            note,
		"updated_at":                      now,
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
	if err := model.DB.First(&user, userID).Error; err != nil {
		return err
	}
	if err := validateModerationUserMutation(&user, adminID, adminRole); err != nil {
		return err
	}
	return model.DB.Where("user_id = ?", userID).Delete(&model.ModerationUserRecord{}).Error
}

func RestoreUserAfterModeration(userID, adminID int, reason string) error {
	now := common.GetTimestamp()
	_, err := model.RestoreUserAndTokensAfterModeration(userID, now)
	if err != nil {
		return err
	}
	action := model.ModerationAction{
		AdminID: adminID,
		UserID:  userID,
		Action:  "restore_user",
		Reason:  reason,
	}
	_ = model.DB.Create(&action).Error
	return nil
}

func UnblockModerationConversation(conversationID int64, adminID int, reason string) error {
	if conversationID <= 0 {
		return errors.New("invalid conversation id")
	}
	var conv model.ModerationConversation
	if err := model.DB.First(&conv, conversationID).Error; err != nil {
		return err
	}
	if err := model.DB.Model(&conv).Updates(map[string]any{
		"status":         model.ModerationConversationActive,
		"blocked_at":     0,
		"blocked_reason": "",
		"updated_at":     common.GetTimestamp(),
	}).Error; err != nil {
		return err
	}
	action := model.ModerationAction{
		AdminID:        adminID,
		UserID:         conv.UserID,
		ConversationID: conv.ConversationID,
		Action:         "unblock_conversation",
		Reason:         reason,
	}
	_ = model.DB.Create(&action).Error
	return nil
}

func ResolveModerationViolation(violationID, adminID int64, status, reason string) error {
	if violationID <= 0 {
		return errors.New("invalid violation id")
	}
	if status != model.ModerationViolationFalsePositive && status != model.ModerationViolationReversed {
		return errors.New("invalid violation resolution status")
	}
	var violation model.ModerationViolation
	if err := model.DB.First(&violation, violationID).Error; err != nil {
		return err
	}
	now := common.GetTimestamp()
	if err := model.DB.Model(&violation).Updates(map[string]any{
		"status":          status,
		"resolved_at":     now,
		"resolved_by":     int(adminID),
		"resolution_note": reason,
	}).Error; err != nil {
		return err
	}
	action := model.ModerationAction{
		AdminID:        int(adminID),
		UserID:         violation.UserID,
		ConversationID: violation.ConversationID,
		ViolationID:    violation.ID,
		Action:         "resolve_violation",
		Reason:         reason,
	}
	_ = model.DB.Create(&action).Error
	return nil
}

func CleanupContentModerationData() error {
	config := setting.GetContentModerationSetting()
	retention := config.GetViolationRetentionDuration()
	now := common.GetTimestamp()

	_ = model.DeleteEncryptedModerationRecords(model.DB)

	_ = model.DB.Where("expires_at > 0 AND expires_at < ?", now).Delete(&model.ModerationTurn{}).Error
	_ = model.DB.Where("expires_at > 0 AND expires_at < ?", now).Delete(&model.ModerationViolation{}).Error
	_ = model.DB.Where("expires_at > 0 AND expires_at < ?", now).Delete(&model.ModerationConversation{}).Error

	_ = model.DeleteExpiredModerationContent(now, int64(retention.Seconds()))
	_ = model.DeleteExpiredModerationMetadata(now, int64(retention.Seconds()))
	return nil
}

func ProcessContentModerationQueue(ctx context.Context) error {
	return nil
}

func DecryptModerationStoredText(value string) (string, error) {
	return common.DecryptSecret(value)
}
