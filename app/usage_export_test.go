package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	bolt "github.com/sagernet/bbolt"
)

func TestUsageDomainNormalization(t *testing.T) {
	for _, tc := range []struct{ host, normalized, root string }{
		{"WWW.Apple.COM.", "www.apple.com", "apple.com"},
		{"www.apple.com.cn", "www.apple.com.cn", "apple.com.cn"},
		{"video.example.co.uk", "video.example.co.uk", "example.co.uk"},
		{"a.github.io", "a.github.io", "a.github.io"}, {"b.github.io", "b.github.io", "b.github.io"},
		{"www.食狮.com.cn", "www.xn--85x722f.com.cn", "xn--85x722f.com.cn"},
		{"192.0.2.1", "192.0.2.1", "192.0.2.1"}, {"2001:db8::1", "2001:db8::1", "2001:db8::1"},
		{"LOCALHOST", "localhost", "localhost"}, {"com.cn", "com.cn", "com.cn"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			host, root := normalizeUsageDomain(tc.host)
			if host != tc.normalized || root != tc.root {
				t.Fatalf("got %q / %q", host, root)
			}
		})
	}
}

func TestUsageHistoricalDomainGrouping(t *testing.T) {
	u := newUsageStore(t.TempDir())
	now := time.Now().Truncate(time.Minute)
	hosts := []string{"WWW.Apple.COM.", "api.apple.com", "www.apple.com.cn", "store.apple.com.cn", "a.github.io", "b.github.io", "www.食狮.com.cn", "cdn.xn--85x722f.com.cn"}
	for _, host := range hosts {
		u.addLocked(now, map[string]string{"host": host, "rootDomain": "legacy", "source": "device"}, usageCounters{Upload: 3, Download: 7, Connections: 1})
	}
	if err := u.flush(); err != nil {
		t.Fatal(err)
	}
	q := usageQuery{From: now.UnixMilli(), To: now.Add(time.Minute).UnixMilli(), Group: "rootDomain"}
	result, err := u.query(q)
	if err != nil || result.Groups != 5 || result.Totals.Upload != 24 {
		t.Fatalf("%+v %v", result, err)
	}
	q.Filters = map[string][]string{"rootDomain": {"apple.com"}}
	q.Group = "host"
	result, err = u.query(q)
	if err != nil || result.Groups != 2 || result.Totals.Upload != 6 {
		t.Fatalf("filtered: %+v %v", result, err)
	}
	// Query-time normalization leaves legacy keys/values and row counts intact.
	db, err := u.database()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(usageRecordsBucket).ForEach(func(k, v []byte) error {
			var record usageRecord
			if err := json.Unmarshal(v, &record); err != nil {
				return err
			}
			if record.Dimensions["rootDomain"] != "legacy" || string(k) != usageRecordKey(record) {
				t.Fatal("historical storage changed")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readUsageCSV(t *testing.T, body string) [][]string {
	t.Helper()
	if !strings.HasPrefix(body, "\ufeff") || !strings.HasSuffix(body, "\r\n") {
		t.Fatal("CSV encoding or line endings")
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestUsageAllDetailsExport(t *testing.T) {
	s := newGRPCTestServer(t)
	s.usage = newUsageStore(s.dataDir)
	now := time.Now().Truncate(time.Minute)
	const large = uint64(9007199254740993)
	var expected usageCounters
	for i := 0; i < 251; i++ {
		dims := connectionUsageDimensions(usageConnection("a", fmt.Sprintf("192.0.2.%d", i), "WWW.Apple.COM.", 0, 0).Connection)
		dims["process"] = "/Applications/测试,\"表格\".app\n/bin/run"
		dims["rule"] = " \t=SUM(A1)"
		dims["user"] = "+cmd"
		dims["rootDomain"] = "legacy"
		counts := usageCounters{Upload: large + uint64(i), Download: 2, Connections: 1}
		expected.add(counts)
		s.usage.addLocked(now.Add(-time.Duration(i)*time.Minute), dims, counts)
	}
	// Pending records are included, and malformed/empty UI parameters are ignored.
	rec := httptest.NewRecorder()
	s.exportUsage(rec, httptest.NewRequest("GET", "/api/v1/usage/export?scope=all&from=invalid&to=0&limit=1&page=999&search=missing&filter.source=missing", nil))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), "usage-details.csv") {
		t.Fatalf("HTTP %d %s", rec.Code, rec.Body)
	}
	rows := readUsageCSV(t, rec.Body.String())
	if len(rows) != 252 || len(rows[0]) != 19 {
		t.Fatalf("shape %d / %d", len(rows), len(rows[0]))
	}
	wantHeader := append([]string{"minute_utc", "minute_unix_ms"}, usageDimensions...)
	wantHeader = append(wantHeader, "upload_bytes", "download_bytes", "total_bytes", "new_connections")
	if !reflect.DeepEqual(rows[0], wantHeader) {
		t.Fatal(rows[0])
	}
	var totals usageCounters
	var previous int64
	for _, row := range rows[1:] {
		minute, err := strconv.ParseInt(row[1], 10, 64)
		if err != nil || minute < previous || row[0] != time.UnixMilli(minute).UTC().Format(time.RFC3339) {
			t.Fatal("minute order", row[:2])
		}
		previous = minute
		if row[3] != "/Applications/测试,\"表格\".app\n/bin/run" || row[4] != "www.apple.com" || row[5] != "apple.com" || row[8] != "' \t=SUM(A1)" || row[10] != "'+cmd" {
			t.Fatal("dimensions", row)
		}
		upload, _ := strconv.ParseUint(row[15], 10, 64)
		download, _ := strconv.ParseUint(row[16], 10, 64)
		total, _ := strconv.ParseUint(row[17], 10, 64)
		connections, _ := strconv.ParseUint(row[18], 10, 64)
		if upload < large || total != upload+download {
			t.Fatal("integer precision", row)
		}
		totals.add(usageCounters{upload, download, connections})
	}
	if totals != expected || len(s.usage.pending) != 0 {
		t.Fatalf("totals %+v / %+v", totals, expected)
	}
	queried, err := s.usage.query(usageQuery{From: now.Add(-300 * time.Minute).UnixMilli(), To: now.Add(time.Minute).UnixMilli(), Group: "rootDomain"})
	if err != nil || queried.Totals != totals {
		t.Fatalf("query/export sums %+v %v", queried, err)
	}
}

type usageFailedWriter struct{}

func (usageFailedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type usageBufferedFailureWriter struct{ writes int }

func (w *usageBufferedFailureWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		return len(p), nil
	}
	return 0, io.ErrClosedPipe
}

func TestUsageDetailSnapshotFailuresAndEmpty(t *testing.T) {
	u := newUsageStore(t.TempDir())
	snapshot, err := u.detailSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	name := snapshot.Name()
	data, err := io.ReadAll(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if rows := readUsageCSV(t, string(data)); len(rows) != 1 {
		t.Fatal(rows)
	}
	// Snapshot readers hold no store lock; a new query can run immediately.
	done := make(chan error, 1)
	go func() { _, err := u.query(usageQuery{From: 1, To: 60000, Group: "source"}); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot held store lock")
	}
	snapshot.Close()
	os.Remove(name)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := u.detailSnapshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	db, err := u.database()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := writeUsageDetails(context.Background(), usageFailedWriter{}, db); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if err := writeUsageDetails(context.Background(), &usageBufferedFailureWriter{}, db); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if err := writeUsageDetails(ctx, io.Discard, db); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// A failed snapshot returns JSON before any CSV response body is committed.
func TestUsageDetailExportError(t *testing.T) {
	s := newGRPCTestServer(t)
	s.usage = newUsageStore(t.TempDir() + "/missing")
	rec := httptest.NewRecorder()
	s.exportUsage(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usage/export?scope=all", nil))
	if rec.Code != 500 || !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
