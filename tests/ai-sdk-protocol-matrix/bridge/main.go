// The loopback-only bridge exercises the checked-out relaykit with real SDKs
// and upstreams, without requiring a gateway database or changing its channels.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

type upstream struct {
	format           types.RelayFormat
	base, key, model string
}

type bridge struct {
	upstreams map[string]upstream
	trace     string
	sequence  atomic.Uint64
	client    *http.Client
}

func main() {
	listen := flag.String("listen", "127.0.0.1:38173", "loopback listen address")
	trace := flag.String("trace", "", "directory for upstream bodies (no authentication headers)")
	flag.Parse()
	if !strings.HasPrefix(*listen, "127.0.0.1:") {
		log.Fatal("the test bridge must listen on 127.0.0.1")
	}
	b := &bridge{upstreams: make(map[string]upstream), trace: *trace, client: &http.Client{Timeout: 5 * time.Minute}}
	for name, format := range map[string]types.RelayFormat{"CHAT": types.RelayFormatOpenAI, "RESPONSES": types.RelayFormatOpenAIResponses, "CLAUDE": types.RelayFormatClaude, "GEMINI": types.RelayFormatGemini} {
		prefix := "MATRIX_" + name + "_"
		u := upstream{format: format, base: strings.TrimRight(os.Getenv(prefix+"BASE_URL"), "/"), key: os.Getenv(prefix + "API_KEY"), model: os.Getenv(prefix + "MODEL")}
		if u.base == "" || u.key == "" || u.model == "" {
			log.Fatalf("missing %s configuration", prefix)
		}
		b.upstreams[u.model] = u
	}
	if b.trace != "" {
		if err := os.MkdirAll(b.trace, 0700); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("relaykit matrix bridge listening on %s", *listen)
	log.Fatal((&http.Server{Addr: *listen, Handler: b, ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
}

func requestDTO(format types.RelayFormat) any {
	switch format {
	case types.RelayFormatOpenAI:
		return &dto.GeneralOpenAIRequest{}
	case types.RelayFormatOpenAIResponses:
		return &dto.OpenAIResponsesRequest{}
	case types.RelayFormatClaude:
		return &dto.ClaudeRequest{}
	default:
		return &dto.GeminiChatRequest{}
	}
}

func responseDTO(format types.RelayFormat, stream bool) any {
	switch format {
	case types.RelayFormatOpenAI:
		if stream {
			return &dto.ChatCompletionsStreamResponse{}
		}
		return &dto.OpenAITextResponse{}
	case types.RelayFormatOpenAIResponses:
		if stream {
			return &dto.ResponsesStreamResponse{}
		}
		return &dto.OpenAIResponsesResponse{}
	case types.RelayFormatClaude:
		return &dto.ClaudeResponse{}
	default:
		return &dto.GeminiChatResponse{}
	}
}

func (b *bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		_, _ = io.WriteString(w, "ok")
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	var source types.RelayFormat
	switch {
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		source = types.RelayFormatOpenAI
	case strings.HasSuffix(r.URL.Path, "/responses"):
		source = types.RelayFormatOpenAIResponses
	case strings.HasSuffix(r.URL.Path, "/messages"):
		source = types.RelayFormatClaude
	case strings.Contains(r.URL.Path, "/models/"):
		source = types.RelayFormatGemini
	default:
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var envelope struct {
		Model  string
		Stream bool
	}
	if err = kitutil.Unmarshal(body, &envelope); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if source == types.RelayFormatGemini {
		_, modelPath, _ := strings.Cut(r.URL.Path, "/models/")
		envelope.Model, _, _ = strings.Cut(modelPath, ":")
		envelope.Stream = strings.Contains(modelPath, ":streamGenerateContent")
	}
	u, ok := b.upstreams[envelope.Model]
	if !ok {
		http.Error(w, "unknown matrix target model", 400)
		return
	}
	request := requestDTO(source)
	if err = kitutil.Unmarshal(body, request); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	info := &convmeta.Values{OriginModelName: u.model, UpstreamModelName: u.model, ChannelMetaAttached: true, IsStream: envelope.Stream, Options: &convmeta.Options{
		Claude: convmeta.ClaudeOptions{DefaultMaxTokens: func(string) int { return 8192 }},
		Gemini: convmeta.GeminiOptions{FunctionCallThoughtSignatureEnabled: true, SupportsJSONSchema: func(string) bool { return true }},
	}}
	converted, err := relayconvert.ConvertRequest(r.Context(), info, u.format, request)
	if err != nil {
		http.Error(w, "request conversion: "+err.Error(), 400)
		return
	}
	// Gemini carries its model in the URL, so a Gemini-source request needs
	// the target model assigned after conversion when testing another format.
	switch value := converted.Value.(type) {
	case *dto.GeneralOpenAIRequest:
		value.Model = u.model
		if envelope.Stream {
			value.StreamOptions = &dto.StreamOptions{IncludeUsage: true}
		}
	case *dto.OpenAIResponsesRequest:
		value.Model = u.model
	case *dto.ClaudeRequest:
		value.Model = u.model
	}
	body, err = kitutil.Marshal(converted.Value)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	endpoint := u.base
	switch u.format {
	case types.RelayFormatOpenAI:
		endpoint += "/chat/completions"
	case types.RelayFormatOpenAIResponses:
		endpoint += "/responses"
	case types.RelayFormatClaude:
		endpoint += "/messages"
	case types.RelayFormatGemini:
		endpoint += "/models/" + url.PathEscape(u.model)
		if envelope.Stream {
			endpoint += ":streamGenerateContent?alt=sse"
		} else {
			endpoint += ":generateContent"
		}
	}
	up, err := http.NewRequestWithContext(r.Context(), "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	up.Header.Set("Content-Type", "application/json")
	switch u.format {
	case types.RelayFormatClaude:
		up.Header.Set("x-api-key", u.key)
		up.Header.Set("anthropic-version", "2023-06-01")
	case types.RelayFormatGemini:
		up.Header.Set("x-goog-api-key", u.key)
	default:
		up.Header.Set("Authorization", "Bearer "+u.key)
	}
	number := b.sequence.Add(1)
	w.Header().Set("X-Matrix-Bridge-Request", fmt.Sprint(number))
	var trace *os.File
	if b.trace != "" {
		prefix := filepath.Join(b.trace, fmt.Sprintf("%04d-%s-to-%s", number, source, u.format))
		if err := os.WriteFile(prefix+".request.json", body, 0600); err != nil {
			http.Error(w, "could not record upstream request", 500)
			return
		}
		trace, err = os.Create(prefix + ".response.txt")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer trace.Close()
	}
	resp, err := b.client.Do(up)
	if err != nil {
		http.Error(w, "upstream transport failed", 502)
		log.Printf("request %d: transport failed", number)
		return
	}
	defer resp.Body.Close()
	var reader io.Reader = resp.Body
	if trace != nil {
		reader = io.TeeReader(reader, trace)
	}
	log.Printf("request %d: %s -> %s status=%d", number, source, u.format, resp.StatusCode)
	if resp.StatusCode >= 300 {
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, reader)
		return
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		data, readErr := io.ReadAll(reader)
		value := responseDTO(u.format, false)
		if readErr == nil {
			readErr = kitutil.Unmarshal(data, value)
		}
		if readErr != nil {
			http.Error(w, "invalid upstream JSON", 502)
			return
		}
		result, conversionErr := relayconvert.ConvertResponse(r.Context(), info, source, value)
		if conversionErr != nil {
			http.Error(w, conversionErr.Error(), 502)
			return
		}
		data, _ = kitutil.Marshal(result.Value)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	state, err := relayconvert.NewResponseStreamState(u.format, source, relayconvert.ResponseStreamOptions{Model: u.model, IncludeUsage: true, EmitSequenceNumber: true})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	emit := func(results []relayconvert.ResponseResult) {
		for _, result := range results {
			value := result.Value
			if event, ok := value.(relayconvert.ChatToResponsesStreamEvent); ok {
				value = event.Payload
			}
			data, marshalErr := kitutil.Marshal(value)
			if marshalErr != nil {
				panic(http.ErrAbortHandler)
			}
			switch source {
			case types.RelayFormatClaude, types.RelayFormatOpenAIResponses:
				var event struct{ Type string }
				_ = kitutil.Unmarshal(data, &event)
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
			default:
				_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	var dataLines []string
	consume := func() bool {
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		if data == "" || data == "[DONE]" {
			return true
		}
		value := responseDTO(u.format, true)
		if err := kitutil.UnmarshalJsonStr(data, value); err != nil {
			log.Printf("request %d: invalid stream JSON: %v", number, err)
			return false
		}
		results, conversionErr := relayconvert.ConvertStreamResponseChunk(r.Context(), info, state, value)
		if conversionErr != nil {
			log.Printf("request %d: stream conversion: %v", number, conversionErr)
			return false
		}
		emit(results)
		return true
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !consume() {
				panic(http.ErrAbortHandler)
			}
			continue
		}
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimPrefix(data, " "))
		}
	}
	if !consume() {
		panic(http.ErrAbortHandler)
	}
	if scanner.Err() != nil {
		log.Printf("request %d: upstream stream read failed", number)
		panic(http.ErrAbortHandler)
	}
	results, err := relayconvert.FinalizeStreamResponse(r.Context(), info, state)
	if err != nil {
		log.Printf("request %d: stream finalization: %v", number, err)
		panic(http.ErrAbortHandler)
	}
	emit(results)
	if source == types.RelayFormatOpenAI {
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}
}
