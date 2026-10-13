package audio

/*
#cgo pkg-config: libavfilter libavutil libswresample
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersrc.h>
#include <libavfilter/buffersink.h>
#include <libavutil/opt.h>
#include <libavutil/mem.h>
#include <libswresample/swresample.h>
#include <stdlib.h>
#include <string.h>

typedef struct BookR128 {
 AVFilterGraph *graph;
 AVFilterContext *src, *sink, *meter;
 AVFrame *input, *filtered;
 int channels, input_rate, short_frames, seen_output;
 int64_t frames;
} BookR128;
static void book_r128_free(BookR128 *m) {
 if (!m) return;
 avfilter_graph_free(&m->graph);
 av_frame_free(&m->input); av_frame_free(&m->filtered); av_free(m);
}
static int book_r128_open(BookR128 **out, int channels, int short_frames) {
 if ((channels != 1 && channels != 2) || short_frames < 0 || short_frames >= 33) return AVERROR(EINVAL);
 int ret; char args[256], description[256];
 BookR128 *m=av_mallocz(sizeof(*m)); if (!m) return AVERROR(ENOMEM); *out=m;
 m->channels=channels;m->short_frames=short_frames;m->input_rate=short_frames?192000:44100; m->graph=avfilter_graph_alloc(); m->input=av_frame_alloc(); m->filtered=av_frame_alloc();
 if (!m->graph || !m->input || !m->filtered) return AVERROR(ENOMEM);
 m->graph->nb_threads=1;
 const AVFilter *src=avfilter_get_by_name("abuffer"), *sink=avfilter_get_by_name("abuffersink");
 if (!src || !sink || !avfilter_get_by_name("ebur128") || !avfilter_get_by_name("aresample")) return AVERROR_FILTER_NOT_FOUND;
 // The meter always runs on two channels. n8.0's AVX filter_channels reads a
 // 16-byte operand from its caches and sums for an odd channel count, one
 // double past their end, which faults when an array ends on a page boundary.
 // Mono is therefore presented as (M, 0): both channels weigh 1.0, so the
 // silent one adds no energy and block gating, loudness and peaks are those
 // of the mono signal. The filter's dualmono option only applies to
 // one-channel input, so its pan-law correction is applied by the caller.
 snprintf(args,sizeof(args),"sample_rate=%d:sample_fmt=%s:channel_layout=stereo:time_base=1/%d",m->input_rate,short_frames?"dbl":"flt",m->input_rate);
 if ((ret=avfilter_graph_create_filter(&m->src,src,"in",args,NULL,m->graph))<0) return ret;
 if ((ret=avfilter_graph_create_filter(&m->sink,sink,"out",NULL,NULL,m->graph))<0) return ret;
 AVFilterInOut *inputs=avfilter_inout_alloc(), *outputs=avfilter_inout_alloc();
 if (!inputs || !outputs) { avfilter_inout_free(&inputs);avfilter_inout_free(&outputs);return AVERROR(ENOMEM); }
 outputs->name=av_strdup("in"); outputs->filter_ctx=m->src;outputs->pad_idx=0;
 inputs->name=av_strdup("out");inputs->filter_ctx=m->sink;inputs->pad_idx=0;
 if (!outputs->name || !inputs->name) { avfilter_inout_free(&inputs);avfilter_inout_free(&outputs);return AVERROR(ENOMEM); }
 // n8.0 ebur128's internal 192 kHz peak resampler is never flushed at EOF.
 // Upstream aresample drains, and ebur128 then resamples 192k->192k without
 // delay. This is one continuous meter, with no artificial tail samples.
 snprintf(description,sizeof(description),"aresample=192000,ebur128=peak=true:framelog=quiet");
 ret=avfilter_graph_parse_ptr(m->graph,description,&inputs,&outputs,NULL);
 avfilter_inout_free(&inputs);avfilter_inout_free(&outputs);
 if (ret<0) return ret;
 for (unsigned i=0;i<m->graph->nb_filters;i++) {
  AVFilterContext *f=m->graph->filters[i];
  if (!strcmp(f->filter->name,"ebur128")) { m->meter=f;break; }
 }
 if (!m->meter) return AVERROR_FILTER_NOT_FOUND;
 return avfilter_graph_config(m->graph,NULL);
}
static void *book_r128_options(BookR128 *m) { return m->meter->priv; }
static int book_r128_feed(BookR128 *m, const void *samples, int frames) {
 int ret;
 if (samples) {
  AVFrame *f=m->input;f->format=m->short_frames?AV_SAMPLE_FMT_DBL:AV_SAMPLE_FMT_FLT;f->sample_rate=m->input_rate;f->nb_samples=frames;f->pts=m->frames;
  av_channel_layout_default(&f->ch_layout,2);
  if ((ret=av_frame_get_buffer(f,0))<0) return ret;
  if (m->channels==2) memcpy(f->data[0],samples,(size_t)frames*2*(m->short_frames?sizeof(double):sizeof(float)));
  else if (m->short_frames) {
   const double *in=samples; double *out=(double *)f->data[0];
   for (int i=0;i<frames;i++) {out[2*i]=in[i];out[2*i+1]=0;}
  } else {
   const float *in=samples; float *out=(float *)f->data[0];
   for (int i=0;i<frames;i++) {out[2*i]=in[i];out[2*i+1]=0;}
  }
  m->frames+=frames;
  ret=av_buffersrc_add_frame_flags(m->src,f,0);av_frame_unref(f);
 } else ret=av_buffersrc_add_frame_flags(m->src,NULL,0);
 if (ret<0) return ret;
 while ((ret=av_buffersink_get_frame(m->sink,m->filtered))>=0) {
  if (m->filtered->nb_samples > 0) m->seen_output=1;
  av_frame_unref(m->filtered);
 }
 // Once the source receives EOF, EAGAIN is not a completed drain.
 if (!samples) return ret==AVERROR_EOF?0:ret;
 return ret==AVERROR(EAGAIN)?0:ret;
}
static int book_r128_push(BookR128 *m, const float *samples, int frames) {
 if (!samples || !m->short_frames) return book_r128_feed(m,samples,frames);
 if (frames != m->short_frames || m->frames != 0) return AVERROR(EINVAL);
 // SWR n8.0 invert_initial_buffer needs filter_length+1 actual buffer
 // entries. At EOF its single reflection is insufficient for N<22.
 // Continue by 33 endpoint-reflected entries beyond the finite programme,
 // enough to cover both initialization and all original-duration FIR taps.
 // Reflected entries preserve channel frames. This prepares the FIR boundary,
 // not programme audio: ONLY the original-duration oversampled prefix
 // enters ebur128. The 32-tap SWR estimator/defaults remain unchanged.
 float reflected[65*2];
 double oversampled[512*2];
 int extended=frames+33;
 for (int i=0;i<extended;i++) {
  int j=i;
  if (i>=frames) {j=(i-frames)%(2*frames);j=j<frames?frames-1-j:j-frames;}
  for (int c=0;c<m->channels;c++) reflected[i*m->channels+c]=samples[j*m->channels+c];
 }
 AVChannelLayout layout;
 av_channel_layout_default(&layout,m->channels);
 SwrContext *swr=NULL;
 int ret=swr_alloc_set_opts2(&swr,&layout,AV_SAMPLE_FMT_DBL,192000,&layout,AV_SAMPLE_FMT_FLT,44100,0,NULL);
 av_channel_layout_uninit(&layout);
 if (ret<0) {swr_free(&swr);return ret;}
 if ((ret=swr_init(swr))<0) {swr_free(&swr);return ret;}
 const uint8_t *in=(const uint8_t *)reflected;
 uint8_t *out=(uint8_t *)oversampled;
 // Ceiling on the 192 kHz grid retains the whole original sample interval;
 // error is less than one 192 kHz sample (5.21 us), never a gating block.
 int wanted=(frames*192000+44100-1)/44100, fed=0;
 ret=swr_convert(swr,&out,512,&in,extended);
 for (;;) {
  if (ret<0) break;
  int n=FFMIN(ret,wanted-fed);
  if (n>0) {int err=book_r128_feed(m,oversampled,n);if (err<0) {ret=err;break;}fed+=n;}
  if (ret==0) break;
  ret=swr_convert(swr,&out,512,NULL,0);
 }
 swr_free(&swr);
 if (ret<0) return ret;
 return fed==wanted?0:AVERROR_INVALIDDATA;
}
static int book_r128_seen_output(BookR128 *m) { return m->seen_output; }

*/
import "C"

