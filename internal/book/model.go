// Package book defines the source-backed, microsecond timeline of an audiobook project.
package book

import "chaptersmith/internal/meta"

// BookProject is the canonical versioned project exchanged with persistence and the editor.
type BookProject struct {
	ID            string                 `json:"id"`
	SchemaVersion int                    `json:"schemaVersion"`
	Sources       []Source               `json:"sources"`
	Chapters      []Chapter              `json:"chapters"`
	Metadata      meta.AudiobookMetadata `json:"metadata"`
	CoverPath     string                 `json:"coverPath"`
	Export        ExportSettings         `json:"export"`
}

// Source describes an imported audio file and its saved filesystem fingerprint.
type Source struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	DurationUS int64  `json:"durationUs"`
	SizeBytes  int64  `json:"sizeBytes"`
	MTimeNS    int64  `json:"mtimeNs,string"`
	SampleRate int    `json:"sampleRate"`
	Channels   int    `json:"channels"`
}

// Chapter owns an ordered sequence of source intervals or manual silence.
type Chapter struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Segments []Segment `json:"segments"`
}

// Segment is either a half-open source interval or a positive silence duration.
type Segment struct {
	SourceID  string `json:"sourceId"`
	StartUS   int64  `json:"startUs"`
	EndUS     int64  `json:"endUs"`
	SilenceUS int64  `json:"silenceUs"`
}

type ExportSettings struct {
	Profile    string `json:"profile"`
	Channels   string `json:"channels"`
	Normalize  bool   `json:"normalize"`
	OutputMode string `json:"outputMode"`
	Format     string `json:"format"`
}

type ImportOptions struct {
	InputPath     string `json:"inputPath"`
	CacheDir      string `json:"cacheDir"`
	PlaylistPath  string `json:"playlistPath"`
	CuePath       string `json:"cuePath"`
	ChapterSource string `json:"chapterSource"`
}

type SilenceOptions struct {
	WindowUS     int64   `json:"windowUs"`
	MinSilenceUS int64   `json:"minSilenceUs"`
	ThresholdDB  float64 `json:"thresholdDb"`
	// ChapterID limits the search to the start of one chapter; empty means all.
	ChapterID string `json:"chapterId,omitempty"`
}

// ChapterPause is the silence already present around one chapter's start:
// BeforeUS at the end of the previous chapter, AfterUS at its own beginning.
type ChapterPause struct {
	ChapterID string `json:"chapterId"`
	BeforeUS  int64  `json:"beforeUs"`
	AfterUS   int64  `json:"afterUs"`
}

// SplitOptions describes pauses long enough to start a new chapter.
type SplitOptions struct {
	MinSilenceUS int64   `json:"minSilenceUs"`
	ThresholdDB  float64 `json:"thresholdDb"`
	// MinChapterUS keeps proposed chapters, and their neighbours, at least this long.
	MinChapterUS int64 `json:"minChapterUs"`
}

// SplitProposal is a place on the output timeline to start a new chapter.
type SplitProposal struct {
	AtUS      int64 `json:"atUs"`
	SilenceUS int64 `json:"silenceUs"`
}

type BoundaryProposal struct {
	ChapterID  string `json:"chapterId"`
	OldStartUS int64  `json:"oldStartUs"`
	NewStartUS int64  `json:"newStartUs"`
}

// Clone returns an independent snapshot, preserving nil and deliberately empty values.
func Clone(project BookProject) BookProject {
	cloned := project
	if project.Sources != nil {
		cloned.Sources = make([]Source, len(project.Sources))
		copy(cloned.Sources, project.Sources)
	}
	if project.Chapters != nil {
		cloned.Chapters = make([]Chapter, len(project.Chapters))
		copy(cloned.Chapters, project.Chapters)
		for i, chapter := range project.Chapters {
			if chapter.Segments != nil {
				cloned.Chapters[i].Segments = make([]Segment, len(chapter.Segments))
				copy(cloned.Chapters[i].Segments, chapter.Segments)
			}
		}
	}
	if project.Metadata.Extra != nil {
		cloned.Metadata.Extra = make(map[string]string, len(project.Metadata.Extra))
		for key, value := range project.Metadata.Extra {
			cloned.Metadata.Extra[key] = value
		}
	}
	return cloned
}
