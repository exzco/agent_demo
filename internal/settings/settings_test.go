package settings

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := Settings{
		GatewayURL: "https://api.example.com",
		APIKey:     "sk-test-123456",
		Model:      "deepseek-chat",
	}
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", got, want)
	}
}

func TestSaveFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced on windows")
	}
	t.Setenv("HOME", t.TempDir())
	if err := Save(Settings{APIKey: "secret"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Fatalf("settings file must be 0600, got %o", perm)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := Load()
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if got != (Settings{}) {
		t.Fatalf("want zero settings, got %+v", got)
	}
}

func TestSaveAtomicOverwrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := Save(Settings{GatewayURL: "https://one"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Settings{GatewayURL: "https://two", Model: "m2"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.GatewayURL != "https://two" || got.Model != "m2" {
		t.Fatalf("overwrite mismatch: %+v", got)
	}
	if strings.Contains(readFile(t, Path()), "https://one") {
		t.Fatalf("stale content left in file: %s", readFile(t, Path()))
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return string(b)
}
