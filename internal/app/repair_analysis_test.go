package app

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

// An undecodable source proves the gate runs before probing/metadata/encoding.
func TestRepairBeforeFixGate(t *testing.T) {
	for _, fixMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "prompt", true: "cli"}[fixMode], func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "bad.mp3")
			os.WriteFile(source, []byte("undecodable"), 0600)
			sentinel := errors.New("gate stopped")
			calls := 0
			prompts := 0
			a := New(Config{InputDir: dir, OutputPath: filepath.Join(dir, "out.m4b"), FixMode: fixMode, Progress: &ProgressReporter{OnPromptFix: func(string) string { prompts++; return "fix" }, OnBeforeFix: func(ctx context.Context, path string) error {
				calls++
				canonical, _ := filepath.EvalSymlinks(source)
				if path != canonical {
					t.Error(path)
				}
				return sentinel
			}}}, log.New(io.Discard, "", 0))
			if err := a.Run(context.Background()); !errors.Is(err, sentinel) || calls != 1 {
				t.Fatalf("gate not first: %v calls=%d", err, calls)
			}
			if fixMode && prompts != 0 {
				t.Fatal("CLI prompted")
			}
			if _, err := os.Stat(filepath.Join(dir, "out.m4b")); !os.IsNotExist(err) {
				t.Fatal("output before gate")
			}
		})
	}
}
