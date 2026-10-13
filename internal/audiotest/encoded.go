package audiotest

/*
#cgo pkg-config: libavcodec libavformat libavutil
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/opt.h>
#include <math.h>
static int fixture_packet(AVCodecContext *enc, AVFormatContext *fmt, AVFrame *frame, AVPacket *pkt) {
 int ret=avcodec_send_frame(enc,frame);if(ret<0)return ret;
 while((ret=avcodec_receive_packet(enc,pkt))>=0){pkt->stream_index=0;av_packet_rescale_ts(pkt,enc->time_base,fmt->streams[0]->time_base);ret=av_interleaved_write_frame(fmt,pkt);av_packet_unref(pkt);if(ret<0)return ret;}
 return ret==AVERROR(EAGAIN)||ret==AVERROR_EOF?0:ret;
}
// Independent linked-libav fixture encoder: exact input sample count, no filters,
// no calls to production DecodePCM/WriteRF64/TranscodeFile.
static int fixture_encode(const char *path,int samples,int origin,const char *format,enum AVCodecID codec_id, const float *input, int bitrate) {
 int ret=0;AVCodecContext *enc=NULL;AVFormatContext *fmt=NULL;AVFrame *f=NULL;AVPacket *p=NULL;AVStream *st=NULL;
 const AVCodec *codec=avcodec_find_encoder(codec_id);if(!codec)return AVERROR_ENCODER_NOT_FOUND;
 enc=avcodec_alloc_context3(codec);f=av_frame_alloc();p=av_packet_alloc();if(!enc||!f||!p){ret=AVERROR(ENOMEM);goto done;}
 enc->sample_rate=44100;enc->sample_fmt=codec_id==AV_CODEC_ID_FLAC?AV_SAMPLE_FMT_S16:AV_SAMPLE_FMT_FLTP;enc->time_base=(AVRational){1,44100};enc->bit_rate=bitrate;enc->ch_layout=(AVChannelLayout)AV_CHANNEL_LAYOUT_MONO;enc->flags|=AV_CODEC_FLAG_GLOBAL_HEADER;
 if((ret=avcodec_open2(enc,codec,NULL))<0)goto done;
 if((ret=avformat_alloc_output_context2(&fmt,NULL,format,path))<0)goto done;
 st=avformat_new_stream(fmt,NULL);if(!st){ret=AVERROR(ENOMEM);goto done;}
 if((ret=avcodec_parameters_from_context(st->codecpar,enc))<0)goto done;st->time_base=enc->time_base;
 if((ret=avio_open(&fmt->pb,path,AVIO_FLAG_WRITE))<0)goto done;
 // Prevent movie-timebase millisecond rounding of the final edit duration.
 AVDictionary *opts=NULL;av_dict_set(&opts,"movie_timescale","44100",0);
 ret=avformat_write_header(fmt,&opts);av_dict_free(&opts);if(ret<0)goto done;
 for(int start=0;start<samples;start+=enc->frame_size){f->format=enc->sample_fmt;f->sample_rate=44100;av_channel_layout_copy(&f->ch_layout,&enc->ch_layout);f->nb_samples=FFMIN(enc->frame_size,samples-start);f->pts=origin+start;if((ret=av_frame_get_buffer(f,0))<0)goto done;for(int i=0;i<f->nb_samples;i++){double sample=input?input[start+i]:.4*sin(2*M_PI*440*(start+i)/44100.0);if(codec_id==AV_CODEC_ID_FLAC)((int16_t*)f->data[0])[i]=(int16_t)(sample*32768);else ((float*)f->data[0])[i]=sample;}ret=fixture_packet(enc,fmt,f,p);av_frame_unref(f);if(ret<0)goto done;}
 if((ret=fixture_packet(enc,fmt,NULL,p))<0)goto done;ret=av_write_trailer(fmt);
 done: av_frame_free(&f);av_packet_free(&p);avcodec_free_context(&enc);if(fmt){if(fmt->pb){int close=avio_closep(&fmt->pb);if(ret>=0)ret=close;}avformat_free_context(fmt);}return ret;
}

static int fixture_missing_pts(const char *path){AVFormatContext *f=NULL;AVPacket *p=av_packet_alloc();int count=0,ret=avformat_open_input(&f,path,NULL,NULL);if(ret<0){av_packet_free(&p);return ret;}while(av_read_frame(f,p)>=0){if(p->pts==AV_NOPTS_VALUE)count++;av_packet_unref(p);}av_packet_free(&p);avformat_close_input(&f);return count;}

// Independent reference for short mono 44.1 kHz AAC fixtures only. Deliberately
// no production PCM core, resampling, seeking, range logic or duration trimming.
static int fixture_decode_aac(const char *path,float *out,int capacity){
 AVFormatContext *fmt=NULL;AVCodecContext *dec=NULL;AVFrame *f=av_frame_alloc();AVPacket *p=av_packet_alloc();const AVCodec *codec=NULL;int ret=0,stream,total=0,eof=0;
 if(!f||!p){ret=AVERROR(ENOMEM);goto done;}if((ret=avformat_open_input(&fmt,path,NULL,NULL))<0)goto done;if((ret=avformat_find_stream_info(fmt,NULL))<0)goto done;
 if((stream=av_find_best_stream(fmt,AVMEDIA_TYPE_AUDIO,-1,-1,&codec,0))<0){ret=stream;goto done;}dec=avcodec_alloc_context3(codec);if(!dec){ret=AVERROR(ENOMEM);goto done;}avcodec_parameters_to_context(dec,fmt->streams[stream]->codecpar);dec->pkt_timebase=fmt->streams[stream]->time_base;if((ret=avcodec_open2(dec,codec,NULL))<0)goto done;
 while(!eof){ret=av_read_frame(fmt,p);if(ret==AVERROR_EOF){eof=1;ret=avcodec_send_packet(dec,NULL);}else if(ret>=0){ret=p->stream_index==stream?avcodec_send_packet(dec,p):0;}av_packet_unref(p);if(ret<0)goto done;
 while((ret=avcodec_receive_frame(dec,f))>=0){if(f->format!=AV_SAMPLE_FMT_FLTP||f->ch_layout.nb_channels!=1||f->sample_rate!=44100||total+f->nb_samples>capacity){ret=AVERROR(EINVAL);goto done;}memcpy(out+total,f->data[0],f->nb_samples*sizeof(float));total+=f->nb_samples;av_frame_unref(f);}if(ret!=AVERROR(EAGAIN)&&ret!=AVERROR_EOF)goto done;
 }ret=total;
 done:av_packet_free(&p);av_frame_free(&f);avcodec_free_context(&dec);avformat_close_input(&fmt);return ret;
}

static int64_t fixture_last_packet(const char *path){AVFormatContext *f=NULL;AVPacket *p=av_packet_alloc();int64_t last=-1;int ret=avformat_open_input(&f,path,NULL,NULL);if(ret<0){av_packet_free(&p);return ret;}if((ret=avformat_find_stream_info(f,NULL))<0){av_packet_free(&p);avformat_close_input(&f);return ret;}while(av_read_frame(f,p)>=0){last=p->pos;av_packet_unref(p);}av_packet_free(&p);avformat_close_input(&f);return last;}
*/
import "C"
import (
	"encoding/binary"
	"fmt"
	"os"
	"testing"
	"unsafe"
)

