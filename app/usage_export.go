package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	bolt "github.com/sagernet/bbolt"
)

func usageCSVCounts(c usageCounters) []string {
	return []string{strconv.FormatUint(c.Upload, 10), strconv.FormatUint(c.Download, 10), strconv.FormatUint(c.Upload+c.Download, 10), strconv.FormatUint(c.Connections, 10)}
}

// The caller holds the store mutex. Iterate the complete retained minute bucket
// in timestamp/key order; query filters and pagination have no role here.
func writeUsageDetails(ctx context.Context, dst io.Writer, db *bolt.DB) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := io.WriteString(dst, "\xef\xbb\xbf"); err != nil {
		return err
	}
	writer := csv.NewWriter(dst)
	writer.UseCRLF = true
	headers := append([]string{"minute_utc", "minute_unix_ms"}, usageDimensions...)
	if err := writer.Write(append(headers, "upload_bytes", "download_bytes", "total_bytes", "new_connections")); err != nil {
		return err
	}
	err := db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(usageRecordsBucket).Cursor()
		for key, data := cursor.First(); key != nil; key, data = cursor.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			var record usageRecord
			if err := json.Unmarshal(data, &record); err != nil {
				return err
			}
			normalizeUsageDimensions(record.Dimensions)
			row := []string{time.UnixMilli(record.Time).UTC().Format(time.RFC3339), strconv.FormatInt(record.Time, 10)}
			for _, dimension := range usageDimensions {
				row = append(row, usageCSVLabel(record.Dimensions[dimension]))
			}
			if err := writer.Write(append(row, usageCSVCounts(record.usageCounters)...)); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	writer.Flush()
	return writer.Error()
}

// Materialize a private snapshot before sending any response bytes. The store
// lock and Bolt transaction are released before a slow client reads the file.
func (u *usageStore) detailSnapshot(ctx context.Context) (snapshot *os.File, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	snapshot, err = os.CreateTemp("", "kiko-usage-*.csv")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = snapshot.Close()
			_ = os.Remove(snapshot.Name())
		}
	}()
	u.mu.Lock()
	defer u.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return snapshot, err
	}
	db, err := u.database()
	if err != nil {
		return snapshot, err
	}
	defer db.Close()
	if err = u.flushLocked(db, time.Now()); err != nil {
		return snapshot, err
	}
	if err = writeUsageDetails(ctx, snapshot, db); err != nil {
		return snapshot, err
	}
	_, err = snapshot.Seek(0, io.SeekStart)
	return snapshot, err
}

func (s *server) exportAllUsage(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.usage.detailSnapshot(r.Context())
	if err != nil {
		if r.Context().Err() == nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
		}
		return
	}
	defer os.Remove(snapshot.Name())
	defer snapshot.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="usage-details.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "usage-details.csv", time.Time{}, snapshot)
}
