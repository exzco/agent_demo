package tui

import (
	"strconv"
	"strings"
	"testing"
)

// withoutColor disables ANSI coloring for the duration of a test.
func withoutColor(t *testing.T) {
	t.Helper()
	saved := ColorEnabled()
	SetColorEnabled(false)
	t.Cleanup(func() { SetColorEnabled(saved) })
}

func TestLineBufferInsertAtCursor(t *testing.T) {
	var b lineBuffer
	b.Set("hello")
	b.MoveHome()
	b.InsertString("say ")
	if got := b.String(); got != "say hello" {
		t.Fatalf("InsertString: got %q, want %q", got, "say hello")
	}
	if b.Cursor() != 4 {
		t.Fatalf("cursor: got %d, want 4", b.Cursor())
	}
	b.InsertRune('!')
	if got := b.String(); got != "say !hello" {
		t.Fatalf("InsertRune: got %q", got)
	}
}

func TestLineBufferBackspaceDelete(t *testing.T) {
	var b lineBuffer
	b.Set("abc")
	if !b.Backspace() || b.String() != "ab" {
		t.Fatalf("Backspace: got %q", b.String())
	}
	b.MoveHome()
	if !b.DeleteChar() || b.String() != "b" {
		t.Fatalf("DeleteChar: got %q", b.String())
	}
	if b.MoveLeft() {
		t.Fatal("MoveLeft at start should return false")
	}
	b.MoveEnd()
	if b.MoveRight() {
		t.Fatal("MoveRight at end should return false")
	}
	b.MoveEnd()
	if b.Backspace() && b.String() != "" {
		t.Fatalf("Backspace on last char: got %q", b.String())
	}
	if b.Backspace() {
		t.Fatal("Backspace on empty buffer should return false")
	}
}

func TestLineBufferKillOps(t *testing.T) {
	var b lineBuffer
	b.Set("hello world")
	for i := 0; i < 5; i++ {
		b.MoveLeft()
	}
	if !b.KillToEnd() || b.String() != "hello " {
		t.Fatalf("KillToEnd: got %q", b.String())
	}
	b.Set("hello world")
	b.MoveEnd()
	for i := 0; i < 5; i++ {
		b.MoveLeft()
	}
	if !b.KillToStart() || b.String() != "world" || b.Cursor() != 0 {
		t.Fatalf("KillToStart: got %q cursor %d", b.String(), b.Cursor())
	}
}

func TestLineBufferWordOps(t *testing.T) {
	var b lineBuffer
	b.Set("foo bar baz")
	if !b.DeleteWordBefore() || b.String() != "foo bar " {
		t.Fatalf("DeleteWordBefore: got %q", b.String())
	}
	b.Set("foo bar")
	if !b.MoveWordLeft() || b.Cursor() != 4 {
		t.Fatalf("MoveWordLeft: cursor %d", b.Cursor())
	}
	if !b.MoveWordRight() || b.Cursor() != 7 {
		t.Fatalf("MoveWordRight: cursor %d", b.Cursor())
	}
	b.MoveHome()
	if b.MoveWordLeft() {
		t.Fatal("MoveWordLeft at start should return false")
	}
}

func TestLineBufferPrefixWidthCJK(t *testing.T) {
	var b lineBuffer
	b.Set("你好ab")
	if w := b.PrefixWidth(2); w != 2+4+2 {
		t.Fatalf("PrefixWidth: got %d, want 8", w)
	}
}

// newTestCore builds a keyCore backed by a buffered channel pre-loaded with
// the given bytes, so decoding never blocks on real input.
func newTestCore(t *testing.T, bytes ...byte) *keyCore {
	t.Helper()
	ch := make(chan byte, 256)
	for _, b := range bytes {
		ch <- b
	}
	return &keyCore{pump: &bytePump{ch: ch}}
}

func TestKeyCoreControlKeys(t *testing.T) {
	cases := []struct {
		b    byte
		want KeyType
	}{
		{1, KeyHome}, {3, KeyCtrlC}, {4, KeyExit}, {5, KeyEnd},
		{11, KeyCtrlK}, {12, KeyCtrlL}, {21, KeyCtrlU}, {23, KeyCtrlW},
		{13, KeyEnter}, {10, KeyEnter}, {8, KeyBackspace}, {127, KeyBackspace},
		{9, KeyTab},
	}
	for _, c := range cases {
		k := newTestCore(t, c.b)
		if got := k.decode(c.b); got.Type != c.want {
			t.Errorf("decode(%d): got %v, want %v", c.b, got.Type, c.want)
		}
	}
}

func TestKeyCoreUTF8(t *testing.T) {
	k := newTestCore(t, 0xBD, 0xA0)
	ev := k.decode(0xE4)
	if ev.Type != KeyChar || ev.Char != '你' {
		t.Fatalf("got %+v, want KeyChar '你'", ev)
	}
}

