// Package services holds the notification service's logic.
package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/karlo/notification-service/internal/channels"
	"github.com/karlo/notification-service/internal/clients"
	"github.com/karlo/notification-service/internal/config"
	"github.com/karlo/notification-service/internal/models"
	notificationv1 "github.com/karlo/notification-service/internal/platform/genproto/karlo/notification/v1"
	"github.com/karlo/notification-service/internal/platform/query"
	"github.com/karlo/notification-service/internal/repository"
	"github.com/karlo/notification-service/internal/templates"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ErrValidation is a rejected request.
var ErrValidation = errors.New("validation failed")

// Dispatcher turns one event into per-recipient records and deliveries.
//
// The order of operations is the important part. The in-app record is written
// first and is the source of truth; the outbound channels are attempted
// afterwards, and their failures are recorded rather than propagated. In the
// monolith this was inverted: a Mongo post-save hook fired FCM, so a delivery
// failure was invisible and a notification nobody could deliver was
// indistinguishable from one nobody had raised.
type Dispatcher struct {
	notifications *repository.NotificationRepository
	auth          *clients.Auth
	push          channels.Sender
	email         *channels.Email
	whatsapp      channels.Sender
	cfg           *config.Config
}

func NewDispatcher(
	notifications *repository.NotificationRepository,
	auth *clients.Auth,
	push channels.Sender,
	email *channels.Email,
	whatsapp channels.Sender,
	cfg *config.Config,
) *Dispatcher {
	return &Dispatcher{
		notifications: notifications,
		auth:          auth,
		push:          push,
		email:         email,
		whatsapp:      whatsapp,
		cfg:           cfg,
	}
}

// NotifyRequest is one event to deliver.
type NotifyRequest struct {
	Event          notificationv1.EventType
	Audience       *notificationv1.Audience
	SubjectID      string
	SubjectType    string
	Params         map[string]interface{}
	Channels       []models.Channel
	ActorID        string
	IdempotencyKey string
}

// NotifyResult reports what happened.
type NotifyResult struct {
	NotificationID string
	Results        []ChannelResult
	Deduplicated   bool
}

// ChannelResult is one channel's aggregate outcome.
type ChannelResult struct {
	Channel        models.Channel
	Success        bool
	RecipientCount int
	Error          string
}

// Notify delivers an event.
func (d *Dispatcher) Notify(ctx context.Context, req NotifyRequest) (*NotifyResult, error) {
	// A repeat within the dedupe window returns the original rather than
	// notifying twice. Business services retry freely, so this matters.
	if req.IdempotencyKey != "" {
		existing, err := d.notifications.FindByIdempotencyKey(ctx, req.IdempotencyKey)
		if err != nil {
			slog.Warn("idempotency lookup failed, proceeding", "error", err)
		} else if len(existing) > 0 {
			return &NotifyResult{
				NotificationID: existing[0].ID.Hex(),
				Deduplicated:   true,
			}, nil
		}
	}

	targets, err := d.resolveAudience(ctx, req.Audience)
	if err != nil {
		return nil, err
	}

	// Never notify someone of their own action.
	targets = excludeActor(targets, req.ActorID)
	if len(targets) == 0 {
		return &NotifyResult{}, nil
	}

	wanted, err := d.channelsFor(req)
	if err != nil {
		return nil, err
	}

	docs, rendered, err := d.buildRecords(req, targets)
	if err != nil {
		return nil, err
	}

	// The durable record comes first.
	if _, err := d.notifications.InsertMany(ctx, docs); err != nil && !errors.Is(err, repository.ErrDuplicate) {
		return nil, fmt.Errorf("dispatcher: store notifications: %w", err)
	}

	results := d.deliver(ctx, wanted, targets, rendered, docs)

	out := &NotifyResult{Results: results}
	if len(docs) > 0 {
		out.NotificationID = docs[0].ID.Hex()
	}
	return out, nil
}

