package audio

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"

	"github.com/asticode/go-astiav"
	"github.com/schollz/progressbar/v3"
)

// ProgressFunc is called after each file is transcoded with (current, total).
type ProgressFunc func(current, total int)

// TranscodeOptions allows callers to transcode only a time segment of the input.
type TranscodeOptions struct {
	StartSec float64 // seek to this position before transcoding (0 = beginning)
	EndSec   float64 // stop transcoding at this position (0 = until EOF)
}

// ptsNormState normalizes decoder PTS so the AAC encoder never receives
// AV_NOPTS_VALUE or inconsistent timestamps (avcodec_send_frame EINVAL).
type ptsNormState struct {
	baseSet   bool
	base      int64
	synthetic bool
	nextPts   int64
	encPts    int64 // monotonic PTS assigned to each 1024-sample encoder frame
}

func (s *ptsNormState) apply(decFrame *astiav.Frame) {
	pts := decFrame.Pts()
	nb := decFrame.NbSamples()
	if nb <= 0 {
		return
	}
	if s.synthetic {
		decFrame.SetPts(s.nextPts)
		s.nextPts += int64(nb)
		return
	}
	if pts == astiav.NoPtsValue {
		s.synthetic = true
		decFrame.SetPts(0)
		s.nextPts = int64(nb)
		return
	}
	if !s.baseSet {
		s.base = pts
		s.baseSet = true
	}
	decFrame.SetPts(pts - s.base)
}

// applyEnc assigns a monotonic PTS to a filter output frame before encoding.
// asetnsamples can drop or reset timestamps, which causes AAC encoder EINVAL.
func (s *ptsNormState) applyEnc(frame *astiav.Frame) {
	nb := frame.NbSamples()
	if nb <= 0 {
		return
	}
	frame.SetPts(s.encPts)
	s.encPts += int64(nb)
}

// TranscodeParallel converts audio files to AAC/M4A using a bounded worker pool.
// Each file is decoded from its source format and re-encoded as AAC into the
// tempDir. On first error the context is cancelled to stop remaining jobs.
// progress is optional; called after each file completes, never concurrently.
// Cancellation before all files are converted returns an error, never a
// partial list.
// showBar controls whether a terminal progress bar is displayed (disable for GUI).
func TranscodeParallel(ctx context.Context, audioFiles []string, tempDir string, parallelism, cpuCores int, progress ProgressFunc, showBar bool) ([]string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	convertedFiles := make([]string, len(audioFiles))

	var (
		mu       sync.Mutex // guards firstErr, done and serializes bar/progress calls
		firstErr error
		done     int
		wg       sync.WaitGroup
	)

	sem := make(chan struct{}, parallelism)
	var bar *progressbar.ProgressBar
	if showBar {
		bar = progressbar.Default(int64(len(audioFiles)))
	}

loop:
	for i, inputFile := range audioFiles {
		select {
		case <-ctx.Done():
			break loop
		case sem <- struct{}{}:
		}

		wg.Go(func() {
			defer func() { <-sem }()

			outputFile := filepath.Join(tempDir, fmt.Sprintf("%03d.m4a", i+1))
			err := TranscodeFile(ctx, inputFile, outputFile)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("converting %s: %w", inputFile, err)
					cancel()
				}
				return
			}
			convertedFiles[i] = outputFile
			done++
			if bar != nil {
				bar.Add(1) //nolint:errcheck
			}
			if progress != nil {
				progress(done, len(audioFiles))
			}
		})
	}

	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	// Cancellation can stop the loop before every file was started while all
	// started jobs succeed; never return a partial list as success.
	if done != len(audioFiles) {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("converting audio files: %w", err)
		}
		return nil, fmt.Errorf("converting audio files: only %d of %d converted", done, len(audioFiles))
	}

	return convertedFiles, nil
}

