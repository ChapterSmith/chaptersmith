package muxer

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func TestFinalizeCancellationPreservesExistingOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(out, []byte("previous audiobook"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Finalize(ctx, Options{InputFile: "../../testdata/formats/m4a/01.m4a", OutputFile: out}, log.New(io.Discard, "", 0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "previous audiobook" {
		t.Fatal("cancelled finalize destroyed existing output")
	}
	entries, err := os.ReadDir(filepath.Dir(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files left after failure: %v", entries)
	}
}
