package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
)

func SetVideoRouter(router *gin.Engine) {
	for _, path := range []string{"/openai/v1/videos", "/v1/videos/generations", "/v1/videos/edits", "/v1/videos/extensions", "/api/v3/contents/generations/tasks"} {
		handlers, err := taskPluginProtocolHandlers("openai_video", "create")
		if err != nil {
			panic(err)
		}
		router.POST(path, handlers...)
	}
	for _, path := range []string{"/openai/v1/videos/:task_id", "/api/v3/contents/generations/tasks/:task_id"} {
		handlers, err := taskPluginProtocolHandlers("openai_video", "retrieve")
		if err != nil {
			panic(err)
		}
		router.GET(path, handlers...)
	}
	for _, method := range []string{"GET", "HEAD"} {
		handlers, err := taskPluginProtocolHandlers("openai_video", "content")
		if err != nil {
			panic(err)
		}
		router.Handle(method, "/openai/v1/videos/:task_id/content", handlers...)
	}

	videoSharedRouter := router.Group("/v1")
	videoSharedRouter.Use(middleware.RouteTag("relay"))
	videoSharedRouter.Use(middleware.TokenAuth())
	videoSharedRouter.Use(middleware.SystemPerformanceCheck())
	videoSharedRouter.POST(
		"/video/generations",
		middleware.PinTaskPluginEndpoint(),
		middleware.TaskPluginEndpointOnly(middleware.ModelRequestRateLimit()),
		middleware.PrepareTaskPluginEndpoint(),
		middleware.Distribute(),
		func(c *gin.Context) {
			controller.RelayTaskPluginEndpoint(c, controller.RelayTask)
		},
	)

	videoV1Router := router.Group("/v1")
	videoV1Router.Use(middleware.RouteTag("relay"))
	videoV1Router.Use(middleware.TokenAuth(), middleware.Distribute())
	{
		videoV1Router.GET("/video/generations/:task_id", controller.RelayTaskFetch)
		videoV1Router.POST("/videos/:video_id/remix", controller.RelayTask)
	}
}