import (
	"fmt"
	"strconv"
	"unsafe"

	"github.com/asticode/go-astiav"
)

// dualMonoGain is ebur128's default pan law, added to a mono programme's
// integrated loudness when it will play from both speakers.
const dualMonoGain = 3.01029995663978

type r128Meter struct {
	ptr      *C.BookR128
	dualMono bool // mono input measured as dual mono
}

func newR128Meter(channels int, dualMono bool) (*r128Meter, error) {
	return newShortR128Meter(channels, dualMono, 0)
}
func newShortR128Meter(channels int, dualMono bool, shortFrames int) (*r128Meter, error) {
	m := &r128Meter{dualMono: dualMono && channels == 1}
	if ret := C.book_r128_open(&m.ptr, C.int(channels), C.int(shortFrames)); ret < 0 {
		m.close()
		return nil, fmt.Errorf("initializing R128 filter: %w", astiav.Error(ret))
	}
	return m, nil
}
func (m *r128Meter) close() { C.book_r128_free(m.ptr); m.ptr = nil }
func (m *r128Meter) push(samples []float32, channels int) error {
	var p *C.float
	if len(samples) > 0 {
		p = (*C.float)(unsafe.Pointer(&samples[0]))
	}
	if ret := C.book_r128_push(m.ptr, p, C.int(len(samples)/channels)); ret < 0 {
		return fmt.Errorf("processing R128 filter: %w", astiav.Error(ret))
	}
	return nil
}
func (m *r128Meter) value(name string) (float64, error) {
	if C.book_r128_seen_output(m.ptr) == 0 {
		return 0, fmt.Errorf("R128 filter produced no measured frames")
	}
	// Pinned go-astiav v0.40 PrivateData is one unsafe.Pointer. Its public
	// FilterContext lacks PrivateData; this narrow bridge mirrors the pinned
	// pointer access used by encode_metadata_cgo.go, retaining the Options API.
	pd := new(astiav.PrivateData)
	*(*unsafe.Pointer)(unsafe.Pointer(pd)) = C.book_r128_options(m.ptr)
	s, e := pd.Options().Get(name, 0)
	if e != nil {
		return 0, fmt.Errorf("reading R128 %s: %w", name, e)
	}
	v, e := strconv.ParseFloat(s, 64)
	if e != nil {
		return 0, fmt.Errorf("parsing R128 %s: %w", name, e)
	}
	// Like the filter, correct only a measured value: -70 means no block
	// passed the absolute gate and must stay recognisable to the caller.
	if m.dualMono && name == "integrated" && v != -70 {
		v += dualMonoGain
	}
	return v, nil
}
