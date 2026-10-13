package gui

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"chaptersmith/internal/analysis"
	"chaptersmith/internal/book"
)

// A reordered, trimmed timeline must reach the engine unchanged, without editing the session.
func TestBookAnalysisSnapshot(t *testing.T) {
	s, _, p := newBookFixture(t)
	p.Chapters = []book.Chapter{{ID: "edited", Title: "Edited", Segments: []book.Segment{{SourceID: "s", StartUS: 12000000, EndUS: 18000000}, {SilenceUS: 2000000}, {SourceID: "s", StartUS: 1000000, EndUS: 5000000}}}}
	s.deps.Import = func(context.Context, book.ImportOptions) (book.BookProject, error) { return book.Clone(p), nil }
	v := loadedBook(t, s)
	s.deps.Analyze = func(ctx context.Context, in analysis.Input, opts analysis.Options, progress func(analysis.Progress)) (analysis.Report, error) {
		want := []analysis.Span{{SourceID: "s", StartUS: 12000000, EndUS: 18000000}, {SilenceUS: 2000000}, {SourceID: "s", StartUS: 1000000, EndUS: 5000000}}
		if !reflect.DeepEqual(in.Spans, want) || in.Channels != 2 {
			t.Errorf("wrong engine input %+v", in)
		}
		if opts.CacheDir != s.analysisOwner.Dir() {
			t.Error("engine outside owned cache")
		}
		return analysis.Report{ID: "engine-id", DurationUS: 12000000, ScannedUS: 12000000, Counts: map[string]int64{"quiet": 1}}, nil
	}
	r, err := s.AnalyzeAudio("analyze", v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if r.ProjectID != p.ID || r.Revision != v.Revision || r.Origin != "book" || r.Report.ID == "engine-id" {
		t.Fatalf("wrong binding %+v", r)
	}
	after, _ := s.GetProject()
	if !reflect.DeepEqual(v, after) {
		t.Fatal("analysis edited session")
	}
	r.Report.Counts["quiet"] = 999
	retained, err := s.GetAudioAnalysis(r.Report.ID)
	if err != nil || retained.Report.Counts["quiet"] != 1 {
		t.Fatal("report aliases caller")
	}
	if _, err = s.ApplyEdit(v.Revision, EditRequest{Kind: "cover-remove"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetAudioAnalysis(r.Report.ID); err == nil {
		t.Fatal("edit retained report")
	}
}

func TestBookAnalysisStaleAndCancel(t *testing.T) {
	s, _, _ := newBookFixture(t)
	v := loadedBook(t, s)
	if _, err := s.AnalyzeAudio("stale", v.Revision-1); err == nil {
		t.Fatal("stale accepted")
	}
	started, cleanup := make(chan struct{}), make(chan struct{})
	terminal := 0
	s.deps.Emit = func(_ string, p any) {
		if st, ok := p.(OperationStatus); ok && terminalStatus(st.Status) {
			terminal++
		}
	}
	s.deps.Analyze = func(ctx context.Context, _ analysis.Input, _ analysis.Options, _ func(analysis.Progress)) (analysis.Report, error) {
		close(started)
		<-ctx.Done()
		<-cleanup
		return analysis.Report{}, nil
	}
	done := make(chan error, 1)
	go func() { _, err := s.AnalyzeAudio("cancel", v.Revision); done <- err }()
	<-started
	if err := s.CancelOperation("cancel"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyEdit(v.Revision, EditRequest{Kind: "cover-remove"}); err == nil {
		t.Fatal("cancel released slot before cleanup")
	}
	close(cleanup)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if terminal != 1 {
		t.Fatalf("terminal events %d", terminal)
	}
	s.mu.Lock()
	retained := s.audioReport
	s.mu.Unlock()
	if retained != nil {
		t.Fatal("late report retained")
	}
}

func TestAnalysisPreviewCrop(t *testing.T) {
	_, _, p := newBookFixture(t)
	p.Sources[0].DurationUS = 90000000
	p.Chapters = []book.Chapter{{ID: "c", Title: "Trimmed", Segments: []book.Segment{{SourceID: "s", StartUS: 10000000, EndUS: 40000000}, {SilenceUS: 3000000}, {SourceID: "s", StartUS: 50000000, EndUS: 90000000}}}}
	a, b, c, d := int64(10000000), int64(18000000), int64(35000000), int64(43000000)
	r := analysis.Report{ScannedUS: 73000000, Findings: []analysis.Finding{{ID: "repeat", StartUS: &a, EndUS: &b, SecondStartUS: &c, SecondEndUS: &d}, {ID: "unlocated"}}}
	got, origin, marker, err := analysisPreviewProject(p, r, "repeat", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []book.Segment{{SilenceUS: 3000000}, {SourceID: "s", StartUS: 50000000, EndUS: 77000000}}
	if origin != 30000000 || marker != 5000000 || book.DurationUS(got) != 30000000 || !reflect.DeepEqual(got.Chapters[0].Segments, want) {
		t.Fatalf("crop %+v %d %d", got, origin, marker)
	}
	for _, id := range []string{"unlocated", "unknown"} {
		if _, _, _, err := analysisPreviewProject(p, r, id, 0); err == nil {
			t.Fatal("unlocated/unknown accepted")
		}
	}
	if _, _, _, err := analysisPreviewProject(p, r, "repeat", 2); err == nil {
		t.Fatal("invalid occurrence")
	}
}

func analysisWAV(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "audio.wav")
	const frames = 44100
	data := make([]byte, 44+frames*2)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 44100)
	binary.LittleEndian.PutUint32(data[28:], 88200)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], frames*2)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func analyzedFixture(t *testing.T) (*BookService, ProjectView, AudioAnalysisResult) {
	t.Helper()
	s, _, _ := newBookFixture(t)
	path := analysisWAV(t, t.TempDir())
	p, err := repairAnalysisProject(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	s.deps.Import = func(context.Context, book.ImportOptions) (book.BookProject, error) { return p, nil }
	v := loadedBook(t, s)
	r, err := s.AnalyzeAudio("real", v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	a, b := int64(100000), int64(200000)
	s.mu.Lock()
	s.audioReport.result.Report.Findings = []analysis.Finding{{ID: "located", StartUS: &a, EndUS: &b}}
	s.mu.Unlock()
	return s, v, r
}

func TestAnalysisPreviewSourceHash(t *testing.T) {
	s, v, r := analyzedFixture(t)
	path := v.Project.Sources[0].Path
	st, _ := os.Stat(path)
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteAt([]byte{1}, 50)
	f.Close()
	os.Chtimes(path, st.ModTime(), st.ModTime())
	s.deps.Preview = func(context.Context, book.BookProject, string, string) (string, error) {
		t.Error("changed source rendered")
		return "", nil
	}
	if _, err = s.PreviewAudioFinding("preview", v.Revision, r.Report.ID, "located", 0); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("hash change accepted: %v", err)
	}
	if _, err := s.GetAudioAnalysis(r.Report.ID); err == nil {
		t.Fatal("changed source report remains current")
	}
}

func TestSharedAnalysisEntrypoints(t *testing.T) {
	path := analysisWAV(t, t.TempDir())
	p, err := repairAnalysisProject(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	in, err := analysisInput(p)
	if err != nil {
		t.Fatal(err)
	}
	if in.Channels != 1 || len(in.Spans) != 1 || in.Spans[0].StartUS != 0 || in.Spans[0].EndUS != 1000000 || p.Export.Normalize || p.CoverPath != "" {
		t.Fatalf("bad diagnostic snapshot %+v", p)
	}
	// All project sources determine automatic channel mode, including unused sources.
	p.Sources = append(p.Sources, book.Source{ID: "unused", Path: path, DurationUS: 1000000, SampleRate: 44100, Channels: 2, SizeBytes: p.Sources[0].SizeBytes, MTimeNS: p.Sources[0].MTimeNS})
	in, err = analysisInput(p)
	if err != nil || in.Channels != 2 {
		t.Fatalf("unused stereo source ignored %+v %v", in, err)
	}
}

func TestBookAnalysisMediaInvalidation(t *testing.T) {
	for _, mutation := range []string{"edit", "load", "dismiss"} {
		t.Run(mutation, func(t *testing.T) {
			s, v, r := analyzedFixture(t)
			var path string
			s.deps.Preview = func(_ context.Context, _ book.BookProject, _, dir string) (string, error) {
				path = renderedPreview(t, dir)
				return path, nil
			}
			preview, err := s.PreviewAudioFinding("audition", v.Revision, r.Report.ID, "located", 0)
			if err != nil {
				t.Fatal(err)
			}
			if preview.MarkerUS != 100000 || preview.DurationUS != 1000000 {
				t.Fatal(preview)
			}
			switch mutation {
			case "edit":
				_, err = s.ApplyEdit(v.Revision, EditRequest{Kind: "cover-remove"})
			case "load":
				_, err = s.LoadBook("reload", LoadRequest{InputPath: "selected"})
			case "dismiss":
				err = s.ReleaseAudioAnalysis(r.Report.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.GetAudioAnalysis(r.Report.ID); err == nil {
				t.Fatal("report retained")
			}
			if _, err = os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatal("media stage retained")
			}
			if _, err = os.Stat(v.Project.Sources[0].Path); err != nil {
				t.Fatal("source removed")
			}
		})
	}
}

func TestSharedAnalysisEngineResults(t *testing.T) {
	f := newRepairFixture(t)
	p, err := repairAnalysisProject(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.b.deps.Import = func(context.Context, book.ImportOptions) (book.BookProject, error) { return p, nil }
	v := loadedBook(t, f.b)
	var inputs []analysis.Input
	engine := func(ctx context.Context, in analysis.Input, opts analysis.Options, progress func(analysis.Progress)) (analysis.Report, error) {
		inputs = append(inputs, in)
		return analysis.Analyze(ctx, in, opts, progress)
	}
	f.b.deps.Analyze = engine
	f.s.deps.Analyze = engine
	br, err := f.b.AnalyzeAudio("book", v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	id := f.start(t, true)
	rs := f.awaiting(t)
	if len(inputs) != 2 || len(inputs[0].Spans) != 1 || len(inputs[1].Spans) != 1 || inputs[0].Spans[0].EndUS != 1000000 || inputs[1].Spans[0].EndUS != 1000000 || inputs[0].Channels != inputs[1].Channels {
		t.Fatalf("different timeline inputs %+v", inputs)
	}
	if !reflect.DeepEqual(br.Report.Counts, rs.Result.Report.Counts) || !reflect.DeepEqual(br.Report.Coverage, rs.Result.Report.Coverage) || br.Report.ScannedUS != rs.Result.Report.ScannedUS {
		t.Fatal("entry points have different diagnostics")
	}
	f.s.ConfirmRepairAnalysis(id, false)
	<-f.done
}

func TestAnalysisMediaRegistrationFailure(t *testing.T) {
	s, v, r := analyzedFixture(t)
	var path string
	s.deps.Preview = func(_ context.Context, _ book.BookProject, _, dir string) (string, error) {
		path = renderedPreview(t, dir)
		if err := s.Media.Close(); err != nil {
			t.Fatal(err)
		}
		return path, nil
	}
	if _, err := s.PreviewAudioFinding("register", v.Revision, r.Report.ID, "located", 0); err == nil || !strings.Contains(err.Error(), "registering preview") {
		t.Fatalf("registration failure hidden: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("failed registration leaked stage")
	}
}

func TestAnalysisSourceIdentityRetained(t *testing.T) {
	s, v, r := analyzedFixture(t)
	path := v.Project.Sources[0].Path
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	replacement := path + ".replacement"
	if err = os.WriteFile(replacement, original, 0600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(replacement, st.ModTime(), st.ModTime())
	if err = os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PreviewAudioFinding("identity", v.Revision, r.Report.ID, "located", 0); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("original FileInfo was lost: %v", err)
	}
}
