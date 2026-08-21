// Package models holds the notification service's documents.
//
// This service consolidates four things the monolith kept apart:
//
//   - The Notification model and its FCM post-save hook (karlo_be, karlo_order).
//   - The Message / MessageGroup chat, which had a second, independent hook.
//   - The nodemailer sender called directly from five controllers.
//   - communication-service-be's WhatsApp, OTP and ticket features.
package models

import (
	"time"

	"github.com/karlo/notification-service/internal/config"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Channel is a delivery mechanism.
type Channel string

const (
	ChannelInApp    Channel = "inApp"
	ChannelPush     Channel = "push"
	ChannelEmail    Channel = "email"
	ChannelWhatsApp Channel = "whatsapp"
)

// Notification is the durable record of something that happened.
//
// The in-app record is the source of truth. Push, email and WhatsApp are
// best-effort projections of it: if one fails, the notification still exists
// and the user still sees it in their inbox. In the monolith, delivery failure
// meant the notification was simply lost.
type Notification struct {
	ID primitive.ObjectID `bson:"_id,omitempty" json:"id"`

	// UserID is the single recipient. An audience is expanded into one document
	// per person, so read state is per-user rather than shared.
	UserID string `bson:"userId" json:"userId"`

	Event string `bson:"event" json:"event"`
	Title string `bson:"title" json:"title"`
	Body  string `bson:"body" json:"body"`

	SubjectID   string `bson:"subjectId,omitempty" json:"subjectId,omitempty"`
	SubjectType string `bson:"subjectType,omitempty" json:"subjectType,omitempty"`

	// ActorID is who caused the event, retained so the UI can show "Budi
	// approved your order" rather than an anonymous status change.
	ActorID string `bson:"actorId,omitempty" json:"actorId,omitempty"`

	Params map[string]interface{} `bson:"params,omitempty" json:"params,omitempty"`

	Read   bool       `bson:"read" json:"read"`
	ReadAt *time.Time `bson:"readAt,omitempty" json:"readAt,omitempty"`

	// Deliveries records what was attempted on each channel and how it went,
	// so an undelivered push is diagnosable rather than invisible.
	Deliveries []DeliveryAttempt `bson:"deliveries,omitempty" json:"deliveries,omitempty"`

	// IdempotencyKey deduplicates retries. Unique when present.
	IdempotencyKey string `bson:"idempotencyKey,omitempty" json:"-"`

	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
}

func (Notification) CollectionName() string { return config.Collection("notifications") }

// DeliveryAttempt is one channel's outcome.
type DeliveryAttempt struct {
	Channel   Channel   `bson:"channel" json:"channel"`
	Success   bool      `bson:"success" json:"success"`
	Error     string    `bson:"error,omitempty" json:"error,omitempty"`
	Target    string    `bson:"target,omitempty" json:"-"`
	AttemptAt time.Time `bson:"attemptAt" json:"attemptAt"`
}

// OTP is a one-time password issued for a phone number.
type OTP struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	PhoneNumber string             `bson:"phoneNumber" json:"phoneNumber"`

	// CodeHash is a SHA-256 of the code. The legacy implementation stored the
	// code in clear, so anyone with database access could read live codes.
	CodeHash string `bson:"codeHash" json:"-"`

	// Purpose separates registration, login and phone-change flows, so a code
	// issued for one cannot be spent on another.
	Purpose string `bson:"purpose" json:"purpose"`

	Attempts int  `bson:"attempts" json:"attempts"`
	Verified bool `bson:"verified" json:"verified"`

	ExpiresAt  time.Time  `bson:"expiresAt" json:"expiresAt"`
	VerifiedAt *time.Time `bson:"verifiedAt,omitempty" json:"verifiedAt,omitempty"`
	CreatedAt  time.Time  `bson:"createdAt" json:"createdAt"`
}

func (OTP) CollectionName() string { return config.Collection("otps") }

// IsSpent reports whether an OTP can no longer be used, whether because it was
// verified, expired, or had too many wrong guesses.
func (o *OTP) IsSpent(maxAttempts int) bool {
	return o.Verified || time.Now().After(o.ExpiresAt) || o.Attempts >= maxAttempts
}

