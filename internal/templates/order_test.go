package templates

import (
	"testing"

	notificationv1 "github.com/karlo/notification-service/internal/platform/genproto/karlo/notification/v1"
)

// The WhatsApp template binds {{1}}, {{2}}, {{3}} by position, so the
// rendered parameters must come out in the declared order, whatever order
// the caller's map iterates in.
func TestRenderKeepsParamOrder(t *testing.T) {
	r, err := Render(notificationv1.EventType_EVENT_TYPE_ORDER_ASSIGNED_DRIVER, "id", map[string]interface{}{
		"destination": "Gudang B",
		"orderNumber": "ORD-1",
		"origin":      "Gudang A",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ORD-1", "Gudang A", "Gudang B"}
	if len(r.Params) != len(want) {
		t.Fatalf("got %v", r.Params)
	}
	for i := range want {
		if r.Params[i] != want[i] {
			t.Fatalf("param %d = %q, want %q (all: %v)", i, r.Params[i], want[i], r.Params)
		}
	}
	if r.WhatsAppTemplate != "order_assigned_driver" {
		t.Fatalf("template %q", r.WhatsAppTemplate)
	}
	if r.Body != "Anda ditugaskan untuk order ORD-1: Gudang A → Gudang B." {
		t.Fatalf("body %q", r.Body)
	}
}
