package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/karlo/notification-service/internal/config"
	"github.com/karlo/notification-service/internal/models"
)

// WhatsApp sends through the WhatsApp Cloud API.
type WhatsApp struct {
	cfg    config.WhatsApp
	client *http.Client
}

func NewWhatsApp(cfg config.WhatsApp) *WhatsApp {
	return &WhatsApp{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (w *WhatsApp) Channel() models.Channel { return models.ChannelWhatsApp }

func (w *WhatsApp) Enabled() bool { return w.cfg.Enabled }

// Send delivers a message.
//
// Business-initiated WhatsApp messages must use an approved template; free-form
// text is only allowed inside a 24-hour window opened by the user. A template
// name is therefore required, and a missing one is a programming error rather
// than something to paper over with a plain-text send that the provider would
// reject anyway.
func (w *WhatsApp) Send(ctx context.Context, msg Message) error {
	if !w.Enabled() {
		return ErrChannelDisabled
	}

	phone, err := NormalisePhone(msg.Target)
	if err != nil {
		return &InvalidTargetError{Target: msg.Target, Reason: err.Error()}
	}

	if msg.Template == "" {
		return fmt.Errorf("channels: whatsapp requires a template name")
	}

	language := msg.Language
	if language == "" {
		language = "id"
	}

	payload := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                phone,
		"type":              "template",
		"template": map[string]interface{}{
			"name":     msg.Template,
			"language": map[string]string{"code": language},
			"components": []map[string]interface{}{
				{
					"type":       "body",
					"parameters": templateParameters(msg.Params),
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("channels: encode whatsapp payload: %w", err)
	}

	url := fmt.Sprintf("https://graph.facebook.com/%s/%s/messages", w.cfg.APIVersion, w.cfg.PhoneNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("channels: build whatsapp request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.cfg.Token)

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("channels: whatsapp request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	var errBody struct {
		Error struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&errBody)

	// 131026 means the number is not a WhatsApp user; retrying will never help.
	if errBody.Error.Code == 131026 {
		return &InvalidTargetError{Target: phone, Reason: "not a WhatsApp user"}
	}

	return fmt.Errorf("channels: whatsapp returned %d: %s", resp.StatusCode, errBody.Error.Message)
}

// templateParameters renders the ordered body parameters a template expects.
//
// The map is ordered by key so the same data always produces the same parameter
// order; a map's iteration order would otherwise scramble the placeholders.
// templateParameters lays the values out positionally. Meta templates bind
// {{1}}, {{2}}… by position, so the order is the template's declared order —
// never a map's, which once sorted these alphabetically and swapped
// origin and destination in a driver's assignment.
func templateParameters(params []string) []map[string]string {
	out := make([]map[string]string, 0, len(params))
	for _, v := range params {
		out = append(out, map[string]string{"type": "text", "text": v})
	}
	return out
}

var digitsOnly = regexp.MustCompile(`\D`)

// NormalisePhone converts an Indonesian number to the E.164 form the WhatsApp
// API requires.
//
// Users enter numbers as 08123..., +628123... and 628123... interchangeably.
// The legacy service passed whatever it was given straight to the API, so
// delivery depended on how the user happened to type their number.
func NormalisePhone(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("empty phone number")
	}

	digits := digitsOnly.ReplaceAllString(s, "")

	switch {
	case strings.HasPrefix(digits, "62"):
		// Already in international form.
	case strings.HasPrefix(digits, "0"):
		digits = "62" + strings.TrimPrefix(digits, "0")
	case strings.HasPrefix(digits, "8"):
		// A bare mobile number with the leading zero omitted.
		digits = "62" + digits
	default:
		// Some other country code; accept it as given.
	}

	if len(digits) < 9 || len(digits) > 15 {
		return "", fmt.Errorf("phone number has %d digits, expected 9 to 15", len(digits))
	}

	return digits, nil
}
