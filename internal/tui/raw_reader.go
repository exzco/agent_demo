package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type KeyType int

const (
	KeyChar KeyType = iota
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyDelete
	KeyWordLeft
	KeyWordRight
	KeyCtrlU
	KeyCtrlK
	KeyCtrlW
	KeyCtrlC
	KeyCtrlL
	KeyCtrlO
	KeyEnter
	KeyBackspace
	KeyTab
	KeyEsc
	KeyExit
	KeyPaste
	KeyLine // for non-terminal fallback
)

type KeyEvent struct {
	Type KeyType
	Char rune
	Text string
}

type keyReader interface {
	ReadKey() (KeyEvent, error)
	Close()
}

// escTimeout bounds how long we wait for the remainder of an ESC sequence.
const escTimeout = 40 * time.Millisecond

// ----------------------------------------------------------------------
// Display width helpers (CJK-aware)
// ----------------------------------------------------------------------

// runeDisplayWidth returns the visual column width of a single rune.
func runeDisplayWidth(r rune) int {
	if r == 0 {
		return 0
	}
	if r < 32 || (r >= 0x7f && r < 0xa0) {
		return 0
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
		return 0
	}
	if isWideRune(r) {
		return 2
	}
	return 1
}

func isWideRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F: // Hangul Jamo
		return true
	case r >= 0x2E80 && r <= 0xA4CF: // CJK radicals .. Yi
		return true
	case r >= 0xAC00 && r <= 0xD7A3: // Hangul syllables
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK compatibility ideographs
		return true
	case r >= 0xFE30 && r <= 0xFE4F: // CJK compatibility forms
		return true
	case r >= 0xFF00 && r <= 0xFF60: // Fullwidth forms
		return true
	case r >= 0xFFE0 && r <= 0xFFE6:
		return true
	case r >= 0x1F300 && r <= 0x1FAFF: // emoji
		return true
	case r >= 0x20000 && r <= 0x3FFFD: // CJK extension B+
		return true
	}
	return false
}

// stringDisplayWidth computes visual column width (CJK characters count as 2 columns).
func stringDisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeDisplayWidth(r)
	}
	return w
}

// ----------------------------------------------------------------------
// Byte pump + shared key decoding
// ----------------------------------------------------------------------

// bytePump continuously drains an *os.File into a channel. It is shared per
// file and survives across editor invocations, so type-ahead is preserved and
// partial escape sequences can be reassembled with timeouts.
type bytePump struct {
	ch chan byte
}

var (
	pumpMu    sync.Mutex
	pumpCache = map[*os.File]*bytePump{}
)

func getBytePump(in *os.File) *bytePump {
	pumpMu.Lock()
	defer pumpMu.Unlock()
	if p, ok := pumpCache[in]; ok {
		return p
	}
	p := &bytePump{ch: make(chan byte, 512)}
	pumpCache[in] = p
	go func() {
		defer close(p.ch)
		buf := make([]byte, 64)
		for {
			n, err := in.Read(buf)
			for i := 0; i < n; i++ {
				p.ch <- buf[i]
			}
			if err != nil {
				return
			}
		}
	}()
	return p
}

// keyCore decodes bytes from a bytePump into KeyEvents.
type keyCore struct {
	pump *bytePump
}

func newKeyCore(in *os.File) *keyCore {
	return &keyCore{pump: getBytePump(in)}
}

// next returns the next byte, blocking or waiting up to timeout (0 = block).
func (k *keyCore) next(timeout time.Duration) (byte, bool) {
	if timeout <= 0 {
		b, ok := <-k.pump.ch
		return b, ok
	}
	select {
	case b, ok := <-k.pump.ch:
		return b, ok
	case <-time.After(timeout):
		return 0, false
	}
}

func (k *keyCore) ReadKey() (KeyEvent, error) {
	b, ok := k.next(0)
	if !ok {
		return KeyEvent{Type: KeyExit}, io.EOF
	}
	return k.decode(b), nil
}

