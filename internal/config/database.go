package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

// ConnectMongo opens the connection and verifies it before returning.
//
// The target is validated first. A refused target produces no connection
// attempt at all, so pointing this service at the legacy production cluster
// fails at startup without touching it. See guard.go.
func ConnectMongo(cfg *Config) (*mongo.Database, error) {
	if err := guardTarget(cfg.MongoURI, cfg.MongoDatabase); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.MongoTimeout)
	defer cancel()

	opts := options.Client().
		ApplyURI(cfg.MongoURI).
		SetConnectTimeout(cfg.MongoTimeout).
		SetServerSelectionTimeout(cfg.MongoTimeout).
		SetMaxPoolSize(50).
		SetMinPoolSize(5).
		SetMaxConnIdleTime(5 * time.Minute)

	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("config: connect mongo: %w", err)
	}

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return nil, fmt.Errorf("config: ping mongo: %w", err)
	}

	slog.Info("mongodb connected",
		"database", cfg.MongoDatabase,
		"collectionPrefix", CollectionPrefix,
	)
	return client.Database(cfg.MongoDatabase), nil
}

// EnsureIndexes creates the indexes the service depends on.
//
// It operates exclusively on prefixed collections, so it cannot alter a legacy
// collection even if it were somehow run against a shared database.
func EnsureIndexes(ctx context.Context, db *mongo.Database) error {
	type indexSpec struct {
		collection string
		model      mongo.IndexModel
	}

	plain := func(keys bson.D) mongo.IndexModel {
		return mongo.IndexModel{Keys: keys}
	}
	// A sparse unique index: it constrains only documents that carry the field,
	// so notifications sent without an idempotency key are unaffected.
	sparseUnique := func(keys bson.D) mongo.IndexModel {
		return mongo.IndexModel{
			Keys:    keys,
			Options: options.Index().SetUnique(true).SetSparse(true),
		}
	}
	// A TTL index expires documents automatically, so spent codes and old
	// webhook payloads do not accumulate forever.
	ttl := func(field string, after time.Duration) mongo.IndexModel {
		return mongo.IndexModel{
			Keys:    bson.D{{Key: field, Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(int32(after.Seconds())),
		}
	}

	specs := []indexSpec{
		// The inbox query: one user's notifications, newest first.
		{Collection("notifications"), plain(bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}})},
		{Collection("notifications"), plain(bson.D{{Key: "userId", Value: 1}, {Key: "read", Value: 1}})},
		// This is what makes retries safe rather than merely unlikely.
		{Collection("notifications"), sparseUnique(bson.D{{Key: "idempotencyKey", Value: 1}})},

		{Collection("otps"), plain(bson.D{{Key: "phoneNumber", Value: 1}, {Key: "purpose", Value: 1}, {Key: "createdAt", Value: -1}})},
		// Codes are useless once expired; a week covers "I never got my
		// code" support queries. Deleted, never archived: an expired OTP
		// has no evidentiary value and its hash is still a secret.
		{Collection("otps"), ttl("expiresAt", 7*24*time.Hour)},

		{Collection("inbound_messages"), sparseUnique(bson.D{{Key: "provider", Value: 1}, {Key: "providerId", Value: 1}})},
		{Collection("inbound_messages"), plain(bson.D{{Key: "processed", Value: 1}, {Key: "receivedAt", Value: 1}})},
		{Collection("inbound_messages"), ttl("receivedAt", 90*24*time.Hour)},

		{Collection("message_groups"), plain(bson.D{{Key: "participantIds", Value: 1}, {Key: "lastMessageAt", Value: -1}})},
		{Collection("message_groups"), plain(bson.D{{Key: "orderId", Value: 1}})},
		{Collection("messages"), plain(bson.D{{Key: "groupId", Value: 1}, {Key: "sentAt", Value: -1}})},

		{Collection("tickets"), plain(bson.D{{Key: "status", Value: 1}, {Key: "createdAt", Value: -1}})},
		{Collection("tickets"), plain(bson.D{{Key: "contactPhone", Value: 1}})},
		{Collection("tickets"), plain(bson.D{{Key: "assignedToUserId", Value: 1}, {Key: "status", Value: 1}})},
		{Collection("ticket_events"), plain(bson.D{{Key: "ticketId", Value: 1}, {Key: "createdAt", Value: 1}})},
	}

	for _, spec := range specs {
		_, err := db.Collection(spec.collection).Indexes().CreateOne(ctx, spec.model)
		if err == nil {
			continue
		}
		// A TTL whose window changed: the index exists with the same keys and
		// a different expireAfterSeconds, which CreateOne refuses (code 85,
		// IndexOptionsConflict). collMod changes the window in place — the
		// only index option Mongo allows to be altered without a rebuild.
		var cmdErr mongo.CommandError
		if errors.As(err, &cmdErr) && cmdErr.Code == 85 && spec.model.Options != nil && spec.model.Options.ExpireAfterSeconds != nil {
			res := db.RunCommand(ctx, bson.D{
				{Key: "collMod", Value: spec.collection},
				{Key: "index", Value: bson.D{
					{Key: "keyPattern", Value: spec.model.Keys},
					{Key: "expireAfterSeconds", Value: *spec.model.Options.ExpireAfterSeconds},
				}},
			})
			if res.Err() == nil {
				slog.Info("ttl index window updated", "collection", spec.collection)
				continue
			}
			err = res.Err()
		}
		return fmt.Errorf("config: create index on %s: %w", spec.collection, err)
	}

	slog.Info("mongodb indexes ensured", "count", len(specs))
	return nil
}
