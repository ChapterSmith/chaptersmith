package audio

import (
	"chaptersmith/internal/meta"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/asticode/go-astiav"
)

// ChapterMarker uses the output sample clock, with a half-open frame interval.
type ChapterMarker struct {
	Title                string
	StartFrame, EndFrame int64
}
type EncodeOptions struct {
	Format               string
	Channels, BitRate    int
	StartFrame, EndFrame int64
	GainDB               float64
	Metadata             meta.AudiobookMetadata
	CoverPath            string
	Chapters             []ChapterMarker
	Track                string
}

func validateEncode(opts EncodeOptions) error {
	switch opts.Format {
	case "m4a", "m4b":
		if opts.BitRate <= 0 {
			return errors.New("AAC bitrate must be positive")
		}
	case "flac", "wav":
	default:
		return fmt.Errorf("unsupported format %q", opts.Format)
	}
	if opts.StartFrame < 0 || opts.EndFrame < -1 || opts.EndFrame != -1 && opts.EndFrame <= opts.StartFrame {
		return errors.New("invalid encoding interval")
	}
	if math.IsNaN(opts.GainDB) || math.IsInf(opts.GainDB, 0) {
		return errors.New("invalid encoding gain")
	}
	var end int64
	for _, ch := range opts.Chapters {
		if ch.StartFrame < end || ch.EndFrame <= ch.StartFrame || strings.ContainsRune(ch.Title, 0) {
			return errors.New("invalid chapter markers")
		}
		end = ch.EndFrame
	}
	if opts.EndFrame != -1 && end > opts.EndFrame-opts.StartFrame {
		return errors.New("chapters extend beyond encoded interval")
	}
	return nil
}

// EncodePCM produces an exclusive output at 44100 Hz. Metadata comes only from
// opts; WAV supports RIFF tags but intentionally has no artwork representation.
func EncodePCM(ctx context.Context, inputPath, outputPath string, opts EncodeOptions) error {
	f, e := bookPCMFormat(opts.Channels)
	if e != nil {
		return e
	}
	return encodePCM(ctx, inputPath, outputPath, opts, f)
}

