package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/karlo/notification-service/internal/config"
	"github.com/karlo/notification-service/internal/models"
	"golang.org/x/oauth2/google"
)

// FCM sends push notifications through Firebase Cloud Messaging's HTTP v1 API.
//
// The legacy module used the deprecated legacy endpoint with a static server
// key, which Google has since retired. This uses a service account and a
// short-lived OAuth token, which is the supported path.
type FCM struct {
	cfg    config.FCM
	client *http.Client
	// tokenSource caches and refreshes the OAuth token, so a token fetch does
	// not happen on every send.
	tokenSource oauth2TokenSource
}

// oauth2TokenSource is the small part of oauth2.TokenSource this needs, kept as
// an interface so tests can supply a stub without network access.
type oauth2TokenSource interface {
	Token() (*oauth2Token, error)
}

type oauth2Token struct {
	AccessToken string
	Expiry      time.Time
}

// NewFCM builds the push sender. A disabled configuration yields a sender that
// reports itself disabled rather than a nil that callers must check.
func NewFCM(cfg config.FCM) (*FCM, error) {
	f := &FCM{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
	if !cfg.Enabled {
		return f, nil
	}

	credentials, err := loadCredentials(cfg)
	if err != nil {
		return nil, fmt.Errorf("channels: load FCM credentials: %w", err)
	}

	// JWTConfigFromJSON rather than CredentialsFromJSON: the latter is
	// deprecated because it accepts any credential configuration without
	// validating it, including external-account configurations that can be
	// pointed at an attacker-controlled token URL. This path accepts only a
	// service-account key, which is what FCM needs.
	conf, err := google.JWTConfigFromJSON(credentials,
		"https://www.googleapis.com/auth/firebase.messaging")
	if err != nil {
		return nil, fmt.Errorf("channels: parse FCM service account: %w", err)
	}
	f.tokenSource = &realTokenSource{ts: conf.TokenSource(context.Background())}

	return f, nil
}

func (f *FCM) Channel() models.Channel { return models.ChannelPush }

func (f *FCM) Enabled() bool { return f.cfg.Enabled && f.tokenSource != nil }

// fcmRequest is the HTTP v1 message envelope.
type fcmRequest struct {
	Message fcmMessage `json:"message"`
}

type fcmMessage struct {
	Token        string            `json:"token"`
	Notification *fcmNotification  `json:"notification,omitempty"`
	Data         map[string]string `json:"data,omitempty"`
	Android      *fcmAndroid       `json:"android,omitempty"`
	APNS         *fcmAPNS          `json:"apns,omitempty"`
}

type fcmNotification struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

type fcmAndroid struct {
	Priority string `json:"priority,omitempty"`
}

type fcmAPNS struct {
	Headers map[string]string `json:"headers,omitempty"`
}

// Send delivers one push.
func (f *FCM) Send(ctx context.Context, msg Message) error {
	if !f.Enabled() {
		return ErrChannelDisabled
	}
	if msg.Target == "" {
		return &InvalidTargetError{Target: "", Reason: "no push token"}
	}

	token, err := f.tokenSource.Token()
	if err != nil {
		return fmt.Errorf("channels: fcm token: %w", err)
	}

	payload := fcmRequest{
		Message: fcmMessage{
			Token: msg.Target,
			Notification: &fcmNotification{
				Title: msg.Title,
				Body:  msg.Body,
			},
			Data:    msg.Data,
			Android: &fcmAndroid{Priority: "high"},
			APNS:    &fcmAPNS{Headers: map[string]string{"apns-priority": "10"}},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("channels: encode fcm payload: %w", err)
	}

	url := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", f.cfg.ProjectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("channels: build fcm request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)

	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("channels: fcm request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	var errBody struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&errBody)

	// UNREGISTERED and INVALID_ARGUMENT mean the token is dead. Reporting that
	// distinctly lets the caller deactivate it instead of retrying forever.
	switch {
	case resp.StatusCode == http.StatusNotFound,
		errBody.Error.Status == "UNREGISTERED",
		errBody.Error.Status == "INVALID_ARGUMENT":
		return &InvalidTargetError{Target: msg.Target, Reason: errBody.Error.Status}
	default:
		return fmt.Errorf("channels: fcm returned %d: %s", resp.StatusCode, errBody.Error.Message)
	}
}

func loadCredentials(cfg config.FCM) ([]byte, error) {
	if cfg.CredentialsJSON != "" {
		return []byte(cfg.CredentialsJSON), nil
	}
	return readFile(cfg.CredentialsFilePath)
}
