package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func payloadFor(phoneNumberID string) map[string]interface{} {
	var p map[string]interface{}
	_ = json.Unmarshal([]byte(`{"entry":[{"changes":[{"value":{"metadata":{"phone_number_id":"`+phoneNumberID+`"},"messages":[{"id":"m1","from":"628","text":{"body":"hi"}}]}}]}]}`), &p)
	return p
}

func TestSupportNumberEventsAreRecognisedByPhoneNumberID(t *testing.T) {
	if !payloadIsForPhoneNumber(payloadFor("B"), "B") {
		t.Fatal("an event for B must be recognised as B's")
	}
	if payloadIsForPhoneNumber(payloadFor("A"), "B") {
		t.Fatal("an event for A must not be taken for B's")
	}
	if payloadIsForPhoneNumber(map[string]interface{}{}, "B") {
		t.Fatal("an empty payload is nobody's")
	}
}

func TestForwardWebhookRepostsTheBodyAsJSON(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	if err := forwardWebhook(context.Background(), srv.URL, payloadFor("B")); err != nil {
		t.Fatal(err)
	}
	if !payloadIsForPhoneNumber(got, "B") {
		t.Fatal("the forwarded body was not the payload")
	}
}
