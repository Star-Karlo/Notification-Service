//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/karlo/notification-service/internal/archive"
	"github.com/karlo/notification-service/internal/config"
)

type memStore struct{ objects map[string][]byte }

func (m *memStore) PutCold(_ context.Context, key string, body []byte) (string, error) {
	m.objects[key] = body
	return "DEEP_ARCHIVE", nil
}

// Old notifications and closed tickets (with their events) leave as Parquet
// files by day and are deleted; recent rows and open tickets stay.
func TestArchiveMovesColdNotificationsAndClosedTickets(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	for _, c := range []string{"notifications", "tickets", "ticket_events", "messages", "inbound_messages"} {
		_ = db.Collection(config.Collection(c)).Drop(ctx)
	}

	now := time.Now().UTC()
	old := now.Add(-45 * 24 * time.Hour)
	notifs := db.Collection(config.Collection("notifications"))
	for _, at := range []time.Time{old, old.Add(2 * time.Hour), old.Add(48 * time.Hour), now.Add(-time.Hour)} {
		if _, err := notifs.InsertOne(ctx, bson.M{"userId": "u1", "title": "x", "read": false, "createdAt": at, "params": bson.M{"orderNumber": "ORD-1"}}); err != nil {
			t.Fatal(err)
		}
	}
	tickets := db.Collection(config.Collection("tickets"))
	closed, err := tickets.InsertOne(ctx, bson.M{"status": "closed", "closedAt": old, "createdAt": old.Add(-time.Hour), "contactPhone": "628"})
	if err != nil {
		t.Fatal(err)
	}
	open, err := tickets.InsertOne(ctx, bson.M{"status": "open", "createdAt": old})
	if err != nil {
		t.Fatal(err)
	}
	events := db.Collection(config.Collection("ticket_events"))
	for _, tid := range []any{closed.InsertedID, closed.InsertedID, open.InsertedID} {
		if _, err := events.InsertOne(ctx, bson.M{"ticketId": tid, "kind": "note", "createdAt": old}); err != nil {
			t.Fatal(err)
		}
	}

	store := &memStore{objects: map[string][]byte{}}
	sum, err := archive.New(db, store, archive.Options{Retain: 30 * 24 * time.Hour, Prefix: "archive/notification"}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Failed != 0 {
		t.Fatalf("summary %+v", sum)
	}
	if sum.Rows[config.Collection("notifications")] != 3 || sum.Rows[config.Collection("tickets")] != 1 || sum.Rows[config.Collection("ticket_events")] != 2 {
		t.Fatalf("rows %v", sum.Rows)
	}
	// notifications: two days → two files; tickets one; events one.
	if len(store.objects) != 4 {
		t.Fatalf("expected 4 files, got %d: %v", len(store.objects), keys(store.objects))
	}
	for k := range store.objects {
		if !strings.HasPrefix(k, "archive/notification/nt_") || !strings.Contains(k, "/dt=") || !strings.HasSuffix(k, ".parquet") {
			t.Fatalf("key layout: %s", k)
		}
	}
	count := func(c string) int64 {
		n, _ := db.Collection(config.Collection(c)).CountDocuments(ctx, bson.M{})
		return n
	}
	if count("notifications") != 1 || count("tickets") != 1 || count("ticket_events") != 1 {
		t.Fatalf("hot rows left: notifications=%d tickets=%d events=%d", count("notifications"), count("tickets"), count("ticket_events"))
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