// The private format seam is shared with future continuous legacy CLI encoding.
func encodePCM(ctx context.Context, inputPath, outputPath string, opts EncodeOptions, f pcmFormat) error {
	if e := validateEncode(opts); e != nil {
		return e
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	return withEncodedOutput(outputPath, func(out *os.File) error {
		return encodeStream(ctx, out, f, opts, func(emit func([]float32) error) (err error) {
			d, e := openPCM(ctx, inputPath, f)
			if e != nil {
				return e
			}
			defer func() { err = errors.Join(err, d.Close()) }()
			return d.Range(opts.StartFrame, opts.EndFrame, emit)
		})
	})
}

// withEncodedOutput removes only the inode it created. The worker owns closing
// the handle, including any error path, before this function removes a partial.
func withEncodedOutput(path string, work func(*os.File) error) error {
	out, e := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	created, e := out.Stat()
	if e != nil {
		return errors.Join(e, out.Close())
	}
	// work may close out; the identity comparison below must still be able to
	// tell this file from one created in its place.
	defer pinFile(out)()
	e = work(out)
	if e == nil {
		current, se := os.Lstat(path)
		if se != nil {
			return fmt.Errorf("encoded output disappeared: %w", se)
		}
		if !os.SameFile(created, current) {
			return errors.New("encoded output was replaced")
		}
		return nil
	}
	if e != nil {
		current, se := os.Lstat(path)
		if os.IsNotExist(se) {
			return e
		}
		if se != nil {
			return errors.Join(e, se)
		}
		if os.SameFile(created, current) {
			return errors.Join(e, os.Remove(path))
		}
		return e
	}
	return nil
}

type encodeMux struct {
	fc    *astiav.FormatContext
	pb    *astiav.IOContext
	ioErr error
	ctx   context.Context
	out   pcmOutput
}

func newEncodeMux(ctx context.Context, out pcmOutput, opts EncodeOptions, rate int) (m *encodeMux, err error) {
	m = &encodeMux{ctx: ctx, out: out}
	format := opts.Format
	if format == "m4a" || format == "m4b" {
		format = "ipod"
	}
	m.fc, err = astiav.AllocOutputFormatContext(nil, format, "book.m4a")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			m.free()
		}
	}()
	m.pb, err = astiav.AllocIOContext(32768, true, nil, func(offset int64, whence int) (int64, error) {
		if e := ctx.Err(); e != nil {
			m.ioErr = errors.Join(m.ioErr, e)
			return 0, e
		}
		n, e := out.Seek(offset, whence)
		if e != nil {
			m.ioErr = errors.Join(m.ioErr, e)
		}
		return n, e
	}, func(b []byte) (int, error) {
		if e := ctx.Err(); e != nil {
			m.ioErr = errors.Join(m.ioErr, e)
			return 0, e
		}
		n, e := out.Write(b)
		if e == nil && n != len(b) {
			e = io.ErrShortWrite
		}
		if e != nil {
			m.ioErr = errors.Join(m.ioErr, e)
		}
		return n, e
	})
	if err != nil {
		return m, err
	}
	m.fc.SetPb(m.pb)
	return m, nil
}
func (m *encodeMux) free() {
	if m.fc != nil {
		m.fc.Free()
	}
	if m.pb != nil {
		m.pb.Free()
	}
}
func (m *encodeMux) header(opts EncodeOptions, rate int) error {
	if e := projectMetadata(m.fc, opts, rate); e != nil {
		return e
	}
	dict := astiav.NewDictionary()
	defer dict.Free()
	values := map[string]string{}
	if opts.Format == "wav" {
		values["rf64"] = "auto"
	} else if opts.Format == "m4b" || opts.Format == "m4a" {
		values["movie_timescale"] = fmt.Sprint(rate)

	}
	for k, v := range values {
		if e := dict.Set(k, v, 0); e != nil {
			return e
		}
	}
	if e := m.fc.WriteHeader(dict); e != nil || m.ioErr != nil {
		return errors.Join(e, m.ioErr)
	}
	return errors.Join(writeBookCover(m.fc), m.ioErr)
}
func (m *encodeMux) finish() error {
	if e := m.ctx.Err(); e != nil {
		return e
	}
	e := m.fc.WriteTrailer()
	m.pb.Flush()
	if e != nil || m.ioErr != nil {
		return errors.Join(e, m.ioErr)
	}
	if e = m.out.Sync(); e != nil {
		return e
	}
	return m.ctx.Err()
}
func encodeStream(ctx context.Context, out pcmOutput, f pcmFormat, opts EncodeOptions, produce func(func([]float32) error) error) (err error) {
	defer func() { err = errors.Join(err, out.Close()) }()
	if err = validateEncode(opts); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if f.rate <= 0 || f.channels <= 0 || f.channels != f.layout.Channels() || produce == nil {
		return errors.New("invalid PCM format or producer")
	}
	m, e := newEncodeMux(ctx, out, opts, f.rate)
	if e != nil {
		return e
	}
	defer m.free()
	encoder, e := newNativeEncoder(m.fc, f, opts)
	if e != nil {
		return e
	}
	defer encoder.close()
	if e = m.header(opts, f.rate); e != nil {
		return e
	}
	var frames int64
	e = produce(func(b []float32) error {
		if len(b)%f.channels != 0 {
			return errors.New("partial PCM frame")
		}
		for len(b) > 0 {
			if e := ctx.Err(); e != nil {
				return e
			}
			n := min(len(b), pcmChunkFrames*f.channels)
			for _, v := range b[:n] {
				if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
					return errors.New("nonfinite PCM sample")
				}
			}
			count := int64(n / f.channels)
			if frames > math.MaxInt64-count {
				return errors.New("encoded length overflow")
			}
			frames += count
			if e := encoder.push(b[:n], f.channels); e != nil || m.ioErr != nil {
				return errors.Join(e, m.ioErr)
			}
			b = b[n:]
		}
		return nil
	})
	if e != nil {
		return errors.Join(e, m.ioErr)
	}
	if frames == 0 {
		return errors.New("empty encoded audio")
	}
	for _, ch := range opts.Chapters {
		if ch.EndFrame > frames {
			return errors.New("chapter extends past audible audio")
		}
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = encoder.push(nil, f.channels); e != nil || m.ioErr != nil {
		return errors.Join(e, m.ioErr)
	}
	return m.finish()
}

