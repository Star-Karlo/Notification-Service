//go:build integration

// Package integration exercises the notification service against a real
// MongoDB.
//
//	go test -tags=integration ./tests/integration/... -v
package integration

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/karlo/notification-service/internal/config"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const testDatabaseName = "karlo_notification_test"

func testDB(t *testing.T) *mongo.Database {
	t.Helper()

	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI is not set; skipping integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("could not connect to %s: %v", uri, err)
	}

	t.Cleanup(func() {
		disconnectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Disconnect(disconnectCtx)
	})

	return client.Database(testDatabaseName)
}

// TestEnsureIndexesSucceedsAgainstRealMongo is the regression test for a bug
// that made this service unable to start at all.
//
// Index keys must be an ordered document. The original implementation used
// map[string]interface{}, and a Go map has no defined order, so the driver
// rejected every compound index with "multi-key map passed in for ordered
// parameter keys". EnsureIndexes returned that error, main propagated it, and
// the process exited. No unit test caught it because none of them connected to
// MongoDB.
func TestEnsureIndexesSucceedsAgainstRealMongo(t *testing.T) {
	db := testDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start clean so the indexes are genuinely created, not merely already
	// present from an earlier run.
	for _, name := range []string{
		"notifications", "otps", "inbound_messages",
		"message_groups", "messages", "tickets", "ticket_events",
	} {
		if err := db.Collection(config.Collection(name)).Drop(ctx); err != nil {
			t.Fatalf("could not drop %s: %v", name, err)
		}
	}

	if err := config.EnsureIndexes(ctx, db); err != nil {
		t.Fatalf("EnsureIndexes failed, which would stop the service from starting: %v", err)
	}

	// Running twice must be a no-op; the service calls it on every boot.
	if err := config.EnsureIndexes(ctx, db); err != nil {
		t.Fatalf("EnsureIndexes is not idempotent: %v", err)
	}
}

// TestNotificationIndexesSupportTheQueriesWeRun checks the specific indexes the
// service depends on. A missing one is invisible in a small test dataset and
// crippling in a real inbox.
func TestNotificationIndexesSupportTheQueriesWeRun(t *testing.T) {
	db := testDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := config.EnsureIndexes(ctx, db); err != nil {
		t.Fatalf("EnsureIndexes failed: %v", err)
	}

	t.Run("the idempotency key is uniquely indexed", func(t *testing.T) {
		indexes := listIndexes(t, db, config.Collection("notifications"))

		var found bool
		for name, idx := range indexes {
			if !strings.Contains(name, "idempotencyKey") {
				continue
			}
			unique, _ := idx["unique"].(bool)
			sparse, _ := idx["sparse"].(bool)
			if unique && sparse {
				found = true
			}
		}
		// Unique makes retries safe; sparse means notifications sent without a
		// key are unaffected by the constraint.
		if !found {
			t.Errorf("no sparse unique idempotencyKey index; retries would deliver twice. Got %v", names(indexes))
		}
	})

	t.Run("the inbox query is indexed", func(t *testing.T) {
		indexes := listIndexes(t, db, config.Collection("notifications"))

		var found bool
		for name := range indexes {
			if strings.Contains(name, "userId") && strings.Contains(name, "createdAt") {
				found = true
			}
		}
		if !found {
			t.Errorf("no (userId, createdAt) index; every inbox load would scan. Got %v", names(indexes))
		}
	})

	t.Run("spent codes and old webhooks expire", func(t *testing.T) {
		for _, collection := range []string{"otps", "inbound_messages"} {
			indexes := listIndexes(t, db, config.Collection(collection))

			var found bool
			for _, idx := range indexes {
				if _, ok := idx["expireAfterSeconds"]; ok {
					found = true
				}
			}
			if !found {
				t.Errorf("%s has no TTL index; it would grow without bound", collection)
			}
		}
	})
}

func listIndexes(t *testing.T, db *mongo.Database, collection string) map[string]bson.M {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cur, err := db.Collection(collection).Indexes().List(ctx)
	if err != nil {
		t.Fatalf("could not list indexes on %s: %v", collection, err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := map[string]bson.M{}
	for cur.Next(ctx) {
		var idx bson.M
		if err := cur.Decode(&idx); err != nil {
			t.Fatalf("could not decode an index: %v", err)
		}
		if name, ok := idx["name"].(string); ok {
			out[name] = idx
		}
	}
	return out
}

func names(indexes map[string]bson.M) []string {
	out := make([]string, 0, len(indexes))
	for name := range indexes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