// TranscodeFile decodes an audio file from any supported format and re-encodes
// it as AAC in an M4A container using the astiav (libav) pipeline:
// input -> decode -> filter (aformat) -> encode (AAC) -> mux (M4A).
// An optional TranscodeOptions may be passed to transcode only a time segment.
func TranscodeFile(ctx context.Context, inputPath, outputPath string, opts ...TranscodeOptions) error {
	var opt TranscodeOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	inputFmtCtx := astiav.AllocFormatContext()
	if inputFmtCtx == nil {
		return errors.New("allocating input format context")
	}
	defer inputFmtCtx.Free()

	if err := inputFmtCtx.OpenInput(inputPath, nil, nil); err != nil {
		return fmt.Errorf("opening input %s: %w", inputPath, err)
	}
	defer inputFmtCtx.CloseInput()

	if err := inputFmtCtx.FindStreamInfo(nil); err != nil {
		return fmt.Errorf("finding stream info: %w", err)
	}

	// Find the best audio stream.
	inputStream, decCodec, err := inputFmtCtx.FindBestStream(astiav.MediaTypeAudio, -1, -1)
	if err != nil {
		return fmt.Errorf("finding audio stream: %w", err)
	}
	if decCodec == nil {
		return errors.New("no audio decoder found")
	}

	decCtx := astiav.AllocCodecContext(decCodec)
	if decCtx == nil {
		return errors.New("allocating decoder context")
	}
	defer decCtx.Free()

	if err := inputStream.CodecParameters().ToCodecContext(decCtx); err != nil {
		return fmt.Errorf("copying codec parameters to decoder: %w", err)
	}
	// Audio decoders do not use video-style frame threading; keep single-threaded.
	decCtx.SetThreadCount(1)
	if err := decCtx.Open(decCodec, nil); err != nil {
		return fmt.Errorf("opening decoder: %w", err)
	}

	encCodec := astiav.FindEncoder(astiav.CodecIDAac)
	if encCodec == nil {
		return errors.New("AAC encoder not found")
	}

	encCtx := astiav.AllocCodecContext(encCodec)
	if encCtx == nil {
		return errors.New("allocating encoder context")
	}
	defer encCtx.Free()

	// Configure AAC encoder.
	encCtx.SetBitRate(128000)
	encCtx.SetSampleRate(decCtx.SampleRate())
	encCtx.SetTimeBase(astiav.NewRational(1, decCtx.SampleRate()))

	// Use the encoder's preferred sample format.
	if formats := encCodec.SupportedSampleFormats(); len(formats) > 0 {
		encCtx.SetSampleFormat(formats[0])
	} else {
		encCtx.SetSampleFormat(decCtx.SampleFormat())
	}

	// Match the input channel layout; aformat in the filter graph handles conversion.
	encCtx.SetChannelLayout(decCtx.ChannelLayout())

	outputFmtCtx, err := astiav.AllocOutputFormatContext(nil, "ipod", outputPath)
	if err != nil {
		return fmt.Errorf("allocating output format context: %w", err)
	}
	if outputFmtCtx == nil {
		return errors.New("output format context is nil")
	}
	defer outputFmtCtx.Free()

	// Set global header flag if the container needs it.
	if outputFmtCtx.OutputFormat().Flags().Has(astiav.IOFormatFlagGlobalheader) {
		encCtx.SetFlags(encCtx.Flags().Add(astiav.CodecContextFlagGlobalHeader))
	}

	if err := encCtx.Open(encCodec, nil); err != nil {
		return fmt.Errorf("opening encoder: %w", err)
	}

	outputStream := outputFmtCtx.NewStream(nil)
	if outputStream == nil {
		return errors.New("creating output stream")
	}
	if err := outputStream.CodecParameters().FromCodecContext(encCtx); err != nil {
		return fmt.Errorf("copying encoder params to output stream: %w", err)
	}
	outputStream.SetTimeBase(encCtx.TimeBase())

	ioCtx, err := astiav.OpenIOContext(outputPath, astiav.NewIOContextFlags(astiav.IOContextFlagWrite), nil, nil)
	if err != nil {
		return fmt.Errorf("opening output file: %w", err)
	}
	defer ioCtx.Close() //nolint:errcheck
	outputFmtCtx.SetPb(ioCtx)

	filterGraph := astiav.AllocFilterGraph()
	if filterGraph == nil {
		return errors.New("allocating filter graph")
	}
	defer filterGraph.Free()
	filterGraph.SetThreadCount(0)

	abuffersrc := astiav.FindFilterByName("abuffer")
	abuffersink := astiav.FindFilterByName("abuffersink")
	if abuffersrc == nil || abuffersink == nil {
		return errors.New("abuffer or abuffersink filter not found")
	}

	buffersrcCtx, err := filterGraph.NewBuffersrcFilterContext(abuffersrc, "in")
	if err != nil {
		return fmt.Errorf("creating buffersrc context: %w", err)
	}

	buffersrcParams := astiav.AllocBuffersrcFilterContextParameters()
	defer buffersrcParams.Free()
	buffersrcParams.SetChannelLayout(decCtx.ChannelLayout())
	buffersrcParams.SetSampleFormat(decCtx.SampleFormat())
	buffersrcParams.SetSampleRate(decCtx.SampleRate())
	buffersrcParams.SetTimeBase(decCtx.TimeBase())
	if err := buffersrcCtx.SetParameters(buffersrcParams); err != nil {
		return fmt.Errorf("setting buffersrc parameters: %w", err)
	}
	if err := buffersrcCtx.Initialize(nil); err != nil {
		return fmt.Errorf("initializing buffersrc: %w", err)
	}

	buffersinkCtx, err := filterGraph.NewBuffersinkFilterContext(abuffersink, "out")
	if err != nil {
		return fmt.Errorf("creating buffersink context: %w", err)
	}

	// Build filter description: aformat then asetnsamples so every frame has
	// AAC-LC's required 1024 samples (avoids "frame_size (1024) was not respected" / EINVAL).
	filterDesc := fmt.Sprintf(
		"aformat=sample_fmts=%s:channel_layouts=%s:sample_rates=%d,asetnsamples=n=1024:p=1",
		encCtx.SampleFormat().Name(),
		encCtx.ChannelLayout().String(),
		encCtx.SampleRate(),
	)

	outputs := astiav.AllocFilterInOut()
	if outputs == nil {
		return errors.New("allocating filter outputs")
	}
	defer outputs.Free()

	inputs := astiav.AllocFilterInOut()
	if inputs == nil {
		return errors.New("allocating filter inputs")
	}
	defer inputs.Free()

	outputs.SetName("in")
	outputs.SetFilterContext(buffersrcCtx.FilterContext())
	outputs.SetPadIdx(0)
	outputs.SetNext(nil)

	inputs.SetName("out")
	inputs.SetFilterContext(buffersinkCtx.FilterContext())
	inputs.SetPadIdx(0)
	inputs.SetNext(nil)

	if err := filterGraph.Parse(filterDesc, inputs, outputs); err != nil {
		return fmt.Errorf("parsing filter graph: %w", err)
	}
	if err := filterGraph.Configure(); err != nil {
		return fmt.Errorf("configuring filter graph: %w", err)
	}

	if opt.StartSec > 0 {
		tb := inputStream.TimeBase()
		seekTS := int64(opt.StartSec * float64(tb.Den()) / float64(tb.Num()))
		if err := inputFmtCtx.SeekFrame(inputStream.Index(), seekTS, astiav.NewSeekFlags(astiav.SeekFlagBackward)); err != nil {
			return fmt.Errorf("seeking to %.2fs: %w", opt.StartSec, err)
		}
	}

	if err := outputFmtCtx.WriteHeader(nil); err != nil {
		return fmt.Errorf("writing output header: %w", err)
	}

	pkt := astiav.AllocPacket()
	defer pkt.Free()

	decFrame := astiav.AllocFrame()
	defer decFrame.Free()

	filterFrame := astiav.AllocFrame()
	defer filterFrame.Free()

	encPkt := astiav.AllocPacket()
	defer encPkt.Free()

	var ptsNorm ptsNormState
	var samplesSinceSeek int64
	segmentDone := false
	hasSegmentBounds := opt.StartSec > 0 || opt.EndSec > 0
	sr := decCtx.SampleRate()
	if sr <= 0 {
		return errors.New("invalid decoder sample rate")
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if segmentDone {
			break
		}

		if err := inputFmtCtx.ReadFrame(pkt); err != nil {
			if errors.Is(err, astiav.ErrEof) {
				break
			}
			return fmt.Errorf("reading frame: %w", err)
		}

		if pkt.StreamIndex() != inputStream.Index() {
			pkt.Unref()
			continue
		}

		// Rescale timestamps from stream to decoder time base.
		pkt.RescaleTs(inputStream.TimeBase(), decCtx.TimeBase())

		if err := decCtx.SendPacket(pkt); err != nil {
			pkt.Unref()
			return fmt.Errorf("sending packet to decoder: %w", err)
		}
		pkt.Unref()

		// Receive decoded frames and push through filter + encoder.
		for {
			if err := decCtx.ReceiveFrame(decFrame); err != nil {
				if errors.Is(err, astiav.ErrEof) || errors.Is(err, astiav.ErrEagain) {
					break
				}
				return fmt.Errorf("receiving decoded frame: %w", err)
			}

			nb := int64(decFrame.NbSamples())
			fileSecStart := opt.StartSec + float64(samplesSinceSeek)/float64(sr)
			samplesSinceSeek += nb

			var frameSec float64
			if decFrame.Pts() != astiav.NoPtsValue {
				tb := decCtx.TimeBase()
				frameSec = float64(decFrame.Pts()) * float64(tb.Num()) / float64(tb.Den())
			} else {
				frameSec = fileSecStart
			}

			if hasSegmentBounds {
				if opt.StartSec > 0 && frameSec < opt.StartSec-0.001 {
					decFrame.Unref()
					continue
				}
				if opt.EndSec > 0 && frameSec >= opt.EndSec {
					decFrame.Unref()
					segmentDone = true
					break
				}
			}

			ptsNorm.apply(decFrame)

			if err := filterEncodeWrite(buffersrcCtx, buffersinkCtx, encCtx, outputFmtCtx, outputStream, decFrame, filterFrame, encPkt, &ptsNorm); err != nil {
				decFrame.Unref()
				return err
			}
			decFrame.Unref()
		}
	}

	// Flush decoder (skip if we hit the segment end boundary).
	if !segmentDone {
		if err := decCtx.SendPacket(nil); err != nil && !errors.Is(err, astiav.ErrEof) {
			return fmt.Errorf("flushing decoder: %w", err)
		}
		for {
			if err := decCtx.ReceiveFrame(decFrame); err != nil {
				if errors.Is(err, astiav.ErrEof) || errors.Is(err, astiav.ErrEagain) {
					break
				}
				return fmt.Errorf("receiving flushed frame: %w", err)
			}

			nb := int64(decFrame.NbSamples())
			fileSecStart := opt.StartSec + float64(samplesSinceSeek)/float64(sr)
			samplesSinceSeek += nb

			var frameSec float64
			if decFrame.Pts() != astiav.NoPtsValue {
				tb := decCtx.TimeBase()
				frameSec = float64(decFrame.Pts()) * float64(tb.Num()) / float64(tb.Den())
			} else {
				frameSec = fileSecStart
			}

			if hasSegmentBounds && opt.EndSec > 0 && frameSec >= opt.EndSec {
				decFrame.Unref()
				break
			}

			ptsNorm.apply(decFrame)

			if err := filterEncodeWrite(buffersrcCtx, buffersinkCtx, encCtx, outputFmtCtx, outputStream, decFrame, filterFrame, encPkt, &ptsNorm); err != nil {
				decFrame.Unref()
				return err
			}
			decFrame.Unref()
		}
	}

	// Flush the filter graph.
	if err := buffersrcCtx.AddFrame(nil, astiav.NewBuffersrcFlags()); err != nil && !errors.Is(err, astiav.ErrEof) {
		return fmt.Errorf("flushing filter: %w", err)
	}
	for {
		if err := buffersinkCtx.GetFrame(filterFrame, astiav.NewBuffersinkFlags()); err != nil {
			if errors.Is(err, astiav.ErrEof) || errors.Is(err, astiav.ErrEagain) {
				break
			}
			return fmt.Errorf("getting flushed filter frame: %w", err)
		}
		filterFrame.SetPictureType(astiav.PictureTypeNone)
		ptsNorm.applyEnc(filterFrame)
		if err := encodeWrite(encCtx, outputFmtCtx, outputStream, filterFrame, encPkt); err != nil {
			filterFrame.Unref()
			return err
		}
		filterFrame.Unref()
	}

	// Flush encoder.
	if err := encodeWrite(encCtx, outputFmtCtx, outputStream, nil, encPkt); err != nil {
		return fmt.Errorf("flushing encoder: %w", err)
	}

	if err := outputFmtCtx.WriteTrailer(); err != nil {
		return fmt.Errorf("writing trailer: %w", err)
	}

	return nil
}

