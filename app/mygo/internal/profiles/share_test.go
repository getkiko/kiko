package profiles

import "testing"

// prefs is a PrefLoader over a map of JSON-free values.
type prefs map[string]string

func (p prefs) Load(key string, v any) bool {
	value, ok := p[key]
	if ok {
		*v.(*string) = value
	}
	return ok
}

func TestShareOriginPreference(t *testing.T) {
	if got := ShareOrigin(nil); got != "" {
		t.Errorf("no prefs: %q", got)
	}
	if got := ShareOrigin(prefs{}); got != "" {
		t.Errorf("unset: %q", got)
	}
	if got := ShareOrigin(prefs{PrefShareOrigin: ""}); got != "" {
		t.Errorf("cleared: %q", got)
	}
	if got := ShareOrigin(prefs{PrefShareOrigin: "https://share.example.com"}); got != "https://share.example.com" {
		t.Errorf("set: %q", got)
	}
}

func TestNormalizeShareOrigin(t *testing.T) {
	good := map[string]string{
		" https://share.example.com/ ":   "https://share.example.com",
		"https://share.example.com:8443": "https://share.example.com:8443",
		"HTTPS://Share.Example.com":      "https://Share.Example.com",
	}
	for in, want := range good {
		if got, err := NormalizeShareOrigin(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "  ", "http://share.example.com", "share.example.com", "https://", "https:///path",
		"ftp://example.com", "https://user:pw@example.com", "https://example.com/?a=1", "https://example.com/?",
		"https://example.com/#top", "https://exa mple.com", "mailto:a@example.com", "https://example.com/kiko"} {
		if err := ValidShareOrigin(in); err == nil {
			t.Errorf("%q taken", in)
		}
	}
	if page, err := ShareUploadPage("https://share.example.com/"); err != nil || page != "https://share.example.com/share-upload" {
		t.Errorf("upload page %q %v", page, err)
	}
	if _, err := ShareUploadPage(""); err == nil {
		t.Error("upload page without a share address")
	}
}
