package audio

/*
#cgo pkg-config: libavcodec libavformat libavutil libavfilter
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersrc.h>
#include <libavfilter/buffersink.h>
#include <libavutil/opt.h>
#include <libavutil/intreadwrite.h>
#include <stdlib.h>
#include <string.h>

typedef struct BookEncoder {
 AVCodecContext *codec;
 AVFilterGraph *graph;
 AVFilterContext *src, *sink;
 AVFrame *input, *filtered;
 AVPacket *packet;
 AVFormatContext *mux;
 AVStream *stream;
 int64_t frames;
} BookEncoder;
static void book_encoder_free(BookEncoder *e) {
 if (!e) return;
 avcodec_free_context(&e->codec); avfilter_graph_free(&e->graph);
 av_frame_free(&e->input); av_frame_free(&e->filtered); av_packet_free(&e->packet); av_free(e);
}
static int book_encoder_open(BookEncoder **out, AVFormatContext *mux, int id, int rate, const char *layout, int bitrate, double gain) {
 int ret; char args[1024], description[2048];
 const AVCodec *codec=avcodec_find_encoder(id);
 if (!codec) return AVERROR_ENCODER_NOT_FOUND;
 BookEncoder *e=av_mallocz(sizeof(*e)); if (!e) return AVERROR(ENOMEM); *out=e; e->mux=mux;
 e->codec=avcodec_alloc_context3(codec); e->graph=avfilter_graph_alloc(); e->input=av_frame_alloc(); e->filtered=av_frame_alloc(); e->packet=av_packet_alloc();
 if (!e->codec || !e->graph || !e->input || !e->filtered || !e->packet) return AVERROR(ENOMEM);
 e->codec->sample_rate=rate; e->codec->time_base=(AVRational){1,rate}; e->codec->bit_rate=bitrate; e->codec->thread_count=1;
 e->codec->sample_fmt=id==AV_CODEC_ID_AAC?AV_SAMPLE_FMT_FLTP:AV_SAMPLE_FMT_S32;
 e->codec->bits_per_raw_sample=id==AV_CODEC_ID_AAC?0:24;
 if ((ret=av_channel_layout_from_string(&e->codec->ch_layout,layout))<0) return ret;
 if (!av_channel_layout_check(&e->codec->ch_layout)) return AVERROR(EINVAL);
 if (mux->oformat->flags & AVFMT_GLOBALHEADER) e->codec->flags|=AV_CODEC_FLAG_GLOBAL_HEADER;
 if ((ret=avcodec_open2(e->codec,codec,NULL))<0) return ret;
 if (!(codec->capabilities & AV_CODEC_CAP_VARIABLE_FRAME_SIZE) && !(codec->capabilities & AV_CODEC_CAP_SMALL_LAST_FRAME)) return AVERROR(ENOSYS);
 e->stream=avformat_new_stream(mux,NULL); if (!e->stream) return AVERROR(ENOMEM);
 if ((ret=avcodec_parameters_from_context(e->stream->codecpar,e->codec))<0) return ret;
 e->stream->time_base=e->codec->time_base;
 e->graph->nb_threads=1;
 snprintf(args,sizeof(args),"sample_rate=%d:sample_fmt=flt:channel_layout=%s:time_base=1/%d",rate,layout,rate);
 const AVFilter *src=avfilter_get_by_name("abuffer"), *sink=avfilter_get_by_name("abuffersink");
 if (!src || !sink) return AVERROR_FILTER_NOT_FOUND;
 if ((ret=avfilter_graph_create_filter(&e->src,src,"in",args,NULL,e->graph))<0) return ret;
 if ((ret=avfilter_graph_create_filter(&e->sink,sink,"out",NULL,NULL,e->graph))<0) return ret;
 AVFilterInOut *inputs=avfilter_inout_alloc(), *outputs=avfilter_inout_alloc();
 if (!inputs || !outputs) {avfilter_inout_free(&inputs);avfilter_inout_free(&outputs);return AVERROR(ENOMEM);}
 outputs->name=av_strdup("in");outputs->filter_ctx=e->src;outputs->pad_idx=0;
 inputs->name=av_strdup("out");inputs->filter_ctx=e->sink;inputs->pad_idx=0;
 if (!outputs->name || !inputs->name) {avfilter_inout_free(&inputs);avfilter_inout_free(&outputs);return AVERROR(ENOMEM);}
 // p=0 keeps the last frame's real sample count. No internal segment framing.
 snprintf(description,sizeof(description),"volume=%.17gdB:precision=double,aformat=sample_fmts=%s:sample_rates=%d:channel_layouts=%s,asetnsamples=n=%d:p=0",gain,av_get_sample_fmt_name(e->codec->sample_fmt),rate,layout,e->codec->frame_size>0?e->codec->frame_size:4096);
 ret=avfilter_graph_parse_ptr(e->graph,description,&inputs,&outputs,NULL);
 avfilter_inout_free(&inputs);avfilter_inout_free(&outputs);
 if (ret<0) return ret;
 return avfilter_graph_config(e->graph,NULL);
}
static int book_encoder_packets(BookEncoder *e) {
 int ret;
 while ((ret=avcodec_receive_packet(e->codec,e->packet))>=0) {
  e->packet->stream_index=e->stream->index;
  av_packet_rescale_ts(e->packet,e->codec->time_base,e->stream->time_base);
  ret=av_interleaved_write_frame(e->mux,e->packet); av_packet_unref(e->packet);
  if (ret<0) return ret;
 }
 return ret==AVERROR(EAGAIN)||ret==AVERROR_EOF?0:ret;
}
static int book_encoder_send(BookEncoder *e, AVFrame *f) {
 int ret=avcodec_send_frame(e->codec,f);
 if (ret==AVERROR(EAGAIN)) {if ((ret=book_encoder_packets(e))<0) return ret;ret=avcodec_send_frame(e->codec,f);}
 if (ret<0) return ret;
 return book_encoder_packets(e);
}
static int book_encoder_push(BookEncoder *e, const float *samples, int frames) {
 int ret;
 if (samples) {
  AVFrame *f=e->input; f->format=AV_SAMPLE_FMT_FLT;f->sample_rate=e->codec->sample_rate;f->nb_samples=frames;f->pts=e->frames;
  if ((ret=av_channel_layout_copy(&f->ch_layout,&e->codec->ch_layout))<0) return ret;
  if ((ret=av_frame_get_buffer(f,0))<0) return ret;
  memcpy(f->data[0],samples,(size_t)frames*f->ch_layout.nb_channels*sizeof(float));e->frames+=frames;
  ret=av_buffersrc_add_frame_flags(e->src,f,0);av_frame_unref(f);
 } else ret=av_buffersrc_add_frame_flags(e->src,NULL,0);
 if (ret<0) return ret;
 while ((ret=av_buffersink_get_frame(e->sink,e->filtered))>=0) {
  ret=book_encoder_send(e,e->filtered);av_frame_unref(e->filtered);if (ret<0) return ret;
 }
 if (ret!=AVERROR(EAGAIN) && ret!=AVERROR_EOF) return ret;
 if (!samples) return book_encoder_send(e,NULL);
 return 0;
}
static int book_tag(AVFormatContext *fc, const char *key, const char *value) { return av_dict_set(&fc->metadata,key,value,0); }
static int book_chapter(AVFormatContext *fc, int64_t id, int rate, int64_t start, int64_t end, const char *title) {
 AVChapter *ch=av_mallocz(sizeof(*ch)); if (!ch) return AVERROR(ENOMEM);
 ch->id=id;ch->time_base=(AVRational){1,rate};ch->start=start;ch->end=end;
 int ret=av_dict_set(&ch->metadata,"title",title,0);
 if (ret<0) {av_free(ch);return ret;}
 AVChapter **chapters=av_realloc_array(fc->chapters,fc->nb_chapters+1,sizeof(*chapters));
 if (!chapters) {av_dict_free(&ch->metadata);av_free(ch);return AVERROR(ENOMEM);}
 fc->chapters=chapters;fc->chapters[fc->nb_chapters++]=ch;return 0;
}
static int book_cover(AVFormatContext *fc, const uint8_t *data, int length, int png, int width, int height) {
 AVStream *st=avformat_new_stream(fc,NULL);if (!st) return AVERROR(ENOMEM);
 st->disposition=AV_DISPOSITION_ATTACHED_PIC;st->time_base=(AVRational){1,90000};
 st->codecpar->codec_type=AVMEDIA_TYPE_VIDEO;st->codecpar->codec_id=png?AV_CODEC_ID_PNG:AV_CODEC_ID_MJPEG;
 st->codecpar->width=width;st->codecpar->height=height;
 int ret=av_new_packet(&st->attached_pic,length);if (ret<0) return ret;
 memcpy(st->attached_pic.data,data,length);st->attached_pic.stream_index=st->index;st->attached_pic.flags|=AV_PKT_FLAG_KEY;
 ret=av_dict_set(&st->metadata,"title","Cover",0);if(ret<0)return ret;
 return av_dict_set(&st->metadata,"comment","Cover (front)",0);
}
static int book_write_cover(AVFormatContext *fc) {
 for (unsigned i=0;i<fc->nb_streams;i++) if (fc->streams[i]->disposition & AV_DISPOSITION_ATTACHED_PIC) {
  AVPacket *p=av_packet_clone(&fc->streams[i]->attached_pic);if(!p)return AVERROR(ENOMEM);
  p->pts=p->dts=0;int ret=av_interleaved_write_frame(fc,p);av_packet_free(&p);if(ret<0)return ret;
 }
 return 0;
}
static int64_t book_packet_skip(AVPacket *p) {
 size_t size=0; uint8_t *side=av_packet_get_side_data(p,AV_PKT_DATA_SKIP_SAMPLES,&size);
 return side && size>=10?AV_RL32(side):0;
}
static int book_aac_parameters(AVCodecParameters *cp, int channels) {
 AVChannelLayout want=channels==1?(AVChannelLayout)AV_CHANNEL_LAYOUT_MONO:(AVChannelLayout)AV_CHANNEL_LAYOUT_STEREO;
 return cp->codec_id==AV_CODEC_ID_AAC && cp->profile==AV_PROFILE_AAC_LOW && cp->sample_rate==44100 && cp->format==AV_SAMPLE_FMT_FLTP &&
 cp->extradata_size>=2 && cp->extradata && av_channel_layout_check(&cp->ch_layout) && av_channel_layout_compare(&cp->ch_layout,&want)==0;
}
static int book_same_parameters(AVCodecParameters *a, AVCodecParameters *b) {
 return a->codec_id==b->codec_id && a->profile==b->profile && a->format==b->format && a->sample_rate==b->sample_rate &&
 av_channel_layout_compare(&a->ch_layout,&b->ch_layout)==0 && a->extradata_size==b->extradata_size && a->extradata_size>0 && !memcmp(a->extradata,b->extradata,a->extradata_size);
}
*/
import "C"

