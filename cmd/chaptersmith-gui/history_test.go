package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryPathKeepsExistingHistory(t *testing.T) {
	config := t.TempDir()
	legacy := filepath.Join(config, "audiobookmerger", "data.db")
	current := filepath.Join(config, "chaptersmith", "data.db")
	if got := historyPath(config); got != current {
		t.Fatalf("new install path: %s", got)
	}
	if err := os.MkdirAll(filepath.Dir(legacy), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("existing database"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := historyPath(config); got != legacy {
		t.Fatalf("existing history hidden by rebrand: %s", got)
	}
	if err := os.MkdirAll(filepath.Dir(current), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte("new database"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := historyPath(config); got != current {
		t.Fatalf("ChapterSmith history should take priority: %s", got)
	}
}
