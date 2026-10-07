package compendium

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Table and fuzz tests for the Markdown scanner (wikilinks, #tags, code)
// and for search (tokenizing, folding, ranking, snippets).

func TestParseLinksAndTagsTable(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		links []string
		tags  []string
	}{
		{"plain", "[[A]] and #tag", []string{"A"}, []string{"tag"}},
		{"alias and heading", "[[A|shown]] [[B#Sec|x]] [[C#Sec]]", []string{"A", "B", "C"}, nil},
		{"inline code", "`[[A]] #t` [[B]]", []string{"B"}, nil},
		{"double-tick span holds a single tick", "``a ` [[A]]`` [[B]]", []string{"B"}, nil},
		{"code span closes only on an equal run", "``a```[[A]]`` [[B]]", []string{"B"}, nil},
		{"code span spans lines in a paragraph", "`code\n[[A]] #t` [[B]]", []string{"B"}, nil},
		{"unclosed tick is literal", "a ` [[A]] #t", []string{"A"}, []string{"t"}},
		{"blank line ends an open code span", "`open\n\n[[A]]`", []string{"A"}, nil},
		{"fence", "```\n[[A]] #t\n```\n[[B]]", []string{"B"}, nil},
		{"fence with info string is not a closer", "```\n[[A]]\n```js\n[[B]]\n```\n[[C]]", []string{"C"}, nil},
		{"shorter fence is not a closer", "````\n```\n[[A]]\n````\n[[B]]", []string{"B"}, nil},
		{"tilde fence ignores backticks", "~~~\n```\n[[A]]\n~~~\n[[B]]", []string{"B"}, nil},
		{"unclosed fence runs to the end", "```\n[[A]]", nil, nil},
		{"escaped open bracket", `\[[A]] [[B]]`, []string{"B"}, nil},
		{"escaped hash", `\#nope #yes`, nil, []string{"yes"}},
		{"escaped pipe in a table cell", "| [[A\\|alias]] |", []string{"A"}, nil},
		{"nested open", "[[a [[B]]", []string{"B"}, nil},
		{"unclosed", "[[A and more", nil, nil},
		{"empty", "[[]] [[ ]] [[|x]]", nil, nil},
		{"unicode titles", "[[Café]] [[東京]] [[Ελλάδα]]", []string{"Café", "東京", "Ελλάδα"}, nil},
		{"duplicate links fold", "[[Note]] [[note]] [[NOTE]]", []string{"Note"}, nil},
		{"embed", "![[Img]]", []string{"Img"}, nil},
		{"tag boundaries", "#a-b/c_d, (#x) #9lives #123 #1-2", nil, []string{"a-b/c_d", "x", "9lives"}},
		{"heading is not a tag", "# Heading\n## Two\n#tag", nil, []string{"tag"}},
		{"word-internal hash", "C# a#b x_#y", nil, nil},
		{"html entity", "&#39; &#x27;", nil, nil},
		{"markdown anchor link", "[jump](#section) and [x](page.md#part)", nil, nil},
		{"url fragment", "see https://example.com/a?b=#frag and http://x.io/#/route", nil, nil},
		{"url without scheme", "www.example.com/?q=#top", nil, nil},
		{"unicode tag", "#日本 #Ünïcode", nil, []string{"日本", "ünïcode"}},
		{"tag trailing punctuation", "#done. #ok, #end-", nil, []string{"done", "ok", "end"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := parseNote(c.in)
			if !reflect.DeepEqual(p.Links, c.links) {
				t.Errorf("links of %q = %q, want %q", c.in, p.Links, c.links)
			}
			if !reflect.DeepEqual(p.Tags, c.tags) {
				t.Errorf("tags of %q = %q, want %q", c.in, p.Tags, c.tags)
			}
		})
	}
}

func TestFoldIsUnicodeCaseInsensitive(t *testing.T) {
	pairs := [][2]string{
		{"ΟΔΟΣ", "οδος"},          // final sigma
		{"ΟΔΟΣ", "οδοσ"},          // medial sigma
		{"\u212Aelvin", "kelvin"}, // KELVIN SIGN folds to k
		{"ſtar", "Star"},          // long s
		{"Straße", "STRAßE"},
	}
	for _, p := range pairs {
		if fold(p[0]) != fold(p[1]) {
			t.Errorf("fold(%q)=%q != fold(%q)=%q", p[0], fold(p[0]), p[1], fold(p[1]))
		}
	}
	// Links resolve and rename with the same folding.
	h := openHarness(t, newMockFS())
	h.must(t, "create", map[string]string{"title": "ΟΔΟΣ", "content": "street"}, nil)
	h.must(t, "create", map[string]string{"title": "Map", "content": "see [[οδος]]"}, nil)
	var n noteOut
	h.must(t, "open", map[string]string{"title": "οδος"}, &n)
	if len(n.Backlinks) != 1 {
		t.Fatalf("final-sigma link did not resolve: %+v", n.Backlinks)
	}
}