func WriteAAC(t testing.TB, path string, samples, origin int) {
	t.Helper()
	p := C.CString(path)
	format := C.CString("ipod")
	defer C.free(unsafe.Pointer(format))
	defer C.free(unsafe.Pointer(p))
	if n := C.fixture_encode(p, C.int(samples), 0, format, C.AV_CODEC_ID_AAC, nil, 128000); n < 0 {
		t.Fatal(fmt.Errorf("AAC fixture encoding: %d", int(n)))
	}
	if origin > 0 {
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		b = shiftAACEdit(t, b, uint32(origin))
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}

}

// Add a standards-defined empty movie edit before the normal media edit. This
// shifts stream timestamps while retaining its existing encoder-delay edit.
func shiftAACEdit(t testing.TB, b []byte, origin uint32) []byte {
	var out []byte
	for len(b) > 0 {
		if len(b) < 8 {
			t.Fatal("short MP4 atom")
		}
		n := int(binary.BigEndian.Uint32(b))
		if n < 8 || n > len(b) {
			t.Fatal("bad MP4 atom")
		}
		atom := append([]byte(nil), b[:n]...)
		kind := string(atom[4:8])
		switch kind {
		case "moov", "trak", "edts":
			atom = append(atom[:8], shiftAACEdit(t, atom[8:], origin)...)
		case "elst":
			if len(atom) != 28 || atom[8] != 0 || binary.BigEndian.Uint32(atom[12:]) != 1 {
				t.Fatal("unexpected edit list")
			}
			entry := make([]byte, 12)
			binary.BigEndian.PutUint32(entry, origin)
			binary.BigEndian.PutUint32(entry[4:], 0xffffffff)
			binary.BigEndian.PutUint16(entry[8:], 1)
			old := append([]byte(nil), atom[16:]...)
			atom = append(atom[:16], entry...)
			atom = append(atom, old...)
			binary.BigEndian.PutUint32(atom[12:], 2)
		}
		binary.BigEndian.PutUint32(atom, uint32(len(atom)))
		out = append(out, atom...)
		b = b[n:]
	}
	return out
}

