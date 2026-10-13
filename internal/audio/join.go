package audio

/*
#include <libavutil/channel_layout.h>
#include <stdlib.h>
static int join_layout_supported(const char *name, int rf64) {
 AVChannelLayout layout={0};
 int ret=av_channel_layout_from_string(&layout,name);
 int ok=ret>=0 && av_channel_layout_check(&layout) && layout.order==AV_CHANNEL_ORDER_NATIVE && layout.u.mask && (!rf64 || layout.u.mask<=UINT32_MAX);
 av_channel_layout_uninit(&layout);return ok;
}
// The bundled n8.0 AAC encoder does not publish AV_CODEC_CONFIG_CHANNEL_LAYOUT.
// These are its exact aac_normal_chan_layouts + aac_pce_configs masks.
static int join_aac_layout(const char *name) {
 static const uint64_t masks[]={
 AV_CH_LAYOUT_MONO,AV_CH_LAYOUT_STEREO,AV_CH_LAYOUT_2POINT1,AV_CH_LAYOUT_2_1,
 AV_CH_LAYOUT_SURROUND,AV_CH_LAYOUT_3POINT1,AV_CH_LAYOUT_4POINT0,AV_CH_LAYOUT_4POINT1,
 AV_CH_LAYOUT_2_2,AV_CH_LAYOUT_QUAD,AV_CH_LAYOUT_5POINT0,AV_CH_LAYOUT_5POINT1,
 AV_CH_LAYOUT_5POINT0_BACK,AV_CH_LAYOUT_5POINT1_BACK,AV_CH_LAYOUT_6POINT0,
 AV_CH_LAYOUT_6POINT0_FRONT,AV_CH_LAYOUT_HEXAGONAL,AV_CH_LAYOUT_6POINT1,
 AV_CH_LAYOUT_6POINT1_BACK,AV_CH_LAYOUT_6POINT1_FRONT,AV_CH_LAYOUT_7POINT0,
 AV_CH_LAYOUT_7POINT0_FRONT,AV_CH_LAYOUT_7POINT1,AV_CH_LAYOUT_7POINT1_WIDE,
 AV_CH_LAYOUT_7POINT1_WIDE_BACK,AV_CH_LAYOUT_OCTAGONAL,
 AV_CH_LAYOUT_OCTAGONAL|AV_CH_TOP_CENTER,
 AV_CH_LAYOUT_6POINT0_FRONT|AV_CH_BACK_CENTER|AV_CH_BACK_LEFT|AV_CH_BACK_RIGHT|AV_CH_TOP_CENTER,
 AV_CH_LAYOUT_HEXADECAGONAL};
 AVChannelLayout layout={0};int ok=0;
 if(av_channel_layout_from_string(&layout,name)>=0 && layout.order==AV_CHANNEL_ORDER_NATIVE)
  for(int i=0;i<sizeof(masks)/sizeof(masks[0]);i++) if(layout.u.mask==masks[i]) {ok=1;break;}
 av_channel_layout_uninit(&layout);return ok;
}

*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"unsafe"

	"github.com/asticode/go-astiav"
)

// JoinResult reports actual audible target frames for each ordered occurrence.
// Repeated paths remain separate occurrences.
type JoinResult struct {
	SampleRate int
	Frames     []int64
}

// JoinAAC decodes original inputs through audible EOF, stages bounded RF64 PCM,
// and encodes once at 128000 bit/s, gain 0 dB. The first source's exact layout
// and rate from its first audio stream are the target; later sources' first
// audio streams are converted by the shared PCM core.
// outputPath must not exist and belongs to the caller after success. Progress
// callbacks run serially. Parallelism cannot split a continuous AAC encoder.
func JoinAAC(ctx context.Context, inputs []string, outputPath string, progress ProgressFunc) (JoinResult, error) {
	return joinAAC(ctx, inputs, outputPath, progress, encodePCM)
}

type joinEncodeFunc func(context.Context, string, string, EncodeOptions, pcmFormat) error

func joinAAC(ctx context.Context, inputs []string, outputPath string, progress ProgressFunc, encode joinEncodeFunc) (result JoinResult, err error) {
	if len(inputs) == 0 {
		return result, errors.New("no input occurrences to join")
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	// Own codec parameters so the target layout outlives every probe context.
	target := astiav.AllocCodecParameters()
	if target == nil {
		return result, errors.New("allocating join parameters")
	}
	defer target.Free()
	for i, path := range inputs {
		if err = probeJoinFormat(ctx, path, func(cp *astiav.CodecParameters) error {
			// Legacy mono/stereo WAV headers omit a mask. Their conventional layouts
			// are unambiguous; never apply a count-based default to multichannel.
			switch cp.ChannelLayout().String() {
			case "1 channels":
				cp.SetChannelLayout(astiav.ChannelLayoutMono)
			case "2 channels":
				cp.SetChannelLayout(astiav.ChannelLayoutStereo)
			}
			if cp.SampleRate() <= 0 || !joinLayoutSupported(cp.ChannelLayout(), false) {
				return errors.New("unknown or unsupported source sample rate/channel layout")
			}
			if i == 0 {
				return cp.Copy(target)
			}
			return nil
		}); err != nil {
			return result, fmt.Errorf("join source %s: %w", path, err)
		}
	}
	f := pcmFormat{rate: target.SampleRate(), channels: target.ChannelLayout().Channels(), layout: target.ChannelLayout()}
	if err = validateJoinFormat(f); err != nil {
		return result, err
	}
	// Reject existing outputs before expensive staging, including source aliases.
	if _, e := os.Lstat(outputPath); e == nil {
		return result, fmt.Errorf("join output %s: %w", outputPath, os.ErrExist)
	} else if !os.IsNotExist(e) {
		return result, e
	}
	// No destination or staging file is created before every source is validated.
	stage, err := os.CreateTemp(filepath.Dir(outputPath), ".join-*.rf64")
	if err != nil {
		return result, err
	}
	stagePath := stage.Name()
	stageInfo, err := stage.Stat()
	if err != nil {
		return result, errors.Join(err, stage.Close())
	}
	var outputInfo os.FileInfo
	defer func() {
		current, e := os.Lstat(stagePath)
		if e == nil && os.SameFile(stageInfo, current) {
			err = errors.Join(err, os.Remove(stagePath))
		} else if e != nil && !os.IsNotExist(e) {
			err = errors.Join(err, e)
		}
		if err != nil && outputInfo != nil {
			current, e := os.Lstat(outputPath)
			if e == nil && os.SameFile(outputInfo, current) {
				err = errors.Join(err, os.Remove(outputPath))
			}
		}
	}()
	result = JoinResult{SampleRate: f.rate, Frames: make([]int64, len(inputs))}
	err = writeRF64(ctx, stage, f, func(emit func([]float32) error) error {
		for i, path := range inputs {
			d, e := openPCM(ctx, path, f)
			if e != nil {
				return fmt.Errorf("join source %s: %w", path, e)
			}
			e = d.Range(0, -1, func(b []float32) error {
				n := int64(len(b) / f.channels)
				if result.Frames[i] > math.MaxInt64-n {
					return errors.New("join frame count overflow")
				}
				if e := emit(b); e != nil {
					return e
				}
				result.Frames[i] += n
				return nil
			})
			e = errors.Join(e, d.Close())
			if e != nil {
				return fmt.Errorf("join source %s: %w", path, e)
			}
			if result.Frames[i] == 0 {
				return fmt.Errorf("join source %s: empty audible audio", path)
			}
			if progress != nil {
				progress(i+1, len(inputs)+1)
			}
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	current, e := os.Lstat(stagePath)
	if e != nil {
		return result, e
	}
	if !os.SameFile(stageInfo, current) {
		return result, errors.New("join staging file was replaced")
	}
	opts := EncodeOptions{Format: "m4a", Channels: f.channels, BitRate: 128000, StartFrame: 0, EndFrame: -1, GainDB: 0}
	if err = encode(ctx, stagePath, outputPath, opts, f); err != nil {
		return result, err
	}
	outputInfo, err = os.Lstat(outputPath)
	if err != nil {
		return result, err
	}
	if progress != nil {
		progress(len(inputs)+1, len(inputs)+1)
	}
	return result, ctx.Err()
}

func joinLayoutSupported(layout astiav.ChannelLayout, rf64 bool) bool {
	name := C.CString(layout.String())
	defer C.free(unsafe.Pointer(name))
	flag := 0
	if rf64 {
		flag = 1
	}
	return C.join_layout_supported(name, C.int(flag)) != 0
}
func validateJoinFormat(f pcmFormat) error {
	if !joinLayoutSupported(f.layout, true) {
		return fmt.Errorf("channel layout %q cannot be represented faithfully by RF64", f.layout.String())
	}
	codec := astiav.FindEncoder(astiav.CodecIDAac)
	if codec == nil {
		return errors.New("AAC encoder unavailable")
	}
	if !slices.Contains(codec.SupportedSampleRates(), f.rate) {
		return fmt.Errorf("AAC does not support sample rate %d", f.rate)
	}
	layouts := codec.SupportedChannelLayouts()
	supported := slices.ContainsFunc(layouts, func(l astiav.ChannelLayout) bool { return l.Equal(f.layout) })
	if len(layouts) == 0 {
		name := C.CString(f.layout.String())
		supported = C.join_aac_layout(name) != 0
		C.free(unsafe.Pointer(name))
	}
	if !supported {
		return fmt.Errorf("AAC does not support channel layout %q", f.layout.String())
	}
	return nil
}

func probeJoinFormat(ctx context.Context, path string, inspect func(*astiav.CodecParameters) error) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	info, e := os.Stat(path)
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() {
		return errors.New("input must be a regular file")
	}
	fc := astiav.AllocFormatContext()
	if fc == nil {
		return errors.New("allocating join probe")
	}
	defer fc.Free()
	interrupt := astiav.NewIOInterrupter()
	defer interrupt.Free()
	fc.SetIOInterrupter(interrupt)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			interrupt.Interrupt()
		case <-stop:
		}
	}()
	defer func() { close(stop); <-done }()
	if e = fc.OpenInput(path, nil, nil); e != nil {
		return errors.Join(e, ctx.Err())
	}
	defer fc.CloseInput()
	if e = fc.FindStreamInfo(nil); e != nil {
		return errors.Join(e, ctx.Err())
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	// Keep the first-audio rule identical to ProbeSource and book_pcm_open;
	// default dispositions must never change which source samples are joined.
	for _, s := range fc.Streams() {
		if s.CodecParameters().MediaType() == astiav.MediaTypeAudio {
			return inspect(s.CodecParameters())
		}
	}
	return errors.New("no audio stream")
}
