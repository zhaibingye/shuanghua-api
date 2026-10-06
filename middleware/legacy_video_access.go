package middleware

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// LegacyVideoContentAuth retains previously issued opaque task URLs. An
// explicit credential always takes the ordinary token path; a task ID never
// overrides a supplied invalid token or grants access to other endpoints.
func LegacyVideoContentAuth() gin.HandlerFunc {
	auth := TokenAuth()
	return func(c *gin.Context) {
		if !model.IsPublicTaskID(c.Param("task_id")) || c.GetHeader("Authorization") != "" || c.GetHeader("x-api-key") != "" || c.GetHeader("x-goog-api-key") != "" || c.Request.URL.RawQuery != "" {
			auth(c)
			return
		}
		task, exists, err := model.GetByPublicTaskID(c.Param("task_id"))
		if err != nil || !exists || task == nil || task.Status != model.TaskStatusSuccess || !task.ResultRetrievable() {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		owner, err := model.GetUserCache(task.UserId)
		if err != nil || owner == nil || owner.Status != common.UserStatusEnabled {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Set("id", task.UserId)
		c.Next()
	}
}