// Native AAC whitelist is movenc.c:mov_write_ilst_tag (FFmpeg n8.0).
// RIFF uses riff.c:ff_riff_info_conv. FLAC accepts arbitrary Vorbis tags.
var aacExtraTags = map[string]bool{"lyrics": true, "show": true, "episode_id": true, "network": true, "keywords": true, "episode_sort": true, "season_number": true, "media_type": true, "hd_video": true, "gapless_playback": true, "compilation": true, "disc": true, "tmpo": true}
var riffTags = map[string]bool{"title": true, "artist": true, "album": true, "comment": true, "copyright": true, "date": true, "genre": true, "track": true, "language": true, "timecode": true}
var ownedTagAliases = map[string]bool{"author": true, "narrator": true, "albumartist": true, "year": true, "synopsis": true, "encoder": true, "encoding_tool": true, "encoded_by": true, "major_brand": true, "minor_version": true, "compatible_brands": true, "creation_time": true, "handler_name": true, "vendor_id": true, "metadata_block_picture": true, "coverart": true, "coverartmime": true}

func projectTagValues(opts EncodeOptions) (map[string]string, []string) {
	md := opts.Metadata
	tags := map[string]string{"title": md.Title, "artist": md.Artist, "album": md.Album, "album_artist": md.AlbumArtist, "composer": md.Composer, "genre": md.Genre, "date": md.Date, "copyright": md.Copyright, "comment": md.Comment, "description": md.Description, "subtitle": md.Subtitle, "grouping": md.Grouping, "track": opts.Track}
	var warnings []string
	for k, v := range md.Extra {
		lower := strings.ToLower(k)
		if _, owned := tags[lower]; owned || ownedTagAliases[lower] {
			continue
		}
		allowed := opts.Format == "flac" || ((opts.Format == "m4a" || opts.Format == "m4b") && aacExtraTags[lower]) || (opts.Format == "wav" && riffTags[lower])
		if allowed {
			tags[lower] = v
		} else if v != "" {
			warnings = append(warnings, fmt.Sprintf("%s does not support Extra tag %q", opts.Format, k))
		}
	}
	for k, v := range tags {
		supported := opts.Format == "flac" || ((opts.Format == "m4a" || opts.Format == "m4b") && k != "subtitle") || (opts.Format == "wav" && riffTags[k])
		if !supported {
			delete(tags, k)
			if v != "" {
				warnings = append(warnings, fmt.Sprintf("%s does not support tag %q", opts.Format, k))
			}
		}
	}
	if opts.Format == "wav" && opts.CoverPath != "" {
		warnings = append(warnings, "WAV does not support embedded cover artwork")
	}
	sort.Strings(warnings)
	return tags, warnings
}

