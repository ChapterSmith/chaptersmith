package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type faultOutput struct {
	*os.File
	kind   string
	writes int
	fault  error
	closed bool
}

func (f *faultOutput) Write(b []byte) (int, error) {
	f.writes++
	if f.kind == "write" || f.kind == "short" {
		if f.kind == "short" {
			return 0, nil
		}
		return 0, f.fault
	}
	return f.File.Write(b)
}
func (f *faultOutput) Seek(o int64, w int) (int64, error) {
	if f.kind == "trailer" {
		return 0, f.fault
	}
	return f.File.Seek(o, w)
}
func (f *faultOutput) Sync() error {
	if f.kind == "sync" {
		return f.fault
	}
	return f.File.Sync()
}
func (f *faultOutput) Close() error {
	f.closed = true
	e := f.File.Close()
	if f.kind == "close" {
		return f.fault
	}
	return e
}
func TestPCMFailuresOutputIO(t *testing.T) {
	for _, kind := range []string{"write", "short", "trailer", "sync", "close"} {
		t.Run(kind, func(t *testing.T) {
			f, e := os.Create(filepath.Join(t.TempDir(), "out.wav"))
			if e != nil {
				t.Fatal(e)
			}
			fault := errors.New(kind + " injected failure")
			out := &faultOutput{File: f, kind: kind, fault: fault}
			format, _ := bookPCMFormat(1)
			e = writeRF64(context.Background(), out, format, func(emit func([]float32) error) error { return emit(make([]float32, 20000)) })
			if kind == "short" {
				fault = io.ErrShortWrite
			}
			if !errors.Is(e, fault) {
				t.Fatalf("lost %s error: %v", kind, e)
			}
			if !out.closed {
				t.Fatal("file not closed")
			}
		})
	}
}

// sparseOutput stores headers and advances over the float payload without
// allocating it on disk. libav still counts every packet and byte normally.
type sparseOutput struct {
	*os.File
	end int64
}

func (s *sparseOutput) Write(b []byte) (int, error) {
	p, e := s.File.Seek(0, io.SeekCurrent)
	if e != nil {
		return 0, e
	}
	s.end = max(s.end, p+int64(len(b)))
	if p < 512 {
		return s.File.Write(b)
	}
	_, e = s.File.Seek(int64(len(b)), io.SeekCurrent)
	if e != nil {
		return 0, e
	}
	return len(b), nil
}
func (s *sparseOutput) Sync() error {
	if e := s.File.Truncate(s.end); e != nil {
		return e
	}
	return s.File.Sync()
}
func TestRF64SparseBeyond4GiB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.wav")
	file, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	format, _ := bookPCMFormat(1)
	frames := int64(1<<30) + 7
	chunk := make([]float32, 16384)
	e = writeRF64(context.Background(), &sparseOutput{File: file}, format, func(emit func([]float32) error) error {
		for left := frames; left > 0; {
			n := min(left, int64(len(chunk)))
			if e := emit(chunk[:n]); e != nil {
				return e
			}
			left -= n
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	header := make([]byte, 48)
	if _, e = f.ReadAt(header, 0); e != nil {
		t.Fatal(e)
	}
	st, e := f.Stat()
	if e != nil {
		t.Fatal(e)
	}
	if string(header[:4]) != "RF64" || binary.LittleEndian.Uint64(header[20:]) != uint64(st.Size()-8) || binary.LittleEndian.Uint64(header[28:]) != uint64(frames*4) || binary.LittleEndian.Uint64(header[36:]) != uint64(frames) {
		t.Fatalf("bad >4GiB ds64 header %x size %d", header, st.Size())
	}
	t.Logf("sparse size=%d data=%d frames=%d", st.Size(), frames*4, frames)
}

func TestRF64RejectsPacketAlignedTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "truncated.wav")
	if e := WriteRF64(context.Background(), path, 1, func(emit func([]float32) error) error { return emit(make([]float32, 44100)) }); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	dataOffset := -1
	for offset := 12; offset+8 <= len(data); {
		size := int(binary.LittleEndian.Uint32(data[offset+4:]))
		if string(data[offset:offset+4]) == "data" {
			dataOffset = offset + 8
			break
		}
		offset += 8 + size + (size & 1)
	}
	if dataOffset < 0 {
		t.Fatal("no RF64 data")
	}
	if e = os.Truncate(path, int64(dataOffset+32768)); e != nil {
		t.Fatal(e)
	}
	if e = DecodePCM(context.Background(), path, 1, 0, -1, func([]float32) error { return nil }); e == nil {
		t.Fatal("packet-aligned RF64 truncation accepted as EOF")
	}
}

func TestRF64DeclaredDataBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data-bound.wav")
	if e := WriteRF64(context.Background(), path, 1, func(emit func([]float32) error) error { return emit(make([]float32, 44100)) }); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	binary.LittleEndian.PutUint64(b[20:28], 0)
	if e = os.WriteFile(path, b[:32768], 0600); e != nil {
		t.Fatal(e)
	}
	if e = DecodePCM(context.Background(), path, 1, 0, -1, func([]float32) error { return nil }); e == nil {
		t.Fatal("ignored ds64 data size when RIFF size is unknown")
	}
}
