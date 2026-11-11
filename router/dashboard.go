package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
)

func SetDashboardRouter(router *gin.Engine) {
	dashboard := router.Group("/")
	dashboard.Use(middleware.RouteTag("old_api"))
	dashboard.Use(gzip.Gzip(gzip.DefaultCompression))
	dashboard.Use(middleware.GlobalAPIRateLimit())
	dashboard.Use(middleware.CORS())
	dashboard.Use(middleware.DisableCache())
	dashboard.GET("/v1/usage", middleware.TokenAuthForUserQuota(), controller.GetSub2APIWalletBalance)

	apiRouter := dashboard.Group("", middleware.TokenAuth())
	{
		apiRouter.GET("/dashboard/billing/subscription", controller.GetSubscription)
		apiRouter.GET("/v1/dashboard/billing/subscription", controller.GetSubscription)
		apiRouter.GET("/dashboard/billing/usage", controller.GetUsage)
		apiRouter.GET("/v1/dashboard/billing/usage", controller.GetUsage)
		for _, prefix := range []string{"", "/v1", "/v1beta"} {
			apiRouter.GET(prefix+"/credits", controller.GetCredits)
			apiRouter.GET(prefix+"/credits/", controller.GetCredits)
		}
	}
}