func (k *keyCore) decode(b byte) KeyEvent {
	switch b {
	case 27:
		return k.parseEscape()
	case 13, 10:
		return KeyEvent{Type: KeyEnter}
	case 8, 127:
		return KeyEvent{Type: KeyBackspace}
	case 9:
		return KeyEvent{Type: KeyTab}
	case 1: // Ctrl+A -> Home
		return KeyEvent{Type: KeyHome}
	case 3: // Ctrl+C
		return KeyEvent{Type: KeyCtrlC}
	case 4: // Ctrl+D -> exit
		return KeyEvent{Type: KeyExit}
	case 5: // Ctrl+E -> End
		return KeyEvent{Type: KeyEnd}
	case 11: // Ctrl+K -> kill to end
		return KeyEvent{Type: KeyCtrlK}
	case 12: // Ctrl+L -> clear screen
		return KeyEvent{Type: KeyCtrlL}
	case 15: // Ctrl+O -> expand/collapse tool output and thought blocks
		return KeyEvent{Type: KeyCtrlO}
	case 21: // Ctrl+U -> kill to start
		return KeyEvent{Type: KeyCtrlU}
	case 23: // Ctrl+W -> delete word before cursor
		return KeyEvent{Type: KeyCtrlW}
	}

	if b < 0x80 {
		return KeyEvent{Type: KeyChar, Char: rune(b)}
	}

	// UTF-8 multi-byte sequence.
	need := utf8SeqLen(b)
	raw := make([]byte, 1, need)
	raw[0] = b
	for len(raw) < need {
		nb, ok := k.next(30 * time.Millisecond)
		if !ok {
			break
		}
		raw = append(raw, nb)
	}
	r, _ := utf8.DecodeRune(raw)
	return KeyEvent{Type: KeyChar, Char: r}
}

func utf8SeqLen(b byte) int {
	switch {
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	}
	return 1
}

// parseEscape decodes everything after an ESC byte.
func (k *keyCore) parseEscape() KeyEvent {
	b, ok := k.next(escTimeout)
	if !ok {
		return KeyEvent{Type: KeyEsc}
	}
	switch b {
	case '[':
		b2, ok := k.next(escTimeout)
		if !ok {
			return KeyEvent{Type: KeyEsc}
		}
		switch b2 {
		case 'A':
			return KeyEvent{Type: KeyUp}
		case 'B':
			return KeyEvent{Type: KeyDown}
		case 'C':
			return KeyEvent{Type: KeyRight}
		case 'D':
			return KeyEvent{Type: KeyLeft}
		case 'H':
			return KeyEvent{Type: KeyHome}
		case 'F':
			return KeyEvent{Type: KeyEnd}
		case 'Z':
			return KeyEvent{Type: KeyTab} // Shift+Tab (cycles dropdown forward)
		}
		if b2 >= '0' && b2 <= '9' {
			var sb strings.Builder
			sb.WriteByte(b2)
			for i := 0; i < 10; i++ {
				c, ok := k.next(escTimeout)
				if !ok {
					break
				}
				sb.WriteByte(c)
				if c == '~' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
					break
				}
			}
			return k.mapCSISequence(sb.String())
		}
		return KeyEvent{Type: KeyEsc}
	case 'O':
		b2, ok := k.next(escTimeout)
		if !ok {
			return KeyEvent{Type: KeyEsc}
		}
		switch b2 {
		case 'A':
			return KeyEvent{Type: KeyUp}
		case 'B':
			return KeyEvent{Type: KeyDown}
		case 'C':
			return KeyEvent{Type: KeyRight}
		case 'D':
			return KeyEvent{Type: KeyLeft}
		case 'H':
			return KeyEvent{Type: KeyHome}
		case 'F':
			return KeyEvent{Type: KeyEnd}
		}
		return KeyEvent{Type: KeyEsc}
	}
	return KeyEvent{Type: KeyEsc}
}