// MetadataWarnings describes format limitations for the export report. It never
// edits the project snapshot or drops keys from persisted project metadata.
func MetadataWarnings(opts EncodeOptions) []string {
	_, warnings := projectTagValues(opts)
	return warnings
}
func projectMetadata(fc *astiav.FormatContext, opts EncodeOptions, rate int) error {
	for k, v := range opts.Metadata.Extra {
		if k == "" || strings.ContainsRune(k, 0) || strings.ContainsRune(v, 0) {
			return errors.New("invalid metadata Extra")
		}
	}
	tags, _ := projectTagValues(opts)
	for k, v := range tags {
		if strings.ContainsRune(v, 0) {
			return errors.New("metadata contains NUL")
		}
		if v != "" {
			if e := setBookTag(fc, k, v); e != nil {
				return e
			}
		}
	}
	// The iTunes short description atom can truncate text; ldes carries it whole.
	if (opts.Format == "m4a" || opts.Format == "m4b") && opts.Metadata.Description != "" {
		if e := setBookTag(fc, "synopsis", opts.Metadata.Description); e != nil {
			return e
		}
	}

	if opts.Format == "m4a" || opts.Format == "m4b" {
		if e := addBookChapters(fc, opts.Chapters, rate); e != nil {
			return e
		}
	}
	if opts.CoverPath != "" {
		file, e := os.Open(opts.CoverPath)
		if e != nil {
			return e
		}
		b, e := io.ReadAll(io.LimitReader(file, 32*1024*1024+1))
		e = errors.Join(e, file.Close())
		if e != nil {
			return e
		}
		if len(b) > 32*1024*1024 {
			return errors.New("cover exceeds 32 MiB")
		}
		config, kind, e := image.DecodeConfig(bytes.NewReader(b))
		if e != nil {
			return fmt.Errorf("invalid requested cover: %w", e)
		}
		if kind != "jpeg" && kind != "png" {
			return errors.New("cover must be JPEG or PNG")
		}
		if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 40000000 {
			return errors.New("invalid cover dimensions")
		}
		if _, _, e = image.Decode(bytes.NewReader(b)); e != nil {
			return fmt.Errorf("invalid requested cover: %w", e)
		}
		if opts.Format != "wav" {
			if e = addBookCover(fc, b, kind == "png", config.Width, config.Height); e != nil {
				return e
			}
		}
	}
	return nil
}

// aacCopyIncompatibility means the source cannot be proven safe to packet-copy.
// It is deliberately distinct from decoding, I/O, finalization and cancellation errors.
type aacCopyIncompatibility struct{ reason string }

func (e *aacCopyIncompatibility) Error() string { return e.reason }
func copyIncompatible(format string, args ...any) error {
	return &aacCopyIncompatibility{reason: fmt.Sprintf(format, args...)}
}

// IsAACCopyIncompatible permits re-encoding only when every cause is a copy
// compatibility rejection. A joined close/write/cancellation failure is fatal,
// even when another leaf describes an incompatible source.
func IsAACCopyIncompatible(err error) bool {
	if err == nil {
		return false
	}
	switch e := err.(type) {
	case *aacCopyIncompatibility:
		return true
	case interface{ Unwrap() []error }:
		found := false
		for _, cause := range e.Unwrap() {
			if cause == nil {
				continue
			}
			found = true
			if !IsAACCopyIncompatible(cause) {
				return false
			}
		}
		return found
	case interface{ Unwrap() error }:
		return IsAACCopyIncompatible(e.Unwrap())
	default:
		return false
	}
}

// AACCopyFrames performs only container/codec inspection. A positive result is
// a candidate, not proof of intact audio; CopyAAC performs cancellable full decode
// and post-copy verification. It never uses an extension as codec evidence.
func AACCopyFrames(ctx context.Context, path string, channels int) (frames int64, err error) {
	fc, st, close, e := openCopySource(ctx, path, channels)
	if e != nil {
		return 0, e
	}
	defer func() { err = errors.Join(err, close()) }()
	_ = fc
	if st.StartTime() != 0 {
		return 0, copyIncompatible("nonzero AAC origin requires full copy verification; standard encoding selected")
	}
	frames = astiav.RescaleQ(st.Duration(), st.TimeBase(), astiav.NewRational(1, 44100))
	if frames <= 0 {
		return 0, copyIncompatible("AAC has no proven duration")
	}
	return frames, nil
}

