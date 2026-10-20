package encoder

import (
	"bytes"
	"testing"
)

func TestGetPNGDimensions(t *testing.T) {
	// Minimal mock PNG: 8 bytes magic + 4 length + 4 "IHDR" + 4 width (800) + 4 height (600) + ...
	mockPNG := []byte{
		0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', // magic
		0x00, 0x00, 0x00, 0x0d, // chunk len 13
		'I', 'H', 'D', 'R', // "IHDR"
		0x00, 0x00, 0x03, 0x20, // width 800
		0x00, 0x00, 0x02, 0x58, // height 600
		0x08, 0x06, 0x00, 0x00, 0x00, // bit depth, color type, etc.
		0x00, 0x00, 0x00, 0x00, // CRC
	}

	w, h, err := GetPNGDimensions(mockPNG)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 800 || h != 600 {
		t.Fatalf("expected 800x600, got %dx%d", w, h)
	}
}

func TestSplitPNGFrames(t *testing.T) {
	frame1 := append(pngHeader, []byte("...IEND1234")...)
	frame2 := append(pngHeader, []byte("...IEND5678")...)
	stream := append(frame1, frame2...)

	frames, err := SplitPNGFrames(bytes.NewReader(stream))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(frames))
	}
}