// mapCSISequence maps numeric CSI sequences (e.g. "3~", "1;5C") to keys.
func (k *keyCore) mapCSISequence(seq string) KeyEvent {
	switch seq {
	case "1~", "7~", "H":
		return KeyEvent{Type: KeyHome}
	case "4~", "8~", "F":
		return KeyEvent{Type: KeyEnd}
	case "2~": // Insert key: no-op
		return KeyEvent{Type: KeyEsc}
	case "3~":
		return KeyEvent{Type: KeyDelete}
	case "5~": // PageUp behaves as history/menu up
		return KeyEvent{Type: KeyUp}
	case "6~": // PageDown behaves as history/menu down
		return KeyEvent{Type: KeyDown}
	case "1;5C", "5C":
		return KeyEvent{Type: KeyWordRight}
	case "1;5D", "5D":
		return KeyEvent{Type: KeyWordLeft}
	case "200~":
		return k.readBracketedPaste()
	case "201~": // stray paste terminator
		return KeyEvent{Type: KeyEsc}
	}
	return KeyEvent{Type: KeyEsc}
}

// readBracketedPaste consumes input until the bracketed-paste terminator
// "\x1b[201~" and returns the pasted content.
func (k *keyCore) readBracketedPaste() KeyEvent {
	const term = "\x1b[201~"
	var sb strings.Builder
	var tail []byte
	for sb.Len() < 1<<20 {
		b, ok := k.next(0)
		if !ok {
			break
		}
		tail = append(tail, b)
		if len(tail) > len(term) {
			sb.WriteByte(tail[0])
			tail = tail[1:]
		}
		if string(tail) == term {
			tail = nil
			break
		}
	}
	sb.Write(tail)
	return KeyEvent{Type: KeyPaste, Text: sb.String()}
}

// ----------------------------------------------------------------------
// lineBuffer: pure, unit-testable single-line editing state
// ----------------------------------------------------------------------

type lineBuffer struct {
	runes  []rune
	cursor int
}

func (b *lineBuffer) String() string { return string(b.runes) }
func (b *lineBuffer) Len() int       { return len(b.runes) }
func (b *lineBuffer) Cursor() int    { return b.cursor }
func (b *lineBuffer) IsEmpty() bool  { return len(b.runes) == 0 }
func (b *lineBuffer) Clear()         { b.runes = nil; b.cursor = 0 }
func (b *lineBuffer) Set(s string)   { b.runes = []rune(s); b.cursor = len(b.runes) }

func (b *lineBuffer) InsertString(s string) {
	b.insertRunes([]rune(s))
}

func (b *lineBuffer) InsertRune(r rune) {
	b.insertRunes([]rune{r})
}

func (b *lineBuffer) insertRunes(rs []rune) {
	if len(rs) == 0 {
		return
	}
	out := make([]rune, 0, len(b.runes)+len(rs))
	out = append(out, b.runes[:b.cursor]...)
	out = append(out, rs...)
	out = append(out, b.runes[b.cursor:]...)
	b.runes = out
	b.cursor += len(rs)
}

func (b *lineBuffer) Backspace() bool {
	if b.cursor == 0 {
		return false
	}
	b.runes = append(b.runes[:b.cursor-1], b.runes[b.cursor:]...)
	b.cursor--
	return true
}

func (b *lineBuffer) DeleteChar() bool {
	if b.cursor >= len(b.runes) {
		return false
	}
	b.runes = append(b.runes[:b.cursor], b.runes[b.cursor+1:]...)
	return true
}

func (b *lineBuffer) MoveLeft() bool {
	if b.cursor == 0 {
		return false
	}
	b.cursor--
	return true
}

func (b *lineBuffer) MoveRight() bool {
	if b.cursor >= len(b.runes) {
		return false
	}
	b.cursor++
	return true
}

func (b *lineBuffer) MoveHome() { b.cursor = 0 }
func (b *lineBuffer) MoveEnd()  { b.cursor = len(b.runes) }

func (b *lineBuffer) KillToStart() bool {
	if b.cursor == 0 {
		return false
	}
	b.runes = append([]rune(nil), b.runes[b.cursor:]...)
	b.cursor = 0
	return true
}

func (b *lineBuffer) KillToEnd() bool {
	if b.cursor >= len(b.runes) {
		return false
	}
	b.runes = b.runes[:b.cursor]
	return true
}

