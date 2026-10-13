package audio

/*
#include <stdint.h>
#include <libavutil/error.h>
#include <libavformat/avio.h>
*/
import "C"
import (
	"context"
	"errors"
	"io"
	"runtime/cgo"
	"unsafe"
)

type pcmInput struct {
	reader io.ReadSeeker
	ctx    context.Context
	err    error
}

//export bookPCMRead
func bookPCMRead(handle C.uintptr_t, buffer *C.uint8_t, size C.int) C.int {
	in := cgo.Handle(handle).Value().(*pcmInput)
	if e := in.ctx.Err(); e != nil {
		in.err = e
		return C.AVERROR_EXIT
	}
	n, e := in.reader.Read(unsafe.Slice((*byte)(unsafe.Pointer(buffer)), int(size)))
	if e != nil && !errors.Is(e, io.EOF) {
		in.err = e
		return C.AVERROR_EXTERNAL
	}
	if n > 0 {
		return C.int(n)
	}
	return C.AVERROR_EOF
}

//export bookPCMSeek
func bookPCMSeek(handle C.uintptr_t, offset C.int64_t, whence C.int) C.int64_t {
	in := cgo.Handle(handle).Value().(*pcmInput)
	if e := in.ctx.Err(); e != nil {
		in.err = e
		return C.AVERROR_EXIT
	}
	w := int(whence) &^ int(C.AVSEEK_FORCE)
	if w == int(C.AVSEEK_SIZE) {
		current, e := in.reader.Seek(0, io.SeekCurrent)
		if e != nil {
			in.err = e
			return C.AVERROR_EXTERNAL
		}
		end, e := in.reader.Seek(0, io.SeekEnd)
		if e != nil {
			in.err = e
			return C.AVERROR_EXTERNAL
		}
		if _, e = in.reader.Seek(current, io.SeekStart); e != nil {
			in.err = e
			return C.AVERROR_EXTERNAL
		}
		return C.int64_t(end)
	}
	n, e := in.reader.Seek(int64(offset), w)
	if e != nil {
		in.err = e
		return C.AVERROR_EXTERNAL
	}
	return C.int64_t(n)
}