// filterEncodeWrite pushes a decoded frame through the filter graph, then encodes
// and writes the resulting filtered frames.
func filterEncodeWrite(
	buffersrcCtx *astiav.BuffersrcFilterContext,
	buffersinkCtx *astiav.BuffersinkFilterContext,
	encCtx *astiav.CodecContext,
	outputFmtCtx *astiav.FormatContext,
	outputStream *astiav.Stream,
	decFrame, filterFrame *astiav.Frame,
	encPkt *astiav.Packet,
	ptsNorm *ptsNormState,
) error {
	if err := buffersrcCtx.AddFrame(decFrame, astiav.NewBuffersrcFlags(astiav.BuffersrcFlagKeepRef)); err != nil {
		return fmt.Errorf("adding frame to filter: %w", err)
	}

	for {
		if err := buffersinkCtx.GetFrame(filterFrame, astiav.NewBuffersinkFlags()); err != nil {
			if errors.Is(err, astiav.ErrEof) || errors.Is(err, astiav.ErrEagain) {
				return nil
			}
			return fmt.Errorf("getting frame from filter: %w", err)
		}

		filterFrame.SetPictureType(astiav.PictureTypeNone)
		ptsNorm.applyEnc(filterFrame)

		if err := encodeWrite(encCtx, outputFmtCtx, outputStream, filterFrame, encPkt); err != nil {
			filterFrame.Unref()
			return err
		}
		filterFrame.Unref()
	}
}