func WriteRawAAC(t testing.TB, path string, samples int) {
	t.Helper()
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	format := C.CString("adts")
	defer C.free(unsafe.Pointer(format))
	if n := C.fixture_encode(p, C.int(samples), 0, format, C.AV_CODEC_ID_AAC, nil, 128000); n < 0 {
		t.Fatalf("raw AAC fixture: %d", int(n))
	}
	if n := C.fixture_missing_pts(p); n <= 0 {
		t.Fatalf("fixture must contain missing PTS: %d", int(n))
	}
}

func ReferenceAAC(t testing.TB, path string, capacity ...int) []float32 {
	t.Helper()
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	size := 100000
	if len(capacity) != 0 {
		size = capacity[0]
	}
	out := make([]float32, size)
	n := C.fixture_decode_aac(p, (*C.float)(unsafe.Pointer(&out[0])), C.int(len(out)))
	if n < 0 {
		t.Fatalf("reference AAC decoder: %d", int(n))
	}
	return out[:int(n)]
}

// WriteFLAC produces a STREAMINFO sample count and complete independently encoded frames.
func WriteFLAC(t testing.TB, path string, samples int) {
	t.Helper()
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	format := C.CString("flac")
	defer C.free(unsafe.Pointer(format))
	if n := C.fixture_encode(p, C.int(samples), 0, format, C.AV_CODEC_ID_FLAC, nil, 128000); n < 0 {
		t.Fatalf("FLAC fixture encoding: %d", int(n))
	}
}
func LastPacketOffset(t testing.TB, path string) int64 {
	t.Helper()
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	n := C.fixture_last_packet(p)
	if n <= 0 {
		t.Fatalf("last packet offset: %d", int64(n))
	}
	return int64(n)
}

// WriteAACSamples is an independent direct-frame oracle (no filters/production encoder).
func WriteAACSamples(t testing.TB, path string, samples []float32, bitrate int) {
	t.Helper()
	p, f := C.CString(path), C.CString("ipod")
	defer C.free(unsafe.Pointer(p))
	defer C.free(unsafe.Pointer(f))
	if r := C.fixture_encode(p, C.int(len(samples)), 0, f, C.AV_CODEC_ID_AAC, (*C.float)(unsafe.Pointer(&samples[0])), C.int(bitrate)); r < 0 {
		t.Fatalf("reference encode: %d", r)
	}
}
