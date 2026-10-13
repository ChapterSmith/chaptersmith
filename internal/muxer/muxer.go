package muxer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/asticode/go-astiav"

	"chaptersmith/internal/chapter"
	"chaptersmith/internal/meta"
)

// Options holds all parameters for the final M4B assembly.
type Options struct {
	InputFile   string
	OutputFile  string
	Metadata    meta.AudiobookMetadata
	CoverFile   string
	HasCover    bool
	Chapters    []chapter.Chapter
	HasChapters bool
}

// Finalize creates the final M4B audiobook by remuxing the merged audio and
// optionally attaching cover art, chapter markers, and metadata tags -- all
// through the astiav (libav) API with no external ffmpeg process.
func Finalize(ctx context.Context, opts Options, logger *log.Logger) (err error) {
	return finalize(ctx, opts, logger, (*astiav.IOContext).Close)
}

// The close operation belongs to this call; tests can simulate a filesystem
// close failure while all packet IO still uses the native muxer.
func finalize(ctx context.Context, opts Options, logger *log.Logger, closeOutput func(*astiav.IOContext) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	stagedFile, err := os.CreateTemp(filepath.Dir(opts.OutputFile), ".chaptersmith-*.m4b")
	if err != nil {
		return fmt.Errorf("creating staged output: %w", err)
	}
	staged := stagedFile.Name()
	defer os.Remove(staged)
	if err := stagedFile.Close(); err != nil {
		return fmt.Errorf("closing staged output: %w", err)
	}
	output := opts.OutputFile
	opts.OutputFile = staged
	if err := muxOutput(ctx, opts, logger, closeOutput); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(staged, output); err != nil {
		return fmt.Errorf("publishing audiobook: %w", err)
	}
	logger.Printf("Audiobook finalized: %s", output)
	return nil
}

