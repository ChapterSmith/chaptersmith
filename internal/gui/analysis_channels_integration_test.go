package gui

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"chaptersmith/internal/audio"
	"chaptersmith/internal/audiotest"
	"chaptersmith/internal/book"
)

// Forced mono export must not turn opposite-phase stereo into a false quiet
// finding. Diagnostics and actual finding audio both use the auto channel mix.
func TestAnalysisFindingAuditionAutoChannels(t *testing.T) {
	for _, channels := range []int{1, 2} {
		for _, exportChannels := range []string{"mono", "stereo"} {
			name := map[int]string{1: "mono-source", 2: "antiphase-stereo-source"}[channels] + "/export-" + exportChannels
			t.Run(name, func(t *testing.T) {
				s, _, _ := newBookFixture(t)
				path := filepath.Join(t.TempDir(), "tone-then-quiet.wav")
				samples := make([]int16, 12*44100*channels)
				for i := 0; i < 6*44100; i++ {
					value := int16(12000 * math.Sin(2*math.Pi*440*float64(i)/44100))
					samples[i*channels] = value
					if channels == 2 {
						samples[i*channels+1] = -value
					}
				}
				audiotest.WriteWAV(t, path, 44100, channels, samples)
				s.deps.Import = book.Import
				v, err := s.LoadBook("load", LoadRequest{InputPath: path})
				if err != nil {
					t.Fatal(err)
				}
				settings := v.Project.Export
				settings.Channels = exportChannels
				settings.Profile = "high"
				settings.Normalize = true
				v, err = s.ApplyEdit(v.Revision, EditRequest{Kind: "export", Export: &settings})
				if err != nil {
					t.Fatal(err)
				}
				result, err := s.AnalyzeAudio("analyze", v.Revision)
				if err != nil {
					t.Fatal(err)
				}
				if s.audioReport.input.Channels != channels {
					t.Errorf("analysis channels=%d, want auto=%d", s.audioReport.input.Channels, channels)
				}
				var findingID string
				for _, f := range result.Report.Findings {
					if f.Kind != "quiet" {
						continue
					}
					if f.StartUS == nil || f.EndUS == nil {
						t.Fatal("unlocated quiet finding")
					}
					if *f.StartUS < 5900000 || *f.StartUS > 6100000 || *f.EndUS < 11900000 || *f.EndUS > 12100000 {
						t.Errorf("quiet interval=%d..%d, want only the final six seconds", *f.StartUS, *f.EndUS)
					}
					findingID = f.ID
				}
				if findingID == "" {
					t.Fatal("missing real six-second quiet finding")
				}
				preview, err := s.PreviewAudioFinding("audition", v.Revision, result.Report.ID, findingID, 0)
				if err != nil {
					t.Fatal(err)
				}
				s.Media.mu.Lock()
				previewPath := s.Media.entries[preview.MediaID].file.Name()
				s.Media.mu.Unlock()
				info, err := audio.ProbeSource(context.Background(), previewPath)
				if err != nil {
					t.Fatal(err)
				}
				if info.Channels != channels {
					t.Errorf("audition channels=%d, want auto=%d", info.Channels, channels)
				}
				var peak, quietPeak, tonePower float64
				var toneSamples int
				var frames int64
				err = audio.DecodePCM(context.Background(), previewPath, channels, 0, -1, func(b []float32) error {
					for i, value := range b {
						a := math.Abs(float64(value))
						peak = math.Max(peak, a)
						frame := frames + int64(i/channels)
						if frame >= 44100 && frame < 2*44100 {
							tonePower += float64(value) * float64(value)
							toneSamples++
						}
						if frame > 7*44100 {
							quietPeak = math.Max(quietPeak, a)
						}
					}
					frames += int64(len(b) / channels)
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if frames < 10*44100 || toneSamples == 0 {
					t.Fatalf("truncated audition: frames=%d toneSamples=%d", frames, toneSamples)
				}
				rms := math.Sqrt(tonePower / float64(toneSamples))
				t.Logf("analysis channels=%d audition channels=%d peak=%.6f toneRMS=%.6f quietPeak=%.6f origin=%d", s.audioReport.input.Channels, info.Channels, peak, rms, quietPeak, preview.OriginUS)
				if peak < .3 || rms < .24 || rms > .28 || quietPeak > .001 {
					t.Errorf("audition should preserve loud tone then silence without normalization: peak=%f toneRMS=%f quiet=%f", peak, rms, quietPeak)
				}
			})
		}
	}
}
