package markview

import (
	"strings"
	"testing"
)

// labels returns the rendered labels, ignoring kind and detail.
func labels(rows []row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.label)
	}
	return out
}

func hasLabel(rows []row, want string) bool {
	for _, r := range rows {
		if r.label == want {
			return true
		}
	}
	return false
}

func kindOf(rows []row, label string) rowKind {
	for _, r := range rows {
		if r.label == label {
			return r.kind
		}
	}
	return rowPara
}

func render(t *testing.T, src string) []row {
	t.Helper()
	rows, capped := renderMarkdown(src, maxRows)
	if capped {
		t.Fatalf("fixture hit the row cap")
	}
	return rows
}

func TestRenderHeadings(t *testing.T) {
	rows := render(t, "# One\n\n## Two ##\n\n### Three\n\nNot a heading\n\n#5 bolt\n\n#\n")
	for _, want := range []string{"# One", "## Two", "### Three", "Not a heading", "#5 bolt", "#"} {
		if !hasLabel(rows, want) {
			t.Fatalf("missing label %q in %v", want, labels(rows))
		}
	}
	if k := kindOf(rows, "# One"); k != rowHeading {
		t.Fatalf("# One kind = %v", k)
	}
	if k := kindOf(rows, "#5 bolt"); k != rowPara {
		t.Fatalf("#5 bolt should be a paragraph, got %v", k)
	}
	for _, r := range rows {
		if r.label == "## Two" && r.level != 2 {
			t.Fatalf("level = %d, want 2", r.level)
		}
	}
}

func TestRenderSetextHeadings(t *testing.T) {
	rows := render(t, "Title\n=====\n\nSub heading\n---\n")
	if len(rows) != 2 {
		t.Fatalf("rows = %v", labels(rows))
	}
	if rows[0].kind != rowHeading || rows[0].level != 1 || rows[0].label != "# Title" {
		t.Fatalf("setext h1 = %+v", rows[0])
	}
	if rows[1].kind != rowHeading || rows[1].level != 2 || rows[1].label != "## Sub heading" {
		t.Fatalf("setext h2 = %+v", rows[1])
	}
}

func TestRenderLists(t *testing.T) {
	src := "- one\n- two\n  - nested\n1. first\n2) second\n- [ ] todo\n- [x] done\n"
	rows := render(t, src)
	for _, want := range []string{"• one", "• two", "  • nested", "1. first", "2) second", "[ ] todo", "[x] done"} {
		if !hasLabel(rows, want) {
			t.Fatalf("missing %q in %v", want, labels(rows))
		}
	}
	if k := kindOf(rows, "[ ] todo"); k != rowTask {
		t.Fatalf("task kind = %v", k)
	}
	if k := kindOf(rows, "1. first"); k != rowList {
		t.Fatalf("ordered kind = %v", k)
	}
}

func TestRenderFencedCode(t *testing.T) {
	src := "```go\nfmt.Println(1)\n\n```\n\n~~~\nno lang\n~~~\n\n```\nunclosed\n"
	rows := render(t, src)
	if !hasLabel(rows, "┃ fmt.Println(1)") || !hasLabel(rows, "┃ no lang") || !hasLabel(rows, "┃ unclosed") {
		t.Fatalf("missing code rows in %v", labels(rows))
	}
	for _, r := range rows {
		if r.label == "┃ fmt.Println(1)" {
			if r.kind != rowCode || r.aux != "go" {
				t.Fatalf("code row = %+v", r)
			}
			if d := r.detail(); !strings.Contains(d, "code · go · line") {
				t.Fatalf("detail = %q", d)
			}
		}
		if r.label == "┃ unclosed" && r.kind != rowCode {
			t.Fatalf("unclosed fence row = %+v", r)
		}
	}
	// Fence markers themselves never render.
	for _, r := range rows {
		if strings.HasPrefix(r.label, "```") || strings.HasPrefix(r.label, "~~~") {
			t.Fatalf("fence marker leaked into label %q", r.label)
		}
	}
}

func TestRenderQuotes(t *testing.T) {
	rows := render(t, "> quoted\n>> nested\n> **bold** inside\n")
	for _, want := range []string{"│ quoted", "│ │ nested", "│ bold inside"} {
		if !hasLabel(rows, want) {
			t.Fatalf("missing %q in %v", want, labels(rows))
		}
	}
	if k := kindOf(rows, "│ quoted"); k != rowQuote {
		t.Fatalf("quote kind = %v", k)
	}
}

