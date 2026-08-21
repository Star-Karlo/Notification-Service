package unit

import (
	"strings"
	"testing"

	notificationv1 "github.com/karlo/notification-service/internal/platform/genproto/karlo/notification/v1"
	"github.com/karlo/notification-service/internal/templates"
)

func TestRenderProducesCompleteCopy(t *testing.T) {
	cases := []struct {
		name     string
		event    notificationv1.EventType
		lang     string
		params   map[string]interface{}
		wantBody string
	}{
		{
			name:     "order created in Indonesian",
			event:    notificationv1.EventType_EVENT_TYPE_ORDER_CREATED,
			lang:     "id",
			params:   map[string]interface{}{"orderNumber": "ORD-2026-000123"},
			wantBody: "Order ORD-2026-000123 menunggu persetujuan Anda.",
		},
		{
			name:     "order created in English",
			event:    notificationv1.EventType_EVENT_TYPE_ORDER_CREATED,
			lang:     "en",
			params:   map[string]interface{}{"orderNumber": "ORD-2026-000123"},
			wantBody: "Order ORD-2026-000123 is awaiting your approval.",
		},
		{
			name:  "two-parameter template",
			event: notificationv1.EventType_EVENT_TYPE_INVOICE_ISSUED,
			lang:  "en",
			params: map[string]interface{}{
				"invoiceNumber": "INV-2026-000045",
				"total":         "9100000",
			},
			wantBody: "Invoice INV-2026-000045 for 9100000 has been issued.",
		},
		{
			// An unknown language falls back to Indonesian rather than
			// rendering nothing.
			name:     "unknown language falls back",
			event:    notificationv1.EventType_EVENT_TYPE_ORDER_CREATED,
			lang:     "fr",
			params:   map[string]interface{}{"orderNumber": "ORD-1"},
			wantBody: "Order ORD-1 menunggu persetujuan Anda.",
		},
		{
			name:     "regional language tag is narrowed",
			event:    notificationv1.EventType_EVENT_TYPE_ORDER_CREATED,
			lang:     "en-GB",
			params:   map[string]interface{}{"orderNumber": "ORD-1"},
			wantBody: "Order ORD-1 is awaiting your approval.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := templates.Render(tc.event, tc.lang, tc.params)
			if err != nil {
				t.Fatalf("Render failed: %v", err)
			}
			if got.Body != tc.wantBody {
				t.Errorf("body = %q, want %q", got.Body, tc.wantBody)
			}
			if got.Title == "" {
				t.Error("title is empty")
			}
			if len(got.Channels) == 0 {
				t.Error("no channels declared")
			}
		})
	}
}

// TestRenderRejectsMissingParams is the point of having a registry at all.
// Sending "Order  is awaiting your approval" is worse than sending nothing: the
// recipient cannot act on it and cannot tell what went wrong.
func TestRenderRejectsMissingParams(t *testing.T) {
	_, err := templates.Render(
		notificationv1.EventType_EVENT_TYPE_ORDER_CREATED,
		"id",
		map[string]interface{}{}, // orderNumber missing
	)
	if err == nil {
		t.Fatal("expected an error when a required parameter is absent")
	}
	if !strings.Contains(err.Error(), "orderNumber") {
		t.Errorf("error should name the missing parameter, got: %v", err)
	}

	// A partially supplied two-parameter template must also fail.
	_, err = templates.Render(
		notificationv1.EventType_EVENT_TYPE_INVOICE_ISSUED,
		"en",
		map[string]interface{}{"invoiceNumber": "INV-1"},
	)
	if err == nil {
		t.Fatal("expected an error when only some parameters are supplied")
	}
}

func TestRenderRejectsUnknownEvent(t *testing.T) {
	_, err := templates.Render(
		notificationv1.EventType_EVENT_TYPE_UNSPECIFIED,
		"id",
		map[string]interface{}{},
	)
	if err == nil {
		t.Fatal("expected an error for an event with no template")
	}
}

// TestEveryContractEventHasATemplate keeps the proto and the registry in step.
// A new event added to the contract without copy would otherwise fail at
// delivery time, in production, rather than here.
func TestEveryContractEventHasATemplate(t *testing.T) {
	values := notificationv1.EventType_name

	for num, name := range values {
		event := notificationv1.EventType(num)
		if event == notificationv1.EventType_EVENT_TYPE_UNSPECIFIED {
			continue
		}
		if _, ok := templates.DefaultChannels(event); !ok {
			t.Errorf("event %s is declared in the contract but has no template", name)
		}
	}
}

// TestRenderedNumbersAreReadable covers the JSON-decoding path: numeric params
// arrive as float64 and must not appear as "123.000000".
func TestRenderedNumbersAreReadable(t *testing.T) {
	got, err := templates.Render(
		notificationv1.EventType_EVENT_TYPE_INVOICE_ISSUED,
		"en",
		map[string]interface{}{
			"invoiceNumber": "INV-1",
			"total":         float64(9100000),
		},
	)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if strings.Contains(got.Body, ".000000") {
		t.Errorf("a whole number rendered with spurious decimals: %q", got.Body)
	}
	if !strings.Contains(got.Body, "9100000") {
		t.Errorf("body does not contain the amount: %q", got.Body)
	}
}
