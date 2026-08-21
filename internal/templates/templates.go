// Package templates owns notification copy.
//
// This is the reason callers pass data rather than prose. In the monolith,
// every one of the ~117 Notification.create sites wrote its own title and
// description inline, so the same event was worded differently depending on
// which controller raised it, and translating any of it meant finding all of
// them.
package templates

import (
	"fmt"
	"strings"

	"github.com/karlo/notification-service/internal/models"
	notificationv1 "github.com/karlo/notification-service/internal/platform/genproto/karlo/notification/v1"
)

// Template describes how one event is rendered and delivered.
type Template struct {
	// Channels is the default delivery set for this event. A caller may narrow
	// it, but the default encodes the intent: an order status change is a push,
	// an invoice is also an email.
	Channels []models.Channel

	// Title and Body are Go-style format strings applied to named parameters.
	// They are per language.
	Title map[string]string
	Body  map[string]string

	// WhatsAppTemplate names the provider-approved template, required for
	// business-initiated WhatsApp messages.
	WhatsAppTemplate string

	// Params lists the parameter names the copy expects, in order. Rendering
	// checks against this so a missing parameter is caught here rather than
	// producing "Order %!s(MISSING) was approved" on someone's phone.
	Params []string
}

// registry maps each event to its template.
var registry = map[notificationv1.EventType]Template{
	notificationv1.EventType_EVENT_TYPE_ORDER_CREATED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush},
		Title: map[string]string{
			"id": "Order baru",
			"en": "New order",
		},
		Body: map[string]string{
			"id": "Order %s menunggu persetujuan Anda.",
			"en": "Order %s is awaiting your approval.",
		},
		Params: []string{"orderNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_ORDER_UPDATED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush},
		Title: map[string]string{
			"id": "Order diperbarui",
			"en": "Order updated",
		},
		Body: map[string]string{
			"id": "Order %s kini berstatus %s.",
			"en": "Order %s is now %s.",
		},
		Params: []string{"orderNumber", "status"},
	},
	notificationv1.EventType_EVENT_TYPE_ORDER_CANCELLED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush},
		Title: map[string]string{
			"id": "Order dibatalkan",
			"en": "Order cancelled",
		},
		Body: map[string]string{
			"id": "Order %s telah dibatalkan.",
			"en": "Order %s has been cancelled.",
		},
		Params: []string{"orderNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_ORDER_READY_TO_PLAN: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush},
		Title: map[string]string{
			"id": "Order siap direncanakan",
			"en": "Order ready to plan",
		},
		Body: map[string]string{
			"id": "Order %s siap untuk penugasan driver.",
			"en": "Order %s is ready for driver assignment.",
		},
		Params: []string{"orderNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_ORDER_ASSIGNED_DRIVER: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush},
		Title: map[string]string{
			"id": "Tugas pengiriman baru",
			"en": "New delivery assignment",
		},
		Body: map[string]string{
			"id": "Anda ditugaskan untuk order %s.",
			"en": "You have been assigned to order %s.",
		},
		Params: []string{"orderNumber"},
	},
	// The legacy Notification model documented this as "request delivery order
	// = request order to driver": the 3PL flow where a manager offers work to a
	// driver or a truck group rather than assigning it outright.
	notificationv1.EventType_EVENT_TYPE_ORDER_REQUEST_DELIVERY: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush},
		Title: map[string]string{
			"id": "Permintaan pengiriman",
			"en": "Delivery request",
		},
		Body: map[string]string{
			"id": "Order %s ditawarkan kepada Anda. Terima untuk mengambil tugas ini.",
			"en": "Order %s has been offered to you. Accept to take this job.",
		},
		Params: []string{"orderNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_ORDER_APPROVAL_REQUIRED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush},
		Title: map[string]string{
			"id": "Persetujuan diperlukan",
			"en": "Approval required",
		},
		Body: map[string]string{
			"id": "Order %s memerlukan persetujuan Anda.",
			"en": "Order %s needs your approval.",
		},
		Params: []string{"orderNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_AGREEMENT_CREATED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush, models.ChannelEmail},
		Title: map[string]string{
			"id": "Perjanjian baru",
			"en": "New agreement",
		},
		Body: map[string]string{
			"id": "Perjanjian %s menunggu peninjauan Anda.",
			"en": "Agreement %s is awaiting your review.",
		},
		Params: []string{"agreementNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_AGREEMENT_APPROVED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush, models.ChannelEmail},
		Title: map[string]string{
			"id": "Perjanjian disetujui",
			"en": "Agreement approved",
		},
		Body: map[string]string{
			"id": "Perjanjian %s telah disetujui.",
			"en": "Agreement %s has been approved.",
		},
		Params: []string{"agreementNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_AGREEMENT_REJECTED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush, models.ChannelEmail},
		Title: map[string]string{
			"id": "Perjanjian ditolak",
			"en": "Agreement rejected",
		},
		Body: map[string]string{
			"id": "Perjanjian %s ditolak.",
			"en": "Agreement %s was rejected.",
		},
		Params: []string{"agreementNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_AGREEMENT_EXPIRED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelEmail},
		Title: map[string]string{
			"id": "Perjanjian kadaluarsa",
			"en": "Agreement expired",
		},
		Body: map[string]string{
			"id": "Perjanjian %s telah kadaluarsa.",
			"en": "Agreement %s has expired.",
		},
		Params: []string{"agreementNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_SHIPMENT_STARTED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush},
		Title: map[string]string{
			"id": "Pengiriman dimulai",
			"en": "Shipment started",
		},
		Body: map[string]string{
			"id": "Driver untuk order %s sedang menuju titik muat.",
			"en": "The driver for order %s is heading to the loading point.",
		},
		Params: []string{"orderNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_SHIPMENT_ARRIVED_LOADING: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush},
		Title: map[string]string{
			"id": "Driver tiba di titik muat",
			"en": "Driver arrived at loading point",
		},
		Body: map[string]string{
			"id": "Driver untuk order %s telah tiba di titik muat.",
			"en": "The driver for order %s has arrived at the loading point.",
		},
		Params: []string{"orderNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_SHIPMENT_LOADED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush},
		Title: map[string]string{
			"id": "Muat selesai",
			"en": "Loading complete",
		},
		Body: map[string]string{
			"id": "Order %s telah selesai dimuat.",
			"en": "Order %s has finished loading.",
		},
		Params: []string{"orderNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_SHIPMENT_ARRIVED_UNLOADING: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush},
		Title: map[string]string{
			"id": "Driver tiba di titik bongkar",
			"en": "Driver arrived at unloading point",
		},
		Body: map[string]string{
			"id": "Driver untuk order %s telah tiba di titik bongkar.",
			"en": "The driver for order %s has arrived at the unloading point.",
		},
		Params: []string{"orderNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_SHIPMENT_FINISHED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush, models.ChannelEmail},
		Title: map[string]string{
			"id": "Pengiriman selesai",
			"en": "Shipment complete",
		},
		Body: map[string]string{
			"id": "Order %s telah selesai dikirim.",
			"en": "Order %s has been delivered.",
		},
		Params: []string{"orderNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_INVOICE_ISSUED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelEmail},
		Title: map[string]string{
			"id": "Invoice diterbitkan",
			"en": "Invoice issued",
		},
		Body: map[string]string{
			"id": "Invoice %s sebesar %s telah diterbitkan.",
			"en": "Invoice %s for %s has been issued.",
		},
		Params: []string{"invoiceNumber", "total"},
	},
	notificationv1.EventType_EVENT_TYPE_INVOICE_PAID: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelEmail},
		Title: map[string]string{
			"id": "Invoice lunas",
			"en": "Invoice paid",
		},
		Body: map[string]string{
			"id": "Invoice %s sebesar %s telah dibayar.",
			"en": "Invoice %s for %s has been paid.",
		},
		Params: []string{"invoiceNumber", "total"},
	},
	notificationv1.EventType_EVENT_TYPE_DRIVER_INVITED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelWhatsApp},
		Title: map[string]string{
			"id": "Undangan driver",
			"en": "Driver invitation",
		},
		Body: map[string]string{
			"id": "%s mengundang Anda bergabung sebagai driver.",
			"en": "%s has invited you to join as a driver.",
		},
		WhatsAppTemplate: "driver_invitation",
		Params:           []string{"companyName"},
	},
	notificationv1.EventType_EVENT_TYPE_COLLABORATION_INVITED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelEmail},
		Title: map[string]string{
			"id": "Undangan kolaborasi",
			"en": "Collaboration invitation",
		},
		Body: map[string]string{
			"id": "%s mengundang Anda untuk berkolaborasi.",
			"en": "%s has invited you to collaborate.",
		},
		Params: []string{"companyName"},
	},
	notificationv1.EventType_EVENT_TYPE_TRUCK_DRIVER_PAIRED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush},
		Title: map[string]string{
			"id": "Kendaraan ditugaskan",
			"en": "Vehicle assigned",
		},
		Body: map[string]string{
			"id": "Anda telah dipasangkan dengan kendaraan %s.",
			"en": "You have been paired with vehicle %s.",
		},
		Params: []string{"policeNumber"},
	},
	notificationv1.EventType_EVENT_TYPE_TASK_ASSIGNED: {
		Channels: []models.Channel{models.ChannelInApp, models.ChannelPush},
		Title: map[string]string{
			"id": "Tugas baru",
			"en": "New task",
		},
		Body: map[string]string{
			"id": "Anda memiliki tugas baru: %s.",
			"en": "You have a new task: %s.",
		},
		Params: []string{"taskName"},
	},
	notificationv1.EventType_EVENT_TYPE_CHAT_MESSAGE: {
		Channels: []models.Channel{models.ChannelPush},
		Title: map[string]string{
			"id": "%s",
			"en": "%s",
		},
		Body: map[string]string{
			"id": "%s",
			"en": "%s",
		},
		Params: []string{"senderName", "preview"},
	},
	notificationv1.EventType_EVENT_TYPE_OTP: {
		Channels: []models.Channel{models.ChannelWhatsApp},
		Title: map[string]string{
			"id": "Kode verifikasi",
			"en": "Verification code",
		},
		Body: map[string]string{
			"id": "Kode verifikasi Anda adalah %s. Berlaku %s menit.",
			"en": "Your verification code is %s. It is valid for %s minutes.",
		},
		WhatsAppTemplate: "otp_verification",
		Params:           []string{"code", "minutes"},
	},
}

// Rendered is the finished copy for one recipient.
type Rendered struct {
	Title            string
	Body             string
	Channels         []models.Channel
	WhatsAppTemplate string
	// Data is the flattened parameter set, passed to push as the deep-link
	// payload and to WhatsApp as template parameters.
	Data map[string]string
}

// ErrUnknownEvent is returned for an event with no template.
type ErrUnknownEvent struct {
	Event notificationv1.EventType
}

func (e *ErrUnknownEvent) Error() string {
	return fmt.Sprintf("templates: no template for event %s", e.Event)
}

// ErrMissingParam is returned when the copy needs a parameter the caller did
// not supply.
type ErrMissingParam struct {
	Event notificationv1.EventType
	Param string
}

func (e *ErrMissingParam) Error() string {
	return fmt.Sprintf("templates: event %s requires parameter %q", e.Event, e.Param)
}

// Render produces the copy for an event in a language.
//
// A missing parameter is an error rather than a blank substitution. Sending
// "Order  is awaiting your approval" is worse than not sending: the recipient
// cannot act on it and cannot tell what went wrong.
func Render(event notificationv1.EventType, language string, params map[string]interface{}) (*Rendered, error) {
	tmpl, ok := registry[event]
	if !ok {
		return nil, &ErrUnknownEvent{Event: event}
	}

	lang := normaliseLanguage(language)

	values := make([]interface{}, 0, len(tmpl.Params))
	data := make(map[string]string, len(tmpl.Params))
	for _, name := range tmpl.Params {
		raw, present := params[name]
		if !present {
			return nil, &ErrMissingParam{Event: event, Param: name}
		}
		s := stringify(raw)
		values = append(values, s)
		data[name] = s
	}

	titleFormat := pick(tmpl.Title, lang)
	bodyFormat := pick(tmpl.Body, lang)

	// A title with no verbs takes no parameters; only substitute where the
	// format actually expects them.
	title := titleFormat
	if strings.Contains(titleFormat, "%") {
		title = fmt.Sprintf(titleFormat, values[:countVerbs(titleFormat)]...)
	}

	body := fmt.Sprintf(bodyFormat, values[:countVerbs(bodyFormat)]...)

	return &Rendered{
		Title:            title,
		Body:             body,
		Channels:         tmpl.Channels,
		WhatsAppTemplate: tmpl.WhatsAppTemplate,
		Data:             data,
	}, nil
}

// DefaultChannels reports the delivery set for an event.
func DefaultChannels(event notificationv1.EventType) ([]models.Channel, bool) {
	tmpl, ok := registry[event]
	if !ok {
		return nil, false
	}
	return tmpl.Channels, true
}

// KnownEvents lists every event with a template, for the health endpoint and
// for tests that assert the contract and the registry agree.
func KnownEvents() []notificationv1.EventType {
	out := make([]notificationv1.EventType, 0, len(registry))
	for event := range registry {
		out = append(out, event)
	}
	return out
}

// countVerbs counts the format verbs in a template string, so a shorter title
// can share a parameter list with a longer body.
func countVerbs(format string) int {
	var n int
	for i := 0; i < len(format)-1; i++ {
		if format[i] != '%' {
			continue
		}
		if format[i+1] == '%' {
			i++ // An escaped percent sign is not a verb.
			continue
		}
		n++
	}
	return n
}

func pick(m map[string]string, lang string) string {
	if v, ok := m[lang]; ok {
		return v
	}
	// Indonesian is the default: it is the language of most of the user base,
	// and an untranslated string is better than an empty one.
	if v, ok := m["id"]; ok {
		return v
	}
	return ""
}

func normaliseLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		return "id"
	}
	if idx := strings.IndexAny(lang, "-_"); idx > 0 {
		lang = lang[:idx]
	}
	if lang != "en" && lang != "id" {
		return "id"
	}
	return lang
}

func stringify(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	case float64:
		// JSON numbers decode as float64; render whole numbers without a
		// trailing ".000000".
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%.2f", t)
	default:
		return fmt.Sprint(v)
	}
}