func TestRenderRulesAndParagraphs(t *testing.T) {
	rows := render(t, "text before\n\n---\n\n***\n\n_ _ _\n\ntext after\n")
	count := 0
	for _, r := range rows {
		if r.kind == rowRule {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("rules = %d in %v", count, labels(rows))
	}
	if !hasLabel(rows, "text before") || !hasLabel(rows, "text after") {
		t.Fatalf("paragraphs missing in %v", labels(rows))
	}
}

func TestRenderTable(t *testing.T) {
	src := "| name | size |\n| --- | ---: |\n| a.md | 10 |\n| b.md | 20 |\n"
	rows := render(t, src)
	if len(rows) != 3 {
		t.Fatalf("rows = %v", labels(rows))
	}
	if rows[0].label != "name │ size" || rows[1].label != "a.md │ 10" || rows[2].label != "b.md │ 20" {
		t.Fatalf("labels = %v", labels(rows))
	}
	for _, r := range rows {
		if r.kind != rowTable {
			t.Fatalf("table row kind = %+v", r)
		}
	}
}

func TestTableRequiresMatchingDelimiter(t *testing.T) {
	// `a | b` followed by `---` is a setext heading, not a table: the
	// delimiter row's cell count must match the header's.
	rows := render(t, "a | b\n---\n")
	if len(rows) != 1 || rows[0].kind != rowHeading || rows[0].label != "## a | b" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRenderIndentedCode(t *testing.T) {
	rows := render(t, "para\n\n    code line\n    more\n\nback to text\n")
	if !hasLabel(rows, "┃ code line") || !hasLabel(rows, "┃ more") {
		t.Fatalf("indented code missing in %v", labels(rows))
	}
	// An indented line inside a paragraph is a lazy continuation, not code.
	rows = render(t, "para\n    still para\n")
	if len(rows) != 1 || rows[0].label != "para still para" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRenderParagraphWrapping(t *testing.T) {
	long := strings.Repeat("word ", 40) // ~200 runes, wraps at 110
	rows := render(t, long+"\n")
	if len(rows) < 2 {
		t.Fatalf("expected wrapped rows, got %v", labels(rows))
	}
	if strings.HasPrefix(rows[0].label, "  ") {
		t.Fatalf("first wrap row should not be indented: %q", rows[0].label)
	}
	if !strings.HasPrefix(rows[1].label, "  ") {
		t.Fatalf("continuation row should be indented: %q", rows[1].label)
	}
}

func TestRenderCRLF(t *testing.T) {
	rows := render(t, "# T\r\n\r\n- a\r\n- b\r\n")
	if !hasLabel(rows, "# T") || !hasLabel(rows, "• a") || !hasLabel(rows, "• b") {
		t.Fatalf("CRLF rows = %v", labels(rows))
	}
}

func TestRenderRowCap(t *testing.T) {
	src := strings.Repeat("- x\n", 10)
	rows, capped := renderMarkdown(src, 3)
	if !capped || len(rows) != 3 {
		t.Fatalf("cap: rows=%d capped=%v", len(rows), capped)
	}
}

func TestRenderEmptyAndBlank(t *testing.T) {
	for _, src := range []string{"", "\n\n\n", "   \n  \n"} {
		if rows := render(t, src); len(rows) != 0 {
			t.Fatalf("src %q rendered %v", src, labels(rows))
		}
	}
}

func TestInlineFlattening(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"**bold**", "bold"},
		{"*it*", "it"},
		{"__b__", "b"},
		{"_i_", "i"},
		{"a **b** c", "a b c"},
		{"***both***", "both"},
		{"~~gone~~", "gone"},
		{"`code`", "code"},
		{"`` a ` b ``", "a ` b"},
		{"[text](http://example.com)", "text (http://example.com)"},
		{"[text](http://x \"a title\")", "text (http://x)"},
		{"[**bold** link](http://x)", "bold link (http://x)"},
		{"![alt text](img.png)", "[image: alt text]"},
		{"![](img.png)", "[image]"},
		{"<http://auto.link>", "http://auto.link"},
		{"<user@host>", "user@host"},
		{"snake_case_name", "snake_case_name"},
		{"a_b_c", "a_b_c"},
		{"a *b* c", "a b c"},
		{"unclosed **star", "unclosed **star"},
		{"a &amp; b", "a & b"},
		{"&lt;tag&gt;", "<tag>"},
		{"&#65;", "A"},
		{"&#x42;", "B"},
		{"<b>x</b>", "x"},
		{"a <div class=\"x\">b</div> c", "a b c"}, // raw tags removed, not rendered
		{"\\*literal\\*", "*literal*"},
		{"\\[not a link](x)", "[not a link](x)"},
		{"100% & 200%", "100% & 200%"},
	} {
		if got := flattenInline(tc.in); got != tc.want {
			t.Errorf("flattenInline(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestInlineInBlockLabels(t *testing.T) {
	rows := render(t, "# Head *with* `code`\n\n- [a link](http://x) and **bold**\n")
	if !hasLabel(rows, "# Head with code") {
		t.Fatalf("heading label = %v", labels(rows))
	}
	if !hasLabel(rows, "• a link (http://x) and bold") {
		t.Fatalf("list label = %v", labels(rows))
	}
}

func TestLabelBound(t *testing.T) {
	// A single very long word hard-wraps into rows that still fit the
	// contract's 256-byte label bound.
	rows := render(t, strings.Repeat("x", 600)+"\n")
	for _, r := range rows {
		if len(r.label) > maxLabelBytes {
			t.Fatalf("label %d bytes exceeds bound", len(r.label))
		}
	}
	if len(rows) < 2 {
		t.Fatalf("expected wrapped rows")
	}
}
