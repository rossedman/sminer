// Package redact strips secrets from transcript text before it is stored or
// sent anywhere.
package redact

import (
	"fmt"
	"regexp"
	"strings"
)

const mask = "[REDACTED]"

type pattern struct {
	re *regexp.Regexp
	// hints are lowercase substrings one of which must be present for the
	// pattern to match; checking them first skips most regex work.
	hints []string
}

var builtin = []pattern{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)`), []string{"private key"}},
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), []string{"akia", "asia"}},
	{regexp.MustCompile(`(?i)aws_?secret_?(access_?)?key\s*[:=]\s*['"]?[A-Za-z0-9/+=]{40}`), []string{"aws"}},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`), []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}\b`), []string{"github_pat_"}},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), []string{"eyj"}},
	{regexp.MustCompile(`\bsk-(ant-)?[A-Za-z0-9_-]{20,}`), []string{"sk-"}},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), []string{"xox"}},
	{regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`), []string{"glpat-"}},
}

var kvHints = []string{"password", "passwd", "secret", "token", "api_key", "api-key", "apikey"}

// kv redacts the value of obvious credential assignments but keeps the key
// name, so excerpts stay readable.
var kv = regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key|client[_-]?secret)(\s*[:=]\s*)(['"]?)[^\s'"]{8,}`)

// Redactor applies built-in and configured patterns.
type Redactor struct {
	extra []*regexp.Regexp
}

// New compiles configured patterns. Each pattern also swallows a following
// "= value" or ": value", so a pattern naming a key redacts its value too.
func New(patterns []string) (*Redactor, error) {
	r := &Redactor{}
	for _, p := range patterns {
		re, err := regexp.Compile(`(?:` + p + `)(?:\s*[:=]\s*\S+)?`)
		if err != nil {
			return nil, fmt.Errorf("redact pattern %q: %w", p, err)
		}
		r.extra = append(r.extra, re)
	}
	return r, nil
}

// String returns s with secrets masked.
func (r *Redactor) String(s string) string {
	if s == "" {
		return s
	}
	lower := strings.ToLower(s)
	for _, p := range builtin {
		if hasAny(lower, p.hints) {
			s = p.re.ReplaceAllString(s, mask)
		}
	}
	if hasAny(lower, kvHints) {
		s = kv.ReplaceAllString(s, "${1}${2}${3}"+mask)
	}
	if r != nil {
		for _, re := range r.extra {
			s = re.ReplaceAllString(s, mask)
		}
	}
	return s
}

func hasAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
