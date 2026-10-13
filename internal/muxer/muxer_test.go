package muxer

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asticode/go-astiav"

	"chaptersmith/internal/meta"
)

func finalizeWith(t *testing.T, md meta.AudiobookMetadata) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out.m4b")
	opts := Options{InputFile: "../../testdata/formats/m4a/01.m4a", OutputFile: out, Metadata: md}
	if err := Finalize(context.Background(), opts, log.New(io.Discard, "", 0)); err != nil {
		t.Fatal(err)
	}
	return out
}

// rawTags returns the container-level tags exactly as libav reads them back.
func rawTags(t *testing.T, path string) map[string]string {
	t.Helper()
	fc := astiav.AllocFormatContext()
	defer fc.Free()
	if err := fc.OpenInput(path, nil, nil); err != nil {
		t.Fatal(err)
	}
	defer fc.CloseInput()
	tags := map[string]string{}
	var prev *astiav.DictionaryEntry
	for {
		e := fc.Metadata().Get("", prev, astiav.NewDictionaryFlags(astiav.DictionaryFlagIgnoreSuffix))
		if e == nil {
			return tags
		}
		prev = e
		tags[e.Key()] = e.Value()
	}
}

func TestFinalizeWritesDescriptionAsSynopsis(t *testing.T) {
	long := strings.Repeat("Long description. ", 30)

	tags := rawTags(t, finalizeWith(t, meta.AudiobookMetadata{Title: "T", Description: long}))

	if tags["synopsis"] != long {
		t.Fatalf("synopsis: got %d chars, want %d", len(tags["synopsis"]), len(long))
	}
}

func TestFinalizeSkipsCommentDuplicatingDescription(t *testing.T) {

	tags := rawTags(t, finalizeWith(t, meta.AudiobookMetadata{Title: "T", Comment: "Same", Description: "Same"}))

	if _, ok := tags["comment"]; ok {
		t.Fatal("comment identical to description should not be written")
	}
}

func TestReadEmbeddedMetadataPrefersSynopsis(t *testing.T) {
	out := finalizeWith(t, meta.AudiobookMetadata{
		Title:       "T",
		Description: "short",
		Extra:       map[string]string{"synopsis": "the full long synopsis"},
	})

	m, err := meta.ReadEmbeddedMetadata(out)
	if err != nil {
		t.Fatal(err)
	}
	if m.Description != "the full long synopsis" {
		t.Fatalf("description: got %q", m.Description)
	}
}

func TestReadEmbeddedMetadataSkipsTrackAndDisc(t *testing.T) {
	out := finalizeWith(t, meta.AudiobookMetadata{
		Title: "T",
		Extra: map[string]string{"track": "3/20", "disc": "1/2"},
	})
	if tags := rawTags(t, out); tags["track"] == "" {
		t.Fatal("precondition: sample output should carry a track tag")
	}

	m, err := meta.ReadEmbeddedMetadata(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Extra["track"]; ok {
		t.Fatal("track should not be carried over")
	}
	if _, ok := m.Extra["disc"]; ok {
		t.Fatal("disc should not be carried over")
	}
}