// openCopySource uses owned custom I/O so context and file-close errors survive.
func openCopySource(ctx context.Context, path string, channels int) (fc *astiav.FormatContext, st *astiav.Stream, close func() error, err error) {
	if channels != 1 && channels != 2 {
		return nil, nil, nil, errors.New("invalid AAC channels")
	}
	if err = ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	file, e := os.Open(path)
	if e != nil {
		return nil, nil, nil, e
	}
	info, e := file.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return nil, nil, nil, errors.Join(e, errors.New("AAC source must be regular"), file.Close())
	}
	fc = astiav.AllocFormatContext()
	if fc == nil {
		return nil, nil, nil, errors.Join(errors.New("allocating AAC demuxer"), file.Close())
	}
	var ioErr error
	pb, e := astiav.AllocIOContext(32768, false, func(b []byte) (int, error) {
		if e := ctx.Err(); e != nil {
			ioErr = errors.Join(ioErr, e)
			return 0, e
		}
		n, e := file.Read(b)
		if e != nil && !errors.Is(e, io.EOF) {
			ioErr = errors.Join(ioErr, e)
		}
		return n, e
	}, func(o int64, w int) (int64, error) {
		if e := ctx.Err(); e != nil {
			ioErr = errors.Join(ioErr, e)
			return 0, e
		}
		if w&0x10000 != 0 {
			return info.Size(), nil
		}
		n, e := file.Seek(o, w&^0x20000)
		if e != nil {
			ioErr = errors.Join(ioErr, e)
		}
		return n, e
	}, nil)
	if e != nil {
		fc.Free()
		return nil, nil, nil, errors.Join(e, file.Close())
	}
	fc.SetPb(pb)
	opened := false
	close = func() error {
		if opened {
			fc.CloseInput()
		} else {
			fc.Free()
		}
		pb.Free()
		after, statErr := os.Stat(path)
		if statErr == nil && (!os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime())) {
			statErr = errors.New("AAC source changed during inspection/copy")
		}
		return errors.Join(ioErr, statErr, file.Close())
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, close())
			close = nil
		}
	}()
	if err = fc.OpenInput(path, nil, nil); err != nil {
		return
	}
	opened = true
	if err = fc.FindStreamInfo(nil); err != nil {
		return
	}
	if fc.InputFormat().Name() != "mov,mp4,m4a,3gp,3g2,mj2" {
		err = copyIncompatible("preserve requires an AAC MOV/MP4 container")
		return
	}
	for _, s := range fc.Streams() {
		if s.CodecParameters().MediaType() == astiav.MediaTypeAudio {
			if st != nil {
				err = copyIncompatible("multiple audio streams cannot be preserved")
				return
			}
			st = s
		}
	}
	if st == nil || !compatibleAAC(st.CodecParameters(), channels) {
		err = copyIncompatible("AAC codec, layout, extradata, sample format or rate is incompatible")
		return
	}
	if st.TimeBase().Num() != 1 || st.TimeBase().Den() != 44100 || st.Duration() <= 0 {
		err = copyIncompatible("AAC timing cannot be proven sample accurate")
		return
	}
	if ioErr != nil {
		err = ioErr
	}
	return
}

func countAACFrames(ctx context.Context, path string, channels int) (frames int64, err error) {
	err = DecodePCM(ctx, path, channels, 0, -1, func(b []float32) error { frames += int64(len(b) / channels); return nil })
	return
}

