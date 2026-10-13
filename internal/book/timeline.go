package book

import "fmt"

// DurationUS derives total duration from ordered segments. Call Validate first.
func DurationUS(project BookProject) int64 {
	var duration int64
	for _, chapter := range project.Chapters {
		duration += chapterDurationUS(chapter)
	}
	return duration
}

// ChapterStartUS validates the project and derives a chapter's output start.
func ChapterStartUS(project BookProject, chapterID string) (int64, error) {
	if err := Validate(project); err != nil {
		return 0, err
	}
	var start int64
	for _, chapter := range project.Chapters {
		if chapter.ID == chapterID {
			return start, nil
		}
		start += chapterDurationUS(chapter)
	}
	return 0, fmt.Errorf("unknown chapter ID %q", chapterID)
}

func segmentDurationUS(segment Segment) int64 {
	if segment.SourceID == "" {
		return segment.SilenceUS
	}
	return segment.EndUS - segment.StartUS
}

func chapterDurationUS(chapter Chapter) int64 {
	var duration int64
	for _, segment := range chapter.Segments {
		duration += segmentDurationUS(segment)
	}
	return duration
}
