package audiotest

/*
#include <libavformat/avformat.h>
static int fixture_chapters(AVFormatContext *f){return f->nb_chapters;}
static int64_t fixture_ch_start(AVFormatContext *f,int i){return av_rescale_q(f->chapters[i]->start,f->chapters[i]->time_base,(AVRational){1,44100});}
static int64_t fixture_ch_end(AVFormatContext *f,int i){return av_rescale_q(f->chapters[i]->end,f->chapters[i]->time_base,(AVRational){1,44100});}
*/
import "C"
import (
	"github.com/asticode/go-astiav"
	"testing"
	"unsafe"
)

type Inspection struct {
	Tags           map[string]string
	Chapters       []InspectedChapter
	DurationFrames int64
	StartFrame     int64
	Covers         int
}
type InspectedChapter struct{ StartFrame, EndFrame int64 }

// Inspect uses native demux metadata/stream clocks, independent of the encoder.
func Inspect(t testing.TB, path string) Inspection {
	t.Helper()
	fc := astiav.AllocFormatContext()
	if fc == nil {
		t.Fatal("alloc format")
	}
	defer fc.Free()
	if e := fc.OpenInput(path, nil, nil); e != nil {
		t.Fatal(e)
	}
	defer fc.CloseInput()
	if e := fc.FindStreamInfo(nil); e != nil {
		t.Fatal(e)
	}
	out := Inspection{Tags: map[string]string{}}
	var prev *astiav.DictionaryEntry
	for {
		entry := fc.Metadata().Get("", prev, astiav.NewDictionaryFlags(astiav.DictionaryFlagIgnoreSuffix))
		if entry == nil {
			break
		}
		out.Tags[entry.Key()] = entry.Value()
		prev = entry
	}
	for _, st := range fc.Streams() {
		if st.CodecParameters().MediaType() == astiav.MediaTypeAudio {
			out.StartFrame = st.StartTime()
			out.DurationFrames = astiav.RescaleQ(st.Duration(), st.TimeBase(), astiav.NewRational(1, 44100))
		}
		if st.DispositionFlags().Has(astiav.DispositionFlagAttachedPic) {
			out.Covers++
		}
	}
	ptr := *(**C.AVFormatContext)(unsafe.Pointer(fc))
	for i := 0; i < int(C.fixture_chapters(ptr)); i++ {
		out.Chapters = append(out.Chapters, InspectedChapter{int64(C.fixture_ch_start(ptr, C.int(i))), int64(C.fixture_ch_end(ptr, C.int(i)))})
	}
	return out
}
