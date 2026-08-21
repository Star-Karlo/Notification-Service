package handlers

import (
	"crypto/subtle"
	"strconv"
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
