//go:build !windows

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

type termios struct {
	Iflag uint32
	Oflag uint32
	Cflag uint32
	Lflag uint32
	Line  uint8
	Cc    [32]uint8
	Pad   [3]byte
}

const (
	tcgets     = 0x5401
	tcsets     = 0x5402
	tcflush    = 0x540b
	tciflash   = 0
	tiocgwinsz = 0x5413
)

func isTerminal(fd uintptr) bool {
	var t termios
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(tcgets), uintptr(unsafe.Pointer(&t)))
	return err == 0
}

// SuppressRunEcho disables terminal echo for the duration of an agent run.
// Without it, pressing Enter while the model is thinking echoes a newline that
// breaks the spinner's single-line redraw (leaving ghost "thinking…" rows) and
// queues a phantom empty submission for the next prompt. ISIG is kept so
// Ctrl+C still interrupts the run. Returns a restore function (no-op when the
// input is not a terminal).
func SuppressRunEcho(in *os.File) func() {
	if in == nil || !isTerminal(in.Fd()) {
		return func() {}
	}
	var old termios
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, in.Fd(), uintptr(tcgets), uintptr(unsafe.Pointer(&old))); err != 0 {
		return func() {}
	}
	silent := old
	silent.Lflag &^= syscall.ECHO
	if _, _, err := syscall.Syscall(syscall.SYS_IOCTL, in.Fd(), uintptr(tcsets), uintptr(unsafe.Pointer(&silent))); err != 0 {
		return func() {}
	}
	return func() {
		restored := old
		syscall.Syscall(syscall.SYS_IOCTL, in.Fd(), uintptr(tcsets), uintptr(unsafe.Pointer(&restored)))
	}
}

// FlushPendingInput discards keystrokes buffered while the agent was running
// (or while a transient tool-approval prompt was pending) so they can neither
// submit a phantom prompt nor answer the next interactive question.
func FlushPendingInput(in *os.File) {
	if in == nil || !isTerminal(in.Fd()) {
		return
	}
	syscall.Syscall(syscall.SYS_IOCTL, in.Fd(), uintptr(tcflush), uintptr(tciflash))
}

// consoleWidth queries the terminal window width in columns.
func consoleWidth(fd uintptr) int {
	var ws struct{ Row, Col, Xpixel, Ypixel uint16 }
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(tiocgwinsz), uintptr(unsafe.Pointer(&ws)))
	if err == 0 && ws.Col > 0 {
		return int(ws.Col)
	}
	return 0
}

// consoleHeight queries the terminal window height in rows.
func consoleHeight(fd uintptr) int {
	var ws struct{ Row, Col, Xpixel, Ypixel uint16 }
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(tiocgwinsz), uintptr(unsafe.Pointer(&ws)))
	if err == 0 && ws.Row > 0 {
		return int(ws.Row)
	}
	return 0
}

type posixKeyReader struct {
	core *keyCore
}

func newKeyReader(in *os.File, out io.Writer) (keyReader, func(), error) {
	if !isTerminal(in.Fd()) {
		return &fallbackLineReader{scanner: bufio.NewScanner(in)}, func() {}, nil
	}

	// Drop any keystrokes typed while the agent was streaming; they belong to
	// the previous (non-interactive) phase, not to this prompt.
	FlushPendingInput(in)

	var old termios
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, in.Fd(), uintptr(tcgets), uintptr(unsafe.Pointer(&old)))
	if err != 0 {
		return &fallbackLineReader{scanner: bufio.NewScanner(in)}, func() {}, nil
	}

	raw := old
	// ISIG must be cleared too: otherwise the tty turns Ctrl+C into SIGINT
	// and kills the process before the byte can reach the key decoder.
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG
	raw.Iflag &^= syscall.IXON | syscall.ICRNL
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0

	_, _, err = syscall.Syscall(syscall.SYS_IOCTL, in.Fd(), uintptr(tcsets), uintptr(unsafe.Pointer(&raw)))
	if err != 0 {
		return &fallbackLineReader{scanner: bufio.NewScanner(in)}, func() {}, nil
	}

	// Enable bracketed paste so multi-line pastes arrive as a single KeyPaste.
	if colorEnabled {
		fmt.Fprint(out, "\x1b[?2004h")
	}

	restore := func() {
		if colorEnabled {
			fmt.Fprint(out, "\x1b[?2004l")
		}
		syscall.Syscall(syscall.SYS_IOCTL, in.Fd(), uintptr(tcsets), uintptr(unsafe.Pointer(&old)))
	}

	return &posixKeyReader{core: newKeyCore(in)}, restore, nil
}

func (p *posixKeyReader) ReadKey() (KeyEvent, error) {
	return p.core.ReadKey()
}

func (p *posixKeyReader) Close() {}

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
