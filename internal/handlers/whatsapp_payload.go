package handlers

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// whatsAppMessage is one message pulled out of a webhook envelope.
type whatsAppMessage struct {
	ID   string
	From string
	Text string
	Raw  map[string]interface{}
}

// extractWhatsAppMessages walks the Cloud API webhook envelope.
//
// The shape is entry[].changes[].value.messages[], and every level is optional.
// Each step is type-asserted rather than assumed, because a malformed or
// unfamiliar payload must be ignored rather than panic the handler: this is an
// endpoint an outside party posts to.
func extractWhatsAppMessages(payload map[string]interface{}) []whatsAppMessage {
	var out []whatsAppMessage

	entries, ok := payload["entry"].([]interface{})
	if !ok {
		return nil
	}

	for _, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]interface{})
		if !ok {
			continue
		}

		changes, ok := entry["changes"].([]interface{})
		if !ok {
			continue
		}

		for _, rawChange := range changes {
			change, ok := rawChange.(map[string]interface{})
			if !ok {
				continue
			}

			value, ok := change["value"].(map[string]interface{})
			if !ok {
				continue
			}

			messages, ok := value["messages"].([]interface{})
			if !ok {
				// Not a message: status callbacks (delivered, read) arrive on
				// the same webhook and have no messages array.
				continue
			}

			for _, rawMessage := range messages {
				message, ok := rawMessage.(map[string]interface{})
				if !ok {
					continue
				}

				msg := whatsAppMessage{Raw: message}
				if id, ok := message["id"].(string); ok {
					msg.ID = id
				}
				if from, ok := message["from"].(string); ok {
					msg.From = from
				}
				if text, ok := message["text"].(map[string]interface{}); ok {
					if body, ok := text["body"].(string); ok {
						msg.Text = body
					}
				}

				// Without a provider id there is nothing to deduplicate on, so
				// a redelivery would create a second copy.
				if msg.ID == "" {
					continue
				}

				out = append(out, msg)
			}
		}
	}

	return out
}

// secureEqual compares two secrets in constant time.
func secureEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func itoa(n int) string { return strconv.Itoa(n) }

// payloadIsForPhoneNumber reports whether any change in a webhook payload
// was addressed to the given phone number id (value.metadata.phone_number_id).
// Meta batches events per app, so one payload names one number in practice.
func payloadIsForPhoneNumber(payload map[string]interface{}, phoneNumberID string) bool {
	entries, _ := payload["entry"].([]interface{})
	for _, rawEntry := range entries {
		entry, _ := rawEntry.(map[string]interface{})
		changes, _ := entry["changes"].([]interface{})
		for _, rawChange := range changes {
			change, _ := rawChange.(map[string]interface{})
			value, _ := change["value"].(map[string]interface{})
			metadata, _ := value["metadata"].(map[string]interface{})
			if id, _ := metadata["phone_number_id"].(string); id == phoneNumberID {
				return true
			}
		}
	}
	return false
}

// forwardWebhook re-posts a webhook payload to another receiver.
func forwardWebhook(ctx context.Context, url string, payload map[string]interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("forward returned %d", resp.StatusCode)
	}
	return nil
}
