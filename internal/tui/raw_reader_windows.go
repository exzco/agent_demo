//go:build windows

package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode       = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode       = kernel32.NewProc("SetConsoleMode")
	procGetScreenInfo        = kernel32.NewProc("GetConsoleScreenBufferInfo")
	procFlushConsoleInputBuf = kernel32.NewProc("FlushConsoleInputBuffer")
)

const (
	enableProcessedInput       = 0x0001
	enableLineInput            = 0x0002
	enableEchoInput            = 0x0004
	enableVirtualTerminalInput = 0x0200
)

func isTerminal(fd uintptr) bool {
	var mode uint32
	ret, _, _ := procGetConsoleMode.Call(fd, uintptr(unsafe.Pointer(&mode)))
	return ret != 0
}

// SuppressRunEcho disables console echo for the duration of an agent run so
// stray keystrokes (e.g. Enter while the model is thinking) cannot break the
// spinner's single-line layout. Returns a restore function (no-op when the
// input is not a console).
func SuppressRunEcho(in *os.File) func() {
	if in == nil || !isTerminal(in.Fd()) {
		return func() {}
	}
	fd := in.Fd()
	var old uint32
	if ret, _, _ := procGetConsoleMode.Call(fd, uintptr(unsafe.Pointer(&old))); ret == 0 {
		return func() {}
	}
	procSetConsoleMode.Call(fd, uintptr(old&^enableEchoInput))
	return func() {
		procSetConsoleMode.Call(fd, uintptr(old))
	}
}

// FlushPendingInput discards keystrokes buffered while the agent was running
// so they cannot submit a phantom prompt afterwards.
func FlushPendingInput(in *os.File) {
	if in == nil || !isTerminal(in.Fd()) {
		return
	}
	procFlushConsoleInputBuf.Call(in.Fd())
}

type consoleCoord struct{ X, Y int16 }

type consoleRect struct{ Left, Top, Right, Bottom int16 }

// consoleWidth queries the visible console window width in columns.
func consoleWidth(fd uintptr) int {
	var info struct {
		Size       consoleCoord
		Cursor     consoleCoord
		Attributes uint16
		Window     consoleRect
		Max        consoleCoord
	}
	ret, _, _ := procGetScreenInfo.Call(fd, uintptr(unsafe.Pointer(&info)))
	if ret != 0 && info.Window.Right > info.Window.Left {
		return int(info.Window.Right-info.Window.Left) + 1
	}
	return 0
}

// consoleHeight queries the visible console window height in rows.
func consoleHeight(fd uintptr) int {
	var info struct {
		Size       consoleCoord
		Cursor     consoleCoord
		Attributes uint16
		Window     consoleRect
		Max        consoleCoord
	}
	ret, _, _ := procGetScreenInfo.Call(fd, uintptr(unsafe.Pointer(&info)))
	if ret != 0 && info.Window.Bottom > info.Window.Top {
		return int(info.Window.Bottom-info.Window.Top) + 1
	}
	return 0
}

type windowsKeyReader struct {
	core *keyCore
}

func newKeyReader(in *os.File, out io.Writer) (keyReader, func(), error) {
	fd := in.Fd()
	var oldMode uint32
	ret, _, _ := procGetConsoleMode.Call(fd, uintptr(unsafe.Pointer(&oldMode)))

	if ret != 0 {
		// Drop keystrokes typed while the agent was streaming: they belong
		// to the previous (non-interactive) phase, not to this prompt.
		FlushPendingInput(in)
		// Real Windows console: enable virtual terminal input, disable line
		// input for single key events and processed input so Ctrl+C arrives
		// as a key event instead of a CTRL_C_EVENT signal.
		rawMode := (oldMode &^ (enableLineInput | enableEchoInput | enableProcessedInput)) | enableVirtualTerminalInput
		procSetConsoleMode.Call(fd, uintptr(rawMode))

		// Enable bracketed paste for single-event multi-line pastes.
		if colorEnabled {
			fmt.Fprint(out, "\x1b[?2004h")
		}

		restore := func() {
			if colorEnabled {
				fmt.Fprint(out, "\x1b[?2004l")
			}
			procSetConsoleMode.Call(fd, uintptr(oldMode))
		}
		return &windowsKeyReader{core: newKeyCore(in)}, restore, nil
	}

	// Git Bash / Mintty / Pipe fallback: read line by line with full UTF-8 / Chinese support
	return &fallbackLineReader{scanner: bufio.NewScanner(in)}, func() {}, nil
}

func (w *windowsKeyReader) ReadKey() (KeyEvent, error) {
	return w.core.ReadKey()
}

func (w *windowsKeyReader) Close() {}

type fallbackLineReader struct {
	scanner *bufio.Scanner
}

func (f *fallbackLineReader) ReadKey() (KeyEvent, error) {
	if !f.scanner.Scan() {
		if err := f.scanner.Err(); err != nil {
			return KeyEvent{Type: KeyExit}, err
		}
		return KeyEvent{Type: KeyExit}, io.EOF
	}
	text := f.scanner.Text()
	return KeyEvent{Type: KeyLine, Text: strings.TrimSpace(text)}, nil
}

func (f *fallbackLineReader) Close() {}
