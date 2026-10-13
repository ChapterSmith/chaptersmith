package book

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSourceChanged(t *testing.T) {
	d := t.TempDir()
	a := importCopy(t, d, "01.mp3", "mp3/01.mp3")
	b := importCopy(t, d, "02.mp3", "mp3/02.mp3")
	c := importCopy(t, d, "03.mp3", "mp3/01.mp3")
	p := importBook(t, ImportOptions{InputPath: d})
	saved := Clone(p)
	if err := CheckSources(p); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(a, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write([]byte("changed"))
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(b, st.ModTime(), st.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(c); err != nil {
		t.Fatal(err)
	}
	err = CheckSources(p)
	if err == nil {
		t.Fatal("changed sources accepted")
	}
	if !reflect.DeepEqual(saved, p) {
		t.Fatal("source fingerprints changed")
	}
	for _, path := range p.Sources {
		if !strings.Contains(err.Error(), path.Path) {
			t.Fatalf("missing %s in %v", path.Path, err)
		}
	}
}