func (b *lineBuffer) DeleteWordBefore() bool {
	if b.cursor == 0 {
		return false
	}
	start := b.cursor
	for start > 0 && unicode.IsSpace(b.runes[start-1]) {
		start--
	}
	for start > 0 && !unicode.IsSpace(b.runes[start-1]) {
		start--
	}
	b.runes = append(b.runes[:start], b.runes[b.cursor:]...)
	b.cursor = start
	return true
}

func (b *lineBuffer) MoveWordLeft() bool {
	start := b.cursor
	for start > 0 && unicode.IsSpace(b.runes[start-1]) {
		start--
	}
	for start > 0 && !unicode.IsSpace(b.runes[start-1]) {
		start--
	}
	if start == b.cursor {
		return false
	}
	b.cursor = start
	return true
}

func (b *lineBuffer) MoveWordRight() bool {
	end := b.cursor
	for end < len(b.runes) && unicode.IsSpace(b.runes[end]) {
		end++
	}
	for end < len(b.runes) && !unicode.IsSpace(b.runes[end]) {
		end++
	}
	if end == b.cursor {
		return false
	}
	b.cursor = end
	return true
}

// PrefixWidth returns the visual width of the text before the cursor.
func (b *lineBuffer) PrefixWidth(promptWidth int) int {
	return promptWidth + stringDisplayWidth(string(b.runes[:b.cursor]))
}

// ----------------------------------------------------------------------
// Interactive line editor
// ----------------------------------------------------------------------

// ReadInteractiveLine reads a line with real-time slash command dropdown,
// arrow key navigation, full Unicode support and inline history.
func ReadInteractiveLine(in *os.File, out io.Writer, prompt string) (string, error) {
	return readInteractiveLine(in, out, prompt, nil, false, false)
}

// ReadInteractiveLineWithHistory is ReadInteractiveLine with a history stack.
// Up/Down arrows navigate history whenever the slash menu is not active.
func ReadInteractiveLineWithHistory(in *os.File, out io.Writer, prompt string, history []string) (string, error) {
	return readInteractiveLine(in, out, prompt, history, false, false)
}

// ReadInteractiveLineWithDivider is ReadInteractiveLineWithHistory preceded by
// a horizontal rule above the prompt, visually separating the input row from
// the transcript above it.
func ReadInteractiveLineWithDivider(in *os.File, out io.Writer, prompt string, history []string) (string, error) {
	return readInteractiveLine(in, out, prompt, history, false, true)
}

// detailToggle, when non-nil, handles Ctrl+O at the prompt: it repaints the
// trailing turn transcript with collapsible details (tool outputs and
// reasoning blocks) expanded or collapsed. The argument is the number of
// structural rows (the divider) directly above the cursor that a repaint
// erased and the caller must reprint; it reports whether the transcript above
// the cursor was repainted.
var detailToggle func(extraAbove int) bool

// SetDetailToggle registers the Ctrl+O handler used to expand and collapse
// tool-output and thought detail blocks in the transcript.
func SetDetailToggle(fn func(extraAbove int) bool) { detailToggle = fn }

// SetThoughtToggle is retained as a compatibility alias for SetDetailToggle;
// Ctrl+O now toggles every collapsible detail block.
func SetThoughtToggle(fn func(extraAbove int) bool) { SetDetailToggle(fn) }

// promptFooter, when non-nil, supplies extra rows drawn right below the
// input box (under the bottom rule), such as the status bar. Lines are
// right-aligned and truncated to the terminal width.
var promptFooter func() []string

// SetPromptFooter registers the provider for the rows shown below the main
// input box.
func SetPromptFooter(fn func() []string) { promptFooter = fn }

// ctrlCExitHint is the dynamic status-line hint shown after the first Ctrl+C
// press on an empty prompt; a second consecutive press exits the CLI.
const ctrlCExitHint = "Press Ctrl+C again to exit"

// ctrlCArmAfterRun carries the double-press exit hint across the boundary
// between an interrupted agent run and the next prompt: main arms it when
// Ctrl+C cancels a run, and the next prompt starts with the hint visible.
var ctrlCArmAfterRun bool

