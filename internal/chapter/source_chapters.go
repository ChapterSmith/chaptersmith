package chapter

import (
	"chaptersmith/internal/audio"
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// SourceChapter covers a local range in one ordered source occurrence.
type SourceChapter struct {
	SourceIndex    int
	Title          string
	StartUS, EndUS int64
}

// ResolveSourceChapters consumes the caller's source order, selected CUE, and canonical probe cache.
// In auto mode cuePath represents a discovered CUE; callers normalize an explicit CUE selection to cue mode.
func ResolveSourceChapters(ctx context.Context, files []string, entries []audio.M3UEntry, probes map[string]audio.SourceInfo, cuePath, chapterSource string) ([]SourceChapter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(entries) != 0 && len(entries) != len(files) {
		return nil, fmt.Errorf("playlist entries (%d) do not align with source occurrences (%d)", len(entries), len(files))
	}
	mode := strings.ToLower(chapterSource)
	if mode == "" {
		mode = "auto"
	}
	switch mode {
	case "auto", "cue", "embedded", "files":
	default:
		return nil, fmt.Errorf("invalid ChapterSource %q", chapterSource)
	}
	if cuePath != "" && (mode == "embedded" || mode == "files") {
		return nil, fmt.Errorf("CUE %s conflicts with ChapterSource=%s", cuePath, mode)
	}
	if mode == "cue" && cuePath == "" {
		return nil, fmt.Errorf("ChapterSource=cue requires CuePath")
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no source occurrences")
	}
	canonical := make([]string, len(files))
	durations := make([]int64, len(files))
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("source %s: %w", f, err)
		}
		p, err := canonicalChapterPath(f)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", f, err)
		}
		canonical[i] = p
		probe, ok := probes[p]
		if !ok || probe.DurationUS <= 0 {
			return nil, fmt.Errorf("source %s: missing probe or invalid duration", f)
		}
		durations[i] = probe.DurationUS
	}
	markers := make(map[string][]Marker)
	if mode == "embedded" || mode == "auto" {
		for _, p := range canonical {
			if _, ok := markers[p]; ok {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("source %s: %w", p, err)
			}
			m, err := ReadEmbeddedMarkers(p)
			if err != nil {
				return nil, err
			}
			markers[p] = m
			if mode == "auto" && cuePath != "" && len(m) > 0 {
				return nil, fmt.Errorf("CUE %s conflicts with embedded chapters in %s; select ChapterSource=cue, embedded or files", cuePath, p)
			}
		}
	}
	if cuePath != "" {
		return resolveCueSourceChapters(ctx, canonical, durations, cuePath)
	}
	var result []SourceChapter
	for i, p := range canonical {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("source %s: %w", p, err)
		}
		m := markers[p]
		if len(m) == 0 {
			result = append(result, SourceChapter{SourceIndex: i, Title: resolveChapterTitle(files[i], i, entries), StartUS: 0, EndUS: durations[i]})
			continue
		}
		ranges, err := sourceMarkerRanges(i, p, durations[i], m)
		if err != nil {
			return nil, err
		}
		result = append(result, ranges...)
	}
	return result, nil
}

func canonicalChapterPath(p string) (string, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

func resolveCueSourceChapters(ctx context.Context, files []string, durations []int64, path string) ([]SourceChapter, error) {
	tracks, err := ParseCueTracks(path)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f] {
			return nil, fmt.Errorf("CUE %s is ambiguous with repeated source occurrence %s; select embedded or files", path, f)
		}
		seen[f] = true
	}
	var cueFiles []string
	var groups [][]Marker
	for _, t := range tracks {
		p, err := canonicalChapterPath(t.FilePath)
		if err != nil {
			return nil, fmt.Errorf("CUE %s FILE %s: %w", path, t.FilePath, err)
		}
		if len(cueFiles) == 0 || cueFiles[len(cueFiles)-1] != p {
			cueFiles = append(cueFiles, p)
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], Marker{Title: t.Title, StartUS: t.StartUS})
	}
	if len(cueFiles) != len(files) {
		return nil, fmt.Errorf("CUE %s FILE list %v does not match ordered sources %v", path, cueFiles, files)
	}
	var result []SourceChapter
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("CUE %s: %w", path, err)
		}
		if cueFiles[i] != f {
			return nil, fmt.Errorf("CUE %s FILE list %v does not match ordered sources %v", path, cueFiles, files)
		}
		ranges, err := sourceMarkerRanges(i, f, durations[i], groups[i])
		if err != nil {
			return nil, fmt.Errorf("CUE %s: %w", path, err)
		}
		result = append(result, ranges...)
	}
	return result, nil
}

func sourceMarkerRanges(index int, path string, duration int64, markers []Marker) ([]SourceChapter, error) {
	for i, m := range markers {
		if m.StartUS < 0 || m.StartUS >= duration || (i > 0 && m.StartUS <= markers[i-1].StartUS) {
			return nil, fmt.Errorf("source %s: invalid, duplicate, decreasing or out-of-bounds chapter start %d", path, m.StartUS)
		}
	}
	var result []SourceChapter
	if len(markers) > 0 && markers[0].StartUS > 0 {
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		result = append(result, SourceChapter{SourceIndex: index, Title: stem + " — Opening", StartUS: 0, EndUS: markers[0].StartUS})
	}
	for i, m := range markers {
		end := duration
		if i+1 < len(markers) {
			end = markers[i+1].StartUS
		}
		title := strings.TrimSpace(m.Title)
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		result = append(result, SourceChapter{SourceIndex: index, Title: title, StartUS: m.StartUS, EndUS: end})
	}
	return result, nil
}