// resolveAudience expands an audience into concrete recipients.
func (d *Dispatcher) resolveAudience(ctx context.Context, audience *notificationv1.Audience) ([]clients.Target, error) {
	if audience == nil {
		return nil, fmt.Errorf("%w: an audience is required", ErrValidation)
	}

	switch target := audience.GetTarget().(type) {
	case *notificationv1.Audience_Users:
		return d.auth.ResolveUsers(ctx, target.Users.GetUserIds())

	case *notificationv1.Audience_CompanyRole:
		return d.auth.ResolveCompanyRoles(ctx,
			target.CompanyRole.GetCompanyId(),
			target.CompanyRole.GetRoles(),
		)

	case *notificationv1.Audience_Phone:
		// A bare phone number has no user behind it. This is the OTP and
		// pre-registration path: reachable by WhatsApp only, with no inbox.
		phone := target.Phone.GetNumber()
		if phone == "" {
			return nil, fmt.Errorf("%w: phone number is empty", ErrValidation)
		}
		return []clients.Target{{Phone: phone, Language: "id"}}, nil

	case *notificationv1.Audience_TruckGroup:
		// Truck groups are master data, so expanding one needs a lookup this
		// service does not have. Rejecting is better than silently delivering
		// to nobody, which is what an empty return would do.
		return nil, fmt.Errorf("%w: truck group audiences are not yet supported; "+
			"resolve the group to user ids in the calling service", ErrValidation)

	default:
		return nil, fmt.Errorf("%w: unrecognised audience", ErrValidation)
	}
}

// channelsFor decides which channels to use: the caller's explicit set, or the
// template's default.
func (d *Dispatcher) channelsFor(req NotifyRequest) ([]models.Channel, error) {
	if len(req.Channels) > 0 {
		return req.Channels, nil
	}
	defaults, ok := templates.DefaultChannels(req.Event)
	if !ok {
		return nil, fmt.Errorf("%w: no template for event %s", ErrValidation, req.Event)
	}
	return defaults, nil
}

// buildRecords renders the copy per recipient and builds their inbox documents.
//
// Copy is rendered per recipient because language is per user: two people on
// the same order can receive the same event in different languages.
func (d *Dispatcher) buildRecords(req NotifyRequest, targets []clients.Target) ([]models.Notification, map[string]*templates.Rendered, error) {
	now := time.Now().UTC()

	docs := make([]models.Notification, 0, len(targets))
	rendered := make(map[string]*templates.Rendered, len(targets))

	for _, t := range targets {
		copy, err := templates.Render(req.Event, t.Language, req.Params)
		if err != nil {
			// A template error is a programming error, not a delivery problem.
			// Failing loudly here beats sending copy with holes in it.
			return nil, nil, fmt.Errorf("%w: %v", ErrValidation, err)
		}
		rendered[t.UserID] = copy

		// A phone-only recipient has no inbox to write to.
		if t.UserID == "" {
			continue
		}

		doc := models.Notification{
			ID:          primitive.NewObjectID(),
			UserID:      t.UserID,
			Event:       req.Event.String(),
			Title:       copy.Title,
			Body:        copy.Body,
			SubjectID:   req.SubjectID,
			SubjectType: req.SubjectType,
			ActorID:     req.ActorID,
			Params:      req.Params,
			CreatedAt:   now,
		}
		// The key is per recipient, so that the unique index deduplicates each
		// person's copy independently.
		if req.IdempotencyKey != "" {
			doc.IdempotencyKey = req.IdempotencyKey + "|" + t.UserID
		}
		docs = append(docs, doc)
	}

	return docs, rendered, nil
}