// ArmCtrlCExitHint marks that the next prompt should show the double-press
// exit hint because Ctrl+C just interrupted an active run.
func ArmCtrlCExitHint() { ctrlCArmAfterRun = true }

// ReadSecretLine reads one line without echoing the typed characters (they
// are masked with '*'), for secrets such as API keys.
func ReadSecretLine(in *os.File, out io.Writer, prompt string) (string, error) {
	return readInteractiveLine(in, out, prompt, nil, true, false)
}

func readInteractiveLine(in *os.File, out io.Writer, prompt string, history []string, secret, divider bool) (string, error) {
	reader, restore, err := newKeyReader(in, out)
	if err != nil {
		return "", err
	}
	defer restore()
	defer reader.Close()

	buf := &lineBuffer{}
	selectedIdx := 0
	activeMenuLines := 0
	histIdx := len(history)
	draft := ""
	promptWidth := stringDisplayWidth(prompt)

	// The main prompt is framed as a box: a rule above (the divider), the
	// input row, then a rule below followed by optional footer rows (the
	// status bar). Footer rows are right-aligned and truncated to fit.
	var footerRows []string
	footerWidth := 0
	dividerShown := divider && isTerminal(in.Fd())
	if dividerShown {
		footerWidth = resolveTerminalWidth(in)
		if promptFooter != nil {
			footerRows = BuildFooterRows(promptFooter(), footerWidth)
		} else {
			footerRows = BuildFooterRows(nil, footerWidth)
		}
	}

	// The exit hint temporarily replaces the last footer row (the status
	// bar) so the double-press affordance lives in the status line itself.
	var hintBase string
	hintShown := false
	showHint := func() {
		if hintShown || len(footerRows) < 2 {
			return
		}
		hintBase = footerRows[len(footerRows)-1]
		txt := truncateANSI(ansi("33", ctrlCExitHint), footerWidth)
		pad := footerWidth - displayWidthANSI(txt)
		if pad < 0 {
			pad = 0
		}
		footerRows[len(footerRows)-1] = strings.Repeat(" ", pad) + txt
		hintShown = true
	}
	hideHint := func() {
		if !hintShown {
			return
		}
		footerRows[len(footerRows)-1] = hintBase
		hintShown = false
	}

	// drawFooter paints the rows below the input row and returns the cursor
	// to the input row at the given column.
	drawFooter := func(col int) {
		for _, ln := range footerRows {
			fmt.Fprintf(out, "\n\r\x1b[2K%s", ln)
		}
		fmt.Fprintf(out, "\x1b[%dA\r", len(footerRows))
		if col > 0 {
			fmt.Fprintf(out, "\x1b[%dC", col)
		}
	}

	// eraseFooter clears the rows below the input row and returns the cursor
	// to the input row.
	eraseFooter := func() {
		for range footerRows {
			fmt.Fprint(out, "\n\r\x1b[2K")
		}
		fmt.Fprintf(out, "\x1b[%dA\r", len(footerRows))
	}

	// clearBelow clears n rows starting at the cursor row and leaves the
	// cursor on the first cleared row.
	clearBelow := func(n int) {
		for i := 0; i < n; i++ {
			fmt.Fprint(out, "\r\x1b[2K")
			if i < n-1 {
				fmt.Fprint(out, "\n")
			}
		}
		if n > 1 {
			fmt.Fprintf(out, "\x1b[%dA", n-1)
		}
		fmt.Fprint(out, "\r")
	}

	// eraseBox removes the whole prompt box (footer rows below the input
	// row, the input row itself and the divider above it) and parks the
	// cursor on the topmost erased row, leaving a clean screen behind so
	// the caller can print the exit line over it.
	eraseBox := func() {
		if len(footerRows) > 0 {
			eraseFooter()
		}
		fmt.Fprint(out, "\r\x1b[2K")
		if dividerShown {
			fmt.Fprint(out, "\x1b[1A\r\x1b[2K")
		}
	}

	clearMenu := func() {
		if activeMenuLines == 0 {
			return
		}
		for i := 0; i < activeMenuLines; i++ {
			fmt.Fprintf(out, "\n\r\x1b[2K")
		}
		fmt.Fprintf(out, "\x1b[%dA\r", activeMenuLines)
		activeMenuLines = 0
	}

	menuMatches := func() []SlashCommand {
		current := buf.String()
		if !strings.HasPrefix(current, "/") {
			return nil
		}
		return MatchSlashCommands(current)
	}

	// visible returns the text to draw/echo: the raw input, or '*' per rune
	// when reading a secret so it never hits the terminal.
	visible := func() string {
		if secret {
			return strings.Repeat("*", buf.Len())
		}
		return buf.String()
	}

	render := func(withMenu bool) {
		clearMenu()
		current := visible()
		width := resolveTerminalWidth(in)
		view := width - 1
		if view < 8 {
			view = 8
		}

		// Render the prompt+input on a single physical row by scrolling it
		// horizontally, so the menu row accounting below stays exact even for
		// long input or narrow terminals.
		total := promptWidth + stringDisplayWidth(current)
		col := promptWidth + buf.Cursor()
		if !secret {
			col = buf.PrefixWidth(promptWidth)
		}
		start := windowStart(total, col, view)
		var text string
		cursorCol := col
		if start > 0 {
			text = "…" + sliceByColumns(prompt+current, start, view-1)
			cursorCol = col - start + 1
		} else {
			text = sliceByColumns(prompt+current, 0, view)
		}
		if cursorCol > view-1 {
			cursorCol = view - 1
		}
		if cursorCol < 0 {
			cursorCol = 0
		}

		// 1. Redraw prompt + input.
		fmt.Fprintf(out, "\r\x1b[2K%s", text)

		// 2. Reposition cursor at its logical column.
		if cursorCol > 0 {
			fmt.Fprintf(out, "\r\x1b[%dC", cursorCol)
		} else {
			fmt.Fprint(out, "\r")
		}

		// 3. Slash dropdown below; every line is truncated to fit one row.
		// The dropdown temporarily replaces the footer rows.
		matches := menuMatches()
		if !withMenu {
			matches = nil
		}
		if len(matches) > 0 {
			if len(footerRows) > 0 {
				eraseFooter()
			}
			if selectedIdx >= len(matches) {
				selectedIdx = 0
			}
			if selectedIdx < 0 {
				selectedIdx = len(matches) - 1
			}
			lines := GetSlashDropdownLinesWidth(matches, selectedIdx, 6, view)
			for _, line := range lines {
				line = truncateANSI(line, view)
				fmt.Fprintf(out, "\n\r\x1b[2K%s", line)
			}
			fmt.Fprintf(out, "\x1b[%dA\r", len(lines))
			if cursorCol > 0 {
				fmt.Fprintf(out, "\x1b[%dC", cursorCol)
			}
			activeMenuLines = len(lines)
		} else if len(footerRows) > 0 {
			// 4. Repaint the footer rows below the input row.
			drawFooter(cursorCol)
		}
	}

	// An interrupted run hands the exit hint over to the next prompt.
	ctrlCArmed := ctrlCArmAfterRun
	ctrlCArmAfterRun = false
	if ctrlCArmed {
		showHint()
	}

	// Initial prompt draw, optionally preceded by a horizontal rule that
	// separates the input row from the transcript above it.
	if dividerShown {
		fmt.Fprintf(out, "%s\n", footerRows[0])
	}
	fmt.Fprintf(out, "\r\x1b[2K%s", prompt)
	if len(footerRows) > 0 {
		drawFooter(promptWidth)
	}

	for {
		ev, err := reader.ReadKey()
		if err != nil {
			return buf.String(), err
		}

		// Any key other than Ctrl+C dismisses the exit hint.
		if ctrlCArmed && ev.Type != KeyCtrlC {
			ctrlCArmed = false
			hideHint()
			render(true)
		}

		switch ev.Type {
		case KeyLine:
			return ev.Text, nil

		case KeyExit:
			// Ctrl+D forward-deletes while text is present and only exits
			// when the prompt is empty (matching the product).
			if !buf.IsEmpty() {
				if buf.DeleteChar() {
					selectedIdx = 0
					render(true)
				}
				break
			}
			clearMenu()
			ctrlCArmed = false
			eraseBox()
			return "/exit", nil

		case KeyCtrlC:
			// First press cancels the draft; a second consecutive press on
			// an empty prompt exits the CLI.
			if !buf.IsEmpty() {
				buf.Clear()
				selectedIdx = 0
				render(true)
				break
			}
			if ctrlCArmed {
				clearMenu()
				ctrlCArmed = false
				eraseBox()
				return "/exit", nil
			}
			ctrlCArmed = true
			showHint()
			render(true)

		case KeyEnter:
			current := buf.String()
			if strings.HasPrefix(current, "/") {
				matches := MatchSlashCommands(current)
				if len(matches) > 0 && selectedIdx < len(matches) {
					if current == "/" || len(strings.Fields(current)) == 1 {
						buf.Set(matches[selectedIdx].Name)
					}
				}
			}
			clearMenu()
			echo := prompt + visible()
			if divider && !secret {
				// The submitted main-prompt line is re-rendered in the
				// transcript as a blue "❯ input" row.
				echo = ansi("94", "❯ "+current)
			}
			fmt.Fprintf(out, "\r\x1b[2K%s\n", echo)
			if len(footerRows) > 0 {
				clearBelow(len(footerRows))
			}
			return buf.String(), nil

		case KeyTab:
			if matches := menuMatches(); len(matches) > 0 {
				selectedIdx = (selectedIdx + 1) % len(matches)
				render(true)
			}

		case KeyEsc:
			clearMenu()
			if len(footerRows) > 0 {
				render(false)
			}

		case KeyPaste:
			text := strings.ReplaceAll(ev.Text, "\r\n", " ")
			text = strings.ReplaceAll(text, "\n", " ")
			text = strings.ReplaceAll(text, "\r", " ")
			text = strings.ReplaceAll(text, "\t", "    ")
			buf.InsertString(text)
			render(true)

		case KeyChar:
			if ev.Char >= 32 && ev.Char != 127 {
				buf.InsertRune(ev.Char)
				selectedIdx = 0
				render(true)
			}

		case KeyBackspace:
			if buf.Backspace() {
				selectedIdx = 0
				render(true)
			}

		case KeyDelete:
			if buf.DeleteChar() {
				selectedIdx = 0
				render(true)
			}

		case KeyLeft:
			if buf.MoveLeft() {
				render(true)
			}

		case KeyRight:
			if buf.MoveRight() {
				render(true)
			}

		case KeyWordLeft:
			if buf.MoveWordLeft() {
				render(true)
			}

		case KeyWordRight:
			if buf.MoveWordRight() {
				render(true)
			}

		case KeyHome:
			buf.MoveHome()
			render(true)

		case KeyEnd:
			buf.MoveEnd()
			render(true)

		case KeyCtrlU:
			if buf.KillToStart() {
				selectedIdx = 0
				render(true)
			}

		case KeyCtrlK:
			if buf.KillToEnd() {
				selectedIdx = 0
				render(true)
			}

		case KeyCtrlW:
			if buf.DeleteWordBefore() {
				selectedIdx = 0
				render(true)
			}

		case KeyCtrlL:
			activeMenuLines = 0
			fmt.Fprint(out, "\x1b[2J\x1b[H")
			render(true)

		case KeyCtrlO:
			if detailToggle == nil {
				break
			}
			clearMenu()
			fmt.Fprint(out, "\r\x1b[2K")
			extra := 0
			if dividerShown {
				extra = 1
			}
			if detailToggle(extra) && dividerShown {
				w := resolveTerminalWidth(in)
				if w < 8 {
					w = 8
				}
				fmt.Fprintf(out, "%s\n", ansi("90", strings.Repeat("─", w)))
			}
			render(true)

		case KeyUp:
			if matches := menuMatches(); len(matches) > 0 {
				selectedIdx = (selectedIdx - 1 + len(matches)) % len(matches)
				render(true)
				break
			}
			if histIdx == len(history) {
				draft = buf.String()
			}
			if histIdx > 0 {
				histIdx--
				buf.Set(history[histIdx])
				selectedIdx = 0
				render(true)
			}

		case KeyDown:
			if matches := menuMatches(); len(matches) > 0 {
				selectedIdx = (selectedIdx + 1) % len(matches)
				render(true)
				break
			}
			if histIdx < len(history) {
				histIdx++
				if histIdx == len(history) {
					buf.Set(draft)
				} else {
					buf.Set(history[histIdx])
				}
				selectedIdx = 0
				render(true)
			}
		}
	}
}

