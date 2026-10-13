package book

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"chaptersmith/internal/audio"
	"chaptersmith/internal/chapter"
	"chaptersmith/internal/muxer"
)

func importWrite(t *testing.T, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func importCopy(t *testing.T, dir, name, fixture string) string {
	t.Helper()
	b, err := os.ReadFile("../../testdata/formats/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	return importWrite(t, dir, name, string(b))
}
func importBook(t *testing.T, opts ImportOptions) BookProject {
	t.Helper()
	if opts.CacheDir == "" {
		opts.CacheDir = t.TempDir()
	}
	p, err := Import(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = Validate(p); err != nil {
		t.Fatal(err)
	}
	return p
}
func importM4B(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := muxer.Finalize(context.Background(), muxer.Options{InputFile: "../../testdata/formats/m4a/01.m4a", OutputFile: p, HasChapters: true, Chapters: []chapter.Chapter{{Title: "First", StartTime: 0}, {Title: "Second", StartTime: 1}}}, log.New(io.Discard, "", 0)); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestImportPlaylistIdentity(t *testing.T) {
	dir := t.TempDir()
	a := importCopy(t, dir, "01.mp3", "mp3/01.mp3")
	b := importCopy(t, dir, "02.mp3", "mp3/02.mp3")
	importWrite(t, dir, "book.M3U8", "#EXTINF:999,Second\n02.mp3\n#EXTINF:1,First\n01.mp3\n#EXTINF:77,Encore\n02.mp3\n")
	p := importBook(t, ImportOptions{InputPath: dir})
	if len(p.Sources) != 2 || len(p.Chapters) != 3 {
		t.Fatalf("project=%+v", p)
	}
	paths := []string{b, a, b}
	seen := map[string]bool{}
	for i, c := range p.Chapters {
		if c.Title != []string{"Second", "First", "Encore"}[i] || seen[c.ID] {
			t.Fatalf("chapters=%+v", p.Chapters)
		}
		seen[c.ID] = true
		s := p.Sources[0]
		for _, v := range p.Sources {
			if v.ID == c.Segments[0].SourceID {
				s = v
			}
		}
		canonical, _ := filepath.EvalSymlinks(paths[i])
		probe, err := audio.ProbeSource(context.Background(), paths[i])
		if err != nil {
			t.Fatal(err)
		}
		if s.Path != canonical || c.Segments[0].EndUS != probe.DurationUS {
			t.Fatalf("source=%+v chapter=%+v", s, c)
		}
	}
	if p.Metadata.Extra == nil || p.Export != (ExportSettings{Profile: "standard", Channels: "auto", OutputMode: "book", Format: "m4b"}) {
		t.Fatalf("defaults=%+v", p)
	}
}
func TestImportTwoEmbeddedM4B(t *testing.T) {
	dir := t.TempDir()
	importM4B(t, dir, "01.m4b")
	importM4B(t, dir, "02.m4b")
	p := importBook(t, ImportOptions{InputPath: dir})
	if len(p.Chapters) != 4 || len(p.Sources) != 2 {
		t.Fatalf("project=%+v", p)
	}
	for i, s := range p.Sources {
		a, b := p.Chapters[i*2], p.Chapters[i*2+1]
		if a.Title != "First" || b.Title != "Second" || a.Segments[0] != (Segment{SourceID: s.ID, EndUS: 1000000}) || b.Segments[0] != (Segment{SourceID: s.ID, StartUS: 1000000, EndUS: s.DurationUS}) {
			t.Fatalf("chapters=%+v", p.Chapters)
		}
	}
}
func TestImportSingleAndFormats(t *testing.T) {
	for _, ext := range []string{"mp3", "m4a", "aac", "ogg", "flac", "wav", "opus", "m4b"} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			var path string
			if ext == "m4b" {
				path = importM4B(t, dir, "01.m4b")
			} else {
				path = importCopy(t, dir, "01."+strings.ToUpper(ext), ext+"/01."+ext)
			}
			p := importBook(t, ImportOptions{InputPath: path})
			if len(p.Sources) != 1 || len(p.Chapters) == 0 || DurationUS(p) <= 0 {
				t.Fatalf("project=%+v", p)
			}
		})
	}
}
func TestImportSelection(t *testing.T) {
	t.Run("playlists", func(t *testing.T) {
		d := t.TempDir()
		importCopy(t, d, "01.mp3", "mp3/01.mp3")
		a := importWrite(t, d, "a.M3u", "01.mp3\n")
		b := importWrite(t, d, "b.m3U8", "01.mp3\n")
		_, err := DiscoverInputs(ImportOptions{InputPath: d}, "")
		if err == nil || !strings.Contains(err.Error(), a) || !strings.Contains(err.Error(), b) || !strings.Contains(err.Error(), "PlaylistPath") {
			t.Fatalf("err=%v", err)
		}
		importBook(t, ImportOptions{InputPath: d, PlaylistPath: b})
	})
	t.Run("cues", func(t *testing.T) {
		d := t.TempDir()
		f := importM4B(t, d, "01.m4b")
		text := "FILE \"01.m4b\" WAVE\n TRACK 01 AUDIO\n INDEX 01 00:00:00\n"
		a := importWrite(t, d, "a.CuE", text)
		b := importWrite(t, d, "b.cue", text)
		_, err := DiscoverInputs(ImportOptions{InputPath: d}, "")
		if err == nil || !strings.Contains(err.Error(), a) || !strings.Contains(err.Error(), b) || !strings.Contains(err.Error(), "CuePath") {
			t.Fatalf("err=%v", err)
		}
		if err := os.Remove(b); err != nil {
			t.Fatal(err)
		}
		_, err = Import(context.Background(), ImportOptions{InputPath: d, CacheDir: t.TempDir()})
		if err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatalf("err=%v", err)
		}
		for _, mode := range []string{"cue", "embedded", "files"} {
			importBook(t, ImportOptions{InputPath: d, ChapterSource: mode})
		}
		importBook(t, ImportOptions{InputPath: d, CuePath: a})
		for _, mode := range []string{"files", "embedded", "invalid"} {
			_, err = DiscoverInputs(ImportOptions{InputPath: d, CuePath: a, ChapterSource: mode}, "")
			if err == nil {
				t.Fatalf("mode %s accepted", mode)
			}
		}
		importWrite(t, d, "broken.m3u", "missing.mp3\n")
		importBook(t, ImportOptions{InputPath: f})
		importBook(t, ImportOptions{InputPath: f, CuePath: a})
		_, err = DiscoverInputs(ImportOptions{InputPath: f, PlaylistPath: "broken.m3u"}, "")
		if err == nil {
			t.Fatal("single accepted playlist")
		}
	})
	t.Run("single cue unrelated", func(t *testing.T) {
		d := t.TempDir()
		f := importCopy(t, d, "01.mp3", "mp3/01.mp3")
		importCopy(t, d, "02.mp3", "mp3/02.mp3")
		c := importWrite(t, d, "book.cue", "FILE \"02.mp3\" WAVE\n TRACK 01 AUDIO\n INDEX 01 00:00:00\n")
		_, err := Import(context.Background(), ImportOptions{InputPath: f, CacheDir: t.TempDir(), CuePath: c})
		if err == nil {
			t.Fatal("unrelated explicit cue accepted")
		}
	})
}
func TestCueOrderWithoutPlaylist(t *testing.T) {
	d := t.TempDir()
	importCopy(t, d, "01.mp3", "mp3/01.mp3")
	importCopy(t, d, "02.mp3", "mp3/02.mp3")
	importCopy(t, d, "99.mp3", "mp3/01.mp3")
	importWrite(t, d, "book.cue", "FILE \"02.mp3\" WAVE\n TRACK 01 AUDIO\n INDEX 01 00:00:00\nFILE \"01.mp3\" WAVE\n TRACK 02 AUDIO\n INDEX 01 00:00:00\n")
	p := importBook(t, ImportOptions{InputPath: d})
	if len(p.Sources) != 2 || filepath.Base(p.Sources[0].Path) != "02.mp3" || filepath.Base(p.Sources[1].Path) != "01.mp3" {
		t.Fatalf("sources=%+v", p.Sources)
	}
	pl := importWrite(t, d, "reverse.m3u", "01.mp3\n02.mp3\n")
	_, err := Import(context.Background(), ImportOptions{InputPath: d, CacheDir: t.TempDir(), PlaylistPath: pl})
	if err == nil || !strings.Contains(err.Error(), "ordered sources") {
		t.Fatalf("err=%v", err)
	}
}
func TestImportCanceled(t *testing.T) {
	d := t.TempDir()
	importCopy(t, d, "01.mp3", "mp3/01.mp3")
	importCopy(t, d, "cover.jpg", "cover.jpg")
	cache := t.TempDir()
	before := directoryHashes(t, d)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, err := Import(ctx, ImportOptions{InputPath: d, CacheDir: cache})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(p, BookProject{}) {
		t.Fatalf("project=%+v err=%v", p, err)
	}
	if !reflect.DeepEqual(before, directoryHashes(t, d)) || len(directoryHashes(t, cache)) != 0 {
		t.Fatal("canceled import changed input or cache")
	}
}
func TestCoverCachePreservesOriginal(t *testing.T) {
	d := t.TempDir()
	audio := importCopy(t, d, "01.mp3", "mp3/01.mp3")
	cover := importCopy(t, d, "cover.jpg", "cover.jpg")
	cache := t.TempDir()
	before := directoryHashes(t, d)
	p := importBook(t, ImportOptions{InputPath: d, CacheDir: cache})
	q := importBook(t, ImportOptions{InputPath: d, CacheDir: cache})
	if p.CoverPath == "" || p.CoverPath != q.CoverPath || filepath.Dir(p.CoverPath) == d || !filepath.IsAbs(p.CoverPath) {
		t.Fatalf("covers=%q %q", p.CoverPath, q.CoverPath)
	}
	if !reflect.DeepEqual(before, directoryHashes(t, d)) {
		t.Fatalf("input modified %s %s", audio, cover)
	}
	data, _ := os.ReadFile(cover)
	cached, _ := os.ReadFile(p.CoverPath)
	if sha256.Sum256(data) != sha256.Sum256(cached) {
		t.Fatal("cover transformed")
	}
}
func directoryHashes(t *testing.T, d string) map[string][32]byte {
	t.Helper()
	entries, err := os.ReadDir(d)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string][32]byte{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(d, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		m[e.Name()] = sha256.Sum256(b)
	}
	return m
}
func TestCacheDirectoryOwnership(t *testing.T) {
	d := t.TempDir()
	f := importCopy(t, d, "01.mp3", "mp3/01.mp3")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(d, alias); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	for _, cache := range []string{d, alias, f, ""} {
		_, err := Import(context.Background(), ImportOptions{InputPath: d, CacheDir: cache})
		if err == nil {
			t.Fatalf("cache %q accepted", cache)
		}
	}
	importBook(t, ImportOptions{InputPath: d, CacheDir: filepath.Join(d, "managed", "covers")})
}
func TestCacheCoverClear(t *testing.T) {
	got, err := CacheCover(t.TempDir(), "")
	if err != nil || got != "" {
		t.Fatalf("cover=%q err=%v", got, err)
	}
}

