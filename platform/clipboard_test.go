package platform_test

import (
	"context"
	"strings"
	"testing"

	"gostalgia/platform"
)

func TestSanitizeClipboard_NormalText(t *testing.T) {
	input := "Hello, World!\nLine 2\tTabbed\r\nLine 3"
	got := platform.SanitizeClipboard(input)
	if got != input {
		t.Fatalf("expected %q, got %q", input, got)
	}
}

func TestSanitizeClipboard_StripsOSCSequences(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "OSC 52 with BEL",
			input:    "prefix\x1b]52;c;YWJj\x07suffix",
			expected: "prefixsuffix",
		},
		{
			name:     "OSC 52 with ST (ESC backslash)",
			input:    "prefix\x1b]52;c;YWJj\x1b\\suffix",
			expected: "prefixsuffix",
		},
		{
			name:     "OSC title change",
			input:    "before\x1b]0;Dangerous Title\x07after",
			expected: "beforeafter",
		},
		{
			name:     "Unterminated OSC at EOF",
			input:    "before\x1b]52;c;unfinished",
			expected: "before",
		},
		{
			name:     "Multiple OSC sequences",
			input:    "\x1b]0;one\x07hello\x1b]52;c;two\x1b\\world",
			expected: "helloworld",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := platform.SanitizeClipboard(tc.input)
			if got != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestSanitizeClipboard_StripsANSIEscapesAndControls(t *testing.T) {
	// CSI color sequences
	input := "\x1b[31;1mRed Bold\x1b[0m and \x1b[2KClear line"
	expected := "Red Bold and Clear line"
	got := platform.SanitizeClipboard(input)
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}

	// Control characters like NUL, BEL, BS, DEL
	inputWithControls := "Clean\x00Text\x07With\x08Controls\x7fDone"
	expectedClean := "CleanTextWithControlsDone"
	gotClean := platform.SanitizeClipboard(inputWithControls)
	if gotClean != expectedClean {
		t.Fatalf("expected %q, got %q", expectedClean, gotClean)
	}
}

func TestSanitizeClipboard_InvalidUTF8AndSizeLimit(t *testing.T) {
	// Invalid UTF-8 bytes
	invalidUTF8 := "valid\xff\xfeinvalid"
	got := platform.SanitizeClipboard(invalidUTF8)
	if strings.Contains(got, "\xff") || strings.Contains(got, "\xfe") {
		t.Fatalf("expected invalid UTF-8 stripped, got %q", got)
	}

	// Max size enforcement
	huge := strings.Repeat("A", platform.MaxClipboardBytes+1000)
	gotHuge := platform.SanitizeClipboard(huge)
	if len(gotHuge) > platform.MaxClipboardBytes {
		t.Fatalf("expected bounded to %d bytes, got %d", platform.MaxClipboardBytes, len(gotHuge))
	}
}

type mockClipboard struct {
	available bool
	text      string
}

func (m *mockClipboard) Available() bool { return m.available }
func (m *mockClipboard) ReadText(ctx context.Context) (string, error) {
	if !m.available {
		return "", platform.ErrClipboardUnsupported
	}
	return m.text, nil
}
func (m *mockClipboard) WriteText(ctx context.Context, text string) error {
	if !m.available {
		return platform.ErrClipboardUnsupported
	}
	m.text = text
	return nil
}

func TestMockClipboardAdapter(t *testing.T) {
	orig := platform.GetHostClipboard()
	defer platform.SetHostClipboard(orig)

	mock := &mockClipboard{available: true, text: "initial"}
	platform.SetHostClipboard(mock)

	if !platform.HostClipboardAvailable() {
		t.Fatalf("expected available")
	}

	text, err := platform.ReadHostClipboard(context.Background())
	if err != nil || text != "initial" {
		t.Fatalf("read failed: text=%q, err=%v", text, err)
	}

	err = platform.WriteHostClipboard(context.Background(), "updated")
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	text, err = platform.ReadHostClipboard(context.Background())
	if err != nil || text != "updated" {
		t.Fatalf("read updated failed: text=%q, err=%v", text, err)
	}

	mock.available = false
	if platform.HostClipboardAvailable() {
		t.Fatalf("expected unavailable")
	}
	if _, err := platform.ReadHostClipboard(context.Background()); err != platform.ErrClipboardUnsupported {
		t.Fatalf("expected ErrClipboardUnsupported, got %v", err)
	}
	if err := platform.WriteHostClipboard(context.Background(), "fail"); err != platform.ErrClipboardUnsupported {
		t.Fatalf("expected ErrClipboardUnsupported, got %v", err)
	}
}
