package book

import (
	"encoding/json"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	"chaptersmith/internal/meta"
	"github.com/google/uuid"
)

func projectFixture(t *testing.T) BookProject {
	t.Helper()
	dir := t.TempDir()
	sources := []Source{
		{ID: uuid.NewString(), Path: filepath.Join(dir, "a.wav"), DurationUS: 10_000_000, SizeBytes: 1, MTimeNS: 1720000000000000001, SampleRate: 48000, Channels: 2},
		{ID: uuid.NewString(), Path: filepath.Join(dir, "b.wav"), DurationUS: 10_000_000, SizeBytes: 1, MTimeNS: 1720000000000000001, SampleRate: 48000, Channels: 2},
	}
	return BookProject{
		ID: uuid.NewString(), SchemaVersion: 1, Sources: sources,
		Chapters: []Chapter{
			{ID: uuid.NewString(), Title: "A", Segments: []Segment{{SourceID: sources[0].ID, EndUS: 10_000_000}}},
			{ID: uuid.NewString(), Title: "B", Segments: []Segment{{SourceID: sources[1].ID, EndUS: 10_000_000}}},
		},
		Metadata: meta.AudiobookMetadata{Extra: map[string]string{"language": "pl"}},
		Export:   ExportSettings{Profile: "standard", Channels: "auto", OutputMode: "book", Format: "m4b"},
	}
}

// These assertions catch schema drift, omitted deliberate empty fields and lost nanosecond precision.
func TestProjectJSONContract(t *testing.T) {
	project := projectFixture(t)
	data, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	var restored BookProject
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(project, restored) {
		t.Fatalf("round trip changed project: %s", data)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["schemaVersion"]) != "1" || string(fields["coverPath"]) != `""` {
		t.Fatalf("project schema: %s", data)
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(fields["metadata"], &metadata); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"title", "artist", "album", "albumArtist", "composer", "genre", "date", "copyright", "comment", "description", "subtitle", "grouping", "extra"} {
		if _, ok := metadata[key]; !ok {
			t.Errorf("missing metadata.%s", key)
		}
	}
	if string(metadata["description"]) != `""` {
		t.Error("empty description not preserved")
	}
	var sources []map[string]json.RawMessage
	if err := json.Unmarshal(fields["sources"], &sources); err != nil {
		t.Fatal(err)
	}
	if string(sources[0]["mtimeNs"]) != `"1720000000000000001"` {
		t.Fatalf("mtime lost precision: %s", data)
	}
	var chapters []struct {
		Segments []map[string]json.RawMessage `json:"segments"`
	}
	if err := json.Unmarshal(fields["chapters"], &chapters); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sourceId", "startUs", "endUs", "silenceUs"} {
		if _, ok := chapters[0].Segments[0][key]; !ok {
			t.Errorf("missing segment.%s", key)
		}
	}
	for _, forbidden := range []string{"Metadata", "StartUS", "author", "narrator"} {
		if _, ok := fields[forbidden]; ok {
			t.Errorf("unexpected %s", forbidden)
		}
		if _, ok := metadata[forbidden]; ok {
			t.Errorf("unexpected metadata.%s", forbidden)
		}
		if _, ok := chapters[0].Segments[0][forbidden]; ok {
			t.Errorf("unexpected segment.%s", forbidden)
		}
	}
}

func TestCloneIsolation(t *testing.T) {
	original := projectFixture(t)
	before, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	copied := Clone(original)
	copied.Metadata.Extra["language"] = "en"
	copied.Sources[0].Path = "/changed.wav"
	copied.Chapters[0].Segments[0].EndUS = 1
	var snapshot BookProject
	if err := json.Unmarshal(before, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, snapshot) {
		t.Fatal("clone mutated original")
	}
	for _, project := range []BookProject{{}, {Sources: []Source{}, Chapters: []Chapter{}, Metadata: meta.AudiobookMetadata{Extra: map[string]string{}}}} {
		if !reflect.DeepEqual(project, Clone(project)) {
			t.Fatal("clone changed nil/empty values")
		}
	}
}

