package book

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

// Validate checks the structural project contract without touching source files.
func Validate(project BookProject) error {
	if project.SchemaVersion != 1 {
		return fmt.Errorf("unsupported project schema version %d", project.SchemaVersion)
	}
	if project.ID == "" {
		return fmt.Errorf("project ID is empty")
	}
	if err := validateSettings(project.Export); err != nil {
		return err
	}
	sources := make(map[string]Source, len(project.Sources))
	for i, source := range project.Sources {
		if source.ID == "" {
			return fmt.Errorf("source %d has an empty ID", i)
		}
		if _, exists := sources[source.ID]; exists {
			return fmt.Errorf("duplicate source ID %q", source.ID)
		}
		if !filepath.IsAbs(source.Path) {
			return fmt.Errorf("source %q path must be absolute", source.ID)
		}
		if source.DurationUS <= 0 || source.SampleRate <= 0 || source.Channels <= 0 || source.SizeBytes < 0 {
			return fmt.Errorf("source %q has invalid duration, sample rate, channels or size", source.ID)
		}
		sources[source.ID] = source
	}
	chapterIDs := make(map[string]bool, len(project.Chapters))
	var total int64
	for i, chapter := range project.Chapters {
		if chapter.ID == "" {
			return fmt.Errorf("chapter %d has an empty ID", i)
		}
		if chapterIDs[chapter.ID] {
			return fmt.Errorf("duplicate chapter ID %q", chapter.ID)
		}
		chapterIDs[chapter.ID] = true
		if strings.TrimSpace(chapter.Title) == "" {
			return fmt.Errorf("chapter %q title is empty", chapter.ID)
		}
		if len(chapter.Segments) == 0 {
			return fmt.Errorf("chapter %q has no segments", chapter.ID)
		}
		var chapterTotal int64
		for j, segment := range chapter.Segments {
			if segment.SourceID == "" {
				if segment.StartUS != 0 || segment.EndUS != 0 || segment.SilenceUS <= 0 {
					return fmt.Errorf("chapter %q segment %d is invalid silence", chapter.ID, j)
				}
			} else {
				source, exists := sources[segment.SourceID]
				if !exists {
					return fmt.Errorf("chapter %q segment %d refers to unknown source %q", chapter.ID, j, segment.SourceID)
				}
				if segment.SilenceUS != 0 || segment.StartUS < 0 || segment.StartUS >= segment.EndUS || segment.EndUS > source.DurationUS {
					return fmt.Errorf("chapter %q segment %d is an invalid audio interval", chapter.ID, j)
				}
			}
			duration := segmentDurationUS(segment)
			var err error
			chapterTotal, err = checkedDurationSum(chapterTotal, duration)
			if err != nil {
				return fmt.Errorf("chapter %q: %w", chapter.ID, err)
			}
		}
		var err error
		total, err = checkedDurationSum(total, chapterTotal)
		if err != nil {
			return fmt.Errorf("project: %w", err)
		}
	}
	return nil
}

func validateSettings(settings ExportSettings) error {
	switch settings.Profile {
	case "preserve", "economy", "standard", "high":
	default:
		return fmt.Errorf("invalid export profile %q", settings.Profile)
	}
	switch settings.Channels {
	case "auto", "mono", "stereo":
	default:
		return fmt.Errorf("invalid export channels %q", settings.Channels)
	}
	switch settings.OutputMode {
	case "book", "chapters", "both":
	default:
		return fmt.Errorf("invalid export output mode %q", settings.OutputMode)
	}
	switch settings.Format {
	case "m4b", "m4a", "flac", "wav":
	default:
		return fmt.Errorf("invalid export format %q", settings.Format)
	}
	return nil
}

// checkedDurationSum accepts only nonnegative durations and rejects int64 overflow.
func checkedDurationSum(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, fmt.Errorf("duration sum exceeds int64 range")
	}
	return a + b, nil
}
