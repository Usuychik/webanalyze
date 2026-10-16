package webanalyze

import (
	"math/rand"
	"os"
	"reflect"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"
	"testing"
	"unicode"
)

func TestRequiredLiterals(t *testing.T) {
	for _, tc := range []struct {
		re   string
		want []string // nil: no requirement
	}{
		{`Powered by <a href="[^>]+phpfusion`, []string{`POWERED BY <A HREF="`}},
		{`go-mpulse\.net/boomerang`, []string{`GO-MPULSE.NET/BOOMERANG`}},
		{`(?:jquery|zepto)\.min\.js`, []string{`.MIN.JS`}},
		// the parser factors the common prefix out: wp-(?:content|includes)/
		{`(?:wp-content|wp-includes)/`, []string{`CONTENT`, `INCLUDES`}},
		{`(?:wp-content|x)/`, nil}, // one branch has nothing long enough
		{`a(?:bcdef)?g`, nil},      // optional part is not required
		{`<div[^>]+(?:abc)+`, []string{`<DIV`}},
		{`(?:abcd){2,}`, []string{`ABCD`}},
		{`\d+\.\d+`, nil},
		{`ab`, nil}, // too short
	} {
		got := requiredLiterals(regexp.MustCompile("(?i)" + tc.re))
		sort.Strings(got)
		sort.Strings(tc.want)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.re, got, tc.want)
		}
	}
}

func TestFoldString(t *testing.T) {
	for in, want := range map[string]string{
		"Powered By":     "POWERED BY",
		"ſ":              "S",   // long s folds with s under (?i)
		"K":              "K",   // Kelvin sign folds with k
		"a\xffb":         "A�B", // invalid UTF-8 as the regexp engine sees it
		"<script src=x>": "<SCRIPT SRC=X>",
		"":               "",
	} {
		if got := foldString(in); got != want {
			t.Errorf("foldString(%q) = %q, want %q", in, got, want)
		}
	}
	// everything (?i) treats as equal must fold to the same string
	for _, pair := range [][2]string{{"ſtyle", "STYLE"}, {"KB", "kb"}, {"ÉTÉ", "été"}} {
		if !regexp.MustCompile("(?i)" + regexp.QuoteMeta(pair[0])).MatchString(pair[1]) {
			t.Fatalf("test pair %q not equal under (?i)", pair)
		}
		if foldString(pair[0]) != foldString(pair[1]) {
			t.Errorf("%q and %q fold differently", pair[0], pair[1])
		}
	}
}

// gen produces a random string the syntax tree can match (not always: empty
// width assertions are ignored, so callers check with MatchString)
func gen(r *rand.Rand, re *syntax.Regexp, b *strings.Builder) {
	pickCase := func(c rune) rune {
		// any member of the case orbit is a valid match under (?i)
		orbit := []rune{c}
		for f := unicode.SimpleFold(c); f != c; f = unicode.SimpleFold(f) {
			orbit = append(orbit, f)
		}
		return orbit[r.Intn(len(orbit))]
	}
	rep := func(min, max int) int {
		if max < 0 {
			max = min + 3
		}
		return min + r.Intn(max-min+1)
	}
	switch re.Op {
	case syntax.OpLiteral:
		for _, c := range re.Rune {
			if re.Flags&syntax.FoldCase != 0 {
				c = pickCase(c)
			}
			b.WriteRune(c)
		}
	case syntax.OpCharClass:
		if len(re.Rune) == 0 {
			return
		}
		i := r.Intn(len(re.Rune)/2) * 2
		lo, hi := re.Rune[i], re.Rune[i+1]
		if hi-lo > 200 {
			hi = lo + 200
		}
		b.WriteRune(lo + rune(r.Intn(int(hi-lo)+1)))
	case syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		b.WriteByte("abc <>/=\"x1"[r.Intn(11)])
	case syntax.OpCapture:
		gen(r, re.Sub[0], b)
	case syntax.OpConcat:
		for _, s := range re.Sub {
			gen(r, s, b)
		}
	case syntax.OpAlternate:
		gen(r, re.Sub[r.Intn(len(re.Sub))], b)
	case syntax.OpStar:
		for n := rep(0, 2); n > 0; n-- {
			gen(r, re.Sub[0], b)
		}
	case syntax.OpPlus:
		for n := rep(1, 3); n > 0; n-- {
			gen(r, re.Sub[0], b)
		}
	case syntax.OpQuest:
		if r.Intn(2) == 0 {
			gen(r, re.Sub[0], b)
		}
	case syntax.OpRepeat:
		for n := rep(re.Min, re.Max); n > 0; n-- {
			gen(r, re.Sub[0], b)
		}
	}
}

