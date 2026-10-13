package render

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	"chaptersmith/internal/audio"
	"chaptersmith/internal/book"
)

const (
	streamHeaderBytes = 44
	streamChunkFrames = 16384
	// Within this distance it is cheaper to keep decoding than to seek.
	streamSeekGap = 2 * 44100
)

// ErrChapterTooLong reports a chapter that does not fit one WAV file.
var ErrChapterTooLong = errors.New("chapter is too long to stream as WAV")

type streamPart struct {
	start, frames int64  // position within the chapter
	path          string // empty for inserted silence
	sourceStart   int64
}

// ChapterStream presents one chapter of the edit as a 16-bit PCM WAV file that
// is never written to disk. Bytes are decoded when they are read, so playback
// can begin at once and jump to any sample: a byte offset is a frame number.
// Where the source container allows it a jump seeks instead of decoding from
// the source origin. ReadAt is safe for concurrent use.
type ChapterStream struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	channels int
	frames   int64
	parts    []streamPart
	header   []byte
	decoder  *audio.PCMDecoder
	source   string
	pcm      []byte
	closed   bool
}

// OpenChapterStream validates the snapshot and its sources but decodes nothing.
func OpenChapterStream(project book.BookProject, chapterID string) (*ChapterStream, error) {
	project = book.Clone(project)
	if err := book.Validate(project); err != nil {
		return nil, err
	}
	if err := book.CheckSources(project); err != nil {
		return nil, err
	}
	channels, err := resolveChannels(project, "auto")
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	for _, source := range project.Sources {
		paths[source.ID] = source.Path
	}
	s := &ChapterStream{channels: channels}
	found := false
	for _, chapter := range project.Chapters {
		if chapter.ID != chapterID {
			continue
		}
		found = true
		for _, segment := range chapter.Segments {
			a, b, err := segmentFrames(segment)
			if err != nil {
				return nil, err
			}
			part := streamPart{start: s.frames, frames: b - a}
			if segment.SilenceUS == 0 {
				part.path, part.sourceStart = paths[segment.SourceID], a
			}
			s.parts = append(s.parts, part)
			s.frames += part.frames
		}
	}
	if !found {
		return nil, fmt.Errorf("unknown chapter ID %q", chapterID)
	}
	data := s.frames * int64(2*channels)
	if s.frames == 0 || data > math.MaxUint32-streamHeaderBytes {
		return nil, ErrChapterTooLong
	}
	s.header = make([]byte, streamHeaderBytes)
	copy(s.header, "RIFF")
	binary.LittleEndian.PutUint32(s.header[4:], uint32(data+streamHeaderBytes-8))
	copy(s.header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(s.header[16:], 16)
	binary.LittleEndian.PutUint16(s.header[20:], 1)
	binary.LittleEndian.PutUint16(s.header[22:], uint16(channels))
	binary.LittleEndian.PutUint32(s.header[24:], 44100)
	binary.LittleEndian.PutUint32(s.header[28:], uint32(44100*2*channels))
	binary.LittleEndian.PutUint16(s.header[32:], uint16(2*channels))
	binary.LittleEndian.PutUint16(s.header[34:], 16)
	copy(s.header[36:], "data")
	binary.LittleEndian.PutUint32(s.header[40:], uint32(data))
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s, nil
}

// Size is the length of the WAV file in bytes.
func (s *ChapterStream) Size() int64 {
	return streamHeaderBytes + s.frames*int64(2*s.channels)
}

func (s *ChapterStream) ReadAt(p []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, errors.New("negative stream offset")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("chapter stream is closed")
	}
	block := int64(2 * s.channels)
	n := 0
	for n < len(p) {
		if offset >= s.Size() {
			return n, io.EOF
		}
		if offset < streamHeaderBytes {
			copied := copy(p[n:], s.header[offset:])
			n, offset = n+copied, offset+int64(copied)
			continue
		}
		frame, within := (offset-streamHeaderBytes)/block, int((offset-streamHeaderBytes)%block)
		want := min((int64(len(p)-n)+int64(within)+block-1)/block, streamChunkFrames)
		pcm, err := s.decode(frame, want)
		if err != nil {
			return n, err
		}
		copied := copy(p[n:], pcm[within:])
		n, offset = n+copied, offset+int64(copied)
	}
	return n, nil
}

// decode returns up to count frames starting at a chapter frame, never
// crossing a part boundary. The result is valid until the next call.
func (s *ChapterStream) decode(frame, count int64) ([]byte, error) {
	var part streamPart
	for _, candidate := range s.parts {
		if frame < candidate.start+candidate.frames {
			part = candidate
			break
		}
	}
	count = min(count, part.start+part.frames-frame)
	s.pcm = s.pcm[:0]
	if part.path == "" {
		s.pcm = append(s.pcm, make([]byte, count*int64(2*s.channels))...)
		return s.pcm, nil
	}
	sourceFrame := part.sourceStart + frame - part.start
	if err := s.position(part.path, sourceFrame); err != nil {
		return nil, err
	}
	err := s.decoder.Range(sourceFrame, sourceFrame+count, func(samples []float32) error {
		for _, v := range samples {
			scaled := math.Round(float64(v) * 32767)
			s.pcm = binary.LittleEndian.AppendUint16(s.pcm, uint16(int16(max(-32768, min(32767, scaled)))))
		}
		return nil
	})
	if err != nil {
		// A failed decoder is not reused; the next read starts cleanly.
		err = errors.Join(err, s.release())
		return nil, err
	}
	return s.pcm, nil
}

// position leaves a decoder for path at or shortly before frame.
func (s *ChapterStream) position(path string, frame int64) error {
	if s.decoder != nil && s.source == path {
		at := s.decoder.Position()
		if frame >= at && frame-at <= streamSeekGap {
			return nil
		}
		moved, err := s.decoder.SeekNear(frame)
		if err == nil && (moved || frame >= at) {
			return nil
		}
	}
	if err := s.release(); err != nil {
		return err
	}
	decoder, err := audio.OpenPCM(s.ctx, path, s.channels)
	if err != nil {
		return err
	}
	if _, err = decoder.SeekNear(frame); err != nil {
		// The source decodes from its origin when it cannot be sought.
		if err = decoder.Close(); err != nil {
			return err
		}
		if decoder, err = audio.OpenPCM(s.ctx, path, s.channels); err != nil {
			return err
		}
	}
	s.decoder, s.source = decoder, path
	return nil
}

func (s *ChapterStream) release() error {
	if s.decoder == nil {
		return nil
	}
	err := s.decoder.Close()
	s.decoder, s.source = nil, ""
	return err
}

// Interrupt makes a read in progress, and every later read, fail promptly.
func (s *ChapterStream) Interrupt() { s.cancel() }

// Close stops any read in progress and releases the source.
func (s *ChapterStream) Close() error {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.release()
}
