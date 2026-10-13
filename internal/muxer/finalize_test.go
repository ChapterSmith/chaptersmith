package muxer

import (
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asticode/go-astiav"

	"chaptersmith/internal/audiotest"
	"chaptersmith/internal/chapter"
	"chaptersmith/internal/meta"
)

func finalizeInput(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "input.m4a")
	audiotest.WriteAAC(t, p, 22050, 0)
	return p
}

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func TestFinalizeRejectsRequestedCover(t *testing.T) {
	for _, kind := range []string{"missing", "empty-path", "corrupt", "no-picture"} {
		t.Run(kind, func(t *testing.T) {
			input := finalizeInput(t)
			before := fileHash(t, input)
			cover := filepath.Join(t.TempDir(), "cover.jpg")
			switch kind {
			case "empty-path":
				cover = ""
			case "corrupt":
				if err := os.WriteFile(cover, []byte("not an image"), 0600); err != nil {
					t.Fatal(err)
				}
			case "no-picture":
				cover = input
			}
			out := filepath.Join(t.TempDir(), "out.m4b")
			err := Finalize(context.Background(), Options{InputFile: input, OutputFile: out, HasCover: true, CoverFile: cover}, log.New(io.Discard, "", 0))
			if err == nil || !strings.Contains(err.Error(), "cover") {
				t.Errorf("requested %s cover: got %v", kind, err)
			}
			if fileHash(t, input) != before {
				t.Fatal("source mutated")
			}
		})
	}
}

func TestFinalizePublicationFailure(t *testing.T) {
	input := finalizeInput(t)
	before := fileHash(t, input)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "out.m4b"), 0700); err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	err := Finalize(context.Background(), Options{InputFile: input, OutputFile: filepath.Join(dir, "out.m4b")}, log.New(&logs, "", 0))
	if err == nil || !strings.Contains(err.Error(), "publishing") {
		t.Errorf("blocked rename: %v", err)
	}
	if strings.Contains(logs.String(), "Audiobook finalized:") {
		t.Error("logged success on failed rename")
	}
	if fileHash(t, input) != before {
		t.Fatal("source mutated")
	}
}

