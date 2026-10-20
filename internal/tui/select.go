package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// SelectItem is one row in an interactive selectable list.
type SelectItem struct {
	Key   string // returned by RunSelect when the row is chosen
	Label string // primary text
	Tag   string // optional right-aligned secondary text
	Group string // optional group header shown above this item
}

// selectMaxVisible bounds how many item rows are drawn at once.
const selectMaxVisible = 8

// filterSelectItems returns the items whose label or tag contains query
// (case-insensitive). An empty query returns all items unchanged.
func filterSelectItems(items []SelectItem, query string) []SelectItem {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return items
	}
	out := make([]SelectItem, 0, len(items))
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.Label), q) || strings.Contains(strings.ToLower(it.Tag), q) {
			out = append(out, it)
		}
	}
	return out
}

// selectRow renders one item row: label left, optional tag right-aligned.
// The selected row is highlighted with a full-width background when colors
// are enabled, or a "> " marker otherwise.
func selectRow(it SelectItem, selected bool, view int) string {
	labelCol := view - 2
	tagW := displayWidthANSI(it.Tag)
	showTag := tagW > 0 && labelCol-tagW >= 8
	if showTag {
		labelCol -= tagW + 1
	}
	label := it.Label
	if displayWidthANSI(label) > labelCol {
		label = truncateANSI(label, labelCol)
	}

	if selected {
		plain := "  " + label
		if showTag {
			plain += strings.Repeat(" ", labelCol-displayWidthANSI(label)+1) + it.Tag
		}
		if colorEnabled {
			return "\x1b[48;5;216m\x1b[1;30m" + padRight(plain, view) + "\x1b[0m"
		}
		return "> " + strings.TrimPrefix(plain, "  ")
	}

	if !colorEnabled {
		row := "  " + padRight(label, labelCol)
		if showTag {
			row += " " + it.Tag
		}
		return row
	}
	row := "  " + ansi("37", padRight(label, labelCol))
	if showTag {
		row += " " + ansi("90", it.Tag)
	}
	return row
}

// buildSelectRows renders the full list body (header with esc hint, search
// line, windowed item rows with group headers, hints) as single-row lines no
// wider than view columns.
func buildSelectRows(title string, items []SelectItem, sel int, query, footer string, view int) []string {
	rows := make([]string, 0, selectMaxVisible+8)

	header := " " + ansi("1;37", title)
	if gap := view - displayWidthANSI(header) - len("esc"); gap >= 1 {
		header += strings.Repeat(" ", gap)
	}
	rows = append(rows, header+ansi("90", "esc"))

	search := " " + ansi("1;37", "Search")
	if query != "" {
		search += " " + ansi("37", query)
	}
	rows = append(rows, search)
	rows = append(rows, "")

	if len(items) == 0 {
		rows = append(rows, ansi("90", "  (no matches)"))
	}

	start := 0
	if sel >= selectMaxVisible {
		start = sel - selectMaxVisible + 1
	}
	end := start + selectMaxVisible
	if end > len(items) {
		end = len(items)
	}
	lastGroup := ""
	for i := start; i < end; i++ {
		it := items[i]
		if it.Group != "" && it.Group != lastGroup {
			rows = append(rows, "", ansi("1;35", " "+it.Group))
			lastGroup = it.Group
		}
		rows = append(rows, selectRow(it, i == sel, view))
	}
	if start > 0 || end < len(items) {
		rows = append(rows, ansi("90", fmt.Sprintf("  showing %d-%d of %d", start+1, end, len(items))))
	}

	if footer != "" {
		rows = append(rows, "", ansi("90", truncateANSI(" "+footer, view)))
	}
	return rows
}

// RunSelect runs an interactive, searchable select list and returns the Key
// of the chosen item. Esc / Ctrl+C cancels and returns "".
func RunSelect(in *os.File, out io.Writer, title string, items []SelectItem, footer string) (string, error) {
	reader, restore, err := newKeyReader(in, out)
	if err != nil {
		return "", err
	}
	defer restore()
	defer reader.Close()

	view := resolveTerminalWidth(in) - 1
	if view < 12 {
		view = 12
	}

	query := ""
	sel := 0
	rows, parked := 0, 0

	clearBlock := func() {
		if rows == 0 {
			return
		}
		if down := rows - 1 - parked; down > 0 {
			fmt.Fprintf(out, "\x1b[%dB", down)
		}
		for i := 0; i < rows; i++ {
			fmt.Fprint(out, "\r\x1b[2K")
			if i < rows-1 {
				fmt.Fprint(out, "\x1b[1A")
			}
		}
		rows, parked = 0, 0
	}

	draw := func() {
		visible := filterSelectItems(items, query)
		if sel >= len(visible) {
			sel = len(visible) - 1
		}
		if sel < 0 {
			sel = 0
		}
		lines := buildSelectRows(title, visible, sel, query, footer, view)

		clearBlock()
		for i, line := range lines {
			if i > 0 {
				fmt.Fprint(out, "\n")
			}
			fmt.Fprintf(out, "\r\x1b[2K%s", truncateANSI(line, view))
		}
		rows = len(lines)
		parked = 1 // keep the cursor on the search row
		if up := rows - 1 - parked; up > 0 {
			fmt.Fprintf(out, "\x1b[%dA", up)
		}
		col := displayWidthANSI(lines[parked])
		if col > view-1 {
			col = view - 1
		}
		if col > 0 {
			fmt.Fprintf(out, "\r\x1b[%dC", col)
		} else {
			fmt.Fprint(out, "\r")
		}
	}

	draw()
	for {
		ev, err := reader.ReadKey()
		if err != nil {
			clearBlock()
			return "", err
		}
		switch ev.Type {
		case KeyEsc, KeyCtrlC, KeyExit:
			clearBlock()
			return "", nil
		case KeyUp:
			if n := len(filterSelectItems(items, query)); n > 0 {
				sel = (sel - 1 + n) % n
			}
			draw()
		case KeyDown:
			if n := len(filterSelectItems(items, query)); n > 0 {
				sel = (sel + 1) % n
			}
			draw()
		case KeyEnter:
			visible := filterSelectItems(items, query)
			if sel >= 0 && sel < len(visible) {
				key := visible[sel].Key
				clearBlock()
				return key, nil
			}
		case KeyBackspace:
			if query != "" {
				_, size := utf8.DecodeLastRuneInString(query)
				query = query[:len(query)-size]
				sel = 0
				draw()
			}
		case KeyCtrlU:
			if query != "" {
				query = ""
				sel = 0
				draw()
			}
		case KeyChar:
			if ev.Char >= 32 && ev.Char != 127 {
				query += string(ev.Char)
				sel = 0
				draw()
			}
		case KeyPaste:
			text := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(ev.Text)
			if text != "" {
				query += text
				sel = 0
				draw()
			}
		}
	}
}
