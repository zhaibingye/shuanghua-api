package helper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

const (
	InitialScannerBufferSize    = 64 << 10  // 64KB (64*1024)
	DefaultMaxScannerBufferSize = 128 << 20 // 128MB default SSE buffer size
	DefaultPingInterval         = 10 * time.Second
	// streamWriteTimeout bounds a write blocked on a client that stopped
	// reading so the unconditional wg.Wait() in cleanup can always finish.
	// Without it, a connected client with a full TCP buffer (no server
	// WriteTimeout) could hang the handler forever. StreamWriteBudget adds the
	// time a client reading at streamMinClientRate needs for the data.
	streamWriteTimeout  = 30 * time.Second
	streamMinClientRate = 8 << 10 // bytes per second
)

func getScannerBufferSize() int {
	if constant.StreamScannerMaxBufferMB > 0 {
		return constant.StreamScannerMaxBufferMB << 20
	}
	return DefaultMaxScannerBufferSize
}

// NewStreamScanner shares relay scanner configuration. Callers buffering bounded
// task state may additionally cap a line without increasing the configured limit.
func NewStreamScanner(reader io.Reader, maxBytes ...int) *bufio.Scanner {
	limit := getScannerBufferSize()
	if len(maxBytes) > 0 && maxBytes[0] > 0 {
		limit = min(limit, maxBytes[0])
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, min(InitialScannerBufferSize, limit)), limit)
	return scanner
}

func copyCodexSSEHeaders(c *gin.Context, resp *http.Response) {
	if c == nil || c.Writer == nil || resp == nil {
		return
	}
	// codex
	for _, name := range []string{"X-Reasoning-Included", "X-Codex-Turn-State"} {
		values := resp.Header.Values(name)
		if !service.ShouldCopyUpstreamHeader(c, name, values) {
			continue
		}
		for _, value := range values {
			if value != "" {
				c.Writer.Header().Add(name, value)
			}
		}
	}
}

// StreamWriteBudget gives the writes of one stream a deadline of
// streamWriteTimeout past the time a client reading at streamMinClientRate
// needs for everything written so far. A fixed deadline per write cut slow
// clients off in the middle of a multi-megabyte image event, and the timeout
// looked like a client disconnect. The budget carries over between writes
// because the kernel can still hold megabytes of earlier writes and wakes a
// blocked writer only after a large part of them drains. A client that stops
// reading is still cut off once the budget runs out. Callers serialize writes.
type StreamWriteBudget struct {
	drainBy time.Time
}

// Deadline returns the write deadline for n more bytes written at now.
func (b *StreamWriteBudget) Deadline(now time.Time, n int) time.Time {
	if b.drainBy.Before(now) {
		b.drainBy = now
	}
	b.drainBy = b.drainBy.Add(time.Duration(n) * time.Second / streamMinClientRate)
	return b.drainBy.Add(streamWriteTimeout)
}

// streamResponseWriter sets the connection write deadline from its budget
// before every write. Writers that don't support deadlines (e.g. httptest
// recorders) are silently ignored.
type streamResponseWriter struct {
	gin.ResponseWriter
	budget StreamWriteBudget
}

func (w *streamResponseWriter) Write(p []byte) (int, error) {
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(w.budget.Deadline(time.Now(), len(p)))
	return w.ResponseWriter.Write(p)
}

func (w *streamResponseWriter) WriteString(s string) (int, error) {
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(w.budget.Deadline(time.Now(), len(s)))
	return w.ResponseWriter.WriteString(s)
}