import (
	"fmt"
	"github.com/asticode/go-astiav"
	"unsafe"
)

// go-astiav v0.40 wrappers begin with their native pointer; keep that bridge here.
func nativeFormat(fc *astiav.FormatContext) *C.AVFormatContext {
	return *(**C.AVFormatContext)(unsafe.Pointer(fc))
}
func nativeParams(cp *astiav.CodecParameters) *C.AVCodecParameters {
	return *(**C.AVCodecParameters)(unsafe.Pointer(cp))
}

type nativeEncoder struct{ ptr *C.BookEncoder }

func newNativeEncoder(fc *astiav.FormatContext, f pcmFormat, opts EncodeOptions) (*nativeEncoder, error) {
	id := C.int(C.AV_CODEC_ID_AAC)
	if opts.Format == "flac" {
		id = C.AV_CODEC_ID_FLAC
	} else if opts.Format == "wav" {
		id = C.AV_CODEC_ID_PCM_S24LE
	}
	layout := C.CString(f.layout.String())
	defer C.free(unsafe.Pointer(layout))
	e := &nativeEncoder{}
	ret := C.book_encoder_open(&e.ptr, nativeFormat(fc), id, C.int(f.rate), layout, C.int(opts.BitRate), C.double(opts.GainDB))
	if ret < 0 {
		e.close()
		return nil, fmt.Errorf("opening encoder: %w", astiav.Error(ret))
	}
	return e, nil
}
func (e *nativeEncoder) close() { C.book_encoder_free(e.ptr); e.ptr = nil }
func (e *nativeEncoder) push(b []float32, ch int) error {
	var p *C.float
	if len(b) > 0 {
		p = (*C.float)(unsafe.Pointer(&b[0]))
	}
	if r := C.book_encoder_push(e.ptr, p, C.int(len(b)/ch)); r < 0 {
		return fmt.Errorf("encoding audio: %w", astiav.Error(r))
	}
	return nil
}
func addBookChapters(fc *astiav.FormatContext, chapters []ChapterMarker, rate int) error {
	for i, ch := range chapters {
		title := C.CString(ch.Title)
		r := C.book_chapter(nativeFormat(fc), C.int64_t(i+1), C.int(rate), C.int64_t(ch.StartFrame), C.int64_t(ch.EndFrame), title)
		C.free(unsafe.Pointer(title))
		if r < 0 {
			return astiav.Error(r)
		}
	}
	return nil
}
func addBookCover(fc *astiav.FormatContext, b []byte, png bool, w, h int) error {
	p := 0
	if png {
		p = 1
	}
	if r := C.book_cover(nativeFormat(fc), (*C.uint8_t)(unsafe.Pointer(&b[0])), C.int(len(b)), C.int(p), C.int(w), C.int(h)); r < 0 {
		return astiav.Error(r)
	}
	return nil
}
func writeBookCover(fc *astiav.FormatContext) error {
	if r := C.book_write_cover(nativeFormat(fc)); r < 0 {
		return astiav.Error(r)
	}
	return nil
}
func compatibleAAC(cp *astiav.CodecParameters, ch int) bool {
	return C.book_aac_parameters(nativeParams(cp), C.int(ch)) != 0
}
func sameAACParameters(a, b *astiav.CodecParameters) bool {
	return C.book_same_parameters(nativeParams(a), nativeParams(b)) != 0
}

func setBookTag(fc *astiav.FormatContext, key, value string) error {
	k, v := C.CString(key), C.CString(value)
	defer C.free(unsafe.Pointer(k))
	defer C.free(unsafe.Pointer(v))
	if r := C.book_tag(nativeFormat(fc), k, v); r < 0 {
		return astiav.Error(r)
	}
	return nil
}

func packetSkip(p *astiav.Packet) int64 {
	return int64(C.book_packet_skip(*(**C.AVPacket)(unsafe.Pointer(p))))
}
