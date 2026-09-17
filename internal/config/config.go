// Package config loads the notification service configuration.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Environment string
	LogLevel    string

	HTTPPort string
	GRPCPort string

	MongoURI      string
	MongoDatabase string
	MongoTimeout  time.Duration

	// Cold storage (see internal/archive): notifications and conversations
	// older than ArchiveRetain move to S3 as Parquet. Needs ArchiveBucket.
	ArchiveBucket string
	ArchiveRegion string
	ArchivePrefix string
	ArchiveRetain time.Duration
	ArchiveBatch  int

	ServiceToken          string
	AcceptedServiceTokens []string

	AuthGRPCAddr string

	CORSAllowedOrigins []string

	FCM      FCM
	Email    Email
	WhatsApp WhatsApp
	OTP      OTP

	// DedupeWindow is how long an idempotency key is honoured. A retry inside
	// this window returns the original notification rather than delivering
	// twice.
	DedupeWindow time.Duration

	// Workers bounds the concurrent delivery goroutines, so a burst of orders
	// cannot open thousands of outbound connections at once.
	Workers int
}

// FCM is the push provider configuration. FCM's HTTP v1 API needs a service
// account, not the legacy server key the monolith used.
type FCM struct {
	ProjectID           string
	CredentialsJSON     string
	CredentialsFilePath string
	Enabled             bool
}

type Email struct {
	Host      string
	Port      int
	Username  string
	Password  string
	FromName  string
	FromEmail string
	Enabled   bool
}

type WhatsApp struct {
	Token       string
	PhoneNumber string
	VerifyToken string
	APIVersion  string
	Enabled     bool

	// Two numbers under one WABA app, as the legacy communication service
	// had them: PhoneNumber (number A) sends OTPs and order/driver messages
	// from here; SupportPhoneNumber (number B) is the chatbot and live-chat
	// number the communication service still runs. Meta delivers one
	// webhook per app, so events for B arrive here and are forwarded to
	// SupportWebhookURL untouched. Empty means there is no second number.
	SupportPhoneNumber string
	SupportWebhookURL  string
}

type OTP struct {
	Length     int
	TTL        time.Duration
	MaxAttempt int
	// ResendCooldown throttles repeat requests for the same number. The legacy
	// endpoint had none, so it could be used to send unlimited messages.
	ResendCooldown time.Duration
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	env := envOr("ENVIRONMENT", "development")

	cfg := &Config{
		Environment:        env,
		LogLevel:           envOr("LOG_LEVEL", "info"),
		HTTPPort:           envOr("HTTP_PORT", "5004"),
		GRPCPort:           envOr("GRPC_PORT", "6004"),
		MongoDatabase:      envOr("MONGO_DATABASE", "karlo_notification"),
		ArchiveBucket:      envOr("ARCHIVE_BUCKET", ""),
		ArchiveRegion:      envOr("ARCHIVE_REGION", envOr("AWS_REGION", "ap-southeast-3")),
		ArchivePrefix:      envOr("ARCHIVE_PREFIX", "archive/notification"),
		ArchiveRetain:      durationOr("ARCHIVE_RETAIN", 30*24*time.Hour),
		ArchiveBatch:       intOr("ARCHIVE_BATCH", 100000),
		MongoTimeout:       durationOr("MONGO_TIMEOUT", 10*time.Second),
		AuthGRPCAddr:       envOr("AUTH_GRPC_ADDR", "localhost:6001"),
		CORSAllowedOrigins: splitOr("CORS_ALLOWED_ORIGINS", nil),
		DedupeWindow:       durationOr("DEDUPE_WINDOW", 5*time.Minute),
		Workers:            intOr("DELIVERY_WORKERS", 8),

		FCM: FCM{
			ProjectID:           os.Getenv("FCM_PROJECT_ID"),
			CredentialsJSON:     os.Getenv("FCM_CREDENTIALS_JSON"),
			CredentialsFilePath: os.Getenv("FCM_CREDENTIALS_FILE"),
		},
		Email: Email{
			Host:      envOr("SMTP_HOST", ""),
			Port:      intOr("SMTP_PORT", 587),
			Username:  os.Getenv("SMTP_USERNAME"),
			Password:  os.Getenv("SMTP_PASSWORD"),
			FromName:  envOr("SMTP_FROM_NAME", "Karlo"),
			FromEmail: os.Getenv("SMTP_FROM_EMAIL"),
		},
		WhatsApp: WhatsApp{
			Token:       os.Getenv("WHATSAPP_TOKEN"),
			PhoneNumber: os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
			VerifyToken: os.Getenv("WHATSAPP_VERIFY_TOKEN"),
			APIVersion:  envOr("WHATSAPP_API_VERSION", "v21.0"),

			SupportPhoneNumber: os.Getenv("WHATSAPP_SUPPORT_PHONE_NUMBER_ID"),
			SupportWebhookURL:  os.Getenv("WHATSAPP_SUPPORT_WEBHOOK_URL"),
		},
		OTP: OTP{
			Length:         intOr("OTP_LENGTH", 6),
			TTL:            durationOr("OTP_TTL", 5*time.Minute),
			MaxAttempt:     intOr("OTP_MAX_ATTEMPTS", 5),
			ResendCooldown: durationOr("OTP_RESEND_COOLDOWN", 60*time.Second),
		},
	}

	// A channel is enabled only when it is fully configured. A half-configured
	// channel that fails at send time is worse than one that is off, because
	// the failure surfaces as a missing notification rather than a startup
	// error someone can act on.
	cfg.FCM.Enabled = cfg.FCM.ProjectID != "" &&
		(cfg.FCM.CredentialsJSON != "" || cfg.FCM.CredentialsFilePath != "")
	cfg.Email.Enabled = cfg.Email.Host != "" && cfg.Email.FromEmail != ""
	cfg.WhatsApp.Enabled = cfg.WhatsApp.Token != "" && cfg.WhatsApp.PhoneNumber != ""

	var missing []string

	cfg.MongoURI = os.Getenv("MONGO_URI")
	if cfg.MongoURI == "" {
		if env == "development" {
			cfg.MongoURI = "mongodb://localhost:27017"
		} else {
			missing = append(missing, "MONGO_URI")
		}
	}

	cfg.ServiceToken = os.Getenv("SERVICE_TOKEN")
	if cfg.ServiceToken == "" {
		missing = append(missing, "SERVICE_TOKEN")
	}

	cfg.AcceptedServiceTokens = splitOr("ACCEPTED_SERVICE_TOKENS", nil)
	if len(cfg.AcceptedServiceTokens) == 0 {
		missing = append(missing, "ACCEPTED_SERVICE_TOKENS")
	}

	if len(cfg.CORSAllowedOrigins) == 0 {
		missing = append(missing, "CORS_ALLOWED_ORIGINS")
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("config: required environment variables not set: %s", strings.Join(missing, ", "))
	}

	if cfg.OTP.Length < 4 || cfg.OTP.Length > 10 {
		return nil, fmt.Errorf("config: OTP_LENGTH must be between 4 and 10, got %d", cfg.OTP.Length)
	}

	return cfg, nil
}

func (c *Config) IsProduction() bool { return c.Environment == "production" }

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func intOr(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func durationOr(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func splitOr(key string, fallback []string) []string {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