// CopyAAC preserves one complete, verified compatible AAC stream. Packet side
// data is retained; input tags, chapters and cover never enter the output.
func CopyAAC(ctx context.Context, inputPath, outputPath string, opts EncodeOptions) (err error) {
	if opts.Format != "m4a" && opts.Format != "m4b" || opts.StartFrame != 0 || opts.GainDB != 0 {
		return copyIncompatible("AAC copy requires a complete unchanged AAC output")
	}
	check := opts
	check.BitRate = 64000
	if e := validateEncode(check); e != nil {
		return e
	}
	fc, st, close, e := openCopySource(ctx, inputPath, opts.Channels)
	if e != nil {
		return e
	}
	defer func() {
		if close != nil {
			err = errors.Join(err, close())
		}
	}()
	expected := astiav.RescaleQ(st.Duration(), st.TimeBase(), astiav.NewRational(1, 44100))

	frames, e := countAACFrames(ctx, inputPath, opts.Channels)
	if e != nil {
		return e
	}
	if st.StartTime() == 0 && frames != expected {
		return copyIncompatible("AAC audible length %d differs from declared %d", frames, expected)
	}
	if opts.EndFrame != -1 && opts.EndFrame != frames {
		return copyIncompatible("AAC copy cannot trim audio")
	}
	for _, ch := range opts.Chapters {
		if ch.EndFrame > frames {
			return copyIncompatible("chapter extends past AAC audio")
		}
	}
	return withEncodedOutput(outputPath, func(out *os.File) (err error) {
		defer func() { err = errors.Join(err, close()); close = nil }()
		// Close the writer before verification, including failures during setup.
		writeErr := func() (err error) {
			defer func() { err = errors.Join(err, out.Close()) }()
			m, e := newEncodeMux(ctx, out, opts, 44100)
			if e != nil {
				return e
			}
			defer m.free()
			dst := m.fc.NewStream(nil)
			if dst == nil {
				return errors.New("allocating copy stream")
			}
			if e = st.CodecParameters().Copy(dst.CodecParameters()); e != nil {
				return e
			}
			dst.CodecParameters().SetCodecTag(0)
			dst.SetTimeBase(st.TimeBase())
			if e = m.header(opts, 44100); e != nil {
				return e
			}
			pkt := astiav.AllocPacket()
			if pkt == nil {
				return errors.New("allocating copy packet")
			}
			defer pkt.Free()
			origin := st.StartTime()
			if origin == astiav.NoPtsValue {
				return copyIncompatible("AAC start time unknown")
			}
			var previousEnd int64
			first := true
			for {
				if e = ctx.Err(); e != nil {
					return e
				}
				e = fc.ReadFrame(pkt)
				if errors.Is(e, astiav.ErrEof) {
					break
				}
				if e != nil {
					return e
				}
				if pkt.StreamIndex() != st.Index() {
					pkt.Unref()
					continue
				}
				if pkt.Pts() == astiav.NoPtsValue || pkt.Dts() == astiav.NoPtsValue {
					pkt.Unref()
					return copyIncompatible("AAC packet timestamps missing")
				}
				if pkt.Duration() <= 0 {
					return copyIncompatible("AAC packet duration unknown")
				}
				if first {
					skip := packetSkip(pkt)
					if pkt.Pts()+skip != origin {
						return copyIncompatible("AAC priming offset cannot be proven")
					}
					declared := expected
					if origin != 0 {
						declared -= skip
					}
					if declared != frames {
						return copyIncompatible("AAC declared duration does not match audible audio")
					}
					first = false
				} else if pkt.Pts() != previousEnd {
					return copyIncompatible("AAC packet timeline is discontinuous")
				}
				previousEnd = pkt.Pts() + pkt.Duration()
				pkt.SetPts(pkt.Pts() - origin)
				pkt.SetDts(pkt.Dts() - origin)
				pkt.RescaleTs(st.TimeBase(), dst.TimeBase())
				pkt.SetStreamIndex(dst.Index())
				pkt.SetPos(-1)
				e = m.fc.WriteInterleavedFrame(pkt)
				pkt.Unref()
				if e != nil || m.ioErr != nil {
					return errors.Join(e, m.ioErr)
				}
			}
			if first || previousEnd-origin != frames {
				return copyIncompatible("AAC packet extent differs from audible duration")
			}
			return m.finish()
		}()
		if writeErr != nil {
			return writeErr
		}
		copied, copyStream, closeCopy, e := openCopySource(ctx, outputPath, opts.Channels)
		if e != nil {
			return errors.Join(errors.New("copied AAC failed verification"), e)
		}
		_ = copied
		equal := sameAACParameters(st.CodecParameters(), copyStream.CodecParameters())
		declared := astiav.RescaleQ(copyStream.Duration(), copyStream.TimeBase(), astiav.NewRational(1, 44100))
		e = closeCopy()
		if e != nil {
			return e
		}
		if !equal || declared != frames {
			return errors.New("copied AAC parameters or declared duration changed")
		}
		got, e := countAACFrames(ctx, outputPath, opts.Channels)
		if e != nil {
			return e
		}
		if got != frames {
			return errors.New("copied AAC audible duration changed")
		}
		return ctx.Err()
	})
}
