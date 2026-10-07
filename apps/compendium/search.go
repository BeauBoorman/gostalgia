package compendium

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// token is a lowercase word and its byte span in the source text.
type token struct {
	term       string
	start, end int
}

const maxTokenBytes = 64

// maxSnippetBytes bounds one search snippet, so a page of results stays
// small whatever the notes hold (a 900 KB unbroken word included).
const maxSnippetBytes = 320

// maxMarkedBytes bounds how much of one matched word a snippet shows.
const maxMarkedBytes = 80

// unspaced reports scripts written without spaces between words (Han,
// Hiragana, Katakana). Each such character is its own token, so a word
// can be found anywhere in a run of text; a multi-character query matches
// as the conjunction of its characters.
func unspaced(r rune) bool {
	return r >= 0x2E80 && unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana)
}

// tokenize splits text into case-folded words: runs of letters and digits,
// with combining marks kept inside the word they modify (so decomposed
// "café" stays one word). A word longer than maxTokenBytes is indexed by
// its first maxTokenBytes bytes rather than dropped; queries are cut the
// same way, so long words remain findable.
func tokenize(text string, withSpans bool) []token {
	var out []token
	start := -1
	flush := func(end int) {
		if start >= 0 {
			term := fold(text[start:end])
			if len(term) > maxTokenBytes {
				cut := maxTokenBytes
				for cut > 0 && !utf8.RuneStart(term[cut]) {
					cut--
				}
				term = term[:cut]
			}
			tk := token{term: term}
			if withSpans {
				tk.start, tk.end = start, end
			}
			out = append(out, tk)
		}
		start = -1
	}
	for i, r := range text {
		if r < utf8.RuneSelf { // ASCII fast path
			if 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' {
				if start < 0 {
					start = i
				}
			} else {
				flush(i)
			}
			continue
		}
		switch {
		case unspaced(r):
			flush(i)
			start = i
			flush(i + utf8.RuneLen(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if start < 0 {
				start = i
			}
		case unicode.Is(unicode.M, r) && start >= 0:
			// a combining mark continues the current word
		default:
			flush(i)
		}
	}
	flush(len(text))
	return out
}

// searchIndex is an in-memory inverted index with BM25 ranking. It is
// derived entirely from note sources at load and kept current on every
// mutation; it is never persisted, so it can never be stale on disk.
type searchIndex struct {
	postings map[string]map[int32]uint16 // term -> doc -> term frequency
	titles   map[int32]map[string]bool   // doc -> title terms (boosted)
	docLen   map[int32]int
	docTerms map[int32][]string
	totalLen int
	vocab    []string // sorted terms, for prefix expansion
	vocabOK  bool
}

func newSearchIndex() *searchIndex {
	return &searchIndex{
		postings: map[string]map[int32]uint16{},
		titles:   map[int32]map[string]bool{},
		docLen:   map[int32]int{},
		docTerms: map[int32][]string{},
	}
}

func (x *searchIndex) add(doc int32, title, content string) {
	x.remove(doc)
	tf := map[string]uint16{}
	tt := map[string]bool{}
	n := 0
	for _, t := range tokenize(title, false) {
		tt[t.term] = true
		if tf[t.term] < math.MaxUint16 {
			tf[t.term]++
		}
		n++
	}
	for _, t := range tokenize(content, false) {
		if tf[t.term] < math.MaxUint16 {
			tf[t.term]++
		}
		n++
	}
	terms := make([]string, 0, len(tf))
	for term, f := range tf {
		p := x.postings[term]
		if p == nil {
			p = map[int32]uint16{}
			x.postings[term] = p
			x.vocabOK = false
		}
		p[doc] = f
		terms = append(terms, term)
	}
	x.titles[doc] = tt
	x.docLen[doc] = n
	x.docTerms[doc] = terms
	x.totalLen += n
}

func (x *searchIndex) remove(doc int32) {
	terms, ok := x.docTerms[doc]
	if !ok {
		return
	}
	for _, term := range terms {
		p := x.postings[term]
		delete(p, doc)
		if len(p) == 0 {
			delete(x.postings, term)
			x.vocabOK = false
		}
	}
	x.totalLen -= x.docLen[doc]
	delete(x.docTerms, doc)
	delete(x.docLen, doc)
	delete(x.titles, doc)
}

// expand returns the indexed terms a query term matches: itself, plus
// (for terms of two or more characters) every indexed term it prefixes,
// found as one contiguous range of the sorted vocabulary. There is no cap:
// a conjunction is only correct if every matching term is considered.
func (x *searchIndex) expand(term string) []string {
	var out []string
	if _, ok := x.postings[term]; ok {
		out = append(out, term)
	}
	if utf8.RuneCountInString(term) < 2 {
		return out
	}
	if !x.vocabOK {
		x.vocab = x.vocab[:0]
		for t := range x.postings {
			x.vocab = append(x.vocab, t)
		}
		sort.Strings(x.vocab)
		x.vocabOK = true
	}
	i := sort.SearchStrings(x.vocab, term)
	for ; i < len(x.vocab) && strings.HasPrefix(x.vocab[i], term); i++ {
		if x.vocab[i] != term {
			out = append(out, x.vocab[i])
		}
	}
	return out
}

type hit struct {
	doc   int32
	score float64
}

// query ranks documents containing every query term (prefix matches
// allowed) by BM25 with a title boost. Terms are evaluated cheapest first
// (fewest postings across their expansion); once the candidate set is
// small, a broad prefix term is checked against each candidate's own
// terms instead of walking every posting of every expansion, so a prefix
// that matches thousands of words stays fast and exact.
func (x *searchIndex) query(terms []string) []hit {
	n := float64(len(x.docLen))
	if n == 0 || len(terms) == 0 {
		return nil
	}
	avg := float64(x.totalLen) / n
	const k1, b = 1.2, 0.75
	type plan struct {
		term string
		exp  []string
		cost int
	}
	plans := make([]plan, 0, len(terms))
	for _, term := range terms {
		p := plan{term: term, exp: x.expand(term)}
		if len(p.exp) == 0 {
			return nil // a term nothing matches: the conjunction is empty
		}
		for _, t := range p.exp {
			p.cost += len(x.postings[t])
		}
		plans = append(plans, p)
	}
	sort.SliceStable(plans, func(i, j int) bool { return plans[i].cost < plans[j].cost })
	score := func(term, t string, doc int32, f uint16) float64 {
		weight := 1.0
		if t != term {
			weight = 0.6
		}
		df := float64(len(x.postings[t]))
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))
		tf := float64(f)
		s := idf * tf * (k1 + 1) / (tf + k1*(1-b+b*float64(x.docLen[doc])/avg))
		if x.titles[doc][t] {
			s += 1.5 * idf
		}
		return s * weight
	}
	var acc map[int32]float64
	for qi, p := range plans {
		cur := map[int32]float64{}
		probe := 0
		if qi > 0 {
			for doc := range acc {
				probe += len(x.docTerms[doc])
			}
		}
		if qi > 0 && probe < p.cost {
			// Few candidates: test each one's terms against the query term.
			for doc := range acc {
				for _, t := range x.docTerms[doc] {
					if t != p.term && (utf8.RuneCountInString(p.term) < 2 || !strings.HasPrefix(t, p.term)) {
						continue
					}
					if s := score(p.term, t, doc, x.postings[t][doc]); s > cur[doc] {
						cur[doc] = s
					}
				}
			}
		} else {
			for _, t := range p.exp {
				for doc, f := range x.postings[t] {
					if qi > 0 {
						if _, ok := acc[doc]; !ok {
							continue
						}
					}
					if s := score(p.term, t, doc, f); s > cur[doc] {
						cur[doc] = s
					}
				}
			}
		}
		if qi == 0 {
			acc = cur
			continue
		}
		for doc, s := range acc {
			if c, ok := cur[doc]; ok {
				acc[doc] = s + c
			} else {
				delete(acc, doc)
			}
		}
		if len(acc) == 0 {
			return nil
		}
	}
	out := make([]hit, 0, len(acc))
	for doc, s := range acc {
		out = append(out, hit{doc, s})
	}
	return out
}