func searchTitles(t *testing.T, h *harness, q string) []string {
	t.Helper()
	var r searchOut
	h.must(t, "search", map[string]any{"query": q}, &r)
	var out []string
	for _, x := range r.Results {
		out = append(out, x.Title)
	}
	return out
}

func TestSearchTable(t *testing.T) {
	h := openHarness(t, newMockFS())
	notes := map[string]string{
		"Greek":    "Η ΟΔΟΣ είναι μεγάλη",
		"Tokyo":    "東京は日本の首都であり、世界でも有数の大都市として知られている都市です。",
		"Decomp":   "a cafe\u0301 on the corner", // NFD "café"
		"Long":     "prefix " + strings.Repeat("abcdefghij", 8) + " suffix",
		"Both":     "alpha beta",
		"AlphaOne": "alpha alone",
		"Beta":     "beta only",
	}
	for title, body := range notes {
		h.must(t, "create", map[string]string{"title": title, "content": body}, nil)
	}
	for _, q := range []string{"", "   ", "!!!", "\t\n", "«»", "[[", "#"} {
		if got := searchTitles(t, h, q); len(got) != 0 {
			t.Errorf("query %q matched %v", q, got)
		}
	}
	cases := []struct {
		q    string
		want string
	}{
		{"οδος", "Greek"},
		{"ΟΔΌΣ", ""}, // accent differs: no match expected
		{"東京", "Tokyo"},
		{"首都", "Tokyo"},
		{"cafe\u0301", "Decomp"},
		{strings.Repeat("abcdefghij", 8), "Long"},
		{"abcdefghijabcdefghij", "Long"},
	}
	for _, c := range cases {
		got := searchTitles(t, h, c.q)
		if c.want == "" {
			continue
		}
		if !contains(got, c.want) {
			t.Errorf("query %q = %v, want %s", c.q, got, c.want)
		}
	}
	if got := searchTitles(t, h, "alpha beta"); len(got) != 1 || got[0] != "Both" {
		t.Errorf("multi-term AND = %v", got)
	}
	if got := searchTitles(t, h, "alpha"); len(got) < 2 || got[0] != "AlphaOne" {
		t.Errorf("title match should rank first: %v", got)
	}
}

func TestSnippetBoundaries(t *testing.T) {
	cases := []struct {
		name, content string
		terms         []string
		want          string
	}{
		{"match at start", "Alpha is first in a short line", []string{"alpha"}, "«Alpha» is first in a short line"},
		{"match at end", "short line ending with omega", []string{"omega"}, "short line ending with «omega»"},
		{"prefix highlight", "the lanterns glow", []string{"lan"}, "the «lanterns» glow"},
		{"cjk", "東京は日本の首都です", []string{"首"}, "東京は日本の«首»都です"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := snippet(c.content, c.terms); got != c.want {
				t.Errorf("snippet = %q, want %q", got, c.want)
			}
		})
	}
	// Long runs without spaces: the excerpt never starts or ends mid-rune
	// or mid-word, and still highlights the match.
	long := strings.Repeat("ö", 100) + " needle " + strings.Repeat("ü", 200)
	got := snippet(long, []string{"needle"})
	if !utf8.ValidString(got) || !strings.Contains(got, "«needle»") {
		t.Fatalf("snippet = %q", got)
	}
	for _, part := range strings.Fields(strings.Trim(got, "…")) {
		if strings.Trim(part, "«»") != "needle" && part != strings.Repeat("ö", 100) && part != strings.Repeat("ü", 200) {
			t.Fatalf("snippet cuts a word: %q", got)
		}
	}
}

// The scanner runs on every save and every open, over notes of up to
// 1 MiB: adversarial content must not make it quadratic.
func TestScannerLinearOnAdversarialInput(t *testing.T) {
	inputs := map[string]string{
		"openers":        strings.Repeat("[[", 1<<19),
		"openers+close":  strings.Repeat("[[a", 340000) + "]]",
		"brackets":       strings.Repeat("[[a]b", 200000) + "]]",
		"unclosed ticks": strings.Repeat("`x\n", 340000),
		"tick runs":      strings.Repeat("`a``b```c````d", 70000),
		"hash words":     strings.Repeat("-#", 1<<19),
		"url hashes":     strings.Repeat("http://a/-#", 90000),
		"fences":         strings.Repeat("```\n", 1<<18),
	}
	for name, in := range inputs {
		start := time.Now()
		parseNote(in)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%s (%d bytes) took %v to scan", name, len(in), d)
		}
	}
}

