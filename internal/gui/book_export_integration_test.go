package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"chaptersmith/internal/audio"
	"chaptersmith/internal/audiotest"
	"chaptersmith/internal/book"
	"chaptersmith/internal/meta"
	"chaptersmith/internal/render"
)

// Exercises the actual service, SQLite store, libav renderer and native demuxer;
// no external ffmpeg process or playback simulation is involved.
func TestBookExportRealRenderer(t *testing.T) {
	s, db, fixture := newBookFixture(t)
	input := t.TempDir()
	samples := make([]int16, 22050)
	for i := range samples {
		samples[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(i)/44100))
	}
	for _, name := range []string{"01.wav", "02.wav"} {
		audiotest.WriteWAV(t, filepath.Join(input, name), 44100, 1, samples)
	}
	s.deps.Import = book.Import
	v, err := s.LoadBook("real-load", LoadRequest{InputPath: input, ChapterSource: "files"})
	if err != nil {
		t.Fatal(err)
	}
	original := map[string][]byte{}
	for _, src := range v.Project.Sources {
		original[src.Path], err = os.ReadFile(src.Path)
		if err != nil {
			t.Fatal(err)
		}
	}
	apply := func(edit EditRequest) {
		t.Helper()
		v, err = s.ApplyEdit(v.Revision, edit)
		if err != nil {
			t.Fatal(err)
		}
	}
	s.deps.Pick = func(string) (string, error) { return fixture.CoverPath, nil }
	v, err = s.SelectCover(v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.SaveProject(v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.OpenProject(v.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	apply(EditRequest{Kind: "metadata", Metadata: &meta.AudiobookMetadata{Title: "", Artist: "Edited author", Description: "", Extra: map[string]string{"keywords": "snapshot"}}})
	apply(EditRequest{Kind: "cover-remove"})
	first := v.Project.Chapters[0].ID
	apply(EditRequest{Kind: "rename", ChapterID: first, Title: "Renamed"})
	apply(EditRequest{Kind: "move", ChapterID: first, Destination: 1})
	apply(EditRequest{Kind: "boundary", ChapterID: first, NewStartUS: 550000, Mode: "transfer"})
	apply(EditRequest{Kind: "silence", ChapterID: first, DurationUS: 100000, Position: "end"})
	v, err = s.SaveProject(v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.OpenProject(v.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Project.Metadata.Title != "" || v.Project.CoverPath != "" || v.DurationUS != 1100000 {
		t.Fatal(v)
	}
	var terminalCount atomic.Int32
	s.deps.Emit = func(name string, payload any) {
		if name == "book-operation" && terminalStatus(payload.(OperationStatus).Status) {
			terminalCount.Add(1)
		}
	}
	exported := 0
	for _, mode := range []string{"book", "chapters", "both"} {
		for _, format := range []string{"m4b", "m4a", "flac", "wav"} {
			t.Run(mode+"/"+format, func(t *testing.T) {
				settings := book.ExportSettings{Profile: "preserve", Channels: "auto", OutputMode: mode, Format: format}
				apply(EditRequest{Kind: "export", Export: &settings})
				dest := filepath.Join(t.TempDir(), "result")
				if mode == "book" {
					dest += "." + format
				}
				id := mode + format
				started, err := s.StartExport(id, v.Revision, dest)
				if err != nil {
					t.Fatal(err)
				}
				s.workers.Wait()
				status, _ := s.GetOperation(id)
				if status.Status != "succeeded" {
					t.Fatalf("%+v", status)
				}
				if !strings.Contains(status.NormalizationReport, "Preserve fallback") {
					t.Fatal(status.NormalizationReport)
				}
				count := 2
				if mode == "book" {
					count = 1
				} else if mode == "both" {
					count = 3
				}
				if len(status.Paths) != count {
					t.Fatal(status)
				}
				for index, path := range status.Paths {
					whole := mode != "chapters" && index == 0
					ci := index
					if mode == "both" {
						ci--
					}
					inspection := audiotest.Inspect(t, path)
					wantTitle := ""
					wantFrames := int64(48510)
					if !whole {
						wantTitle = v.Project.Chapters[ci].Title
						wantFrames = v.ChapterRows[ci].DurationUS * 44100 / 1000000
						if inspection.Tags["track"] != fmt.Sprintf("%d/2", ci+1) {
							t.Fatal(inspection.Tags)
						}
					}
					if inspection.Tags["title"] != wantTitle || inspection.Tags["artist"] != "Edited author" || inspection.Covers != 0 {
						t.Fatal(inspection)
					}
					if whole && (format == "m4b" || format == "m4a") {
						if !reflect.DeepEqual(inspection.Chapters, []audiotest.InspectedChapter{{StartFrame: 0, EndFrame: 24255}, {StartFrame: 24255, EndFrame: 48510}}) {
							t.Fatal(inspection.Chapters)
						}
					} else if len(inspection.Chapters) != 0 {
						t.Fatal(inspection.Chapters)
					}
					var frames int64
					if err = audio.DecodePCM(context.Background(), path, 1, 0, -1, func(samples []float32) error { frames += int64(len(samples)); return nil }); err != nil || frames != wantFrames {
						t.Fatalf("frames=%d expected=%d err=%v", frames, wantFrames, err)
					}
					if !whole && format == "m4b" && filepath.Ext(path) != ".m4a" {
						t.Fatal(path)
					}
				}
				history, err := db.GetHistory(1)
				if err != nil || len(history) != 1 || history[0].ID != started.TaskID || !history[0].Success {
					t.Fatalf("%+v %v", history, err)
				}
				var stored book.BookProject
				if err = json.Unmarshal([]byte(*history[0].ProjectSnapshot), &stored); err != nil || !reflect.DeepEqual(stored, v.Project) {
					t.Fatalf("snapshot mismatch: %v", err)
				}
				logs, err := db.GetTaskLogs(started.TaskID)
				if err != nil || len(logs) < 3 {
					t.Fatalf("%+v %v", logs, err)
				}
				exported++
			})
		}
	}
	// Cancel once real staging exists, then prove cleanup and slot recovery.
	settings := v.Project.Export
	settings.OutputMode = "book"
	settings.Format = "m4b"
	apply(EditRequest{Kind: "export", Export: &settings})
	parent := t.TempDir()
	destination := filepath.Join(parent, "cancel.m4b")
	s.deps.Export = func(ctx context.Context, p book.BookProject, path string, l *log.Logger, progress func(int, int, string)) (render.ExportResult, error) {
		return render.Export(ctx, p, path, l, func(current, total int, message string) {
			progress(current, total, message)
			if message == "Rendered whole-book PCM" {
				_ = s.CancelOperation("real-cancel")
			}
		})
	}
	if _, err = s.StartExport("real-cancel", v.Revision, destination); err != nil {
		t.Fatal(err)
	}
	s.workers.Wait()
	status, _ := s.GetOperation("real-cancel")
	if status.Status != "cancelled" {
		t.Fatal(status)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial files: %+v %v", entries, err)
	}
	s.deps.Export = render.Export
	if _, err = s.StartExport("recovered", v.Revision, destination); err != nil {
		t.Fatal(err)
	}
	s.workers.Wait()
	status, _ = s.GetOperation("recovered")
	if status.Status != "succeeded" {
		t.Fatal(status)
	}
	if terminalCount.Load() != int32(exported+2) {
		t.Fatalf("terminal events=%d", terminalCount.Load())
	}
	for _, src := range v.Project.Sources {
		data, err := os.ReadFile(src.Path)
		if err != nil || !bytes.Equal(data, original[src.Path]) {
			t.Fatal("source bytes changed")
		}
		st, err := os.Stat(src.Path)
		if err != nil || st.ModTime().UnixNano() != src.MTimeNS {
			t.Fatal("source mtime changed")
		}
	}
}