// queryTerms tokenizes a free-text query into distinct terms.
func queryTerms(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tokenize(q, false) {
		if !seen[t.term] {
			seen[t.term] = true
			out = append(out, t.term)
		}
	}
	return out
}

// snippet extracts a real excerpt of content around the first match of any
// query term (prefix-aware), marking matches with «» and eliding with ….
func snippet(content string, terms []string) string {
	toks := tokenize(content, true)
	matches := func(term string) bool {
		for _, q := range terms {
			if term == q || (utf8.RuneCountInString(q) >= 2 && strings.HasPrefix(term, q)) {
				return true
			}
		}
		return false
	}
	first := -1
	for i, t := range toks {
		if matches(t.term) {
			first = i
			break
		}
	}
	if first < 0 {
		return flatten(clip(strings.TrimSpace(content), 160))
	}
	from := toks[first].start - 60
	to := toks[first].end + 120
	if from < 0 {
		from = 0
	}
	if to > len(content) {
		to = len(content)
	}
	for from > 0 && !utf8.RuneStart(content[from]) {
		from--
	}
	for to < len(content) && !utf8.RuneStart(content[to]) {
		to++
	}
	// Never cut a word: a window edge inside a word moves to that word's
	// edge (out of the window). Token edges are rune boundaries, and the
	// matched word itself always stays inside.
	for _, t := range toks {
		if t.start < from && from < t.end {
			from = t.end
		}
		if t.start < to && to < t.end {
			to = t.start
		}
	}
	var b strings.Builder
	if from > 0 {
		b.WriteString("…")
	}
	last := from
	for _, t := range toks {
		if t.start < from || t.end > to || !matches(t.term) {
			continue
		}
		b.WriteString(content[last:t.start])
		b.WriteString("«")
		b.WriteString(clip(content[t.start:t.end], maxMarkedBytes))
		b.WriteString("»")
		last = t.end
		if b.Len() > 2*maxSnippetBytes {
			break
		}
	}
	if last < to {
		b.WriteString(clip(content[last:to], 2*maxSnippetBytes))
	}
	if to < len(content) {
		b.WriteString("…")
	}
	return balanceMarks(clip(flatten(b.String()), maxSnippetBytes))
}

// balanceMarks drops a trailing «… whose closing » the length bound cut.
func balanceMarks(s string) string {
	if open := strings.LastIndex(s, "«"); open >= 0 && !strings.Contains(s[open:], "»") {
		return strings.TrimRight(s[:open], " ") + "…"
	}
	return s
}

// flatten collapses whitespace runs (including newlines) to single spaces.
func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }
