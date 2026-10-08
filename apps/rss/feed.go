package rss

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Headline is one stored feed entry. ID is the stable view identity
// (item_<seq>); Key is the dedupe identity derived from the feed's own
// guid, link, or title, so a refresh never double-lists an entry and a
// headline's row never changes identity under it.
type Headline struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Title     string    `json:"title"`
	Link      string    `json:"link"`
	Published time.Time `json:"published,omitempty"`
	Read      bool      `json:"read"`
}

// Feed is one subscription. Status carries the honest last-fetch problem
// ("host is not on the operator allowlist…", "unreadable feed: …"); an
// empty Status with a set FetchedAt means the last fetch succeeded.
type Feed struct {
	ID        string     `json:"id"`
	URL       string     `json:"url"`
	Title     string     `json:"title"`
	Items     []Headline `json:"items"`
	AddedAt   time.Time  `json:"added_at"`
	CheckedAt time.Time  `json:"checked_at,omitempty"` // last fetch attempt
	FetchedAt time.Time  `json:"fetched_at,omitempty"` // last fetch success
	Status    string     `json:"status,omitempty"`
}

// unread counts headlines that still need a look.
func (f *Feed) unread() int {
	n := 0
	for _, h := range f.Items {
		if !h.Read {
			n++
		}
	}
	return n
}

// --- parsing: RSS 2.0 and Atom via encoding/xml only ---

// parsedItem is a feed entry before it earns a stored Headline.
type parsedItem struct {
	Title     string
	Link      string
	GUID      string
	Published time.Time
}

// parsedFeed is a decoded document: a channel title and its entries.
type parsedFeed struct {
	Title string
	Items []parsedItem
}

// dateLayouts covers RSS pubDate (RFC 1123/822 variants), Atom's RFC
// 3339, and the sloppy real-world shapes between them.
var dateLayouts = []string{
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
	"02 Jan 2006 15:04:05 -0700",
	"02 Jan 2006 15:04:05 MST",
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
	time.ANSIC,
	time.UnixDate,
	time.RFC850,
}

// parseDate tries every known layout; an unparseable date is not an
// error — feeds are sloppy — it is a zero time, which renders as "no
// date" and sorts as the oldest entry.
func parseDate(raw string) time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// parseFeed detects the document flavor from its root element and
// decodes it. Anything that is not RSS 2.0 (<rss>) or Atom (<feed>) —
// including well-formed non-feed XML — is an honest parse rejection.
func parseFeed(data []byte) (parsedFeed, error) {
	var probe struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(data, &probe); err != nil {
		return parsedFeed{}, fmt.Errorf("unreadable feed: %v", err)
	}
	switch strings.ToLower(probe.XMLName.Local) {
	case "rss":
		return parseRSS(data)
	case "feed":
		return parseAtom(data)
	case "rdf":
		return parsedFeed{}, fmt.Errorf("unreadable feed: RSS 1.0/RDF is not supported (RSS 2.0 and Atom only)")
	default:
		return parsedFeed{}, fmt.Errorf("unreadable feed: not an RSS 2.0 or Atom document (root <%s>)", probe.XMLName.Local)
	}
}

type rssXML struct {
	Channel struct {
		Title string `xml:"title"`
		Items []struct {
			Title   string `xml:"title"`
			Link    string `xml:"link"`
			GUID    string `xml:"guid"`
			PubDate string `xml:"pubDate"`
			Date    string `xml:"date"`
		} `xml:"item"`
	} `xml:"channel"`
}

func parseRSS(data []byte) (parsedFeed, error) {
	var doc rssXML
	if err := xml.Unmarshal(data, &doc); err != nil {
		return parsedFeed{}, fmt.Errorf("unreadable feed: %v", err)
	}
	out := parsedFeed{Title: cleanText(doc.Channel.Title)}
	for _, it := range doc.Channel.Items {
		when := parseDate(it.PubDate)
		if when.IsZero() {
			// Some channels carry dc:date instead of pubDate; encoding/xml
			// matches it by local name into Date.
			when = parseDate(it.Date)
		}
		out.Items = append(out.Items, parsedItem{
			Title:     cleanText(it.Title),
			Link:      strings.TrimSpace(it.Link),
			GUID:      strings.TrimSpace(it.GUID),
			Published: when,
		})
	}
	if out.Title == "" && len(out.Items) == 0 {
		return parsedFeed{}, fmt.Errorf("unreadable feed: <rss> document has no channel title or items")
	}
	return out, nil
}

type atomXML struct {
	Title   string `xml:"title"`
	Entries []struct {
		Title     string `xml:"title"`
		ID        string `xml:"id"`
		Published string `xml:"published"`
		Updated   string `xml:"updated"`
		Links     []struct {
			Rel  string `xml:"rel,attr"`
			Href string `xml:"href,attr"`
		} `xml:"link"`
	} `xml:"entry"`
}

func parseAtom(data []byte) (parsedFeed, error) {
	var doc atomXML
	if err := xml.Unmarshal(data, &doc); err != nil {
		return parsedFeed{}, fmt.Errorf("unreadable feed: %v", err)
	}
	out := parsedFeed{Title: cleanText(doc.Title)}
	for _, e := range doc.Entries {
		link := ""
		for _, l := range e.Links {
			// The readable entry is the alternate (or only) link; self,
			// enclosure, and edit links are plumbing.
			if l.Href != "" && (l.Rel == "" || l.Rel == "alternate") {
				link = strings.TrimSpace(l.Href)
				break
			}
		}
		when := parseDate(e.Published)
		if when.IsZero() {
			when = parseDate(e.Updated)
		}
		out.Items = append(out.Items, parsedItem{
			Title:     cleanText(e.Title),
			Link:      link,
			GUID:      strings.TrimSpace(e.ID),
			Published: when,
		})
	}
	if out.Title == "" && len(out.Items) == 0 {
		return parsedFeed{}, fmt.Errorf("unreadable feed: <feed> document has no title or entries")
	}
	return out, nil
}

// itemKey is the dedupe identity for an entry: the feed's own guid when
// it provides one, else link+title together so a homepage-link feed does
// not collapse and a retitled entry does not duplicate. Hashed so keys
// stay bounded whatever the feed emits.
func itemKey(it parsedItem) string {
	raw := it.GUID
	if raw == "" {
		raw = it.Link + "|" + it.Title
	}
	sum := sha1.Sum([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// sortHeadlines orders a feed's stored items newest-first: dated entries
// by published time descending, undated entries sinking to the tail,
// with the larger item sequence breaking ties (later-merged first).
func sortHeadlines(items []Headline) {
	seq := func(h Headline) int {
		var n int
		_, _ = fmt.Sscanf(h.ID, "item_%d", &n)
		return n
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].Published, items[j].Published
		if a.Equal(b) {
			return seq(items[i]) > seq(items[j])
		}
		if a.IsZero() {
			return false
		}
		if b.IsZero() {
			return true
		}
		return a.After(b)
	})
}

// cleanText normalizes feed-supplied text for storage and display:
// trimmed, single-line, whitespace-collapsed, and length-bounded.
func cleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 {
			return -1 // strip control bytes outright
		}
		return r
	}, strings.TrimSpace(s))
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if len(s) > maxTitleLen {
		s = strings.TrimSpace(s[:maxTitleLen])
	}
	return s
}

// normalizeFeedURL validates and tidies a subscription URL. Only
// http(s) URLs with a host qualify; whether the scheme and host are
// actually reachable is the fetch's honest problem, not subscribe's.
func normalizeFeedURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("paste a feed URL first")
	}
	if len(s) > maxURLLen {
		return "", fmt.Errorf("feed URL is too long (%d chars max)", maxURLLen)
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not a valid URL — use https://host/feed.xml", s)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", fmt.Errorf("only http(s) feed URLs are supported, got %q", u.Scheme)
	}
	u.Fragment = "" // anchors are meaningless to a fetch
	return u.String(), nil
}

// describeFetchFailure turns a net/fetch rejection into honest
// operator-facing status text. The three policy shapes — missing grant,
// egress disabled, host not allowlisted — each say what happened and who
// can fix it; everything else is an ordinary fetch failure.
func describeFetchFailure(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "missing capability"):
		return "egress permission denied — the net.egress grant is missing or revoked"
	case strings.Contains(msg, "network egress is disabled"):
		return "network egress is disabled by operator policy — enable it to fetch feeds"
	case strings.Contains(msg, "not in allowed hosts"):
		return "host is not on the operator allowlist — add it (or \"*\") to allowed_hosts"
	case strings.Contains(msg, "plaintext HTTP"):
		return "plaintext HTTP is blocked by operator policy — use an https:// feed URL"
	case strings.Contains(msg, "policy_denied"):
		return "blocked by operator policy — " + msg
	default:
		return "fetch failed: " + msg
	}
}
