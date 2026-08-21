package unit

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/karlo/notification-service/internal/config"
	"github.com/karlo/notification-service/internal/handlers"
	"github.com/karlo/notification-service/internal/platform/authctx"
	"github.com/karlo/notification-service/internal/routes"
)

const testVerifyToken = "verify-token-for-tests"

// buildRouter constructs the HTTP surface. The webhook handler is real, since
// its verification path needs no database; the others are nil because the tests
// that touch them assert rejection before a handler runs.
func buildRouter(t *testing.T, environment string) http.Handler {
	t.Helper()

	verifier, err := authctx.NewVerifier(testPublicKeyPEM(t))
	if err != nil {
		t.Fatalf("could not build verifier: %v", err)
	}

	cfg := &config.Config{
		Environment:        environment,
		CORSAllowedOrigins: []string{"http://localhost:5173"},
		WhatsApp: config.WhatsApp{
			VerifyToken: testVerifyToken,
		},
	}

	return routes.Setup(routes.Deps{
		Config:   cfg,
		Verifier: verifier,
		Webhook:  handlers.NewWebhookHandler(nil, cfg),
	})
}

func testPublicKeyPEM(t *testing.T) []byte {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("could not generate a key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("could not marshal the public key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// TestHealthReportsChannelAvailability matters operationally: a channel that is
// silently unconfigured looks identical to one that is broken, and the legacy
// system gave no way to tell them apart without reading the environment.
func TestHealthReportsChannelAvailability(t *testing.T) {
	router := buildRouter(t, "development")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health = %d, want 200", rec.Code)
	}

	var body struct {
		Status   string          `json:"status"`
		Channels map[string]bool `json:"channels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("health response is not valid JSON: %v", err)
	}

	for _, channel := range []string{"push", "email", "whatsapp"} {
		if _, ok := body.Channels[channel]; !ok {
			t.Errorf("health does not report the %q channel", channel)
		}
	}
	// None are configured in this test, so all must report false rather than
	// being absent or defaulting to true.
	for name, enabled := range body.Channels {
		if enabled {
			t.Errorf("channel %q reports enabled with no configuration", name)
		}
	}
}

func TestSwaggerVisibility(t *testing.T) {
	dev := buildRouter(t, "development")
	req := httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)
	rec := httptest.NewRecorder()
	dev.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Error("the Swagger UI should be served in development")
	}

	prod := buildRouter(t, "production")
	req = httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)
	rec = httptest.NewRecorder()
	prod.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("the Swagger UI should be hidden in production, got %d", rec.Code)
	}
}

// TestInboxRequiresAuthentication: a user's notifications are theirs alone, and
// the user id is taken from the token rather than from a parameter.
func TestInboxRequiresAuthentication(t *testing.T) {
	router := buildRouter(t, "development")

	protected := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/notifications"},
		{http.MethodGet, "/api/v1/notifications/unread"},
		{http.MethodPost, "/api/v1/notifications/read"},
	}

	for _, route := range protected {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("= %d, want 401", rec.Code)
			}
		})
	}
}

// TestInboxIgnoresUserIdParameter is the tenancy regression test. Supplying
// someone else's id as a query parameter must not work; the id comes from the
// token, and with no token the request is refused outright.
func TestInboxIgnoresUserIdParameter(t *testing.T) {
	router := buildRouter(t, "development")

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/notifications?userId=00000000-0000-0000-0000-000000000001", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("= %d, want 401; a userId parameter must not stand in for a token", rec.Code)
	}
}

// TestWebhookVerification covers Meta's subscription handshake.
func TestWebhookVerification(t *testing.T) {
	router := buildRouter(t, "development")

	t.Run("correct token echoes the challenge", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/api/v1/webhooks/whatsapp?hub.mode=subscribe&hub.verify_token="+
				testVerifyToken+"&hub.challenge=12345", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("= %d, want 200", rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "12345" {
			t.Errorf("body = %q, want the challenge echoed back", got)
		}
	})

	t.Run("wrong token is refused", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/api/v1/webhooks/whatsapp?hub.mode=subscribe&hub.verify_token=wrong&hub.challenge=12345", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("= %d, want 403", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "12345") {
			t.Error("the challenge was echoed to an unverified caller")
		}
	})

	t.Run("missing token is refused", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/api/v1/webhooks/whatsapp?hub.mode=subscribe&hub.challenge=12345", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("= %d, want 403", rec.Code)
		}
	})

	t.Run("wrong mode is refused", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/api/v1/webhooks/whatsapp?hub.mode=unsubscribe&hub.verify_token="+
				testVerifyToken+"&hub.challenge=12345", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("= %d, want 403", rec.Code)
		}
	})
}

// TestWebhookAcknowledgesUnparseableBody: Meta redelivers on any non-2xx, and a
// body this service cannot parse will not become parseable on retry. It must be
// acknowledged rather than retried forever.
func TestWebhookAcknowledgesUnparseableBody(t *testing.T) {
	router := buildRouter(t, "development")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/whatsapp",
		strings.NewReader("this is not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("= %d, want 200 so the provider stops retrying", rec.Code)
	}
}

// TestOTPRoutesArePublic: these are reached before a user has a token, by
// necessity. Abuse is bounded by the resend cooldown and attempt cap instead.
func TestOTPRoutesArePublic(t *testing.T) {
	router := buildRouter(t, "development")

	for _, path := range []string{"/api/v1/otp/send", "/api/v1/otp/verify"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s = 401; the OTP flow must be reachable without a token", path)
		}
		if rec.Code == http.StatusNotFound {
			t.Errorf("%s = 404; the route is not registered", path)
		}
	}
}
