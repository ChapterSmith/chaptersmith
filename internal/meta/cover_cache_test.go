package meta_test

import (
	"chaptersmith/internal/meta"
	"chaptersmith/internal/muxer"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func cacheLogger() *log.Logger { return log.New(io.Discard, "", 0) }
func cacheHashes(t *testing.T, d string) map[string][32]byte {
	t.Helper()
	es, err := os.ReadDir(d)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string][32]byte{}
	for _, e := range es {
		b, err := os.ReadFile(filepath.Join(d, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		m[e.Name()] = sha256.Sum256(b)
	}
	return m
}
func TestCoverCacheEmbeddedPNG(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "book.m4b")
	cover := "../../testdata/formats/cover.png"
	if err := muxer.Finalize(context.Background(), muxer.Options{InputFile: "../../testdata/formats/m4a/01.m4a", OutputFile: p, HasCover: true, CoverFile: cover}, cacheLogger()); err != nil {
		t.Fatal(err)
	}
	before := cacheHashes(t, d)
	cache := t.TempDir()
	got, err := meta.CacheCoverImage(d, p, cache, cacheLogger())
	if err != nil || filepath.Ext(got) != ".png" {
		t.Fatalf("cover=%q err=%v", got, err)
	}
	original, _ := os.ReadFile(cover)
	copied, _ := os.ReadFile(got)
	if !reflect.DeepEqual(original, copied) || !reflect.DeepEqual(before, cacheHashes(t, d)) {
		t.Fatal("artwork or input changed")
	}
}
func TestCoverCacheNoArtwork(t *testing.T) {
	for _, path := range []string{"../../testdata/formats/m4a/01.m4a", "../../testdata/formats/cover.png"} {
		got, err := meta.CacheCoverImage(t.TempDir(), path, t.TempDir(), cacheLogger())
		if got != "" || err != nil {
			t.Fatalf("path=%s cover=%q err=%v", path, got, err)
		}
	}
}
func TestCoverCacheFailure(t *testing.T) {
	d := t.TempDir()
	b, err := os.ReadFile("../../testdata/formats/cover.jpg")
	if err != nil {
		t.Fatal(err)
	}
	cover := filepath.Join(d, "cover.jpg")
	if err = os.WriteFile(cover, b, 0600); err != nil {
		t.Fatal(err)
	}
	before := cacheHashes(t, d)
	fileCache := filepath.Join(t.TempDir(), "file")
	if err = os.WriteFile(fileCache, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	unwritable := t.TempDir()
	if err = os.Chmod(unwritable, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(unwritable, 0700) })
	caches := []string{fileCache}
	// chmod does not stop writes on Windows, nor for root (a container CI runner).
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		caches = append(caches, unwritable)
	}
	for _, cache := range caches {
		_, err = meta.CopyCoverToCache(cover, cache)
		if err == nil {
			t.Fatalf("cache=%s accepted", cache)
		}
	}
	if !reflect.DeepEqual(before, cacheHashes(t, d)) {
		t.Fatal("input changed")
	}
	cache := t.TempDir()
	if err = os.WriteFile(cover, []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = meta.CacheCoverImage(d, "", cache, cacheLogger())
	if err == nil {
		t.Fatal("malformed image accepted")
	}
	es, _ := os.ReadDir(cache)
	if len(es) != 0 {
		t.Fatalf("leaked files=%v", es)
	}
}
func TestCacheCoverContentAddressed(t *testing.T) {
	cache := t.TempDir()
	a, err := meta.CopyCoverToCache("../../testdata/formats/cover.jpg", cache)
	if err != nil {
		t.Fatal(err)
	}
	b, err := meta.CopyCoverToCache("../../testdata/formats/cover.jpg", cache)
	if err != nil || a != b || !filepath.IsAbs(a) {
		t.Fatalf("covers=%q %q err=%v", a, b, err)
	}
	raw, _ := os.ReadFile(a)
	sum := sha256.Sum256(raw)
	if !strings.HasPrefix(filepath.Base(a), fmt.Sprintf("%x", sum)) {
		t.Fatalf("not content addressed: %s", a)
	}
	if err = os.WriteFile(a, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = meta.CopyCoverToCache("../../testdata/formats/cover.jpg", cache)
	if err == nil {
		t.Fatal("corrupt existing cache accepted")
	}
	es, _ := os.ReadDir(cache)
	if len(es) != 1 {
		t.Fatal("temporary file leaked")
	}
}
func TestCoverCacheGIFConversion(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "cover.gif")
	var buf bytes.Buffer
	im := image.NewRGBA(image.Rect(0, 0, 2, 2))
	im.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := gif.Encode(&buf, im, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	before := cacheHashes(t, d)
	got, err := meta.CopyCoverToCache(p, t.TempDir())
	if err != nil || filepath.Ext(got) != ".png" {
		t.Fatalf("cover=%s err=%v", got, err)
	}
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	_, format, err := image.Decode(bytes.NewReader(b))
	if err != nil || format != "png" {
		t.Fatalf("format=%s err=%v", format, err)
	}
	if !reflect.DeepEqual(before, cacheHashes(t, d)) {
		t.Fatal("GIF source changed")
	}
}

func TestCoverCacheSameDirectoryOwnership(t *testing.T) {
	d := t.TempDir()
	source := filepath.Join(d, "cover.jpg")
	data, err := os.ReadFile("../../testdata/formats/cover.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	before := cacheHashes(t, d)
	alias := filepath.Join(t.TempDir(), "alias")
	if err = os.Symlink(d, alias); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	for _, cache := range []string{d, alias} {
		if _, err := meta.CopyCoverToCache(source, cache); err == nil {
			t.Fatal("source directory accepted as cache")
		}
		if _, err := meta.CacheCoverImage(d, "", cache, cacheLogger()); err == nil {
			t.Fatal("source directory accepted for discovered cover")
		}
	}
	if !reflect.DeepEqual(before, cacheHashes(t, d)) {
		t.Fatal("source directory changed")
	}
	cache := t.TempDir()
	cached, err := meta.CopyCoverToCache(source, cache)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := meta.CopyCoverToCache(cached, cache)
	if err != nil || reused != cached {
		t.Fatalf("reuse=%q err=%v", reused, err)
	}
}
func TestCoverCacheWebPUnsupported(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "cover.webp")
	if err := os.WriteFile(p, []byte("RIFF-invalid-WEBP"), 0600); err != nil {
		t.Fatal(err)
	}
	before := cacheHashes(t, d)
	cache := t.TempDir()
	_, err := meta.CopyCoverToCache(p, cache)
	if err == nil || !strings.Contains(err.Error(), "JPEG or PNG") {
		t.Fatalf("err=%v", err)
	}
	if !reflect.DeepEqual(before, cacheHashes(t, d)) || len(cacheHashes(t, cache)) != 0 {
		t.Fatal("unsupported cover changed files")
	}
}