// MessageGroup is a chat thread, usually attached to an order.
type MessageGroup struct {
	ID   primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Code string             `bson:"code" json:"code"`

	// OrderID scopes a thread to a job, which is how the legacy chat was used
	// in practice even though the model did not say so.
	OrderID string `bson:"orderId,omitempty" json:"orderId,omitempty"`

	ParticipantIDs []string `bson:"participantIds" json:"participantIds"`

	LastMessageAt      *time.Time `bson:"lastMessageAt,omitempty" json:"lastMessageAt,omitempty"`
	LastMessagePreview string     `bson:"lastMessagePreview,omitempty" json:"lastMessagePreview,omitempty"`

	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
}

func (MessageGroup) CollectionName() string { return config.Collection("message_groups") }

// Message is one chat message.
type Message struct {
	ID       primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	GroupID  primitive.ObjectID `bson:"groupId" json:"groupId"`
	SenderID string             `bson:"senderId" json:"senderId"`

	Text     string   `bson:"text,omitempty" json:"text,omitempty"`
	Pictures []string `bson:"pictures,omitempty" json:"pictures,omitempty"`

	// ReadBy records who has seen the message, so unread counts are per person
	// rather than a single flag shared by the thread.
	ReadBy []string `bson:"readBy" json:"readBy"`

	SentAt time.Time `bson:"sentAt" json:"sentAt"`
}

func (Message) CollectionName() string { return config.Collection("messages") }

// Ticket is a customer support conversation, from communication-service-be.
type Ticket struct {
	ID     primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Number string             `bson:"number" json:"number"`

	ContactPhone string `bson:"contactPhone" json:"contactPhone"`
	ContactName  string `bson:"contactName,omitempty" json:"contactName,omitempty"`

	Subject  string `bson:"subject,omitempty" json:"subject,omitempty"`
	Category string `bson:"category,omitempty" json:"category,omitempty"`
	Priority string `bson:"priority" json:"priority"`
	Status   string `bson:"status" json:"status"`

	AssignedToUserID string `bson:"assignedToUserId,omitempty" json:"assignedToUserId,omitempty"`
	// OrderID links a complaint to the job it concerns.
	OrderID string `bson:"orderId,omitempty" json:"orderId,omitempty"`

	ResolvedAt *time.Time `bson:"resolvedAt,omitempty" json:"resolvedAt,omitempty"`
	ClosedAt   *time.Time `bson:"closedAt,omitempty" json:"closedAt,omitempty"`

	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
}

func (Ticket) CollectionName() string { return config.Collection("tickets") }

// Ticket statuses.
const (
	TicketOpen       = "open"
	TicketInProgress = "inProgress"
	TicketWaiting    = "waiting"
	TicketResolved   = "resolved"
	TicketClosed     = "closed"
)

// TicketEvent is one entry in a ticket's audit trail.
type TicketEvent struct {
	ID       primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	TicketID primitive.ObjectID `bson:"ticketId" json:"ticketId"`

	Type      string                 `bson:"type" json:"type"`
	ActorID   string                 `bson:"actorId,omitempty" json:"actorId,omitempty"`
	Detail    map[string]interface{} `bson:"detail,omitempty" json:"detail,omitempty"`
	CreatedAt time.Time              `bson:"createdAt" json:"createdAt"`
}

func (TicketEvent) CollectionName() string { return config.Collection("ticket_events") }

// InboundMessage is a raw message received from WhatsApp.
//
// Storing it separately from Ticket means a webhook can be accepted and
// acknowledged quickly, then processed, without losing the payload if
// processing fails. The legacy webhook handler logged the body and dropped it.
type InboundMessage struct {
	ID primitive.ObjectID `bson:"_id,omitempty" json:"id"`

	Provider   string `bson:"provider" json:"provider"`
	ProviderID string `bson:"providerId" json:"providerId"`

	FromPhone string                 `bson:"fromPhone" json:"fromPhone"`
	Text      string                 `bson:"text,omitempty" json:"text,omitempty"`
	Payload   map[string]interface{} `bson:"payload" json:"payload"`

	Processed   bool                `bson:"processed" json:"processed"`
	ProcessedAt *time.Time          `bson:"processedAt,omitempty" json:"processedAt,omitempty"`
	ProcessErr  string              `bson:"processError,omitempty" json:"processError,omitempty"`
	TicketID    *primitive.ObjectID `bson:"ticketId,omitempty" json:"ticketId,omitempty"`

	ReceivedAt time.Time `bson:"receivedAt" json:"receivedAt"`
}

func (InboundMessage) CollectionName() string { return config.Collection("inbound_messages") }
