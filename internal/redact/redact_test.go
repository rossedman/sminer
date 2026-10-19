package redact

import (
	"strings"
	"testing"
)

func TestBuiltins(t *testing.T) {
	r, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{
		"AKIAABCDEFGHIJKLMNOP",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789AB",
		"github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUV",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
		"xoxb-123456789012-abcdefghij",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY-----",
	}
	for _, s := range secrets {
		got := r.String("value: " + s + " end")
		if strings.Contains(got, s) {
			t.Errorf("not redacted: %q -> %q", s, got)
		}
	}
	if got := r.String(`password = "hunter2hunter2"`); got != `password = "[REDACTED]"` {
		t.Errorf("kv: %q", got)
	}
	if got := r.String("plain text stays"); got != "plain text stays" {
		t.Errorf("plain: %q", got)
	}
}

func TestConfiguredPatternSwallowsValue(t *testing.T) {
	r, err := New([]string{"(?i)aws_secret"})
	if err != nil {
		t.Fatal(err)
	}
	got := r.String("AWS_SECRET=abc123def456 next")
	if strings.Contains(got, "abc123") || !strings.Contains(got, "next") {
		t.Errorf("got %q", got)
	}
	if _, err := New([]string{"("}); err == nil {
		t.Error("bad pattern accepted")
	}
}