func loadTestApps(t *testing.T) *WebAnalyzer {
	f, err := os.Open("technologies.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	wa, err := NewWebAnalyzer(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	return wa
}

// The prefilter must never reject a page a pattern actually matches: generate
// matching text for every pattern in technologies.json, wrap it in page
// noise, and check mayMatch agrees.
func TestPrefilterNeverRejectsAMatch(t *testing.T) {
	wa := loadTestApps(t)
	r := rand.New(rand.NewSource(1))
	var patterns, filterable, matched int
	for name, app := range wa.appDefs.Apps {
		for _, set := range [][]AppRegexp{app.HTMLRegex, app.ScriptRegex, app.URLRegex} {
			for i := range set {
				ar := &set[i]
				patterns++
				if ar.literals == nil {
					continue
				}
				filterable++
				tree, err := syntax.Parse(ar.Regexp.String(), syntax.Perl)
				if err != nil {
					t.Fatal(err)
				}
				tree = tree.Simplify()
				for k := 0; k < 20; k++ {
					var b strings.Builder
					b.WriteString("<html><body>noise ")
					gen(r, tree, &b)
					b.WriteString(" more noise</body></html>")
					page := b.String()
					if !ar.Regexp.MatchString(page) {
						continue
					}
					matched++
					if !ar.mayMatch(foldString(page)) {
						t.Fatalf("%s: prefilter %q rejected a page %s matches: %q", name, ar.literals, ar.Regexp, page)
					}
				}
			}
		}
	}
	t.Logf("%d patterns, %d with a prefilter, %d generated matches all kept", patterns, filterable, matched)
	if filterable == 0 || matched < filterable {
		t.Fatalf("too few generated matches (%d for %d patterns) to trust the check", matched, filterable)
	}
}

// Process must report exactly the same apps, matches and versions with the
// prefilter as without it
func TestProcessSameWithPrefilter(t *testing.T) {
	wa := loadTestApps(t)
	r := rand.New(rand.NewSource(2))

	// pages carrying matches for a random mix of apps
	var names []string
	for name := range wa.appDefs.Apps {
		names = append(names, name)
	}
	sort.Strings(names)
	detected := 0
	for p := 0; p < 30; p++ {
		var b strings.Builder
		b.WriteString("<html><head><title>t</title></head><body>")
		for k := 0; k < 40; k++ {
			app := wa.appDefs.Apps[names[r.Intn(len(names))]]
			if len(app.HTMLRegex) == 0 {
				continue
			}
			tree, _ := syntax.Parse(app.HTMLRegex[r.Intn(len(app.HTMLRegex))].Regexp.String(), syntax.Perl)
			gen(r, tree.Simplify(), &b)
			b.WriteString("\n<p>filler text</p>\n")
		}
		b.WriteString("</body></html>")
		page := b.String()

		run := func(on bool) []Match {
			usePrefilter = on
			defer func() { usePrefilter = true }()
			res, _ := wa.Process(NewOfflineJob("https://example.com/", page, nil))
			sort.Slice(res.Matches, func(i, j int) bool { return res.Matches[i].AppName < res.Matches[j].AppName })
			return res.Matches
		}
		without, with := run(false), run(true)
		if !reflect.DeepEqual(without, with) {
			t.Fatalf("page %d: results differ with the prefilter\nwithout: %d apps\nwith: %d apps", p, len(without), len(with))
		}
		if len(with) > 0 {
			detected++
		}
	}
	// generated text does not always satisfy anchors, but most pages must
	// detect something or the comparison proves little
	if detected < 20 {
		t.Fatalf("only %d of 30 pages detected any app", detected)
	}
}

func BenchmarkProcessLargePage(b *testing.B) {
	f, _ := os.Open("technologies.json")
	wa, err := NewWebAnalyzer(f, nil)
	f.Close()
	if err != nil {
		b.Fatal(err)
	}
	page := "<html><body>" + strings.Repeat(`<div class="card"><a href="/p/1">Product</a> <span>$19.99</span></div>`+"\n", 15000) + "</body></html>"
	for _, on := range []bool{false, true} {
		name := "without-prefilter"
		if on {
			name = "with-prefilter"
		}
		b.Run(name, func(b *testing.B) {
			usePrefilter = on
			defer func() { usePrefilter = true }()
			b.SetBytes(int64(len(page)))
			for i := 0; i < b.N; i++ {
				wa.Process(NewOfflineJob("https://example.com/", page, nil))
			}
		})
	}
}
