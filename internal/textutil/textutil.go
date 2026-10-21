// Package textutil holds small string helpers shared across stages.
package textutil

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Truncate cuts s to at most n bytes on a rune boundary, adding an ellipsis.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	cut := n - 3
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// OneLine collapses whitespace runs to single spaces.
func OneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// FirstLine returns the first non-empty line of s.
func FirstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// Hash returns a short stable hex digest of the parts.
func Hash(n int, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	s := hex.EncodeToString(h.Sum(nil))
	if n > 0 && n < len(s) {
		return s[:n]
	}
	return s
}

var stop = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "to": true, "of": true,
	"in": true, "on": true, "for": true, "is": true, "it": true, "this": true, "that": true,
	"be": true, "with": true, "as": true, "at": true, "by": true, "we": true, "you": true,
	"i": true, "me": true, "my": true, "our": true, "are": true, "was": true, "so": true,
	"can": true, "please": true, "just": true, "then": true, "now": true, "if": true,
	"do": true, "but": true, "all": true, "from": true, "will": true, "should": true,
	"would": true, "could": true, "have": true, "has": true, "there": true, "here": true,
	"what": true, "when": true, "which": true, "your": true, "its": true, "into": true,
}

// Tokens lowercases s and splits it into content words, dropping stopwords.
// Contractions like "don't" become "dont" so negation survives.
func Tokens(s string) []string {
	s = strings.ToLower(strings.ReplaceAll(s, "'", ""))
	f := strings.FieldsFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '/' || r == '-')
	})
	out := f[:0]
	for _, w := range f {
		w = strings.Trim(w, ".-/")
		if len(w) < 2 || stop[w] {
			continue
		}
		out = append(out, w)
	}
	return out
}

// Jaccard is the token-set overlap of a and b.
func Jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	sa := map[string]bool{}
	for _, w := range a {
		sa[w] = true
	}
	sb := map[string]bool{}
	inter := 0
	for _, w := range b {
		if sb[w] {
			continue
		}
		sb[w] = true
		if sa[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(sa)+len(sb)-inter)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug makes a short filesystem-safe name from s.
func Slug(s string, maxLen int) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > maxLen {
		s = strings.TrimRight(s[:maxLen], "-")
	}
	if s == "" {
		s = "finding"
	}
	return s
}
