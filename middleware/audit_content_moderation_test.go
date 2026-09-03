package middleware

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModerationCapturePreservesAuditWriterStreamingCopy(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	auditWriter := beginAdminAudit(ginContext)
	require.NotNil(t, auditWriter)
	capture := service.NewModerationCapture(ginContext.Writer)
	ginContext.Writer = capture

	payload := "data: first\n\ndata: second\n\n"
	written, err := io.Copy(ginContext.Writer, strings.NewReader(payload))
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), written)
	assert.Equal(t, payload, recorder.Body.String())
	assert.Equal(t, payload, string(capture.Bytes()))
	assert.Equal(t, payload, auditWriter.body.String())
}
