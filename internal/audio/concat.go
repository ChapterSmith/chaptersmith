package audio

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/asticode/go-astiav"
)

// Concat remuxes one source. Independent AAC occurrences require JoinAAC to
// preserve decoder priming and avoid corruption at internal boundaries.
func Concat(ctx context.Context, inputFiles []string, outputPath string, logger *log.Logger) (err error) {
	if len(inputFiles) == 0 {
		return errors.New("no input files to concatenate")
	}

	if len(inputFiles) != 1 {
		return errors.New("multi-occurrence packet concatenation is unproven; use JoinAAC")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	logger.Printf("Concatenating %d audio files...", len(inputFiles))

	outputFmtCtx, err := astiav.AllocOutputFormatContext(nil, "ipod", outputPath)
	if err != nil {
		return fmt.Errorf("allocating output format context: %w", err)
	}
	if outputFmtCtx == nil {
		return errors.New("output format context is nil")
	}
	defer outputFmtCtx.Free()

	// We'll set up the output stream from the first input file.
	var outputStream *astiav.Stream
	var outputTimeBase astiav.Rational
	var ptsOffset int64

	ioCtx, err := astiav.OpenIOContext(outputPath, astiav.NewIOContextFlags(astiav.IOContextFlagWrite), nil, nil)
	if err != nil {
		return fmt.Errorf("opening output file: %w", err)
	}
	defer func() { err = errors.Join(err, ioCtx.Close()) }()
	outputFmtCtx.SetPb(ioCtx)

	headerWritten := false
	pkt := astiav.AllocPacket()
	defer pkt.Free()

	for fileIdx, inputPath := range inputFiles {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		inputFmtCtx := astiav.AllocFormatContext()
		if inputFmtCtx == nil {
			return fmt.Errorf("allocating format context for file %d", fileIdx)
		}

		if err := inputFmtCtx.OpenInput(inputPath, nil, nil); err != nil {
			inputFmtCtx.Free()
			return fmt.Errorf("opening input %s: %w", inputPath, err)
		}

		if err := inputFmtCtx.FindStreamInfo(nil); err != nil {
			inputFmtCtx.CloseInput()
			inputFmtCtx.Free()
			return fmt.Errorf("finding stream info for %s: %w", inputPath, err)
		}

		var inputStream *astiav.Stream
		for _, s := range inputFmtCtx.Streams() {
			if s.CodecParameters().MediaType() == astiav.MediaTypeAudio {
				inputStream = s
				break
			}
		}
		if inputStream == nil {
			inputFmtCtx.CloseInput()
			inputFmtCtx.Free()
			return fmt.Errorf("no audio stream in %s", inputPath)
		}

		// On the first file, set up the output stream and write the header.
		if !headerWritten {
			outputStream = outputFmtCtx.NewStream(nil)
			if outputStream == nil {
				inputFmtCtx.CloseInput()
				inputFmtCtx.Free()
				return errors.New("creating output stream")
			}

			if err := inputStream.CodecParameters().Copy(outputStream.CodecParameters()); err != nil {
				inputFmtCtx.CloseInput()
				inputFmtCtx.Free()
				return fmt.Errorf("copying codec parameters: %w", err)
			}
			outputStream.SetTimeBase(inputStream.TimeBase())

			if err := outputFmtCtx.WriteHeader(nil); err != nil {
				inputFmtCtx.CloseInput()
				inputFmtCtx.Free()
				return fmt.Errorf("writing output header: %w", err)
			}
			// The muxer may change the time base when writing its header
			// (e.g. ADTS ticks become MP4 sample-rate ticks). Use that final
			// time base for both packet rescaling and segment offsets.
			outputTimeBase = outputStream.TimeBase()
			headerWritten = true
		}

		// Track the maximum PTS+duration for offset calculation.
		var maxPTS int64

		for {
			if ctx.Err() != nil {
				inputFmtCtx.CloseInput()
				inputFmtCtx.Free()
				return ctx.Err()
			}

			if err := inputFmtCtx.ReadFrame(pkt); err != nil {
				if errors.Is(err, astiav.ErrEof) {
					break
				}
				inputFmtCtx.CloseInput()
				inputFmtCtx.Free()
				return fmt.Errorf("reading frame from %s: %w", inputPath, err)
			}

			if pkt.StreamIndex() != inputStream.Index() {
				pkt.Unref()
				continue
			}

			pkt.RescaleTs(inputStream.TimeBase(), outputTimeBase)

			origPTS := pkt.Pts()
			origDTS := pkt.Dts()
			origDur := pkt.Duration()

			// Each AAC encoder adds priming before time zero. Only the first
			// segment can carry that delay into the output stream's edit list.
			if fileIdx > 0 && origPTS != astiav.NoPtsValue && origPTS < 0 && origPTS+origDur <= 0 {
				pkt.Unref()
				continue
			}
			if origPTS != astiav.NoPtsValue {
				pkt.SetPts(origPTS + ptsOffset)
			}
			if origDTS != astiav.NoPtsValue {
				pkt.SetDts(origDTS + ptsOffset)
			}
			pkt.SetStreamIndex(outputStream.Index())
			pkt.SetPos(-1)

			if endPTS := pkt.Pts() + origDur; endPTS > maxPTS {
				maxPTS = endPTS
			}

			if err := outputFmtCtx.WriteInterleavedFrame(pkt); err != nil {
				pkt.Unref()
				inputFmtCtx.CloseInput()
				inputFmtCtx.Free()
				return fmt.Errorf("writing packet from %s: %w", inputPath, err)
			}
			pkt.Unref()
		}

		// Advance the PTS offset for the next file (+1 ensures strict DTS
		// monotonicity at segment boundaries from parallel transcode splits).
		ptsOffset = maxPTS + 1

		inputFmtCtx.CloseInput()
		inputFmtCtx.Free()
	}

	if headerWritten {
		if err := outputFmtCtx.WriteTrailer(); err != nil {
			return fmt.Errorf("writing trailer: %w", err)
		}
	}

	logger.Printf("Concatenation complete.")
	return nil
}
