package muxer

/*
#include <libavformat/avformat.h>
#include <libavutil/dict.h>
#include <libavutil/error.h>
#include <stdlib.h>

// add_chapter adds a chapter to an AVFormatContext.
// Returns 0 on success, negative on error.
static int add_chapter(AVFormatContext *fmt_ctx, int64_t id,
                       int tb_num, int tb_den,
                       int64_t start, int64_t end,
                       const char *title)
{
    AVChapter *ch = av_mallocz(sizeof(AVChapter));
    if (!ch) return AVERROR(ENOMEM);

    ch->id = id;
    ch->time_base.num = tb_num;
    ch->time_base.den = tb_den;
    ch->start = start;
    ch->end = end;

    if (title) {
        int ret = av_dict_set(&ch->metadata, "title", title, 0);
        if (ret < 0) {
            av_dict_free(&ch->metadata);
            av_freep(&ch);
            return ret;
        }
    }

    AVChapter **tmp = av_realloc_array(fmt_ctx->chapters,
                                       (size_t)(fmt_ctx->nb_chapters + 1),
                                       sizeof(AVChapter *));
    if (!tmp) {
        av_dict_free(&ch->metadata);
        av_freep(&ch);
        return AVERROR(ENOMEM);
    }
    fmt_ctx->chapters = tmp;
    fmt_ctx->chapters[fmt_ctx->nb_chapters] = ch;
    fmt_ctx->nb_chapters++;
    return 0;
}
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/asticode/go-astiav"

	"chaptersmith/internal/chapter"
)

// addChaptersCGO adds chapter markers to an astiav FormatContext by calling
// the FFmpeg C API directly. This works around the fact that go-astiav does
// not expose an AddChapter method.
//
// The FormatContext struct has a single unexported field: c *C.AVFormatContext.
// We extract it via unsafe.Pointer since it's the first field of the struct.
func addChaptersCGO(fmtCtx *astiav.FormatContext, chapters []chapter.Chapter, totalDurationSec float64) error {
	// Extract the underlying *C.AVFormatContext from the go-astiav wrapper.
	// FormatContext is defined as: type FormatContext struct { c *C.AVFormatContext }
	cPtr := *(**C.AVFormatContext)(unsafe.Pointer(fmtCtx))
	if cPtr == nil {
		return fmt.Errorf("nil format context C pointer")
	}

	for i, ch := range chapters {
		startMs := C.int64_t(ch.StartTime * 1000)

		var endMs C.int64_t
		if i < len(chapters)-1 {
			endMs = C.int64_t(chapters[i+1].StartTime * 1000)
		} else {
			endMs = C.int64_t(totalDurationSec * 1000)
		}

		cTitle := C.CString(ch.Title)
		if cTitle == nil {
			return fmt.Errorf("allocating chapter %d title", i+1)
		}
		ret := C.add_chapter(cPtr, C.int64_t(i+1), 1, 1000, startMs, endMs, cTitle)
		C.free(unsafe.Pointer(cTitle))
		if ret < 0 {
			return fmt.Errorf("adding chapter %d (%s): ffmpeg error %d", i+1, ch.Title, ret)
		}
	}

	return nil
}
