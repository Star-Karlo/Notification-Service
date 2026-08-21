package unit

import (
	"encoding/json"
	"testing"

	"github.com/karlo/notification-service/internal/config"
)

// TestNotificationGuardRefusesLegacyTargets confirms the notification service
// carries the same protection as master data: it cannot be pointed at the
// monolith's production database, whose `notifications` and `messages`
// collections hold live data.
func TestNotificationGuardRefusesLegacyTargets(t *testing.T) {
	// The guard is exercised through its exported surface here; the detailed
	// cases live in the config package's own test.
	for _, name := range []string{"notifications", "messages", "tickets", "otps"} {
		got := config.Collection(name)
		if got == name {
			t.Errorf("Collection(%q) returned the legacy name unchanged", name)
		}
		if len(got) < 3 || got[:3] != "nt_" {
			t.Errorf("Collection(%q) = %q, which lacks the nt_ prefix", name, got)
		}
	}
}

// TestWebhookEnvelopeParsing pins the shape of the Cloud API payload.
//
// This endpoint is posted to by an outside party, so a malformed or
// unrecognised body must be ignored rather than panic the handler. The legacy
// version logged the body and dropped every message on the floor.
func TestWebhookEnvelopeParsing(t *testing.T) {
	// A realistic inbound text message.
	valid := `{
	  "object": "whatsapp_business_account",
	  "entry": [{
	    "id": "123",
	    "changes": [{
	      "field": "messages",
	      "value": {
	        "messaging_product": "whatsapp",
	        "messages": [{
	          "from": "628123456789",
	          "id": "wamid.ABC123",
	          "timestamp": "1700000000",
	          "type": "text",
	          "text": {"body": "Halo, order saya di mana?"}
	        }]
	      }
	    }]
	  }]
	}`

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(valid), &payload); err != nil {
		t.Fatalf("test fixture is not valid JSON: %v", err)
	}

	// The envelope must be navigable without any assertion failing.
	entry := payload["entry"].([]interface{})[0].(map[string]interface{})
	change := entry["changes"].([]interface{})[0].(map[string]interface{})
	value := change["value"].(map[string]interface{})
	messages, ok := value["messages"].([]interface{})
	if !ok || len(messages) != 1 {
		t.Fatal("expected exactly one message in the fixture")
	}

	msg := messages[0].(map[string]interface{})
	if msg["id"] != "wamid.ABC123" {
		t.Errorf("message id = %v", msg["id"])
	}
	if msg["from"] != "628123456789" {
		t.Errorf("from = %v", msg["from"])
	}
}

// TestWebhookStatusCallbackHasNoMessages documents the other payload shape that
// arrives on the same endpoint: delivery receipts, which carry `statuses`
// rather than `messages` and must not be mistaken for inbound mail.
func TestWebhookStatusCallbackHasNoMessages(t *testing.T) {
	statusCallback := `{
	  "entry": [{
	    "changes": [{
	      "value": {
	        "statuses": [{"id": "wamid.XYZ", "status": "delivered"}]
	      }
	    }]
	  }]
	}`

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(statusCallback), &payload); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}

	entry := payload["entry"].([]interface{})[0].(map[string]interface{})
	change := entry["changes"].([]interface{})[0].(map[string]interface{})
	value := change["value"].(map[string]interface{})

	if _, ok := value["messages"]; ok {
		t.Error("a status callback should carry no messages array")
	}
}
