package chapter

/*
#include <libavformat/avformat.h>
#include <libavutil/dict.h>
#include <libavutil/mathematics.h>
#include <limits.h>
#include <stdlib.h>
#include <string.h>

typedef struct { int64_t start_us; char *title; } chapter_info;

// Returns distinct status for an empty table, an allocation failure and malformed data.
static int read_chapters(AVFormatContext *ctx, chapter_info **out, int *out_count) {
    *out = NULL; *out_count = 0;
    if (!ctx || ctx->nb_chapters == 0) return 0;
    if (!ctx->chapters || ctx->nb_chapters > INT_MAX) return -2;
    unsigned int n = ctx->nb_chapters;
    chapter_info *infos = calloc(n, sizeof(*infos));
    if (!infos) return -1;
    int status = -2;
    for (unsigned int i = 0; i < n; i++) {
        AVChapter *ch = ctx->chapters[i];
        if (!ch || ch->time_base.num <= 0 || ch->time_base.den <= 0 || ch->start < 0) goto fail;
        infos[i].start_us = av_rescale_q(ch->start, ch->time_base, (AVRational){1,1000000});
        if (infos[i].start_us < 0 || (i > 0 && infos[i].start_us <= infos[i-1].start_us)) goto fail;
        AVDictionaryEntry *tag = av_dict_get(ch->metadata, "title", NULL, 0);
        if (tag && tag->value) {
            infos[i].title = strdup(tag->value);
            if (!infos[i].title) { status = -1; goto fail; }
        }
    }
    *out = infos; *out_count = (int)n; return 0;
fail:
    for (unsigned int i = 0; i < n; i++) free(infos[i].title);
    free(infos); return status;
}
*/
import "C"
import (
	"fmt"
	"github.com/asticode/go-astiav"
	"strings"
	"unsafe"
)

func readChaptersCGO(fmtCtx *astiav.FormatContext) ([]Marker, error) {
	cPtr := *(**C.AVFormatContext)(unsafe.Pointer(fmtCtx))
	if cPtr == nil {
		return nil, fmt.Errorf("nil format context C pointer")
	}
	var infos *C.chapter_info
	var count C.int
	status := C.read_chapters(cPtr, &infos, &count)
	if status == -1 {
		return nil, fmt.Errorf("allocating chapter table")
	}
	if status != 0 {
		return nil, fmt.Errorf("malformed chapter table: invalid, duplicate or decreasing start")
	}
	if count == 0 {
		return nil, nil
	}
	defer C.free(unsafe.Pointer(infos))
	slice := unsafe.Slice(infos, int(count))
	markers := make([]Marker, len(slice))
	for i, ci := range slice {
		title := ""
		if ci.title != nil {
			title = strings.TrimSpace(C.GoString(ci.title))
			C.free(unsafe.Pointer(ci.title))
		}
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		markers[i] = Marker{Title: title, StartUS: int64(ci.start_us)}
	}
	return markers, nil
}
