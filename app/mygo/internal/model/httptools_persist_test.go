package model

import (
	"reflect"
	"testing"
)

func TestMITMPersistLimits(t *testing.T) {
	var none *MITMPersist
	got := none.Limits()
	if got.MaxMB != 256 || got.MaxRecords != 20000 || got.RetentionDays != 7 || len(got.RedactHeaders) != 0 {
		t.Fatalf("defaults %+v", got)
	}
	got = (&MITMPersist{MaxMB: -5, MaxRecords: -1, RetentionDays: -1}).Limits()
	if got.MaxMB != 256 || got.MaxRecords != 20000 || got.RetentionDays != 7 {
		t.Fatalf("negative values must fall back: %+v", got)
	}
	got = (&MITMPersist{MaxMB: 1 << 30, MaxRecords: 1 << 30, RetentionDays: 1 << 30}).Limits()
	if got.MaxMB != MITMPersistMaxMB || got.MaxRecords != MITMPersistMaxRecords || got.RetentionDays != MITMPersistMaxRetentionDays {
		t.Fatalf("clamp %+v", got)
	}
	got = (&MITMPersist{RedactHeaders: []string{" X-A ", "x-a", "a b", "c:d", "", "X-B"}}).Limits()
	if !reflect.DeepEqual(got.RedactHeaders, []string{"X-A", "X-B"}) || !reflect.DeepEqual(got.Invalid, []string{"a b", "c:d"}) {
		t.Fatalf("headers %+v", got)
	}
	var many []string
	for i := 0; i < MITMPersistMaxRedactHeaders+3; i++ {
		many = append(many, "X-H"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	got = (&MITMPersist{RedactHeaders: many}).Limits()
	if len(got.RedactHeaders) != MITMPersistMaxRedactHeaders || len(got.Invalid) != 3 {
		t.Fatalf("limit %d %d", len(got.RedactHeaders), len(got.Invalid))
	}
}