// ----------------------------------------------------------------------
// Single-key confirmation (used by the tool-approval gate)
// ----------------------------------------------------------------------

// ConfirmKey reads a single key press and returns true for approve
// (y/Y/Enter), false for reject (n/N/Esc/Ctrl+C).
func ConfirmKey(in *os.File, out io.Writer) (bool, error) {
	return ConfirmKeyPrompt(in, out, "Approve?")
}

// ConfirmKeyPrompt is ConfirmKey with a custom prompt message shown before
// the [y/Enter · n/Esc] hint.
func ConfirmKeyPrompt(in *os.File, out io.Writer, prompt string) (bool, error) {
	ok, _, err := ConfirmKeyPromptTracked(in, out, prompt)
	return ok, err
}

// ConfirmKeyPromptTracked behaves like ConfirmKeyPrompt but also reports how
// many terminal rows the interaction occupied after the cursor row, letting
// callers move the cursor back to a previously printed line.
func ConfirmKeyPromptTracked(in *os.File, out io.Writer, prompt string) (bool, int, error) {
	reader, restore, err := newKeyReader(in, out)
	if err != nil {
		return false, 0, err
	}
	defer restore()
	defer reader.Close()

	promptText := prompt + " [y/Enter approve · n/Esc reject]: "
	fmt.Fprintf(out, "\n%s", promptText)
	// The leading "\n" plus the prompt row(s) (echo appended, then newline)
	// determine how many rows the cursor advanced.
	cols := displayWidthANSI(promptText) + 1
	width := resolveTerminalWidth(in)
	if width < 1 {
		width = 80
	}
	rows := 1 + (cols+width-1)/width

	decide := func(ok bool, echo string) (bool, int, error) {
		fmt.Fprintf(out, "%s\n", echo)
		return ok, rows, nil
	}
	for {
		ev, err := reader.ReadKey()
		if err != nil {
			return false, 0, err
		}
		switch ev.Type {
		case KeyChar:
			switch ev.Char {
			case 'y', 'Y':
				return decide(true, "y")
			case 'n', 'N':
				return decide(false, "n")
			}
		case KeyEnter:
			return decide(true, "y")
		case KeyEsc, KeyCtrlC, KeyExit:
			return decide(false, "n")
		case KeyLine:
			t := strings.ToLower(strings.TrimSpace(ev.Text))
			if t == "y" || t == "yes" {
				return decide(true, "y")
			}
			if t == "n" || t == "no" || t == "" {
				return decide(false, "n")
			}
		}
	}
}

// FoldText folds a long tool output into a compact preview. It returns the
// folded text plus the number of hidden trailing lines.
func FoldText(s string, maxLines, maxWidth int) (string, int) {
	s = strings.TrimRight(s, "\r\n")
	if s == "" {
		return "(empty)", 0
	}
	lines := strings.Split(s, "\n")
	hidden := 0
	if maxLines > 0 && len(lines) > maxLines {
		hidden = len(lines) - maxLines
		lines = lines[:maxLines]
	}
	var sb strings.Builder
	for i, l := range lines {
		if maxWidth > 0 && stringDisplayWidth(l) > maxWidth {
			l = truncateANSI(l, maxWidth)
		}
		if i > 0 {
			sb.WriteString("\n  ")
		}
		sb.WriteString(l)
	}
	return sb.String(), hidden
}
