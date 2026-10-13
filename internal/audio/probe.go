package audio

import (
	"context"
	"fmt"
	"os"

	"github.com/asticode/go-astiav"
)

// SourceInfo describes the first audio stream and the unchanged source fingerprint.
type SourceInfo struct {
	DurationUS, SizeBytes, MTimeNS int64
	SampleRate, Channels           int
}

// ProbeSource probes a local source using libav and integer stream timing.
func ProbeSource(ctx context.Context, path string) (info SourceInfo, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("probing source %s: %w", path, err)
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	before, err := os.Stat(path)
	if err != nil {
		return info, err
	}
	if !before.Mode().IsRegular() {
		return info, fmt.Errorf("not a regular file")
	}
	fc := astiav.AllocFormatContext()
	if fc == nil {
		return info, fmt.Errorf("allocating format context")
	}
	defer fc.Free()
	ii := astiav.NewIOInterrupter()
	defer ii.Free()
	fc.SetIOInterrupter(ii)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			ii.Interrupt()
		case <-stop:
		}
	}()
	defer func() { close(stop); <-done }()
	if err = fc.OpenInput(path, nil, nil); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return
	}
	defer fc.CloseInput()
	if err = fc.FindStreamInfo(nil); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return
	}
	found := false
	for _, stream := range fc.Streams() {
		cp := stream.CodecParameters()
		if cp.MediaType() != astiav.MediaTypeAudio {
			continue
		}
		found = true
		info.SampleRate = cp.SampleRate()
		info.Channels = cp.ChannelLayout().Channels()
		if stream.Duration() > 0 {
			info.DurationUS = astiav.RescaleQ(stream.Duration(), stream.TimeBase(), astiav.NewRational(1, 1000000))
		} else {
			info.DurationUS = fc.Duration()
		}
		break
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if !found || info.DurationUS <= 0 || info.SampleRate <= 0 || info.Channels <= 0 {
		return info, fmt.Errorf("missing audio stream or invalid duration/audio parameters")
	}
	after, err := os.Stat(path)
	if err != nil {
		return info, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return info, fmt.Errorf("source changed during probe")
	}
	info.SizeBytes = after.Size()
	info.MTimeNS = after.ModTime().UnixNano()
	return info, nil
}

// GetDuration retains the legacy seconds API.
func GetDuration(file string) (float64, error) {
	info, err := ProbeSource(context.Background(), file)
	if err != nil {
		return 0, err
	}
	return float64(info.DurationUS) / 1000000, nil
}

// FormatDuration formats a duration in seconds as HH:MM:SS.
func FormatDuration(seconds float64) string {
	d := int(seconds)
	return fmt.Sprintf("%02d:%02d:%02d", d/3600, (d%3600)/60, d%60)
}

// GetFileSize returns a human-readable file size string.
func GetFileSize(file string) (string, error) {
	info, err := os.Stat(file)
	if err != nil {
		return "", fmt.Errorf("getting file info: %w", err)
	}

	size := float64(info.Size())
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case size >= GB:
		return fmt.Sprintf("%.2f GB", size/GB), nil
	case size >= MB:
		return fmt.Sprintf("%.2f MB", size/MB), nil
	case size >= KB:
		return fmt.Sprintf("%.2f KB", size/KB), nil
	default:
		return fmt.Sprintf("%d bytes", int(size)), nil
	}
}

// FileHasContent returns true if the file exists and is non-empty.
func FileHasContent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// CheckFilesCompatible probes all files and returns true when every file
// contains an AAC audio stream with the same sample rate and channel layout,
// meaning they can be concatenated via remux without transcoding.
// When files are incompatible the returned reason explains the first mismatch.
func CheckFilesCompatible(files []string) (bool, string) {
	if len(files) == 0 {
		return false, "no files"
	}

	type audioParams struct {
		codecID      astiav.CodecID
		sampleRate   int
		channelCount int
	}

	probe := func(path string) (audioParams, error) {
		fmtCtx := astiav.AllocFormatContext()
		if fmtCtx == nil {
			return audioParams{}, fmt.Errorf("allocating format context for %s", path)
		}
		defer fmtCtx.Free()

		if err := fmtCtx.OpenInput(path, nil, nil); err != nil {
			return audioParams{}, fmt.Errorf("opening %s: %w", path, err)
		}
		defer fmtCtx.CloseInput()

		if err := fmtCtx.FindStreamInfo(nil); err != nil {
			return audioParams{}, fmt.Errorf("finding stream info for %s: %w", path, err)
		}

		for _, s := range fmtCtx.Streams() {
			if s.CodecParameters().MediaType() == astiav.MediaTypeAudio {
				cp := s.CodecParameters()
				return audioParams{
					codecID:      cp.CodecID(),
					sampleRate:   cp.SampleRate(),
					channelCount: cp.ChannelLayout().Channels(),
				}, nil
			}
		}
		return audioParams{}, fmt.Errorf("no audio stream in %s", path)
	}

	ref, err := probe(files[0])
	if err != nil {
		return false, fmt.Sprintf("failed to probe first file: %v", err)
	}

	if ref.codecID != astiav.CodecIDAac {
		return false, fmt.Sprintf("%s is not AAC (codec %d)", files[0], ref.codecID)
	}

	for _, f := range files[1:] {
		p, err := probe(f)
		if err != nil {
			return false, fmt.Sprintf("failed to probe %s: %v", f, err)
		}
		if p.codecID != astiav.CodecIDAac {
			return false, fmt.Sprintf("%s is not AAC (codec %d)", f, p.codecID)
		}
		if p.sampleRate != ref.sampleRate {
			return false, fmt.Sprintf("sample rate mismatch: %s has %d Hz, expected %d Hz", f, p.sampleRate, ref.sampleRate)
		}
		if p.channelCount != ref.channelCount {
			return false, fmt.Sprintf("channel count mismatch: %s has %d channels, expected %d", f, p.channelCount, ref.channelCount)
		}
	}

	return true, ""
}