// TranscodeFileParallel retains the legacy signature. A single continuous
// encoder is required even when callers request parallelism, to preserve onsets.
func TranscodeFileParallel(ctx context.Context, inputPath, outputPath string, parallelism int, logger *log.Logger) error {
	_, err := JoinAAC(ctx, []string{inputPath}, outputPath, nil)
	return err
}

// encodeWrite sends a frame to the encoder and writes encoded packets to the output.
func encodeWrite(
	encCtx *astiav.CodecContext,
	outputFmtCtx *astiav.FormatContext,
	outputStream *astiav.Stream,
	frame *astiav.Frame,
	pkt *astiav.Packet,
) error {
	if err := encCtx.SendFrame(frame); err != nil {
		if errors.Is(err, astiav.ErrEof) {
			return nil
		}
		return fmt.Errorf("sending frame to encoder: %w", err)
	}

	for {
		if err := encCtx.ReceivePacket(pkt); err != nil {
			if errors.Is(err, astiav.ErrEof) || errors.Is(err, astiav.ErrEagain) {
				return nil
			}
			return fmt.Errorf("receiving encoded packet: %w", err)
		}

		pkt.SetStreamIndex(outputStream.Index())
		pkt.RescaleTs(encCtx.TimeBase(), outputStream.TimeBase())

		if err := outputFmtCtx.WriteInterleavedFrame(pkt); err != nil {
			pkt.Unref()
			return fmt.Errorf("writing packet: %w", err)
		}
		pkt.Unref()
	}
}
