package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"os"
	"time"
)

// Operators copy objects first, then explicitly verify the destination while
// the install is stopped. Pinning never happens until every retained row has
// matching bytes; failed verification keeps the old backend identity.
func verifyMigratedBackend(app *sdk.AppCtx, be Backend, identity string) error {
	var pending int
	if err := app.AppDB().QueryRow(`SELECT count(*) FROM pending_uploads`).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return errors.New("finish or abort direct uploads before migrating")
	}
	entries, err := os.ReadDir(uploadsDir(app))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, e := range entries {
		if e.IsDir() && validUploadID(e.Name()) {
			return errors.New("finish or abort multipart uploads before migrating")
		}
	}
	var cleanup int
	if err = app.AppDB().QueryRow(`SELECT count(*) FROM blob_cleanup`).Scan(&cleanup); err != nil {
		return err
	}
	if cleanup > 0 {
		return errors.New("drain pending blob cleanup on the old backend before migrating")
	}
	rows, err := app.AppDB().Query(`SELECT storage_key,sha256,size_bytes FROM files`)
	if err != nil {
		return err
	}
	type object struct {
		key, sha string
		size     int64
	}
	var objects []object
	for rows.Next() {
		var o object
		if err = rows.Scan(&o.key, &o.sha, &o.size); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, o := range objects {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		r, e := be.OpenObject(c, objectKey(o.sha, o.key), ObjectReadOptions{})
		if e != nil {
			cancel()
			return fmt.Errorf("migration: missing object %s: %w", o.key, e)
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(r.Body, o.size+1))
		r.Body.Close()
		cancel()
		if e != nil || n != o.size || hex.EncodeToString(h.Sum(nil)) != o.sha {
			return fmt.Errorf("migration: object %s failed size/checksum verification", o.key)
		}
	}
	_, err = app.AppDB().Exec(`INSERT INTO storage_state(key,value) VALUES('backend',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, identity)
	return err
}