func TestImportCanonicalPlaylistSources(t *testing.T) {
	d := t.TempDir()
	source := importCopy(t, d, "01.mp3", "mp3/01.mp3")
	if err := os.Symlink(source, filepath.Join(d, "alias.mp3")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	importWrite(t, d, "book.m3u", "#EXTINF:9,Original\n01.mp3\n#EXTINF:9,Alias\nalias.mp3\n")
	p := importBook(t, ImportOptions{InputPath: d})
	if len(p.Sources) != 1 || len(p.Chapters) != 2 || p.Chapters[0].Segments[0].SourceID != p.Chapters[1].Segments[0].SourceID || p.Chapters[0].ID == p.Chapters[1].ID {
		t.Fatalf("project=%+v", p)
	}
}
func TestImportCoverFailure(t *testing.T) {
	d := t.TempDir()
	importCopy(t, d, "01.mp3", "mp3/01.mp3")
	importCopy(t, d, "cover.jpg", "cover.jpg")
	cache := t.TempDir()
	p := importBook(t, ImportOptions{InputPath: d, CacheDir: cache})
	cachedBefore := directoryHashes(t, cache)
	importWrite(t, d, "cover.jpg", "malformed artwork")
	before := directoryHashes(t, d)
	failed, err := Import(context.Background(), ImportOptions{InputPath: d, CacheDir: cache})
	if err == nil || !reflect.DeepEqual(failed, BookProject{}) {
		t.Fatalf("project=%+v err=%v", failed, err)
	}
	if !reflect.DeepEqual(before, directoryHashes(t, d)) || !reflect.DeepEqual(cachedBefore, directoryHashes(t, cache)) {
		t.Fatal("failed import changed input or reusable cache")
	}
	if _, err = os.Stat(p.CoverPath); err != nil {
		t.Fatal(err)
	}
}
func TestDiscoverInputsReadOnly(t *testing.T) {
	d := t.TempDir()
	importCopy(t, d, "01.mp3", "mp3/01.mp3")
	importCopy(t, d, "output.m4b", "m4a/01.m4a")
	before := directoryHashes(t, d)
	inputs, err := DiscoverInputs(ImportOptions{InputPath: d}, "output.m4b")
	if err != nil || len(inputs.Files) != 1 || filepath.Base(inputs.Files[0]) != "01.mp3" || !reflect.DeepEqual(before, directoryHashes(t, d)) {
		t.Fatalf("inputs=%+v err=%v", inputs, err)
	}
	p := importBook(t, ImportOptions{InputPath: d})
	if len(p.Sources) != 2 {
		t.Fatalf("project import unexpectedly excluded audio: %+v", p.Sources)
	}
}

func TestCueOrderIgnoresUnrelatedBrokenAudio(t *testing.T) {
	dir := t.TempDir()
	source := importCopy(t, dir, "01.mp3", "mp3/01.mp3")
	cue := importWrite(t, dir, "book.cue", "FILE \"01.mp3\" WAVE\n TRACK 01 AUDIO\n INDEX 01 00:00:00\n")
	if err := os.Symlink(filepath.Join(dir, "absent.mp3"), filepath.Join(dir, "unrelated.mp3")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []string{"", cue} {
		inputs, err := DiscoverInputs(ImportOptions{InputPath: dir, CuePath: explicit}, "")
		if err != nil || !reflect.DeepEqual(inputs.Files, []string{canonical}) {
			t.Fatalf("cue=%q inputs=%+v err=%v", explicit, inputs, err)
		}
		project := importBook(t, ImportOptions{InputPath: dir, CuePath: explicit})
		if len(project.Sources) != 1 || project.Sources[0].Path != canonical {
			t.Fatalf("sources=%+v", project.Sources)
		}
	}
	// A broken playlist still wins selection and errors; CUE must not silently replace it.
	playlist := importWrite(t, dir, "broken.M3U8", "missing.mp3\n")
	_, err = DiscoverInputs(ImportOptions{InputPath: dir}, "")
	if err == nil || !strings.Contains(err.Error(), playlist) || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("broken playlist err=%v", err)
	}
}

func TestCueOrderSourcesOutsideEmptyInputDirectory(t *testing.T) {
	dir := t.TempDir()
	external := t.TempDir()
	source := importCopy(t, external, "01.mp3", "mp3/01.mp3")
	importWrite(t, dir, "book.cue", "FILE \""+source+"\" WAVE\n TRACK 01 AUDIO\n INDEX 01 00:00:00\n")
	inputs, err := DiscoverInputs(ImportOptions{InputPath: dir}, "")
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inputs.Files, []string{canonical}) {
		t.Fatalf("inputs=%+v", inputs)
	}
	project := importBook(t, ImportOptions{InputPath: dir})
	if len(project.Sources) != 1 || project.Sources[0].Path != canonical {
		t.Fatalf("sources=%+v", project.Sources)
	}
}

func TestDiscoverInputsPreservesSelectedPlaylistOutputOccurrences(t *testing.T) {
	dir := t.TempDir()
	source := importCopy(t, dir, "output.m4b", "m4a/01.m4a")
	playlist := importWrite(t, dir, "book.m3u", "#EXTINF:3,First\noutput.m4b\n#EXTINF:3,Repeated\noutput.m4b\n")
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{"", playlist} {
		inputs, err := DiscoverInputs(ImportOptions{InputPath: dir, PlaylistPath: selected}, source)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(inputs.Files, []string{canonical, canonical}) || len(inputs.Entries) != 2 {
			t.Fatalf("selected playlist occurrences lost: %+v", inputs)
		}
	}
}