func TestFinalizeCancellation(t *testing.T) {
	input := finalizeInput(t)
	before := fileHash(t, input)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var logs strings.Builder
	err := Finalize(ctx, Options{InputFile: input, OutputFile: filepath.Join(t.TempDir(), "out.tmp")}, log.New(&logs, "", 0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(logs.String(), "Audiobook finalized:") {
		t.Error("logged success after cancellation")
	}
	if fileHash(t, input) != before {
		t.Fatal("source mutated")
	}
}

func TestFinalizeClosesBeforePublication(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "close-failure"}[fail], func(t *testing.T) {
			input := finalizeInput(t)
			before := fileHash(t, input)
			requested := filepath.Join(t.TempDir(), "out.tmp")
			final := requested
			closeErr := errors.New("injected output close failure")
			calls := 0
			var logs strings.Builder
			err := finalize(context.Background(), Options{InputFile: input, OutputFile: requested}, log.New(&logs, "", 0), func(p *astiav.IOContext) error {
				calls++
				if _, err := os.Stat(final); !os.IsNotExist(err) {
					t.Error("renamed before closing output", err)
				}
				err := p.Close()
				if fail {
					err = errors.Join(err, closeErr)
				}
				return err
			})
			if calls != 1 {
				t.Fatalf("closed %d times", calls)
			}
			if fail {
				if !errors.Is(err, closeErr) {
					t.Fatalf("lost close error: %v", err)
				}
				if _, err := os.Stat(final); !os.IsNotExist(err) {
					t.Error("published despite close failure", err)
				}
				if strings.Contains(logs.String(), "Audiobook finalized:") {
					t.Error("logged success despite close failure")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if fileHash(t, input) != before {
				t.Fatal("source mutated")
			}
		})
	}
}

func TestFinalizeJoinsPrimaryAndCloseError(t *testing.T) {
	input := filepath.Join(t.TempDir(), "input.flac")
	audiotest.WriteFLAC(t, input, 22050)
	before := fileHash(t, input)
	closeErr := errors.New("injected output close failure")
	calls := 0
	var logs strings.Builder
	// FLAC cannot be written to the ipod container, so the real native
	// header operation fails after output IO has been opened.
	err := finalize(context.Background(), Options{InputFile: input, OutputFile: filepath.Join(t.TempDir(), "out.m4b")}, log.New(&logs, "", 0), func(p *astiav.IOContext) error {
		calls++
		return errors.Join(p.Close(), closeErr)
	})
	if !errors.Is(err, closeErr) || !strings.Contains(err.Error(), "writing header:") || calls != 1 {
		t.Fatalf("primary/close errors: %v, closes=%d", err, calls)
	}
	if strings.Contains(logs.String(), "Audiobook finalized:") {
		t.Error("logged success on header/close failure")
	}
	if fileHash(t, input) != before {
		t.Fatal("source mutated")
	}
}

func TestFinalizeRequestedChaptersMissing(t *testing.T) {
	err := Finalize(context.Background(), Options{InputFile: finalizeInput(t), OutputFile: filepath.Join(t.TempDir(), "out.m4b"), HasChapters: true}, log.New(io.Discard, "", 0))
	if err == nil || !strings.Contains(err.Error(), "chapter") {
		t.Fatalf("missing requested chapters: %v", err)
	}
}

func packetHashes(t *testing.T, path string, media astiav.MediaType) [][32]byte {
	t.Helper()
	fc := astiav.AllocFormatContext()
	defer fc.Free()
	if err := fc.OpenInput(path, nil, nil); err != nil {
		t.Fatal(err)
	}
	defer fc.CloseInput()
	if err := fc.FindStreamInfo(nil); err != nil {
		t.Fatal(err)
	}
	index := -1
	for _, s := range fc.Streams() {
		if s.CodecParameters().MediaType() == media {
			index = s.Index()
			if media == astiav.MediaTypeVideo && !s.DispositionFlags().Has(astiav.DispositionFlagAttachedPic) {
				t.Fatal("picture not attached")
			}
			break
		}
	}
	p := astiav.AllocPacket()
	defer p.Free()
	var out [][32]byte
	for {
		err := fc.ReadFrame(p)
		if errors.Is(err, astiav.ErrEof) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if p.StreamIndex() == index {
			out = append(out, sha256.Sum256(p.Data()))
		}
		p.Unref()
	}
	return out
}

func TestFinalizeNativeMetadataChaptersAndCover(t *testing.T) {
	for _, ext := range []string{"jpg", "png"} {
		t.Run(ext, func(t *testing.T) {
			input := finalizeInput(t)
			before := fileHash(t, input)
			cover := filepath.Join(t.TempDir(), "cover."+ext)
			img := image.NewRGBA(image.Rect(0, 0, 16, 16))
			for y := 0; y < 16; y++ {
				for x := 0; x < 16; x++ {
					img.Set(x, y, color.RGBA{R: 120, G: uint8(x * 12), B: uint8(y * 12), A: 255})
				}
			}
			f, err := os.Create(cover)
			if err != nil {
				t.Fatal(err)
			}
			if ext == "jpg" {
				err = jpeg.Encode(f, img, nil)
			} else {
				err = png.Encode(f, img)
			}
			if err = errors.Join(err, f.Close()); err != nil {
				t.Fatal(err)
			}
			coverBefore := fileHash(t, cover)
			requested := filepath.Join(t.TempDir(), "book.tmp")
			out := requested
			var logs strings.Builder
			long := strings.Repeat("Long description. ", 30)
			chapters := []chapter.Chapter{{Title: "First", StartTime: 0}, {Title: "Second", StartTime: .25}}
			err = Finalize(context.Background(), Options{InputFile: input, OutputFile: requested, CoverFile: cover, HasCover: true, HasChapters: true, Chapters: chapters, Metadata: meta.AudiobookMetadata{Title: "Book", Artist: "Author", Album: "Album", Description: long, Comment: long}}, log.New(&logs, "", 0))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(logs.String(), "Audiobook finalized: "+out) {
				t.Error("final log does not report actual path", logs.String())
			}
			if _, err := os.Stat(strings.TrimSuffix(requested, ".tmp") + ".m4b"); !os.IsNotExist(err) {
				t.Fatalf("unexpected renamed output: %v", err)
			}
			tags := rawTags(t, out)
			if tags["title"] != "Book" || tags["artist"] != "Author" || tags["album"] != "Album" || tags["genre"] != "Audiobook" || tags["synopsis"] != long || tags["comment"] != "" {
				t.Fatalf("tags: %v", tags)
			}
			got, err := chapter.ReadEmbeddedChapters(out)
			if err != nil || !reflect.DeepEqual(got, chapters) {
				t.Fatalf("chapters: %v, %v", got, err)
			}
			if !reflect.DeepEqual(packetHashes(t, input, astiav.MediaTypeAudio), packetHashes(t, out, astiav.MediaTypeAudio)) {
				t.Fatal("AAC packets changed")
			}
			pictures := packetHashes(t, out, astiav.MediaTypeVideo)
			if len(pictures) != 1 || pictures[0] != coverBefore {
				t.Fatal("cover packet missing or changed")
			}
			if fileHash(t, input) != before || fileHash(t, cover) != coverBefore {
				t.Fatal("source or cover mutated")
			}
		})
	}
}

func TestFinalizeCloseFailurePreservesExistingOutput(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "out.m4b")
	previous := []byte("previous audiobook")
	if err := os.WriteFile(output, previous, 0600); err != nil {
		t.Fatal(err)
	}
	closeErr := errors.New("injected close failure")
	err := finalize(context.Background(), Options{InputFile: finalizeInput(t), OutputFile: output}, log.New(io.Discard, "", 0), func(p *astiav.IOContext) error { return errors.Join(p.Close(), closeErr) })
	if !errors.Is(err, closeErr) {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != string(previous) {
		t.Fatalf("existing output lost: %q, %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("staging files left: %v %v", entries, err)
	}
}