// Unwrap lets http.ResponseController reach the connection.
func (w *streamResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// StreamScannerHandler owns all downstream writes and stream state. Only the
// upstream read runs in a worker, which is cancelled and joined before returning.
func StreamScannerHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, dataHandler func(data string, sr *StreamResult)) {
	if resp == nil || dataHandler == nil {
		return
	}

	// Stop the preparation keepalive before changing headers. The request
	// goroutine owns all subsequent downstream writes.
	if writer, ok := c.Writer.(*streamKeepAliveWriter); ok {
		writer.stop()
	}

	info.StreamStatus = relaycommon.NewStreamStatus()
	ctx, cancel := context.WithCancel(c.Request.Context())
	// Cancellation must interrupt upstream reads even during a blocked client
	// write. This callback is the sole owner of closing the response body.
	bodyClosed := make(chan struct{})
	context.AfterFunc(ctx, func() {
		defer close(bodyClosed)
		_ = resp.Body.Close()
	})
	streamingTimeout := time.Duration(constant.StreamingTimeout) * time.Second
	timer := time.NewTimer(streamingTimeout)
	var wg sync.WaitGroup
	var pingTicker *time.Ticker
	var pingC <-chan time.Time

	defer func() {
		if r := recover(); r != nil {
			logger.LogError(c, common.LogText("data handler goroutine panic: %v", r))
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("handler panic: %v", r))
		}
		cancel()
		<-bodyClosed
		wg.Wait()
		timer.Stop()
		if pingTicker != nil {
			pingTicker.Stop()
		}
		if info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() {
			logger.LogInfo(c, common.LogText("stream ended: %s", info.StreamStatus.Summary()))
		} else {
			logger.LogError(c, common.LogText("stream ended: %s, received=%d", info.StreamStatus.Summary(), info.ReceivedResponseCount))
		}
	}()

	generalSettings := operation_setting.GetGeneralSetting()
	if generalSettings.PingIntervalEnabled && !info.DisablePing {
		pingInterval := time.Duration(generalSettings.PingIntervalSeconds) * time.Second
		if pingInterval <= 0 {
			pingInterval = DefaultPingInterval
		}
		pingTicker = time.NewTicker(pingInterval)
		pingC = pingTicker.C
	}

	copyCodexSSEHeaders(c, resp)
	SetEventStreamHeaders(c)

	// Closing lines publishes the read outcome after all buffered lines. The
	// reader never accesses gin.Context, RelayInfo, or the downstream writer.
	lines := make(chan string, 10)
	var scanErr error
	var scanPanic any
	wg.Go(func() {
		defer close(lines)
		defer func() { scanPanic = recover() }()
		scanner := NewStreamScanner(resp.Body)
		for scanner.Scan() {
			// Idle timeout tracks upstream activity, independently of writes.
			timer.Reset(streamingTimeout)
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		scanErr = scanner.Err()
	})

	sr := newStreamResult(info.StreamStatus)
	for {
		// A buffered line must not start another callback after cancellation.
		if err := ctx.Err(); err != nil {
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, err)
			return
		}
		select {
		case <-ctx.Done():
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, ctx.Err())
			return
		case <-timer.C:
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonTimeout, nil)
			return
		case <-pingC:
			if err := PingData(c); err != nil {
				logger.LogError(c, common.LogText("ping data error: %s", err.Error()))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPingFail, err)
				return
			}
		case line, ok := <-lines:
			if err := ctx.Err(); err != nil {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, err)
				return
			}
			if !ok {
				switch {
				case scanPanic != nil:
					logger.LogError(c, common.LogText("scanner goroutine panic: %v", scanPanic))
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("scanner panic: %v", scanPanic))
				case scanErr != nil:
					logger.LogError(c, common.LogText("scanner error: %s", scanErr.Error()))
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, scanErr)
				default:
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
				}
				return
			}
			logger.LogDebug(c, "stream scanner data: %s", line)
			data, hasDataPrefix := strings.CutPrefix(line, "data:")
			data = strings.TrimSpace(data)
			if !hasDataPrefix && data != "[DONE]" {
				continue
			}
			if data == "" {
				continue
			}
			if data == "[DONE]" {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				return
			}
			info.SetFirstResponseTime()
			info.ReceivedResponseCount++
			sr.reset()
			dataHandler(data, sr)
			if sr.IsStopped() {
				return
			}
		}
	}
}
