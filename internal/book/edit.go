package book

import (
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// RenameChapter returns a snapshot with the selected chapter's trimmed title.
func RenameChapter(project BookProject, chapterID, title string) (BookProject, error) {
	cloned, index, err := cloneForEdit(project, chapterID)
	if err != nil {
		return BookProject{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return BookProject{}, fmt.Errorf("chapter title must not be blank")
	}
	cloned.Chapters[index].Title = title
	return validatedEdit(cloned)
}

// MoveChapter moves a chapter and all its segments to a zero-based final index.
func MoveChapter(project BookProject, chapterID string, destination int) (BookProject, error) {
	cloned, index, err := cloneForEdit(project, chapterID)
	if err != nil {
		return BookProject{}, err
	}
	if destination < 0 || destination >= len(cloned.Chapters) {
		return BookProject{}, fmt.Errorf("chapter destination %d is out of bounds", destination)
	}
	moved := cloned.Chapters[index]
	if destination < index {
		copy(cloned.Chapters[destination+1:index+1], cloned.Chapters[destination:index])
	} else {
		copy(cloned.Chapters[index:destination], cloned.Chapters[index+1:destination+1])
	}
	cloned.Chapters[destination] = moved
	return validatedEdit(cloned)
}

// ShiftBoundary moves or removes source audio/silence at an immediate chapter boundary.
// newStartUS is on the current output timeline; removal closes the resulting gap.
func ShiftBoundary(project BookProject, chapterID string, newStartUS int64, mode string) (BookProject, error) {
	cloned, index, err := cloneForEdit(project, chapterID)
	if err != nil {
		return BookProject{}, err
	}
	if index == 0 {
		return BookProject{}, fmt.Errorf("the first chapter boundary cannot move")
	}
	if mode != "transfer" && mode != "remove" {
		return BookProject{}, fmt.Errorf("invalid boundary mode %q", mode)
	}
	var currentStart int64
	for _, chapter := range cloned.Chapters[:index] {
		currentStart += chapterDurationUS(chapter)
	}
	previousStart := currentStart - chapterDurationUS(cloned.Chapters[index-1])
	currentEnd := currentStart + chapterDurationUS(cloned.Chapters[index])
	if newStartUS <= previousStart || newStartUS >= currentEnd {
		return BookProject{}, fmt.Errorf("boundary must be strictly between previous chapter start and current chapter end")
	}
	if mode == "remove" && newStartUS <= currentStart {
		return BookProject{}, fmt.Errorf("remove requires a forward boundary cut")
	}
	if newStartUS == currentStart {
		return validatedEdit(cloned)
	}
	previous := &cloned.Chapters[index-1]
	current := &cloned.Chapters[index]
	if newStartUS > currentStart {
		prefix, suffix := splitSegments(current.Segments, newStartUS-currentStart)
		current.Segments = suffix
		if mode == "transfer" {
			previous.Segments = append(previous.Segments, prefix...)
		}
	} else {
		prefix, suffix := splitSegments(previous.Segments, newStartUS-previousStart)
		previous.Segments = prefix
		current.Segments = append(suffix, current.Segments...)
	}
	return validatedEdit(cloned)
}

// AddSilence adds a positive manual silence segment at the chapter's start or end.
func AddSilence(project BookProject, chapterID string, durationUS int64, position string) (BookProject, error) {
	cloned, index, err := cloneForEdit(project, chapterID)
	if err != nil {
		return BookProject{}, err
	}
	if durationUS <= 0 {
		return BookProject{}, fmt.Errorf("silence duration must be positive")
	}
	if position != "start" && position != "end" {
		return BookProject{}, fmt.Errorf("invalid silence position %q", position)
	}
	if _, err := checkedDurationSum(DurationUS(project), durationUS); err != nil {
		return BookProject{}, err
	}
	silence := Segment{SilenceUS: durationUS}
	if position == "start" {
		cloned.Chapters[index].Segments = append([]Segment{silence}, cloned.Chapters[index].Segments...)
	} else {
		cloned.Chapters[index].Segments = append(cloned.Chapters[index].Segments, silence)
	}
	return validatedEdit(cloned)
}

func cloneForEdit(project BookProject, chapterID string) (BookProject, int, error) {
	if err := Validate(project); err != nil {
		return BookProject{}, 0, err
	}
	for index, chapter := range project.Chapters {
		if chapter.ID == chapterID {
			return Clone(project), index, nil
		}
	}
	return BookProject{}, 0, fmt.Errorf("unknown chapter ID %q", chapterID)
}

func validatedEdit(project BookProject) (BookProject, error) {
	if err := Validate(project); err != nil {
		return BookProject{}, err
	}
	return project, nil
}

// splitSegments splits a valid sequence at an interior duration, preserving source offsets.
func splitSegments(segments []Segment, durationUS int64) (prefix, suffix []Segment) {
	for _, segment := range segments {
		duration := segmentDurationUS(segment)
		if durationUS == 0 {
			suffix = append(suffix, segment)
			continue
		}
		if durationUS >= duration {
			prefix = append(prefix, segment)
			durationUS -= duration
			continue
		}
		left, right := segment, segment
		if segment.SourceID == "" {
			left.SilenceUS = durationUS
			right.SilenceUS = duration - durationUS
		} else {
			left.EndUS = segment.StartUS + durationUS
			right.StartUS = left.EndUS
		}
		prefix = append(prefix, left)
		suffix = append(suffix, right)
		durationUS = 0
	}
	return prefix, suffix
}

// SplitChapters cuts chapters at positions on the current output timeline and
// commits every cut as one edit. Each position must lie strictly inside a
// chapter. The part before a cut keeps the chapter's ID and title; the part
// after it becomes a new chapter named for its place in the book.
func SplitChapters(project BookProject, positionsUS []int64) (BookProject, error) {
	if err := Validate(project); err != nil {
		return BookProject{}, err
	}
	if len(positionsUS) == 0 {
		return BookProject{}, fmt.Errorf("no split positions")
	}
	cuts := slices.Clone(positionsUS)
	slices.Sort(cuts)
	cuts = slices.Compact(cuts)
	cloned := Clone(project)
	var chapters []Chapter
	created := map[int]bool{}
	var start int64
	next := 0
	for _, chapter := range cloned.Chapters {
		end := start + chapterDurationUS(chapter)
		rest, restStart := chapter, start
		for next < len(cuts) && cuts[next] < end {
			at := cuts[next]
			if at <= restStart {
				return BookProject{}, fmt.Errorf("split position %d is not inside a chapter", at)
			}
			prefix, suffix := splitSegments(rest.Segments, at-restStart)
			head := rest
			head.Segments = prefix
			chapters = append(chapters, head)
			rest = Chapter{ID: uuid.NewString(), Segments: suffix}
			created[len(chapters)] = true
			restStart = at
			next++
		}
		chapters = append(chapters, rest)
		start = end
	}
	if next < len(cuts) {
		return BookProject{}, fmt.Errorf("split position %d is not inside a chapter", cuts[next])
	}
	for index := range chapters {
		if created[index] {
			chapters[index].Title = fmt.Sprintf("Chapter %d", index+1)
		}
	}
	cloned.Chapters = chapters
	return validatedEdit(cloned)
}

// MergeChapter appends a chapter to the one before it, which keeps its ID and
// title. Audio that was contiguous in the source becomes one segment again.
func MergeChapter(project BookProject, chapterID string) (BookProject, error) {
	cloned, index, err := cloneForEdit(project, chapterID)
	if err != nil {
		return BookProject{}, err
	}
	if index == 0 {
		return BookProject{}, fmt.Errorf("the first chapter has no previous chapter to merge into")
	}
	previous := &cloned.Chapters[index-1]
	for _, segment := range cloned.Chapters[index].Segments {
		last := len(previous.Segments) - 1
		if last >= 0 && segment.SourceID != "" && previous.Segments[last].SourceID == segment.SourceID && previous.Segments[last].EndUS == segment.StartUS {
			previous.Segments[last].EndUS = segment.EndUS
			continue
		}
		previous.Segments = append(previous.Segments, segment)
	}
	cloned.Chapters = slices.Delete(cloned.Chapters, index, index+1)
	return validatedEdit(cloned)
}

// SetChapterPauses gives every chapter after the first the same pause before
// it, as one edit. The silence listed in existing is cut away first, so the
// pause replaces what the recording already had; chapters without an entry
// only gain the pause. A chapter never loses all of its audio: when the listed
// silence would consume it, that chapter is left untrimmed. pauseUS may be zero
// to close the gaps.
func SetChapterPauses(project BookProject, existing []ChapterPause, pauseUS int64) (BookProject, error) {
	if err := Validate(project); err != nil {
		return BookProject{}, err
	}
	if pauseUS < 0 {
		return BookProject{}, fmt.Errorf("pause length must not be negative")
	}
	if len(project.Chapters) < 2 {
		return BookProject{}, fmt.Errorf("the book needs at least two chapters to have a pause between them")
	}
	cloned := Clone(project)
	head, tail := make([]int64, len(cloned.Chapters)), make([]int64, len(cloned.Chapters))
	index := map[string]int{}
	for i, chapter := range cloned.Chapters {
		index[chapter.ID] = i
	}
	for _, pause := range existing {
		i, known := index[pause.ChapterID]
		if !known || i == 0 || pause.BeforeUS < 0 || pause.AfterUS < 0 {
			return BookProject{}, fmt.Errorf("invalid pause for chapter %q", pause.ChapterID)
		}
		head[i], tail[i-1] = pause.AfterUS, pause.BeforeUS
	}
	if _, err := checkedDurationSum(DurationUS(project), pauseUS*int64(len(cloned.Chapters)-1)); err != nil {
		return BookProject{}, err
	}
	for i := range cloned.Chapters {
		chapter := &cloned.Chapters[i]
		duration := chapterDurationUS(*chapter)
		if head[i]+tail[i] >= duration {
			head[i], tail[i] = 0, 0
		}
		_, chapter.Segments = splitSegments(chapter.Segments, head[i])
		chapter.Segments, _ = splitSegments(chapter.Segments, duration-head[i]-tail[i])
		if pauseUS > 0 && i < len(cloned.Chapters)-1 {
			chapter.Segments = append(chapter.Segments, Segment{SilenceUS: pauseUS})
		}
	}
	return validatedEdit(cloned)
}
