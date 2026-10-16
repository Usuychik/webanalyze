package webanalyze

import (
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Most HTML patterns cannot match unless some fixed text occurs in the page
// ("powered by <a href=", "go-mpulse.net", ...). Checking for that text with
// strings.Contains is far cheaper than running the regexp, and almost every
// pattern fails that check on any given page, so only the few that pass are
// run. Both sides are compared case-folded, which keeps the check exact for
// the (?i) every pattern is compiled with.

// shorter literals are too common to reject anything worth the check
const minLiteralLen = 3

// usePrefilter can be switched off to compare results with and without it
var usePrefilter = true

// requiredLiterals returns case-folded strings of which at least one must
// occur in any text re matches, or nil if no such set can be derived
func requiredLiterals(re *regexp.Regexp) []string {
	tree, err := syntax.Parse(re.String(), syntax.Perl)
	if err != nil {
		return nil
	}
	lits := required(tree.Simplify())
	if minRunes(lits) < minLiteralLen {
		return nil
	}
	for i, l := range lits {
		lits[i] = foldString(l)
	}
	return lits
}

func required(re *syntax.Regexp) []string {
	switch re.Op {
	case syntax.OpLiteral:
		return []string{string(re.Rune)}
	case syntax.OpCapture, syntax.OpPlus:
		return required(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return required(re.Sub[0])
		}
	case syntax.OpConcat:
		// adjacent literals form one longer run; keep the most selective
		// requirement among the runs and the other parts
		var best []string
		var run strings.Builder
		consider := func(c []string) {
			if c != nil && minRunes(c) > minRunes(best) {
				best = c
			}
		}
		for _, sub := range re.Sub {
			if sub.Op == syntax.OpLiteral {
				run.WriteString(string(sub.Rune))
				continue
			}
			if run.Len() > 0 {
				consider([]string{run.String()})
				run.Reset()
			}
			consider(required(sub))
		}
		if run.Len() > 0 {
			consider([]string{run.String()})
		}
		return best
	case syntax.OpAlternate:
		// every branch needs a requirement, any of them will do
		var all []string
		for _, sub := range re.Sub {
			r := required(sub)
			if r == nil {
				return nil
			}
			all = append(all, r...)
		}
		return all
	}
	return nil
}

func minRunes(lits []string) int {
	if len(lits) == 0 {
		return 0
	}
	m := -1
	for _, l := range lits {
		if n := utf8.RuneCountInString(l); m < 0 || n < m {
			m = n
		}
	}
	return m
}

// mayMatch reports whether the regexp can possibly match the text whose
// case-folded form is folded
func (r *AppRegexp) mayMatch(folded string) bool {
	if !usePrefilter || r.literals == nil {
		return true
	}
	for _, l := range r.literals {
		if strings.Contains(folded, l) {
			return true
		}
	}
	return false
}

// foldRune maps r to the smallest rune of its simple case-folding orbit, the
// same equivalence (?i) matches by: 's', 'S' and 'ſ' all map to 'S'
func foldRune(r rune) rune {
	min := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		if f < min {
			min = f
		}
	}
	return min
}

// foldString case-folds s rune by rune with foldRune; ASCII, nearly all of
// a page, takes a fast path. Invalid UTF-8 becomes U+FFFD, as it does for
// the regexp engine.
func foldString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if 'a' <= c && c <= 'z' {
				c -= 'a' - 'A'
			}
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(foldRune(r))
		i += size
	}
	return b.String()
}
