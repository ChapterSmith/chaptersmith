package gui

import (
	"chaptersmith/internal/book"
	"chaptersmith/internal/meta"
)

// LoadRequest preserves explicit user choices when retrying an ambiguous import.
type LoadRequest struct {
	InputPath     string `json:"inputPath"`
	PlaylistPath  string `json:"playlistPath"`
	CuePath       string `json:"cuePath"`
	ChapterSource string `json:"chapterSource"`
}

// ProjectView is an independent snapshot with a derived output timeline.
type ProjectView struct {
	Project       book.BookProject `json:"project"`
	Revision      int64            `json:"revision"`
	Dirty         bool             `json:"dirty"`
	CanUndo       bool             `json:"canUndo"`
	DurationUS    int64            `json:"durationUs"`
	ChapterRows   []ChapterRow     `json:"chapterRows"`
	CoverURL      string           `json:"coverUrl"`
	SourceWarning string           `json:"sourceWarning"`
}

type ChapterRow struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	StartUS    int64  `json:"startUs"`
	DurationUS int64  `json:"durationUs"`
}

// EditRequest carries complete nested snapshots; literal blanks are meaningful.
type EditRequest struct {
	Kind        string                  `json:"kind"`
	ChapterID   string                  `json:"chapterId"`
	Title       string                  `json:"title"`
	Mode        string                  `json:"mode"`
	Position    string                  `json:"position"`
	Destination int                     `json:"destination"`
	NewStartUS  int64                   `json:"newStartUs"`
	DurationUS  int64                   `json:"durationUs"`
	PositionsUS []int64                 `json:"positionsUs"`
	Pauses      []book.ChapterPause     `json:"pauses"`
	Metadata    *meta.AudiobookMetadata `json:"metadata"`
	Export      *book.ExportSettings    `json:"export"`
}

// OperationStatus remains queryable after completion for fast RPC/event races.
type OperationStatus struct {
	RequestID           string   `json:"requestId"`
	ProjectID           string   `json:"projectId"`
	Revision            int64    `json:"revision"`
	Kind                string   `json:"kind"`
	Status              string   `json:"status"`
	Current             int      `json:"current"`
	Total               int      `json:"total"`
	Message             string   `json:"message"`
	TaskID              int64    `json:"taskId"`
	Error               string   `json:"error"`
	Paths               []string `json:"paths"`
	NormalizationReport string   `json:"normalizationReport"`
}
