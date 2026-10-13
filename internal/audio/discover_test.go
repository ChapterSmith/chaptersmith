package audio

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeDiscoveryFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestM3UOccurrenceOrder(t *testing.T) {
	d := t.TempDir()
	a := filepath.Join(d, "disc2", "01.mp3")
	b := filepath.Join(d, "disc1", "01.mp3")
	writeDiscoveryFile(t, a, "")
	writeDiscoveryFile(t, b, "")
	p := filepath.Join(d, "book.m3u")
	writeDiscoveryFile(t, p, "\ufeff#EXTM3U\r\n#EXTINF:999,Second\r\ndisc2/01.mp3\r\n#EXTINF:1,First\r\ndisc1/01.mp3\r\n#EXTINF:2,Encore\r\ndisc2/01.mp3\r\n")
	files, entries, err := ParseM3UFile(p, d)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, []string{a, b, a}) || len(entries) != 3 {
		t.Fatalf("files=%v entries=%v", files, entries)
	}
	for i, title := range []string{"Second", "First", "Encore"} {
		if entries[i].Title != title || entries[i].Filename != files[i] {
			t.Fatalf("entry %d=%+v", i, entries[i])
		}
	}
}
func TestM3UStrictErrors(t *testing.T) {
	for _, s := range []string{"missing.mp3", "https://example.org/audio.mp3", "folder", "notes.txt", ""} {
		t.Run(s, func(t *testing.T) {
			d := t.TempDir()
			os.Mkdir(filepath.Join(d, "folder"), 0700)
			writeDiscoveryFile(t, filepath.Join(d, "notes.txt"), "x")
			p := filepath.Join(d, "bad.m3u")
			writeDiscoveryFile(t, p, s)
			_, _, err := ParseM3UFile(p, d)
			if err == nil || !strings.Contains(err.Error(), p) || (s != "" && !strings.Contains(err.Error(), "line 1")) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	d := t.TempDir()
	writeDiscoveryFile(t, filepath.Join(d, "a.mp3"), "")
	p := filepath.Join(d, "valid.m3u")
	writeDiscoveryFile(t, p, "#EXTINF:123,old\n#extinf:bad,new\na.mp3\na.mp3\n")
	_, e, err := ParseM3UFile(p, d)
	if err != nil || len(e) != 2 || e[0].Duration != 0 || e[0].Title != "new" || e[1].Title != "" {
		t.Fatalf("entries=%+v err=%v", e, err)
	}
}
func TestM3UOverflowDurationResets(t *testing.T) {
	for _, duration := range []string{"999999999999999999999999", "-999999999999999999999999"} {
		t.Run(duration, func(t *testing.T) {
			dir := t.TempDir()
			audioPath := filepath.Join(dir, "a.mp3")
			writeDiscoveryFile(t, audioPath, "")
			playlist := filepath.Join(dir, "overflow.m3u")
			writeDiscoveryFile(t, playlist, "#EXTINF:123,Previous\n#EXTINF:"+duration+",Title\na.mp3\n")
			files, entries, err := ParseM3UFile(playlist, dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 || len(entries) != 1 || entries[0].Duration != 0 || entries[0].Title != "Title" || entries[0].Filename != audioPath {
				t.Fatalf("files=%v entries=%+v", files, entries)
			}
		})
	}
}

func TestDiscovery(t *testing.T) {
	d := t.TempDir()
	for _, n := range []string{"10.MP3", "2.flac", "output.wav"} {
		writeDiscoveryFile(t, filepath.Join(d, n), "")
	}
	f, err := ListAudioFiles(d, "output.wav")
	if err != nil || !reflect.DeepEqual(f, []string{filepath.Join(d, "2.flac"), filepath.Join(d, "10.MP3")}) {
		t.Fatalf("files=%v err=%v", f, err)
	}
	p := filepath.Join(d, "a.M3U8")
	q := filepath.Join(d, "b.m3u")
	writeDiscoveryFile(t, p, "10.MP3\n")
	f, _, err = FindAudioFiles(d, "")
	if err != nil || len(f) != 1 || f[0] != filepath.Join(d, "10.MP3") {
		t.Fatalf("files=%v err=%v", f, err)
	}
	writeDiscoveryFile(t, q, "2.flac\n")
	_, _, err = FindAudioFiles(d, "")
	if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), q) {
		t.Fatalf("err=%v", err)
	}
	f, _, err = FindAudioFilesWithPlaylist(d, "", q)
	if err != nil || len(f) != 1 || f[0] != filepath.Join(d, "2.flac") {
		t.Fatalf("files=%v err=%v", f, err)
	}
	writeDiscoveryFile(t, q, "missing.mp3\n")
	f, _, err = FindAudioFilesWithPlaylist(d, "", q)
	if err == nil || len(f) != 0 {
		t.Fatalf("files=%v err=%v", f, err)
	}
}

func TestPlaylistExcludesOutput(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"01.m4a", "out.m4b"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("audio"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "book.m3u"), []byte("out.m4b\n01.m4a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	files, entries, err := FindAudioFiles(dir, "out.m4b")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "01.m4a" || len(entries) != 1 {
		t.Fatalf("output included as input: %v, %+v", files, entries)
	}
}

func TestOutputElsewhereDoesNotExcludeSameBasename(t *testing.T) {
	for _, playlist := range []bool{false, true} {
		t.Run(map[bool]string{false: "directory", true: "playlist"}[playlist], func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "01.m4a")
			if err := os.WriteFile(input, []byte("audio"), 0600); err != nil {
				t.Fatal(err)
			}
			if playlist {
				if err := os.WriteFile(filepath.Join(dir, "book.m3u"), []byte("01.m4a\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			files, _, err := FindAudioFiles(dir, filepath.Join(t.TempDir(), "01.m4a"))
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 || files[0] != input {
				t.Fatalf("unrelated output excluded source: %v", files)
			}
		})
	}
}
