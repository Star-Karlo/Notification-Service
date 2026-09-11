// Command server runs the notification service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/karlo/notification-service/internal/channels"
	"github.com/karlo/notification-service/internal/clients"
	"github.com/karlo/notification-service/internal/config"
	"github.com/karlo/notification-service/internal/grpcserver"
	"github.com/karlo/notification-service/internal/handlers"
	"github.com/karlo/notification-service/internal/platform/authctx"
	"github.com/karlo/notification-service/internal/platform/cache"
	notificationv1 "github.com/karlo/notification-service/internal/platform/genproto/karlo/notification/v1"
	"github.com/karlo/notification-service/internal/platform/grpcutil"
	"github.com/karlo/notification-service/internal/platform/logger"
	"github.com/karlo/notification-service/internal/platform/revocation"
	"github.com/karlo/notification-service/internal/repository"
	"github.com/karlo/notification-service/internal/routes"
	"github.com/karlo/notification-service/internal/services"
)

// @title           Karlo Notification API
// @version         1.0
// @description     The single outbound-messaging boundary: in-app, push, email and WhatsApp, plus OTP and support tickets.\n\nMost traffic arrives over gRPC from other services. These HTTP endpoints serve a user's own inbox, the OTP flow, and the WhatsApp webhook.
// @termsOfService  https://karlo.co.id/terms
//
// @contact.name    Karlo Engineering
// @contact.email   engineering@karlo.co.id
//
// @host            localhost:5004
// @BasePath        /api/v1
// @schemes         http https
//
// @securityDefinitions.apikey BearerAuth
// @in                         header
// @name                       Authorization
// @description                RS256 access token issued by the authentication service, as "Bearer <token>".
func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Logs go to stdout as JSON, and additionally to Fluentd when
	// FLUENTD_HOST is set. An unreachable collector degrades to
	// stdout-only rather than stopping the service.
	// This service belongs to TMS; authctx resolves HasModule, Role and
	// HasRole against it.
	authctx.SetProduct(authctx.ProductTMS)

	logger.InitFromEnv("notification")
	defer logger.Close()

	verifier, err := authctx.NewVerifierFromEnv()
	if err != nil {
		return err
	}

	// Channel availability is logged at startup. A channel that is off because
	// it was never configured should be visible in the logs, not discovered
	// when someone reports a missing notification.
	slog.Info("delivery channels",
		"push", cfg.FCM.Enabled,
		"email", cfg.Email.Enabled,
		"whatsapp", cfg.WhatsApp.Enabled,
	)
	if cfg.IsProduction() && !cfg.FCM.Enabled {
		slog.Warn("push notifications are disabled in production")
	}

	db, err := config.ConnectMongo(cfg)
	if err != nil {
		return err
	}

	indexCtx, cancelIndex := context.WithTimeout(context.Background(), 30*time.Second)
	err = config.EnsureIndexes(indexCtx, db)
	cancelIndex()
	if err != nil {
		return err
	}

	authClient, err := clients.NewAuth(cfg.AuthGRPCAddr, "notification", cfg.ServiceToken)
	if err != nil {
		return err
	}
	defer func() {
		if err := authClient.Close(); err != nil {
			slog.Error("auth client close failed", "error", err)
		}
	}()

	pushSender, err := channels.NewFCM(cfg.FCM)
	if err != nil {
		return err
	}
	emailSender := channels.NewEmail(cfg.Email)
	whatsappSender := channels.NewWhatsApp(cfg.WhatsApp)

	// Optional. Without REDIS_ADDR this is a no-op and idempotency falls back
	// to the durable record in MongoDB.
	cacheClient := cache.FromEnv("notification")
	defer func() {
		if err := cacheClient.Close(); err != nil {
			slog.Error("cache close failed", "error", err)
		}
	}()

	notificationRepo := repository.NewNotificationRepository(db)
	otpRepo := repository.NewOTPRepository(db)
	inboundRepo := repository.NewInboundRepository(db)

	dispatcher := services.NewDispatcher(notificationRepo, authClient, pushSender, emailSender, whatsappSender, cacheClient, cfg)
	otpService := services.NewOTPService(otpRepo, whatsappSender, cfg.OTP)

	grpcSrv := grpcutil.NewServer(grpcutil.ServerConfig{
		Service:               "notification",
		Addr:                  ":" + cfg.GRPCPort,
		Verifier:              verifier,
		AcceptedServiceTokens: cfg.AcceptedServiceTokens,
		EnableReflection:      !cfg.IsProduction(),
	})
	notificationv1.RegisterNotificationServiceServer(
		grpcSrv.Registrar(),
		grpcserver.New(dispatcher, otpService, emailSender),
	)

	// Revocations announced by the authentication service. Honoured locally,
	// so a suspension or a permission change takes effect at once without
	// putting authentication on the critical path of every request.
	watchCtx, stopWatching := context.WithCancel(context.Background())
	defer stopWatching()
	revocationChecker, _ := revocation.FromEnv(watchCtx, "notification", tokenLifetimeHint())

	router := routes.Setup(routes.Deps{
		Config:      cfg,
		Verifier:    verifier,
		Remote:      authClient,
		Revocations: revocationChecker,
		Inbox:       handlers.NewInboxHandler(dispatcher),
		OTP:         handlers.NewOTPHandler(otpService),
		Webhook:     handlers.NewWebhookHandler(inboundRepo, cfg),
	})

	httpSrv := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 2)

	go func() {
		slog.Info("http server listening", "addr", httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	go func() {
		if err := grpcSrv.Serve(); err != nil {
			errCh <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		slog.Info("shutting down", "signal", sig.String())
	}

	// The shutdown window is generous because in-flight deliveries are detached
	// from their request contexts and should be allowed to finish.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := httpSrv.Shutdown(ctx); err != nil {
		slog.Error("http shutdown failed", "error", err)
	}
	grpcSrv.Shutdown(ctx)

	if err := db.Client().Disconnect(ctx); err != nil {
		slog.Error("mongodb disconnect failed", "error", err)
	}

	slog.Info("stopped")
	return nil
}

// tokenLifetimeHint is how long a revocation entry must be kept: at least as
// long as the longest token that could still be in circulation.
//
// This service does not mint tokens and so cannot read the real setting. Two
// hours matches the authentication service's default; erring long is the safe
// direction, since an entry kept too long merely refuses a token that had
// already expired.
func tokenLifetimeHint() time.Duration { return 2 * time.Hour }
