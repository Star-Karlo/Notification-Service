// Package repository is the notification service's data layer.
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/karlo/notification-service/internal/models"
	"github.com/karlo/notification-service/internal/platform/query"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var (
	ErrNotFound = errors.New("repository: not found")
	// ErrDuplicate signals that an idempotency key has already been used.
	ErrDuplicate = errors.New("repository: duplicate")
)

// NotificationRepository stores the durable in-app records.
type NotificationRepository struct {
	col *mongo.Collection
}

func NewNotificationRepository(db *mongo.Database) *NotificationRepository {
	return &NotificationRepository{col: db.Collection(models.Notification{}.CollectionName())}
}

var notificationFields = query.FieldSet{
	"event":       "event",
	"read":        "read",
	"subjectType": "subjectType",
	"createdAt":   "createdAt",
}

func NotificationFields() query.FieldSet { return notificationFields }

// InsertMany stores one document per recipient.
//
// Ordered inserts are disabled so that a duplicate for one recipient does not
// abandon the rest of the batch: a fan-out to forty people should not fail
// because one of them already had this notification.
func (r *NotificationRepository) InsertMany(ctx context.Context, docs []models.Notification) (int, error) {
	if len(docs) == 0 {
		return 0, nil
	}

	items := make([]interface{}, 0, len(docs))
	for i := range docs {
		items = append(items, docs[i])
	}

	res, err := r.col.InsertMany(ctx, items, options.InsertMany().SetOrdered(false))
	inserted := 0
	if res != nil {
		inserted = len(res.InsertedIDs)
	}
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return inserted, ErrDuplicate
		}
		return inserted, fmt.Errorf("repository: insert notifications: %w", err)
	}
	return inserted, nil
}

// FindByIdempotencyKey returns the notifications a previous call created, so a
// retry can report the original result rather than delivering twice.
func (r *NotificationRepository) FindByIdempotencyKey(ctx context.Context, key string) ([]models.Notification, error) {
	if key == "" {
		return nil, nil
	}

	cur, err := r.col.Find(ctx, bson.M{"idempotencyKey": key})
	if err != nil {
		return nil, fmt.Errorf("repository: find by idempotency key: %w", err)
	}
	// A cursor close failure cannot be acted on here: the rows have
	// already been read, and the connection is returned to the pool
	// either way.
	defer func() { _ = cur.Close(ctx) }()

	var out []models.Notification
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("repository: decode notifications: %w", err)
	}
	return out, nil
}

// ListForUser pages one user's inbox.
func (r *NotificationRepository) ListForUser(ctx context.Context, userID string, unreadOnly bool, p query.Params) ([]models.Notification, int64, error) {
	filter := bson.M{"userId": userID}
	if unreadOnly {
		filter["read"] = false
	}

	total, err := r.col.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("repository: count notifications: %w", err)
	}

	opts := options.Find().
		SetSkip(int64(p.Offset())).
		SetLimit(int64(p.PageSize)).
		SetSort(bson.D{{Key: "createdAt", Value: -1}})

	cur, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, fmt.Errorf("repository: list notifications: %w", err)
	}
	// A cursor close failure cannot be acted on here: the rows have
	// already been read, and the connection is returned to the pool
	// either way.
	defer func() { _ = cur.Close(ctx) }()

	var out []models.Notification
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, fmt.Errorf("repository: decode notifications: %w", err)
	}
	return out, total, nil
}

// CountUnread backs the badge.
func (r *NotificationRepository) CountUnread(ctx context.Context, userID string) (int64, error) {
	count, err := r.col.CountDocuments(ctx, bson.M{"userId": userID, "read": false})
	if err != nil {
		return 0, fmt.Errorf("repository: count unread: %w", err)
	}
	return count, nil
}

// MarkRead marks specific notifications, or the whole inbox, as read.
//
// The user id is always part of the filter, so a caller cannot mark someone
// else's notifications read by supplying their ids.
func (r *NotificationRepository) MarkRead(ctx context.Context, userID string, ids []primitive.ObjectID, all bool) (int64, error) {
	filter := bson.M{"userId": userID, "read": false}
	if !all {
		if len(ids) == 0 {
			return 0, nil
		}
		filter["_id"] = bson.M{"$in": ids}
	}

	res, err := r.col.UpdateMany(ctx, filter, bson.M{
		"$set": bson.M{"read": true, "readAt": time.Now().UTC()},
	})
	if err != nil {
		return 0, fmt.Errorf("repository: mark read: %w", err)
	}
	return res.ModifiedCount, nil
}

// RecordDelivery appends a channel outcome to a notification.
func (r *NotificationRepository) RecordDelivery(ctx context.Context, id primitive.ObjectID, attempt models.DeliveryAttempt) error {
	_, err := r.col.UpdateOne(ctx,
		bson.M{"_id": id},
		bson.M{"$push": bson.M{"deliveries": attempt}},
	)
	if err != nil {
		return fmt.Errorf("repository: record delivery: %w", err)
	}
	return nil
}