func muxOutput(ctx context.Context, opts Options, logger *log.Logger, closeOutput func(*astiav.IOContext) error) (err error) {
	logger.Printf("Creating final audiobook file...")
	if err := ctx.Err(); err != nil {
		return err
	}
	if opts.HasChapters && len(opts.Chapters) == 0 {
		return errors.New("requested chapters are empty")
	}

	// --- Open the merged audio input ---
	inputFmtCtx := astiav.AllocFormatContext()
	if inputFmtCtx == nil {
		return errors.New("allocating input format context")
	}
	defer inputFmtCtx.Free()

	if err := inputFmtCtx.OpenInput(opts.InputFile, nil, nil); err != nil {
		return fmt.Errorf("opening merged audio: %w", err)
	}
	defer inputFmtCtx.CloseInput()

	if err := inputFmtCtx.FindStreamInfo(nil); err != nil {
		return fmt.Errorf("finding stream info: %w", err)
	}

	// Find the audio stream in the merged file.
	var inputAudioStream *astiav.Stream
	for _, s := range inputFmtCtx.Streams() {
		if s.CodecParameters().MediaType() == astiav.MediaTypeAudio {
			inputAudioStream = s
			break
		}
	}
	if inputAudioStream == nil {
		return errors.New("no audio stream in merged file")
	}

	// --- Create output format context (M4B = ipod container) ---
	outputFmtCtx, err := astiav.AllocOutputFormatContext(nil, "ipod", opts.OutputFile)
	if err != nil {
		return fmt.Errorf("allocating output format context: %w", err)
	}
	if outputFmtCtx == nil {
		return errors.New("output format context is nil")
	}
	defer outputFmtCtx.Free()

	// Create audio output stream (copy from input).
	outputAudioStream := outputFmtCtx.NewStream(nil)
	if outputAudioStream == nil {
		return errors.New("creating output audio stream")
	}
	if err := inputAudioStream.CodecParameters().Copy(outputAudioStream.CodecParameters()); err != nil {
		return fmt.Errorf("copying audio codec parameters: %w", err)
	}
	outputAudioStream.SetTimeBase(inputAudioStream.TimeBase())

	// --- Optionally add cover image as an attached_pic video stream ---
	var (
		coverFmtCtx       *astiav.FormatContext
		inputCoverStream  *astiav.Stream
		outputCoverStream *astiav.Stream
	)

	var coverPkt *astiav.Packet
	if opts.HasCover {
		if opts.CoverFile == "" {
			return errors.New("requested cover path is empty")
		}
		coverFmtCtx = astiav.AllocFormatContext()
		if coverFmtCtx == nil {
			return errors.New("allocating cover format context")
		}
		defer coverFmtCtx.Free()
		if err := coverFmtCtx.OpenInput(opts.CoverFile, nil, nil); err != nil {
			return fmt.Errorf("opening requested cover: %w", err)
		}
		defer coverFmtCtx.CloseInput()
		if err := coverFmtCtx.FindStreamInfo(nil); err != nil {
			return fmt.Errorf("probing requested cover: %w", err)
		}
		for _, s := range coverFmtCtx.Streams() {
			if s.CodecParameters().MediaType() == astiav.MediaTypeVideo {
				inputCoverStream = s
				break
			}
		}
		if inputCoverStream == nil {
			return errors.New("requested cover contains no picture stream")
		}
		if inputCoverStream.CodecParameters().Width() <= 0 || inputCoverStream.CodecParameters().Height() <= 0 {
			return errors.New("requested cover has invalid picture dimensions")
		}
		outputCoverStream = outputFmtCtx.NewStream(nil)
		if outputCoverStream == nil {
			return errors.New("creating cover output stream")
		}
		if err := inputCoverStream.CodecParameters().Copy(outputCoverStream.CodecParameters()); err != nil {
			return fmt.Errorf("copying cover codec parameters: %w", err)
		}
		outputCoverStream.SetTimeBase(inputCoverStream.TimeBase())
		outputCoverStream.SetDispositionFlags(astiav.NewDispositionFlags(astiav.DispositionFlagAttachedPic))

		// Require an actual picture packet before opening the output. A probe
		// can identify a video stream even if the image has no readable data.
		coverPkt = astiav.AllocPacket()
		if coverPkt == nil {
			return errors.New("allocating cover packet")
		}
		defer coverPkt.Free()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := coverFmtCtx.ReadFrame(coverPkt); err != nil {
				if errors.Is(err, astiav.ErrEof) {
					return errors.New("requested cover contains no picture packet")
				}
				return fmt.Errorf("reading cover packet: %w", err)
			}
			if coverPkt.StreamIndex() == inputCoverStream.Index() && coverPkt.Size() > 0 {
				break
			}
			coverPkt.Unref()
		}
	}

	// --- Set metadata tags ---
	// A new output format context has no metadata dictionary, so create one.
	// Do NOT free this dictionary -- SetMetadata transfers ownership to the
	// format context, which frees it in avformat_free_context.
	md := astiav.NewDictionary()
	metadataAttached := false
	defer func() {
		if !metadataAttached {
			md.Free()
		}
	}()
	setTag := func(key, value string) error {
		if strings.TrimSpace(value) == "" {
			return nil
		}
		if err := md.Set(key, value, 0); err != nil {
			return fmt.Errorf("setting metadata tag %q: %w", key, err)
		}
		return nil
	}
	genre := opts.Metadata.Genre
	if genre == "" {
		genre = "Audiobook"
	}
	comment := opts.Metadata.Comment
	if comment == opts.Metadata.Description {
		comment = ""
	}
	for _, tag := range []struct{ key, value string }{
		{"title", opts.Metadata.Title},
		{"artist", opts.Metadata.Artist},
		{"album", opts.Metadata.Album},
		{"album_artist", opts.Metadata.AlbumArtist},
		{"composer", opts.Metadata.Composer},
		{"genre", genre},
		{"date", opts.Metadata.Date},
		{"copyright", opts.Metadata.Copyright},
		{"comment", comment},
		{"description", opts.Metadata.Description},
		{"synopsis", opts.Metadata.Description}, // ldes: untruncated long description
		{"subtitle", opts.Metadata.Subtitle},
		{"grouping", opts.Metadata.Grouping},
	} {
		if err := setTag(tag.key, tag.value); err != nil {
			return err
		}
	}
	for k, v := range opts.Metadata.Extra {
		if err := setTag(k, v); err != nil {
			return err
		}
	}
	outputFmtCtx.SetMetadata(md)
	metadataAttached = true

	// --- Add chapters via CGO bridge ---
	if opts.HasChapters && len(opts.Chapters) > 0 {
		totalDuration := float64(inputFmtCtx.Duration()) / float64(astiav.TimeBase)
		if err := addChaptersCGO(outputFmtCtx, opts.Chapters, totalDuration); err != nil {
			return fmt.Errorf("adding requested chapters: %w", err)
		}
		logger.Printf("Added %d chapters to output.", len(opts.Chapters))
	}

	// --- Open output file ---
	ioCtx, err := astiav.OpenIOContext(opts.OutputFile, astiav.NewIOContextFlags(astiav.IOContextFlagWrite), nil, nil)
	if err != nil {
		return fmt.Errorf("opening output file: %w", err)
	}
	if ioCtx == nil {
		return errors.New("output IO context is nil")
	}
	outputFmtCtx.SetPb(ioCtx)
	closed := false
	closeIO := func() error {
		if closed {
			return nil
		}
		closed = true
		closeErr := closeOutput(ioCtx)
		// Close clears the IO wrapper's native pointer; detach it from the
		// format context before that context is freed.
		outputFmtCtx.SetPb(ioCtx)
		if closeErr != nil {
			return fmt.Errorf("closing output file: %w", closeErr)
		}
		return nil
	}
	defer func() { err = errors.Join(err, closeIO()) }()

	// Write header (this commits the metadata and chapter atoms).
	if err := outputFmtCtx.WriteHeader(nil); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}

	// --- Copy audio packets ---
	pkt := astiav.AllocPacket()
	if pkt == nil {
		return errors.New("allocating audio packet")
	}
	defer pkt.Free()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := inputFmtCtx.ReadFrame(pkt); err != nil {
			if errors.Is(err, astiav.ErrEof) {
				break
			}
			return fmt.Errorf("reading packet: %w", err)
		}

		if pkt.StreamIndex() != inputAudioStream.Index() {
			pkt.Unref()
			continue
		}

		pkt.RescaleTs(inputAudioStream.TimeBase(), outputAudioStream.TimeBase())
		pkt.SetStreamIndex(outputAudioStream.Index())
		pkt.SetPos(-1)

		if err := outputFmtCtx.WriteInterleavedFrame(pkt); err != nil {
			pkt.Unref()
			return fmt.Errorf("writing audio packet: %w", err)
		}
		pkt.Unref()
	}

	// --- Copy the required cover image packet (if requested) ---
	if err := ctx.Err(); err != nil {
		return err
	}
	if coverPkt != nil {
		coverPkt.RescaleTs(inputCoverStream.TimeBase(), outputCoverStream.TimeBase())
		coverPkt.SetStreamIndex(outputCoverStream.Index())
		coverPkt.SetPos(-1)
		if err := outputFmtCtx.WriteInterleavedFrame(coverPkt); err != nil {
			return fmt.Errorf("writing cover packet: %w", err)
		}
		coverPkt.Unref()
	}

	// Write trailer.
	if err := outputFmtCtx.WriteTrailer(); err != nil {
		return fmt.Errorf("writing trailer: %w", err)
	}

	// Finish the native output before publishing its final name. A close
	// error must not result in a rename or a success message.
	if err := closeIO(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	return nil
}