// deliver attempts each channel for each recipient, bounded by a worker pool.
func (d *Dispatcher) deliver(
	ctx context.Context,
	wanted []models.Channel,
	targets []clients.Target,
	rendered map[string]*templates.Rendered,
	docs []models.Notification,
) []ChannelResult {
	docByUser := make(map[string]primitive.ObjectID, len(docs))
	for _, doc := range docs {
		docByUser[doc.UserID] = doc.ID
	}

	var (
		mu      sync.Mutex
		results = map[models.Channel]*ChannelResult{}
		wg      sync.WaitGroup
		// A semaphore bounds concurrency: a fan-out to two hundred people must
		// not open two hundred simultaneous outbound connections.
		sem = make(chan struct{}, max(1, d.cfg.Workers))
	)

	record := func(ch models.Channel, err error) {
		mu.Lock()
		defer mu.Unlock()

		r, ok := results[ch]
		if !ok {
			r = &ChannelResult{Channel: ch, Success: true}
			results[ch] = r
		}
		if err != nil {
			r.Success = false
			if r.Error == "" {
				r.Error = err.Error()
			}
			return
		}
		r.RecipientCount++
	}

	for _, ch := range wanted {
		// The in-app channel is already satisfied by the stored document.
		if ch == models.ChannelInApp {
			record(ch, nil)
			mu.Lock()
			results[ch].RecipientCount = len(docs)
			mu.Unlock()
			continue
		}

		sender := d.senderFor(ch)
		if sender == nil || !sender.Enabled() {
			record(ch, channels.ErrChannelDisabled)
			continue
		}

		for _, t := range targets {
			copy := rendered[t.UserID]
			if copy == nil {
				continue
			}

			for _, msg := range messagesFor(ch, t, copy) {
				wg.Add(1)
				go func(ch models.Channel, msg channels.Message, userID string) {
					defer wg.Done()

					sem <- struct{}{}
					defer func() { <-sem }()

					// Detached from the caller's context: their HTTP response
					// may already have been written.
					sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
					defer cancel()

					err := sender.Send(sendCtx, msg)
					record(ch, err)

					if id, ok := docByUser[userID]; ok {
						attempt := models.DeliveryAttempt{
							Channel:   ch,
							Success:   err == nil,
							Target:    msg.Target,
							AttemptAt: time.Now().UTC(),
						}
						if err != nil {
							attempt.Error = err.Error()
						}
						if rerr := d.notifications.RecordDelivery(sendCtx, id, attempt); rerr != nil {
							slog.Warn("could not record delivery attempt", "error", rerr)
						}
					}

					if err != nil {
						level := slog.LevelWarn
						if channels.IsInvalidTarget(err) {
							// A dead token is expected churn, not an incident.
							level = slog.LevelInfo
						}
						slog.Log(sendCtx, level, "delivery failed",
							"channel", string(ch), "user_id", userID, "error", err)
					}
				}(ch, msg, t.UserID)
			}
		}
	}

	wg.Wait()

	out := make([]ChannelResult, 0, len(results))
	for _, r := range results {
		out = append(out, *r)
	}
	return out
}

// messagesFor builds the per-channel messages for one recipient. Push produces
// one message per registered device; the others produce at most one.
func messagesFor(ch models.Channel, t clients.Target, copy *templates.Rendered) []channels.Message {
	base := channels.Message{
		Title:    copy.Title,
		Body:     copy.Body,
		Data:     copy.Data,
		Language: t.Language,
	}

	switch ch {
	case models.ChannelPush:
		out := make([]channels.Message, 0, len(t.PushTokens))
		for _, token := range t.PushTokens {
			msg := base
			msg.Target = token
			out = append(out, msg)
		}
		return out

	case models.ChannelEmail:
		if t.Email == "" {
			return nil
		}
		msg := base
		msg.Target = t.Email
		return []channels.Message{msg}

	case models.ChannelWhatsApp:
		if t.Phone == "" || copy.WhatsAppTemplate == "" {
			return nil
		}
		msg := base
		msg.Target = t.Phone
		msg.Template = copy.WhatsAppTemplate
		return []channels.Message{msg}

	default:
		return nil
	}
}

func (d *Dispatcher) senderFor(ch models.Channel) channels.Sender {
	switch ch {
	case models.ChannelPush:
		return d.push
	case models.ChannelEmail:
		return d.email
	case models.ChannelWhatsApp:
		return d.whatsapp
	default:
		return nil
	}
}

// excludeActor drops the person who caused the event from the audience.
func excludeActor(targets []clients.Target, actorID string) []clients.Target {
	if actorID == "" {
		return targets
	}
	out := make([]clients.Target, 0, len(targets))
	for _, t := range targets {
		if t.UserID == actorID {
			continue
		}
		out = append(out, t)
	}
	return out
}

// ListForUser pages a user's inbox.
func (d *Dispatcher) ListForUser(ctx context.Context, userID string, unreadOnly bool, p query.Params) ([]models.Notification, int64, error) {
	return d.notifications.ListForUser(ctx, userID, unreadOnly, p)
}

// CountUnread backs the badge.
func (d *Dispatcher) CountUnread(ctx context.Context, userID string) (int64, error) {
	return d.notifications.CountUnread(ctx, userID)
}

// MarkRead marks notifications read.
func (d *Dispatcher) MarkRead(ctx context.Context, userID string, ids []primitive.ObjectID, all bool) (int64, error) {
	return d.notifications.MarkRead(ctx, userID, ids, all)
}