func FuzzScanMarkdown(f *testing.F) {
	for _, s := range []string{"[[A]] #t", "`[[A]]`", "```\n[[A]]\n```", "[[A|b]] [[B#c]]", "\\[[x]]", "#a/b-", "``a```b``", "[[a [[b]]", "ΟΔΟΣ #日本", "`x\n[[A]]`"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		last := -1
		scanMarkdown(s, func(sp span) {
			if sp.Start < last || sp.Start < 0 || sp.End > len(s) || sp.NameStart < sp.Start || sp.NameEnd > sp.End || sp.NameStart > sp.NameEnd {
				t.Fatalf("bad span %+v in %q", sp, s)
			}
			if s[sp.NameStart:sp.NameEnd] != sp.Name || strings.TrimSpace(sp.Name) != sp.Name || sp.Name == "" {
				t.Fatalf("span name %q != source %q", sp.Name, s[sp.NameStart:sp.NameEnd])
			}
			last = sp.End
		}, func(tag string, at int) {
			if at < 0 || at >= len(s) || s[at] != '#' || tag == "" {
				t.Fatalf("bad tag %q at %d in %q", tag, at, s)
			}
		})
		p := parseNote(s)
		if out, n := rewriteLinks(s, "zz-unused-title", "Other"); n != 0 || out != s {
			t.Fatal("rewrite touched content without matching links")
		}
		for _, l := range p.Links {
			out, n := rewriteLinks(s, l, "Renamed Target")
			if n == 0 {
				t.Fatalf("link %q found but not rewritten", l)
			}
			if !linksTo(out, "Renamed Target") {
				t.Fatalf("rewritten content does not link to new title: %q", out)
			}
			break
		}
		if !strings.Contains(s, "```") && !strings.Contains(s, "~~~") {
			fenced := "````\n" + s + "\n````"
			if q := parseNote(fenced); len(q.Links)+len(q.Tags) != 0 && !strings.Contains(s, "````") {
				t.Fatalf("fenced content produced links/tags %v %v", q.Links, q.Tags)
			}
		}
		if !strings.ContainsAny(s, "`\n") {
			if q := parseNote("`" + s + "`"); len(q.Links)+len(q.Tags) != 0 {
				t.Fatalf("inline code produced links/tags %v %v", q.Links, q.Tags)
			}
		}
		_ = render(s, func(string) (string, bool) { return "T", true })
		_ = contextLine(s, "A")
	})
}

func FuzzSearchAndSnippet(f *testing.F) {
	for _, s := range []string{"alpha beta", "東京は日本", "ΟΔΟΣ", "cafe\u0301", "a\u00a0b", "«x»"} {
		f.Add(s, "alpha")
	}
	f.Fuzz(func(t *testing.T, content, query string) {
		toks := tokenize(content, true)
		for _, tk := range toks {
			if tk.start < 0 || tk.end > len(content) || tk.start >= tk.end || !utf8.ValidString(content[tk.start:tk.end]) && utf8.ValidString(content) {
				t.Fatalf("bad token %+v", tk)
			}
		}
		terms := queryTerms(query)
		got := snippet(content, terms)
		if utf8.ValidString(content) && !utf8.ValidString(got) {
			t.Fatalf("snippet produced invalid UTF-8: %q", got)
		}
		x := newSearchIndex()
		x.add(1, "t", content)
		x.add(2, "u", query)
		_ = x.query(terms)
		x.remove(1)
		x.remove(2)
		if len(x.postings) != 0 || x.totalLen != 0 {
			t.Fatalf("index not empty after removing every doc: %d terms", len(x.postings))
		}
		// A query built from a document's own text finds that document.
		if len(toks) > 0 && utf8.ValidString(content) {
			y := newSearchIndex()
			y.add(7, "t", content)
			q := content[toks[0].start:toks[0].end]
			hits := y.query(queryTerms(q))
			if len(queryTerms(q)) > 0 && len(hits) != 1 {
				t.Fatalf("document not found by its own token %q", q)
			}
		}
	})
}

func FuzzTitleStemRoundTrip(f *testing.F) {
	for _, s := range []string{"CON", "trailing dot.", ".hidden", "a%b", "ΟΔΟΣ", "x:y*z?"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, title string) {
		if validTitle(title) != nil {
			return
		}
		if got := titleFromStem(fileStem(title)); got != title {
			t.Fatalf("round trip %q -> %q -> %q", title, fileStem(title), got)
		}
		if fold(fold(title)) != fold(title) {
			t.Fatalf("fold not idempotent for %q", title)
		}
		if strings.ContainsAny(fileStem(title), `/\:*?"<>|`) {
			t.Fatalf("stem %q has reserved bytes", fileStem(title))
		}
	})
}
