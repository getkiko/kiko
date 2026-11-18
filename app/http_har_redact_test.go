package main

import "testing"

func TestHARAndPersistShareSecretRule(t *testing.T) {
	for _, name := range []string{"code", "sig", "Key", "apikey", "jwt", "sid", "my_passphrase", "X-Auth"} {
		if !harSecretParam(name) {
			t.Errorf("%s not masked", name)
		}
	}
	if harSecretParam("page") {
		t.Error("page masked")
	}
	if got := redactURL("https://u:p@h/p?code=1&q=2"); got != "https://[redacted]@h/p?code=%5Bredacted%5D&q=2" {
		t.Errorf("redactURL = %q", got)
	}
}