func TestKeyCoreEscapeSequences(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
		want  KeyType
	}{
		{"up", []byte{'[', 'A'}, KeyUp},
		{"down", []byte{'[', 'B'}, KeyDown},
		{"right", []byte{'[', 'C'}, KeyRight},
		{"left", []byte{'[', 'D'}, KeyLeft},
		{"home", []byte{'[', 'H'}, KeyHome},
		{"end", []byte{'[', 'F'}, KeyEnd},
		{"delete", []byte{'[', '3', '~'}, KeyDelete},
		{"pageup", []byte{'[', '5', '~'}, KeyUp},
		{"pagedown", []byte{'[', '6', '~'}, KeyDown},
		{"wordright", []byte{'[', '1', ';', '5', 'C'}, KeyWordRight},
		{"wordleft", []byte{'[', '1', ';', '5', 'D'}, KeyWordLeft},
		{"ss3 home", []byte{'O', 'H'}, KeyHome},
		{"ss3 end", []byte{'O', 'F'}, KeyEnd},
	}
	for _, c := range cases {
		k := newTestCore(t, c.bytes...)
		if got := k.decode(27); got.Type != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got.Type, c.want)
		}
	}
}

func TestKeyCoreLoneEsc(t *testing.T) {
	k := newTestCore(t)
	if got := k.decode(27); got.Type != KeyEsc {
		t.Fatalf("lone ESC: got %v, want KeyEsc", got.Type)
	}
}

func TestKeyCoreBracketedPaste(t *testing.T) {
	payload := "hello\nworld"
	bytes := []byte("[200~" + payload + "\x1b[201~")
	k := newTestCore(t, bytes...)
	ev := k.decode(27)
	if ev.Type != KeyPaste {
		t.Fatalf("got %v, want KeyPaste", ev.Type)
	}
	if ev.Text != payload {
		t.Fatalf("paste text: got %q, want %q", ev.Text, payload)
	}
}

func TestFoldText(t *testing.T) {
	withoutColor(t)
	if got, hidden := FoldText("", 5, 10); got != "(empty)" || hidden != 0 {
		t.Fatalf("empty: got %q/%d", got, hidden)
	}
	got, hidden := FoldText("a\nb\nc", 2, 0)
	if got != "a\n  b" || hidden != 1 {
		t.Fatalf("fold: got %q hidden %d", got, hidden)
	}
	got, _ = FoldText("abcdefghij", 1, 5)
	if got != "abcd…" {
		t.Fatalf("width fold: got %q", got)
	}
	got, _ = FoldText("keep\ntrim\n\n", 10, 0)
	if got != "keep\n  trim" {
		t.Fatalf("trailing newline trim: got %q", got)
	}
}

func TestDisplayWidthHelpers(t *testing.T) {
	if w := displayWidthANSI("\x1b[31mred\x1b[0m"); w != 3 {
		t.Fatalf("displayWidthANSI: got %d", w)
	}
	if got := padRight("ab", 4); got != "ab  " {
		t.Fatalf("padRight: got %q", got)
	}
	if got := padRight("abcdef", 4); got != "abcdef" {
		t.Fatalf("padRight no-op: got %q", got)
	}
	withoutColor(t)
	if got := truncateANSI("abcdefghij", 5); got != "abcd…" {
		t.Fatalf("truncateANSI: got %q", got)
	}
	if got := truncateANSI("abc", 5); got != "abc" {
		t.Fatalf("truncateANSI short: got %q", got)
	}
}

func TestSliceByColumns(t *testing.T) {
	if got := sliceByColumns("hello世界", 0, 5); got != "hello" {
		t.Fatalf("head: got %q", got)
	}
	if got := sliceByColumns("hello世界", 5, 4); got != "世界" {
		t.Fatalf("CJK window: got %q", got)
	}
	if got := sliceByColumns("hello世界", 0, 7); got != "hello世" {
		t.Fatalf("straddling rune should be omitted: got %q", got)
	}
	if got := sliceByColumns("\x1b[31mred\x1b[0m", 0, 2); got != "\x1b[31mre" {
		t.Fatalf("ANSI preserved: got %q", got)
	}
	if got := sliceByColumns("abc", 5, 10); got != "" {
		t.Fatalf("out of range: got %q", got)
	}
}

func TestWindowStart(t *testing.T) {
	cases := []struct {
		total, col, view, want int
	}{
		{10, 10, 20, 0},   // fits entirely
		{100, 50, 20, 32}, // cursor in the middle of long input
		{100, 99, 20, 80}, // clamped to tail window
		{100, 10, 20, 0},  // cursor near start
	}
	for _, c := range cases {
		if got := windowStart(c.total, c.col, c.view); got != c.want {
			t.Errorf("windowStart(%d,%d,%d): got %d, want %d", c.total, c.col, c.view, got, c.want)
		}
	}
}

func TestHistoryAppendLoadDedup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	AppendHistory("first")
	AppendHistory("second")
	AppendHistory("second") // consecutive duplicate: skipped
	AppendHistory("first")  // non-consecutive duplicate: kept
	AppendHistory("/exit")  // never persisted
	AppendHistory("   ")    // blank: skipped

	got := LoadHistory()
	want := []string{"first", "second", "first"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("LoadHistory: got %v, want %v", got, want)
	}
	if p := HistoryPath(); !strings.HasSuffix(p, ".agy/history") {
		t.Fatalf("HistoryPath: %q", p)
	}
}

func TestHistoryLimit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for i := 0; i < defaultHistoryLimit+20; i++ {
		AppendHistory("cmd-" + strconv.Itoa(i))
	}
	got := LoadHistory()
	if len(got) != defaultHistoryLimit {
		t.Fatalf("got %d entries, want %d", len(got), defaultHistoryLimit)
	}
	if got[len(got)-1] != "cmd-"+strconv.Itoa(defaultHistoryLimit+19) {
		t.Fatalf("last entry: got %q", got[len(got)-1])
	}
}
