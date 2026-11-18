package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestMITMShareClient(t *testing.T) {
	var calls []string
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			_, _ = w.Write([]byte(`{"active":true,"addresses":["192.168.1.5","10.0.0.2"],"address":"192.168.1.5","url":"http://192.168.1.5:9000/tok/","fingerprint":"ab","expiresAt":"2026-11-04T10:00:00Z","qr":["101","010","101"],"downloads":3}`))
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"active":false,"addresses":["192.168.1.5"]}`))
		default:
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"没有根证书"}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()

	got, err := c.MITMShareStart(ctx, "192.168.1.5")
	if err != nil || !got.Active || got.URL != "http://192.168.1.5:9000/tok/" || got.Downloads != 3 || !slices.Equal(got.QR, []string{"101", "010", "101"}) {
		t.Fatalf("start %+v, %v", got, err)
	}
	var sent map[string]string
	if json.Unmarshal([]byte(body), &sent) != nil || sent["address"] != "192.168.1.5" {
		t.Errorf("start body %q", body)
	}
	stopped, err := c.MITMShareStop(ctx)
	if err != nil || stopped.Active || len(stopped.Addresses) != 1 {
		t.Fatalf("stop %+v, %v", stopped, err)
	}
	if _, err := c.MITMShare(ctx); err == nil || err.Error() == "" {
		t.Errorf("409 not reported: %v", err)
	}
	want := []string{"POST /api/v1/mitm/share", "DELETE /api/v1/mitm/share", "GET /api/v1/mitm/share"}
	if !slices.Equal(calls, want) {
		t.Errorf("calls %q, want %q", calls, want)
	}
}

func TestMITMExportFormats(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	c := New(srv.URL)
	for _, format := range []string{MITMFormatPEM, MITMFormatCER, MITMFormatCRT, MITMFormatP12, MITMFormatMobileConfig} {
		if _, err := c.MITMExport(context.Background(), format); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"format=pem", "format=cer", "format=crt", "format=p12", "format=mobileconfig"}
	if !slices.Equal(queries, want) {
		t.Errorf("queries %q", queries)
	}
}