// OTPRepository stores one-time passwords.
type OTPRepository struct {
	col *mongo.Collection
}

func NewOTPRepository(db *mongo.Database) *OTPRepository {
	return &OTPRepository{col: db.Collection(models.OTP{}.CollectionName())}
}

func (r *OTPRepository) Create(ctx context.Context, otp *models.OTP) error {
	res, err := r.col.InsertOne(ctx, otp)
	if err != nil {
		return fmt.Errorf("repository: create otp: %w", err)
	}
	if id, ok := res.InsertedID.(primitive.ObjectID); ok {
		otp.ID = id
	}
	return nil
}

// FindLatest returns the most recent unspent OTP for a number and purpose.
func (r *OTPRepository) FindLatest(ctx context.Context, phone, purpose string) (*models.OTP, error) {
	var otp models.OTP
	err := r.col.FindOne(ctx,
		bson.M{"phoneNumber": phone, "purpose": purpose, "verified": false},
		options.FindOne().SetSort(bson.D{{Key: "createdAt", Value: -1}}),
	).Decode(&otp)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("repository: find otp: %w", err)
	}
	return &otp, nil
}

// LastIssuedAt reports when a code was last sent to a number, for the resend
// cooldown.
func (r *OTPRepository) LastIssuedAt(ctx context.Context, phone, purpose string) (time.Time, error) {
	var otp models.OTP
	err := r.col.FindOne(ctx,
		bson.M{"phoneNumber": phone, "purpose": purpose},
		options.FindOne().SetSort(bson.D{{Key: "createdAt", Value: -1}}),
	).Decode(&otp)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("repository: last otp: %w", err)
	}
	return otp.CreatedAt, nil
}

// IncrementAttempts records a wrong guess and returns the new count.
func (r *OTPRepository) IncrementAttempts(ctx context.Context, id primitive.ObjectID) (int, error) {
	var updated models.OTP
	err := r.col.FindOneAndUpdate(ctx,
		bson.M{"_id": id},
		bson.M{"$inc": bson.M{"attempts": 1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&updated)
	if err != nil {
		return 0, fmt.Errorf("repository: increment otp attempts: %w", err)
	}
	return updated.Attempts, nil
}

// MarkVerified consumes an OTP.
//
// The filter includes verified:false, so two concurrent verifications of the
// same code cannot both succeed.
func (r *OTPRepository) MarkVerified(ctx context.Context, id primitive.ObjectID) error {
	now := time.Now().UTC()
	res, err := r.col.UpdateOne(ctx,
		bson.M{"_id": id, "verified": false},
		bson.M{"$set": bson.M{"verified": true, "verifiedAt": now}},
	)
	if err != nil {
		return fmt.Errorf("repository: mark otp verified: %w", err)
	}
	if res.ModifiedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// InboundRepository stores raw provider webhooks.
type InboundRepository struct {
	col *mongo.Collection
}

func NewInboundRepository(db *mongo.Database) *InboundRepository {
	return &InboundRepository{col: db.Collection(models.InboundMessage{}.CollectionName())}
}

// Store persists a received message.
//
// The provider id is unique, so a webhook redelivery (which Meta does freely)
// does not create a second copy. A duplicate is reported, not an error: the
// correct response to a redelivery is still 200.
func (r *InboundRepository) Store(ctx context.Context, msg *models.InboundMessage) (bool, error) {
	msg.ReceivedAt = time.Now().UTC()

	res, err := r.col.UpdateOne(ctx,
		bson.M{"provider": msg.Provider, "providerId": msg.ProviderID},
		bson.M{"$setOnInsert": msg},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return false, fmt.Errorf("repository: store inbound message: %w", err)
	}
	return res.UpsertedCount > 0, nil
}

// ListUnprocessed returns messages awaiting handling.
func (r *InboundRepository) ListUnprocessed(ctx context.Context, limit int) ([]models.InboundMessage, error) {
	cur, err := r.col.Find(ctx,
		bson.M{"processed": false},
		options.Find().SetSort(bson.D{{Key: "receivedAt", Value: 1}}).SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, fmt.Errorf("repository: list unprocessed: %w", err)
	}
	// A cursor close failure cannot be acted on here: the rows have
	// already been read, and the connection is returned to the pool
	// either way.
	defer func() { _ = cur.Close(ctx) }()

	var out []models.InboundMessage
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("repository: decode inbound messages: %w", err)
	}
	return out, nil
}

// MarkProcessed records the outcome of handling a message.
func (r *InboundRepository) MarkProcessed(ctx context.Context, id primitive.ObjectID, processErr string) error {
	now := time.Now().UTC()
	set := bson.M{"processed": true, "processedAt": now}
	if processErr != "" {
		set["processError"] = processErr
	}

	_, err := r.col.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": set})
	if err != nil {
		return fmt.Errorf("repository: mark processed: %w", err)
	}
	return nil
}
