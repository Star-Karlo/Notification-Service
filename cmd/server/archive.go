package main

import (
	"context"
	"errors"
	"log/slog"
	"os/signal"
	"syscall"

	"go.mongodb.org/mongo-driver/mongo"

	"github.com/karlo/notification-service/internal/archive"
	"github.com/karlo/notification-service/internal/config"
	"github.com/karlo/notification-service/internal/platform/coldstore"
)

func runArchive(cfg *config.Config, db *mongo.Database, dryRun bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := coldstore.NewS3(ctx, cfg.ArchiveBucket, cfg.ArchiveRegion)
	if err != nil {
		return err
	}
	if !store.Configured() {
		return errors.New("archive: ARCHIVE_BUCKET is not set; nowhere to write")
	}

	sum, err := archive.New(db, store, archive.Options{
		Retain: cfg.ArchiveRetain,
		Batch:  cfg.ArchiveBatch,
		DryRun: dryRun,
		Prefix: cfg.ArchivePrefix,
	}).Run(ctx)
	slog.Info("archive run finished", "rows", sum.Rows, "files", sum.Files, "bytes", sum.Bytes, "failed", sum.Failed, "dryRun", dryRun)
	if err != nil {
		return err
	}
	if sum.Failed > 0 {
		return errors.New("archive: some collections failed; see log")
	}
	return nil
}
