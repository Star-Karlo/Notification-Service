// Package archive is the notification service's cold storage.
//
// Every event fans out to every recipient as a notification row, and every
// WhatsApp exchange is a message; both grow with the business and are
// read for days. Past the window they go to S3 as Parquet, one file per
// collection per day, and are deleted here. A conversation is evidence in
// a dispute, so it is kept — cold, queryable by date, never in the hot
// database's way.
//
// Mongo has no schema, so the file's columns are the union of the batch's
// top-level fields, typed from the values seen: ObjectIDs and times as
// strings, nested documents as their JSON. Same conventions as the other
// services' files (see coldstore).
package archive

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/karlo/notification-service/internal/config"
	"github.com/karlo/notification-service/internal/platform/coldstore"
)

type Options struct {
	// Retain is how long rows stay hot after they were created (or, for a
	// ticket, closed).
	Retain time.Duration
	// Batch bounds one collection's rows per run.
	Batch  int
	DryRun bool
	Prefix string
}

type Archiver struct {
	db    *mongo.Database
	store coldstore.Store
	opts  Options
	now   func() time.Time
}

func New(db *mongo.Database, store coldstore.Store, opts Options) *Archiver {
	if opts.Retain <= 0 {
		opts.Retain = 30 * 24 * time.Hour
	}
	if opts.Batch <= 0 {
		opts.Batch = 100000
	}
	if opts.Prefix == "" {
		opts.Prefix = "archive/notification"
	}
	return &Archiver{db: db, store: store, opts: opts, now: time.Now}
}

type Summary struct {
	Rows   map[string]int `json:"rows"`
	Files  int            `json:"files"`
	Bytes  int            `json:"bytes"`
	Failed int            `json:"failed"`
}

// target is one collection and how its cold rows are found.
type target struct {
	collection string
	// timeField partitions the files and, with filter, selects the rows.
	timeField string
	// filter narrows beyond age: a ticket must be closed, for instance.
	filter bson.M
}

func (a *Archiver) targets() []target {
	return []target{
		{collection: config.Collection("notifications"), timeField: "createdAt"},
		{collection: config.Collection("messages"), timeField: "sentAt"},
		{collection: config.Collection("inbound_messages"), timeField: "receivedAt"},
		// Tickets leave once closed and cold; their events go with them,
		// selected by ticket rather than by their own age so a ticket's
		// history is never split across "archived" and "hot".
		{collection: config.Collection("tickets"), timeField: "closedAt", filter: bson.M{"status": bson.M{"$in": bson.A{"closed", "resolved"}}}},
	}
}

// Run archives every target, one collection at a time.
func (a *Archiver) Run(ctx context.Context) (Summary, error) {
	sum := Summary{Rows: map[string]int{}}
	run := coldstore.RunID(a.now())
	cutoff := a.now().Add(-a.opts.Retain)

	for _, t := range a.targets() {
		filter := bson.M{t.timeField: bson.M{"$lt": cutoff}}
		for k, v := range t.filter {
			filter[k] = v
		}
		docs, err := a.read(ctx, t.collection, filter, t.timeField)
		if err != nil {
			return sum, fmt.Errorf("archive: reading %s: %w", t.collection, err)
		}
		slog.InfoContext(ctx, "rows past retention", "collection", t.collection, "count", len(docs), "cutoff", cutoff, "dryRun", a.opts.DryRun)
		if len(docs) == 0 {
			continue
		}
		n, err := a.archiveDocs(ctx, t.collection, t.timeField, docs, run, &sum)
		if err != nil {
			sum.Failed++
			slog.ErrorContext(ctx, "archive failed", "collection", t.collection, "error", err)
			continue
		}
		sum.Rows[t.collection] += n

		// A closed ticket's events travel with it.
		if t.collection == config.Collection("tickets") {
			ids := make(bson.A, 0, len(docs))
			for _, d := range docs {
				ids = append(ids, d["_id"])
			}
			eventsColl := config.Collection("ticket_events")
			events, err := a.read(ctx, eventsColl, bson.M{"ticketId": bson.M{"$in": ids}}, "createdAt")
			if err != nil {
				sum.Failed++
				slog.ErrorContext(ctx, "archive failed", "collection", eventsColl, "error", err)
				continue
			}
			if len(events) > 0 {
				n, err := a.archiveDocs(ctx, eventsColl, "createdAt", events, run, &sum)
				if err != nil {
					sum.Failed++
					slog.ErrorContext(ctx, "archive failed", "collection", eventsColl, "error", err)
					continue
				}
				sum.Rows[eventsColl] += n
			}
		}
	}
	return sum, nil
}

