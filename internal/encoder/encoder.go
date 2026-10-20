package encoder

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

var pngHeader = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// GetPNGDimensions reads the IHDR chunk from a PNG buffer and returns width, height.
func GetPNGDimensions(data []byte) (int, int, error) {
	if len(data) < 24 {
		return 0, 0, errors.New("data too short for PNG header")
	}
	if !bytes.Equal(data[:8], pngHeader) {
		return 0, 0, errors.New("invalid PNG magic header")
	}
	// IHDR chunk starts at byte 8 (4 bytes length, 4 bytes chunk type "IHDR")
	if string(data[12:16]) != "IHDR" {
		return 0, 0, errors.New("missing IHDR chunk")
	}
	w := binary.BigEndian.Uint32(data[16:20])
	h := binary.BigEndian.Uint32(data[20:24])
	return int(w), int(h), nil
}

// SplitPNGFrames parses a concatenated stream of PNG images into individual frames.
func SplitPNGFrames(r io.Reader) ([][]byte, error) {
	buf, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var frames [][]byte
	idx := 0
	for {
		next := bytes.Index(buf[idx:], pngHeader)
		if next == -1 {
			break
		}
		start := idx + next
		// Find end of this PNG: IEND chunk is 49 45 4E 44 followed by 4 bytes CRC
		iend := bytes.Index(buf[start:], []byte("IEND"))
		if iend == -1 {
			// Incomplete trailing frame
			break
		}
		end := start + iend + 8 // "IEND" + 4 bytes CRC
		frames = append(frames, buf[start:end])
		idx = end
	}
	return frames, nil
}

// GetChromeExecutablePath locates the Chrome browser executable.
func GetChromeExecutablePath() (string, error) {
	if runtime.GOOS == "windows" {
		candidates := []string{
			filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("LocalAppData"), "Google", "Chrome", "Application", "chrome.exe"),
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
		return exec.LookPath("chrome.exe")
	}

	// Linux / macOS
	for _, bin := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if path, err := exec.LookPath(bin); err == nil {
			return path, nil
		}
	}
	return "", errors.New("chrome executable not found")
}

// BrowserConfig reads the browser configuration.
type BrowserConfig struct {
	WebSocketURL string
	Headless     bool
}

func GetBrowserConfig() BrowserConfig {
	ws := os.Getenv("AGY_BROWSER_WS_URL")
	return BrowserConfig{
		WebSocketURL: ws,
		Headless:     ws == "",
	}
}
