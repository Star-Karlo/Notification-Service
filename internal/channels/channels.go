// Package channels holds the outbound delivery adapters.
//
// Each channel implements Sender. The dispatcher does not know which is which,
// which is what lets business code say "notify these people" without naming a
// transport.
package channels

import (
	"context"
	"errors"

	"github.com/karlo/notification-service/internal/models"
)

// ErrChannelDisabled is returned by a channel that is not configured. It is
// distinguished from a delivery failure so an unconfigured channel in
// development does not look like an outage.
var ErrChannelDisabled = errors.New("channel disabled")

// Message is a rendered notification ready to send.
type Message struct {
	// Target is the channel-specific address: a push token, an email address,
	// a phone number.
	Target string
	Title  string
	Body   string
	// Data carries the deep-link payload for push, and template parameters for
	// email and WhatsApp.
	Data map[string]string
	// Params are the template parameters in declared order, for WhatsApp,
	// whose templates are positional.
	Params []string
	// Template names a provider-side template. WhatsApp requires one for
	// business-initiated messages; email uses it to select a layout.
	Template string
	Language string
}

// Sender delivers on one channel.
type Sender interface {
	// Channel identifies which channel this is, for recording the attempt.
	Channel() models.Channel
	// Send delivers one message. It returns ErrChannelDisabled when the channel
	// is not configured.
	Send(ctx context.Context, msg Message) error
	// Enabled reports whether the channel is configured.
	Enabled() bool
}

// InvalidTargetError marks a permanently undeliverable address, such as an FCM
// token the provider has rejected as unregistered.
//
// The distinction matters: a transient failure should be retried, while an
// invalid target should deactivate the stored token so it is not retried
// forever. The legacy FCM module logged every error identically and kept dead
// tokens indefinitely.
type InvalidTargetError struct {
	Target string
	Reason string
}

func (e *InvalidTargetError) Error() string {
	return "invalid target " + e.Target + ": " + e.Reason
}

// IsInvalidTarget reports whether an error means the address is permanently bad.
func IsInvalidTarget(err error) bool {
	var e *InvalidTargetError
	return errors.As(err, &e)
}