func (a *Archiver) read(ctx context.Context, collection string, filter bson.M, sortBy string) ([]bson.M, error) {
	cur, err := a.db.Collection(collection).Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: sortBy, Value: 1}}).SetLimit(int64(a.opts.Batch)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = cur.Close(ctx) }()
	var docs []bson.M
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

// archiveDocs writes one file per calendar day of timeField, deleting each
// day's documents by id once its file is in S3.
func (a *Archiver) archiveDocs(ctx context.Context, collection, timeField string, docs []bson.M, run string, sum *Summary) (int, error) {
	rows := make([]map[string]any, 0, len(docs))
	kinds := map[string]coldstore.Kind{}
	for _, d := range docs {
		row := flatten(d)
		rows = append(rows, row)
		for k, v := range row {
			if v == nil {
				if _, seen := kinds[k]; !seen {
					kinds[k] = coldstore.String
				}
				continue
			}
			kind := coldstore.KindOfValue(v)
			if prev, seen := kinds[k]; seen && prev != kind {
				kind = coldstore.String // mixed types: text is the safe superset
			}
			kinds[k] = kind
		}
	}
	schema := coldstore.NewSchema(collection, kinds)

	// Group by day. Rows arrive sorted by timeField.
	dayOf := func(row map[string]any) time.Time {
		if s, ok := row[timeField].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				return t.UTC().Truncate(24 * time.Hour)
			}
		}
		return time.Time{}
	}
	done := 0
	for start := 0; start < len(rows); {
		day := dayOf(rows[start])
		end := start
		for end < len(rows) && dayOf(rows[end]).Equal(day) {
			end++
		}
		chunk := rows[start:end]
		data, err := schema.Encode(chunk)
		if err != nil {
			return done, fmt.Errorf("encoding %s: %w", collection, err)
		}
		key := coldstore.Key(a.opts.Prefix, collection, day, run)
		sum.Files++
		sum.Bytes += len(data)
		if a.opts.DryRun {
			slog.InfoContext(ctx, "would write", "key", key, "rows", len(chunk), "bytes", len(data))
		} else {
			if _, err := a.store.PutCold(ctx, key, data); err != nil {
				return done, err
			}
			ids := make(bson.A, 0, len(chunk))
			for _, r := range chunk {
				if oid, err := primitive.ObjectIDFromHex(fmt.Sprint(r["_id"])); err == nil {
					ids = append(ids, oid)
				}
			}
			if _, err := a.db.Collection(collection).DeleteMany(ctx, bson.M{"_id": bson.M{"$in": ids}}); err != nil {
				return done, fmt.Errorf("deleting archived %s: %w", collection, err)
			}
			slog.InfoContext(ctx, "archive file written", "key", key, "rows", len(chunk), "bytes", len(data))
		}
		done += len(chunk)
		start = end
	}
	return done, nil
}

// flatten turns a document into a row: top-level fields as columns, with
// ObjectIDs and times as strings and anything nested as JSON text (which
// coldstore.Coerce does for maps and slices).
func flatten(d bson.M) map[string]any {
	row := make(map[string]any, len(d))
	for k, v := range d {
		switch x := v.(type) {
		case primitive.ObjectID:
			row[k] = x.Hex()
		case primitive.DateTime:
			row[k] = x.Time().UTC().Format(time.RFC3339Nano)
		case time.Time:
			row[k] = x.UTC().Format(time.RFC3339Nano)
		case primitive.A:
			row[k] = []any(x)
		case primitive.M:
			row[k] = map[string]any(x)
		case primitive.D:
			m := make(map[string]any, len(x))
			for _, e := range x {
				m[e.Key] = e.Value
			}
			row[k] = m
		default:
			row[k] = v
		}
	}
	return row
}
