package meta

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

const taggedAudioPath = "../../testdata/formats/tagged.m4a"

func discardLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseNFOFileLeavesMissingFieldsEmpty(t *testing.T) {
	nfo := writeFile(t, t.TempDir(), "book.nfo", "Title: Only Title\n")

	m, err := parseNFOFile(nfo, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Only Title" {
		t.Fatalf("title: got %q", m.Title)
	}
	if m.Artist != "" || m.Album != "" {
		t.Fatalf("expected empty artist/album, got %q / %q", m.Artist, m.Album)
	}
}

func TestCollectFromSourcesAppliesDefaultsAfterMerge(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "book.info", "Title: From Info\n")

	m, found := collectFromSources(dir, "", discardLogger())
	if !found {
		t.Fatal("expected .info file to count as a metadata source")
	}
	if m.Title != "From Info" {
		t.Fatalf("title: got %q", m.Title)
	}
	if m.Artist != "Unknown Artist" || m.Album != "From Info" || m.Genre != "Audiobook" {
		t.Fatalf("defaults not applied: artist=%q album=%q genre=%q", m.Artist, m.Album, m.Genre)
	}
}

func TestCollectFromSourcesNothingFound(t *testing.T) {
	dir := t.TempDir()

	m, found := collectFromSources(dir, "", discardLogger())
	if found {
		t.Fatal("expected no metadata source")
	}
	if m.Title != filepath.Base(dir) {
		t.Fatalf("title should default to dir name, got %q", m.Title)
	}
}

func TestCollectFromSourcesNFODoesNotOverrideEmbeddedTags(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "book.NFO", "Title: NFO Title\n")

	m, found := collectFromSources(dir, taggedAudioPath, discardLogger())
	if !found {
		t.Fatal("expected sources to be found")
	}
	if m.Title != "NFO Title" {
		t.Fatalf("NFO title should win, got %q", m.Title)
	}
	if m.Artist != "Fixture Artist" {
		t.Fatalf("embedded artist should survive, got %q", m.Artist)
	}
	if m.Genre == "" || m.Genre == "Audiobook" {
		t.Fatalf("embedded genre should survive, got %q", m.Genre)
	}
	if m.Copyright == "" {
		t.Fatal("embedded copyright should survive")
	}
}
