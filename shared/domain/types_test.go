package domain

import "testing"

func TestParseAPIKeyPrefix(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"short", "abc", "abc"},
		{"canonical", "pk_test_1234567890abcdef", "pk_test"},
		{"with_underscore", "pk_live_abc123", "pk_live"},
		{"no_underscore_long", "abcd1234abcd1234", "abcd1234"},
		{"no_underscore_short", "abc", "abc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseAPIKeyPrefix(c.input); got != c.want {
				t.Errorf("ParseAPIKeyPrefix(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

func TestAPIKeyVerifierKeyHashAndVerify(t *testing.T) {
	v := NewAPIKeyVerifier("test-secret")

	raw := "pk_live_1234567890abcdef1234567890abcdef"
	hash := v.KeyHash(raw)

	if hash == "" {
		t.Fatal("expected non-empty verifier")
	}
	if !v.Verify(raw, hash) {
		t.Fatal("expected verifier to match")
	}
	if v.Verify("wrong-key", hash) {
		t.Fatal("expected verifier to reject wrong key")
	}
}

func TestAPIKeyVerifierRejectsWrongSecret(t *testing.T) {
	a := NewAPIKeyVerifier("secret-a")
	b := NewAPIKeyVerifier("secret-b")

	raw := "pk_live_shared_secret"
	hash := a.KeyHash(raw)

	if b.Verify(raw, hash) {
		t.Fatal("expected different secrets to produce non-matching verifiers")
	}
}
