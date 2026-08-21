package unit

import (
	"testing"

	"github.com/karlo/notification-service/internal/channels"
)

// TestNormalisePhone covers the formats Indonesian users actually type.
//
// The legacy service passed whatever it was given straight to the WhatsApp API,
// so whether a code arrived depended on how the user happened to enter their
// number.
func TestNormalisePhone(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"08123456789", "628123456789"},
		{"+628123456789", "628123456789"},
		{"628123456789", "628123456789"},
		{"8123456789", "628123456789"},
		{"0812-3456-789", "628123456789"},
		{"0812 3456 789", "628123456789"},
		{"(0812) 3456789", "628123456789"},
		{"+62 812-3456-789", "628123456789"},
		{"  08123456789  ", "628123456789"},
	}

	for _, tc := range cases {
		got, err := channels.NormalisePhone(tc.in)
		if err != nil {
			t.Errorf("NormalisePhone(%q) failed: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalisePhone(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalisePhoneRejectsBadInput(t *testing.T) {
	for _, in := range []string{"", "   ", "abc", "123", "0812", "+", "0812345678901234567"} {
		if got, err := channels.NormalisePhone(in); err == nil {
			t.Errorf("NormalisePhone(%q) = %q, expected an error", in, got)
		}
	}
}

// TestNormalisePhoneIsIdempotent: a number that has already been normalised
// must survive a second pass unchanged, since it is stored and reused.
func TestNormalisePhoneIsIdempotent(t *testing.T) {
	once, err := channels.NormalisePhone("08123456789")
	if err != nil {
		t.Fatalf("first pass failed: %v", err)
	}
	twice, err := channels.NormalisePhone(once)
	if err != nil {
		t.Fatalf("second pass failed: %v", err)
	}
	if once != twice {
		t.Errorf("not idempotent: %q then %q", once, twice)
	}
}
