package meta

import (
	"testing"
)

func TestReadEmbeddedMetadataTaggedFixture(t *testing.T) {

	m, err := ReadEmbeddedMetadata(taggedAudioPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Fixture Book" {
		t.Fatalf("title: got %q", m.Title)
	}
	if m.Artist != "Fixture Artist" {
		t.Fatalf("artist: got %q", m.Artist)
	}
	if m.Composer != "Fixture Narrator" {
		t.Fatalf("composer: got %q", m.Composer)
	}
	if m.Genre == "" || m.Genre == "Audiobook" {
		t.Fatalf("genre should keep source value, got %q", m.Genre)
	}
	if m.Copyright == "" {
		t.Fatal("copyright missing")
	}
	if m.Date == "" {
		t.Fatal("date missing")
	}
	if m.Comment == "" && m.Description == "" {
		t.Fatal("comment/description missing")
	}
	if len(m.Extra) == 0 {
		t.Fatal("expected extra tags (series, language, etc.)")
	}
	t.Logf("genre=%q copyright=%q date=%q comment=%d desc=%d extra=%d",
		m.Genre, m.Copyright, m.Date, len(m.Comment), len(m.Description), len(m.Extra))
}
