// Package routes wires the notification service HTTP surface.
package routes

import (
	"net/http"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	swaggerfiles "github.com/swaggo/files"
	ginswagger "github.com/swaggo/gin-swagger"

	// Imported for its side effect: the generated package registers the
	// OpenAPI document with the swagger runtime on init.
	_ "github.com/karlo/notification-service/docs"
	"github.com/karlo/notification-service/internal/config"
	"github.com/karlo/notification-service/internal/handlers"
	"github.com/karlo/notification-service/internal/middleware"
	"github.com/karlo/notification-service/internal/platform/authctx"
)

type Deps struct {
	Config   *config.Config
	Verifier *authctx.Verifier
	Remote   authctx.RemoteValidator

	Inbox   *handlers.InboxHandler
	OTP     *handlers.OTPHandler
	Webhook *handlers.WebhookHandler
}

func Setup(d Deps) *gin.Engine {
	if d.Config.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(middleware.RequestLogger())
	router.Use(cors.New(cors.Config{
		AllowOrigins:     d.Config.CORSAllowedOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "X-Request-Id"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	router.GET("/health", func(c *gin.Context) {
		// The health response reports which channels are live, so a silently
		// unconfigured provider is visible without reading the environment.
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"service": "notification",
			"channels": gin.H{
				"push":     d.Config.FCM.Enabled,
				"email":    d.Config.Email.Enabled,
				"whatsapp": d.Config.WhatsApp.Enabled,
			},
		})
	})

	// The interactive API browser. It is served only outside production: the
	// document describes every endpoint and its shapes, which is exactly the
	// reconnaissance an attacker would otherwise have to guess at.
	if !d.Config.IsProduction() {
		// /swagger/index.html is the browser; /swagger/doc.json is the raw
		// document, which is what client generators want.
		router.GET("/swagger/*any", ginswagger.WrapHandler(swaggerfiles.Handler))
	}

	api := router.Group("/api/v1")

	// The webhook is public: Meta authenticates by the verify token on
	// subscription, not by a bearer token on each delivery.
	webhooks := api.Group("/webhooks")
	webhooks.GET("/whatsapp", d.Webhook.Verify)
	webhooks.POST("/whatsapp", d.Webhook.Receive)

	// OTP endpoints are public by necessity: they are used before a user has a
	// token. Abuse is bounded by the resend cooldown and the attempt cap rather
	// than by authentication.
	otp := api.Group("/otp")
	otp.POST("/send", d.OTP.Send)
	otp.POST("/verify", d.OTP.Verify)

	// The inbox is per user and always scoped to the token.
	notifications := api.Group("/notifications")
	notifications.Use(authctx.RequireAuth(d.Verifier, d.Remote))
	notifications.GET("", d.Inbox.List)
	notifications.GET("/unread", d.Inbox.Unread)
	notifications.POST("/read", d.Inbox.MarkRead)

	return router
}
