package gui

import (
	"chaptersmith/internal/book"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBookChapterCommands(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    EditRequest
		lengths []int64
		total   int64
	}{
		{"transfer", EditRequest{Kind: "boundary", ChapterID: "c2", NewStartUS: 13000000, Mode: "transfer"}, []int64{13000000, 7000000}, 20000000},
		{"remove", EditRequest{Kind: "boundary", ChapterID: "c2", NewStartUS: 13000000, Mode: "remove"}, []int64{10000000, 7000000}, 17000000},
		{"backward", EditRequest{Kind: "boundary", ChapterID: "c2", NewStartUS: 8000000, Mode: "transfer"}, []int64{8000000, 12000000}, 20000000},
		{"silence", EditRequest{Kind: "silence", ChapterID: "c2", DurationUS: 500000, Position: "start"}, []int64{10000000, 10500000}, 20500000},
		{"rename", EditRequest{Kind: "rename", ChapterID: "c2", Title: "New title"}, []int64{10000000, 10000000}, 20000000},
		{"move", EditRequest{Kind: "move", ChapterID: "c2", Destination: 0}, []int64{10000000, 10000000}, 20000000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newBookFixture(t)
			old := loadedBook(t, s)
			before, err := os.ReadFile(old.Project.Sources[0].Path)
			if err != nil {
				t.Fatal(err)
			}
			v, err := s.ApplyEdit(old.Revision, tc.edit)
			if err != nil {
				t.Fatal(err)
			}
			if v.DurationUS != tc.total || v.ChapterRows[0].DurationUS != tc.lengths[0] || v.ChapterRows[1].DurationUS != tc.lengths[1] || v.ChapterRows[1].StartUS != tc.lengths[0] {
				t.Fatalf("timeline: %+v", v.ChapterRows)
			}
			if tc.name == "move" && !reflect.DeepEqual(v.Project.Chapters[0], old.Project.Chapters[1]) {
				t.Fatal("move lost segments")
			}
			if tc.name == "rename" && v.ChapterRows[1].Title != "New title" {
				t.Fatal("rename missing")
			}
			undone, err := s.Undo(v.Revision)
			if err != nil || !reflect.DeepEqual(undone.Project, old.Project) {
				t.Fatalf("undo: %v", err)
			}
			after, err := os.ReadFile(old.Project.Sources[0].Path)
			if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
				t.Fatal("chapter edit changed source bytes")
			}
		})
	}
	s, _, _ := newBookFixture(t)
	old := loadedBook(t, s)
	for _, e := range []EditRequest{
		{Kind: "boundary", ChapterID: "c1", NewStartUS: 1000000, Mode: "transfer"},
		{Kind: "boundary", ChapterID: "c2", NewStartUS: 20000000, Mode: "transfer"},
		{Kind: "boundary", ChapterID: "c2", NewStartUS: 0, Mode: "transfer"},
		{Kind: "boundary", ChapterID: "c2", NewStartUS: 8000000, Mode: "remove"},
		{Kind: "silence", ChapterID: "c2", DurationUS: -1, Position: "end"},
	} {
		if _, err := s.ApplyEdit(old.Revision, e); err == nil {
			t.Fatal("accepted invalid edit")
		}
		v, _ := s.GetProject()
		if !reflect.DeepEqual(v, old) {
			t.Fatal("rejected edit mutated view")
		}
	}
}

func TestBookProposalAtomicity(t *testing.T) {
	s, _, _ := newBookFixture(t)
	old := loadedBook(t, s)
	// A third chapter allows a later invalid proposal to test rollback after a valid shift.
	s.project.Chapters[1].Segments[0].EndUS = 15000000
	s.project.Chapters = append(s.project.Chapters, book.Chapter{ID: "c3", Title: "Third", Segments: []book.Segment{{SourceID: "s", StartUS: 15000000, EndUS: 20000000}}})
	old, _ = s.GetProject()
	a := book.BoundaryProposal{ChapterID: "c2", OldStartUS: 10000000, NewStartUS: 12000000}
	for _, proposals := range [][]book.BoundaryProposal{
		{{ChapterID: "c2", OldStartUS: 9, NewStartUS: 12000000}}, {a, a},
		{a, {ChapterID: "c3", OldStartUS: 15000000, NewStartUS: 20000000}},
		{a, {ChapterID: "c3", OldStartUS: 15000000, NewStartUS: 12000000}},
		{{ChapterID: "c1", OldStartUS: 0, NewStartUS: 1}},
	} {
		if _, err := s.ApplyBoundaries(old.Revision, proposals); err == nil {
			t.Fatal("invalid batch accepted")
		}
		v, _ := s.GetProject()
		if !reflect.DeepEqual(v, old) {
			t.Fatal("batch partially committed")
		}
	}
	empty, err := s.ApplyBoundaries(old.Revision, nil)
	if err != nil || !reflect.DeepEqual(empty, old) {
		t.Fatal("empty batch changed view")
	}
	v, err := s.ApplyBoundaries(old.Revision, []book.BoundaryProposal{{ChapterID: "c3", OldStartUS: 15000000, NewStartUS: 17000000}, a})
	if err != nil {
		t.Fatal(err)
	}
	if v.Revision != old.Revision+1 || len(s.undo) != 1 || v.DurationUS != 20000000 || v.ChapterRows[1].StartUS != 12000000 || v.ChapterRows[2].StartUS != 17000000 {
		t.Fatalf("bad batch: %+v", v)
	}
	if _, err = s.ApplyBoundaries(old.Revision, []book.BoundaryProposal{a}); err == nil {
		t.Fatal("stale accepted")
	}
	v, err = s.Undo(v.Revision)
	if err != nil || !reflect.DeepEqual(v.Project, old.Project) {
		t.Fatal("batch undo failed")
	}
}