func TestValidation(t *testing.T) {
	valid := projectFixture(t)
	if err := Validate(valid); err != nil {
		t.Fatal(err)
	}
	invalid := []struct {
		name   string
		change func(*BookProject)
	}{
		{"mixed", func(p *BookProject) { p.Chapters[0].Segments[0].SilenceUS = 1 }},
		{"empty chapter", func(p *BookProject) { p.Chapters[0].Segments = nil }},
		{"unknown source", func(p *BookProject) { p.Chapters[0].Segments[0].SourceID = "missing" }},
		{"missing source", func(p *BookProject) { p.Chapters[0].Segments[0].SourceID = "" }},
		{"negative start", func(p *BookProject) { p.Chapters[0].Segments[0].StartUS = -1 }},
		{"empty audio", func(p *BookProject) { p.Chapters[0].Segments[0].EndUS = 0 }},
		{"past source", func(p *BookProject) { p.Chapters[0].Segments[0].EndUS++ }},
		{"zero silence", func(p *BookProject) { p.Chapters[0].Segments[0] = Segment{} }},
		{"negative silence", func(p *BookProject) { p.Chapters[0].Segments[0] = Segment{SilenceUS: -1} }},
		{"silence offsets", func(p *BookProject) { p.Chapters[0].Segments[0] = Segment{StartUS: 1, SilenceUS: 1} }},
		{"schema", func(p *BookProject) { p.SchemaVersion = 2 }},
		{"profile", func(p *BookProject) { p.Export.Profile = "unknown" }},
		{"channels", func(p *BookProject) { p.Export.Channels = "unknown" }},
		{"mode", func(p *BookProject) { p.Export.OutputMode = "unknown" }},
		{"format", func(p *BookProject) { p.Export.Format = "unknown" }},
		{"duplicate source", func(p *BookProject) { p.Sources = append(p.Sources, p.Sources[0]) }},
		{"duplicate chapter", func(p *BookProject) { p.Chapters[1].ID = p.Chapters[0].ID }},
		{"source id", func(p *BookProject) { p.Sources[0].ID = "" }},
		{"chapter id", func(p *BookProject) { p.Chapters[0].ID = "" }},
		{"project id", func(p *BookProject) { p.ID = "" }},
		{"chapter title", func(p *BookProject) { p.Chapters[0].Title = " \t " }},
		{"source duration", func(p *BookProject) { p.Sources[0].DurationUS = 0 }},
		{"sample rate", func(p *BookProject) { p.Sources[0].SampleRate = 0 }},
		{"channels count", func(p *BookProject) { p.Sources[0].Channels = 0 }},
		{"size", func(p *BookProject) { p.Sources[0].SizeBytes = -1 }},
		{"relative path", func(p *BookProject) { p.Sources[0].Path = "a.wav" }},
		{"chapter overflow", func(p *BookProject) { p.Chapters[0].Segments = []Segment{{SilenceUS: math.MaxInt64}, {SilenceUS: 1}} }},
		{"project overflow", func(p *BookProject) { p.Chapters[0].Segments = []Segment{{SilenceUS: math.MaxInt64}} }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			p := projectFixture(t)
			test.change(&p)
			if err := Validate(p); err == nil {
				t.Fatal("accepted invalid project")
			}
		})
	}
	repeated := projectFixture(t)
	repeated.Chapters[1].Segments = append([]Segment(nil), repeated.Chapters[0].Segments...)
	if err := Validate(repeated); err != nil {
		t.Fatalf("intentional repetition rejected: %v", err)
	}
	for _, profile := range []string{"preserve", "economy", "standard", "high"} {
		for _, channels := range []string{"auto", "mono", "stereo"} {
			for _, mode := range []string{"book", "chapters", "both"} {
				for _, format := range []string{"m4b", "m4a", "flac", "wav"} {
					p := Clone(valid)
					p.Export = ExportSettings{Profile: profile, Channels: channels, OutputMode: mode, Format: format}
					if err := Validate(p); err != nil {
						t.Errorf("valid settings %+v: %v", p.Export, err)
					}
				}
			}
		}
	}
	if _, err := ChapterStartUS(valid, "unknown"); err == nil {
		t.Fatal("unknown chapter accepted")
	}
	invalidTimeline := Clone(valid)
	invalidTimeline.SchemaVersion = 0
	if _, err := ChapterStartUS(invalidTimeline, valid.Chapters[0].ID); err == nil {
		t.Fatal("invalid timeline accepted")
	}
}
