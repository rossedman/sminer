package redact

import (
	"strings"
	"testing"
)

// secrets are shapes the built-in patterns must always mask.
var secrets = []string{
	"sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123",
	"sk-abcdefghijklmnopqrstuvwxyz0123456789",
	"ghp_abcdefghijklmnopqrstuvwxyz0123456789AB",
	"AKIAABCDEFGHIJKLMNOP",
	"xoxb-123456789012-abcdefghij",
	"glpat-abcdefghijklmnopqrstuvwx",
	"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
}

const realistic = `export ANTHROPIC_API_KEY=sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123 && curl -H "Authorization: Bearer ghp_abcdefghijklmnopqrstuvwxyz0123456789AB" https://api.github.com/user
password: correct-horse-battery
git push https://oauth2:glpat-abcdefghijklmnopqrstuvwx@gitlab.com/x/y.git
`

func BenchmarkRedactorString(b *testing.B) {
	r, err := New([]string{`internal-[a-z]+-id`})
	if err != nil {
		b.Fatal(err)
	}
	text := strings.Repeat(realistic+"go test ./... && go vet ./...\nplain prose with no secrets in it at all\n", 8)
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		if out := r.String(text); strings.Contains(out, "ghp_") {
			b.Fatal("secret leaked")
		}
	}
}

func BenchmarkRedactorStringClean(b *testing.B) {
	r, err := New(nil)
	if err != nil {
		b.Fatal(err)
	}
	text := strings.Repeat("Add a handler for GET /widgets with tests; run go test ./... afterwards.\n", 32)
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		if r.String(text) != text {
			b.Fatal("clean text changed")
		}
	}
}

func FuzzRedactorString(f *testing.F) {
	for i, s := range secrets {
		f.Add("token for deploy: ", s, " done", uint8(i))
	}
	f.Add("", realistic, "", uint8(0))
	f.Add("password = ", "hunter2hunter2", "\n", uint8(0))
	f.Add("-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n", secrets[0], "\n-----END RSA PRIVATE KEY-----", uint8(1))
	r, err := New([]string{`internal-[a-z]+-id`})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, prefix, body, suffix string, pick uint8) {
		in := prefix + body + suffix
		once := r.String(in)
		if twice := r.String(once); twice != once {
			t.Fatalf("not idempotent:\n in:    %q\n once:  %q\n twice: %q", in, once, twice)
		}
		if nilOnce := (*Redactor)(nil).String(in); (*Redactor)(nil).String(nilOnce) != nilOnce {
			t.Fatalf("nil redactor not idempotent for %q", in)
		}
		// A secret set off by whitespace never survives, whatever surrounds it.
		secret := secrets[int(pick)%len(secrets)]
		withSecret := prefix + " " + secret + " " + suffix
		if out := r.String(withSecret); strings.Contains(out, secret) {
			t.Fatalf("secret survived: %q -> %q", withSecret, out)
		}
	})
}