func TestBookBoundaryPreviewWindow(t *testing.T) {
	s, _, p := newBookFixture(t)
	v := loadedBook(t, s)
	proposal := book.BoundaryProposal{ChapterID: "c2", OldStartUS: 10000000, NewStartUS: 13000000}
	clipped, origin, err := boundaryPreviewProject(p, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if origin != 5000000 || len(clipped.Chapters) != 1 || book.DurationUS(clipped) != 13000000 || !reflect.DeepEqual(clipped.Chapters[0].Segments, []book.Segment{{SourceID: "s", StartUS: 5000000, EndUS: 10000000}, {SourceID: "s", StartUS: 10000000, EndUS: 18000000}}) {
		t.Fatalf("bad clip: %+v origin %d", clipped, origin)
	}
	s.deps.Preview = func(_ context.Context, got book.BookProject, id, dir string) (string, error) {
		if !reflect.DeepEqual(got, clipped) || id != clipped.Chapters[0].ID {
			t.Fatal("wrong preview project")
		}
		stage, err := os.MkdirTemp(dir, ".book-preview-")
		if err != nil {
			return "", err
		}
		path := filepath.Join(stage, "preview.m4a")
		return path, os.WriteFile(path, []byte("audio"), 0600)
	}
	result, err := s.PreviewBoundary("boundary", v.Revision, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if result.OriginUS != 5000000 || result.OldMarkerUS != 5000000 || result.NewMarkerUS != 8000000 || result.DurationUS != 13000000 {
		t.Fatalf("markers %+v", result)
	}
	if err = s.ReleaseMedia(result.MediaID); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetProject()
	if !reflect.DeepEqual(after, v) {
		t.Fatal("preview mutated project")
	}
	proposal.OldStartUS++
	if _, _, err = boundaryPreviewProject(p, proposal); err == nil {
		t.Fatal("stale preview accepted")
	}
	p.Chapters[1].Segments = []book.Segment{{SilenceUS: 2000000}, {SourceID: "s", StartUS: 12000000, EndUS: 20000000}}
	proposal.OldStartUS = 10000000
	clipped, _, err = boundaryPreviewProject(p, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(clipped.Chapters[0].Segments, []book.Segment{{SourceID: "s", StartUS: 5000000, EndUS: 10000000}, {SilenceUS: 2000000}, {SourceID: "s", StartUS: 12000000, EndUS: 18000000}}) {
		t.Fatal("silence/offset lost")
	}
}

func TestBookSuggestionDefaultsAndStale(t *testing.T) {
	s, _, _ := newBookFixture(t)
	old := loadedBook(t, s)
	s.deps.Suggest = func(_ context.Context, p book.BookProject, opts book.SilenceOptions) ([]book.BoundaryProposal, error) {
		if opts != (book.SilenceOptions{WindowUS: 5000000, ThresholdDB: -40, MinSilenceUS: 500000}) {
			t.Fatalf("defaults: %+v", opts)
		}
		p.Chapters[0].Title = "alias mutation"
		return nil, nil
	}
	result, err := s.SuggestBoundaries("suggest", old.Revision, book.SilenceOptions{})
	if err != nil || result.RequestID != "suggest" || result.ProjectID != old.Project.ID || result.Revision != old.Revision || result.Proposals == nil {
		t.Fatalf("result %+v %v", result, err)
	}
	after, _ := s.GetProject()
	if !reflect.DeepEqual(after, old) {
		t.Fatal("suggest changed project")
	}
	if _, err = s.SuggestBoundaries("stale", old.Revision-1, book.SilenceOptions{}); err == nil {
		t.Fatal("stale accepted")
	}
	s.deps.Suggest = func(ctx context.Context, _ book.BookProject, _ book.SilenceOptions) ([]book.BoundaryProposal, error) {
		if err := s.CancelOperation("cancel"); err != nil {
			t.Fatal(err)
		}
		return []book.BoundaryProposal{{ChapterID: "c2", OldStartUS: 10000000, NewStartUS: 13000000}}, nil
	}
	if _, err = s.SuggestBoundaries("cancel", old.Revision, book.SilenceOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled result accepted: %v", err)
	}
}

// A later proposal can depend on a prior boundary moving backward in the same batch.
func TestBookProposalChronologicalNeighborTransfer(t *testing.T) {
	s, _, _ := newBookFixture(t)
	old := loadedBook(t, s)
	s.project.Chapters[1].Segments[0].EndUS = 15000000
	s.project.Chapters = append(s.project.Chapters, book.Chapter{ID: "c3", Title: "Third", Segments: []book.Segment{{SourceID: "s", StartUS: 15000000, EndUS: 20000000}}})
	proposals := []book.BoundaryProposal{{ChapterID: "c3", OldStartUS: 15000000, NewStartUS: 9000000}, {ChapterID: "c2", OldStartUS: 10000000, NewStartUS: 8000000}}
	if _, _, err := boundaryPreviewProject(s.project, proposals[0]); err != nil {
		t.Fatal(err)
	}
	v, err := s.ApplyBoundaries(old.Revision, proposals)
	if err != nil {
		t.Fatal(err)
	}
	if v.ChapterRows[1].StartUS != 8000000 || v.ChapterRows[2].StartUS != 9000000 || v.DurationUS != 20000000 {
		t.Fatalf("bad timeline %+v", v.ChapterRows)
	}
}
