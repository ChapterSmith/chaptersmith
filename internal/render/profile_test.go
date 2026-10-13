package render

import (
	"chaptersmith/internal/audiotest"
	"chaptersmith/internal/book"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Wrong profile/channel selection changes the actual encoder plan.
func TestAACProfileMatrix(t *testing.T) {
	for _, c := range []struct {
		profile      string
		mono, stereo int
	}{{"economy", 48000, 80000}, {"standard", 64000, 96000}, {"high", 96000, 160000}} {
		for _, ch := range []string{"mono", "stereo"} {
			p, _ := fixtureProject(t, 44100)
			p.Export.Profile = c.profile
			p.Export.Channels = ch
			before := book.Clone(p)
			plan, e := resolveEncoding(p)
			want := c.mono
			channels := 1
			if ch == "stereo" {
				want = c.stereo
				channels = 2
			}
			if e != nil || plan.BitRate != want || plan.Channels != channels || plan.Format != "m4b" {
				t.Fatalf("%s/%s: %+v %v", c.profile, ch, plan, e)
			}
			if !reflect.DeepEqual(p, before) {
				t.Fatal("project mutated")
			}
		}
	}
	for _, field := range []string{"profile", "channels", "format", "mode"} {
		p, _ := fixtureProject(t, 44100)
		switch field {
		case "profile":
			p.Export.Profile = "unknown"
		case "channels":
			p.Export.Channels = "unknown"
		case "format":
			p.Export.Format = "unknown"
		case "mode":
			p.Export.OutputMode = "unknown"
		}
		if _, e := resolveEncoding(p); e == nil {
			t.Fatal("unknown enum accepted", field)
		}
	}
	for _, format := range []string{"wav", "flac"} {
		p, _ := fixtureProject(t, 44100)
		p.Export.Format = format
		plan, e := resolveEncoding(p)
		if e != nil || plan.BitRate != 0 {
			t.Fatal("lossless bitrate", plan, e)
		}
	}
}
func preserveFixture(t *testing.T) book.BookProject {
	p, _ := fixtureProject(t, 44100)
	path := filepath.Join(t.TempDir(), "disguised.bin")
	audiotest.WriteAAC(t, path, 44100, 0)
	st, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	p.Sources[0].Path = path
	p.Sources[0].SizeBytes = st.Size()
	p.Sources[0].MTimeNS = st.ModTime().UnixNano()
	p.Export.Profile = "preserve"
	p.Chapters = []book.Chapter{{ID: "a", Title: "A", Segments: []book.Segment{{SourceID: "s", EndUS: 400000}}}, {ID: "b", Title: "B", Segments: []book.Segment{{SourceID: "s", StartUS: 400000, EndUS: 1000000}}}}
	return p
}
func TestPreserveEligibility(t *testing.T) {
	p := preserveFixture(t)
	plan, e := resolveEncoding(p)
	if e != nil || plan.CopySource != p.Sources[0].Path || plan.FallbackReason != "" {
		t.Fatal(plan, e)
	}
	cases := map[string]func(*book.BookProject){
		"trim":    func(p *book.BookProject) { p.Chapters[0].Segments[0].StartUS = 1 },
		"reorder": func(p *book.BookProject) { p.Chapters[0], p.Chapters[1] = p.Chapters[1], p.Chapters[0] },
		"silence": func(p *book.BookProject) {
			p.Chapters[0].Segments = append(p.Chapters[0].Segments, book.Segment{SilenceUS: 1000})
		},
		"normalize":  func(p *book.BookProject) { p.Export.Normalize = true },
		"conversion": func(p *book.BookProject) { p.Export.Channels = "stereo" },
		"multi":      func(p *book.BookProject) { s := p.Sources[0]; s.ID = "s2"; p.Sources = append(p.Sources, s) },
		"chapters":   func(p *book.BookProject) { p.Export.OutputMode = "chapters" },
		"both":       func(p *book.BookProject) { p.Export.OutputMode = "both" },
		"lossless":   func(p *book.BookProject) { p.Export.Format = "flac" },
		"notAAC":     func(p *book.BookProject) { q, _ := fixtureProject(t, 44100); p.Sources = q.Sources },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			q := book.Clone(p)
			change(&q)
			plan, e := resolveEncoding(q)
			if e != nil || plan.CopySource != "" || plan.FallbackReason == "" {
				t.Fatal(plan, e)
			}
			if q.Export.Format != "flac" && plan.BitRate != map[int]int{1: 64000, 2: 96000}[plan.Channels] {
				t.Fatal("fallback is not standard", plan)
			}
		})
	}
}

// A changed source is an operational failure, never permission to re-encode it.
func TestPreserveSourceFailureIsFatal(t *testing.T) {
	p := preserveFixture(t)
	p.Sources[0].SizeBytes++
	plan, e := resolveEncoding(p)
	if e == nil || plan.FallbackReason != "" || plan.CopySource != "" {
		t.Fatal("source failure converted to fallback", plan, e)
	}
}
