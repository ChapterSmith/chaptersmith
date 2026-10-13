#include "pcm_native.h"
#include <libavutil/opt.h>
#include <libavutil/intreadwrite.h>
extern int bookPCMRead(uintptr_t input, uint8_t *buffer, int size);
extern int64_t bookPCMSeek(uintptr_t input, int64_t offset, int whence);
static int pcm_read(void *input, uint8_t *buffer, int size) { return bookPCMRead((uintptr_t)input,buffer,size); }
static int64_t pcm_seek(void *input, int64_t offset, int whence) { return bookPCMSeek((uintptr_t)input,offset,whence); }
struct BookPCM {
    AVIOContext *input;
    AVFormatContext *format;
    AVCodecContext *codec;
    AVPacket *packet;
    AVFrame *frame;
    SwrContext *swr;
    AVChannelLayout output_layout, input_layout;
    int stream, rate, input_rate, input_format;
    int offset, demux_eof, decoder_eof, resampler_eof, drain_pending;
    uint64_t declared_samples, decoded_samples;
    int observation_ready, seek_pending;
    int64_t audible_samples;
    BookPCMObservation observation;
};
void book_pcm_close(BookPCM **ptr) {
    BookPCM *d = *ptr;
    if (!d) return;
    swr_free(&d->swr);
    av_frame_free(&d->frame);
    av_packet_free(&d->packet);
    avcodec_free_context(&d->codec);
    avformat_close_input(&d->format);
    if (d->input) { av_freep(&d->input->buffer); avio_context_free(&d->input); }
    av_channel_layout_uninit(&d->output_layout);
    av_channel_layout_uninit(&d->input_layout);
    av_freep(ptr);
}
int book_pcm_open(BookPCM **out, const char *path, int rate, const char *layout, uintptr_t input) {
    int ret;
    const AVCodec *codec;
    BookPCM *d = av_mallocz(sizeof(*d));
    if (!d) return AVERROR(ENOMEM);
    *out = d;
    d->rate = rate;
    if ((ret = av_channel_layout_from_string(&d->output_layout, layout)) < 0) return ret;
    d->format = avformat_alloc_context();
    uint8_t *buffer = av_malloc(32768);
    if (!d->format || !buffer) { av_free(buffer); return AVERROR(ENOMEM); }
    d->input = avio_alloc_context(buffer,32768,0,(void*)input,pcm_read,NULL,pcm_seek);
    if (!d->input) { av_free(buffer); return AVERROR(ENOMEM); }
    d->format->pb = d->input;
    d->format->flags |= AVFMT_FLAG_CUSTOM_IO;
    if ((ret = avformat_open_input(&d->format, path, NULL, NULL)) < 0) return ret;
    if ((ret = avformat_find_stream_info(d->format, NULL)) < 0) return ret;
    // Match ProbeSource and probeJoinFormat: always select the first audio
    // stream in demux order, regardless of default disposition. An unavailable
    // decoder on that stream is an error, never permission to change tracks.
    d->stream = -1;
    for (unsigned int i = 0; i < d->format->nb_streams; i++) {
        if (d->format->streams[i]->codecpar->codec_type == AVMEDIA_TYPE_AUDIO) {
            d->stream = i;
            break;
        }
    }
    if (d->stream < 0) return AVERROR_STREAM_NOT_FOUND;
    AVCodecParameters *parameters = d->format->streams[d->stream]->codecpar;
    codec = avcodec_find_decoder(parameters->codec_id);
    if (!codec) return AVERROR_DECODER_NOT_FOUND;
    // Native FLAC STREAMINFO has an exact 36-bit source-sample count. Zero
    // means unknown. Do not substitute AVStream.duration (it may be estimated)
    // or apply this count to other containers/codecs with priming/edit lists.
    if (!strcmp(d->format->iformat->name, "flac") &&
        parameters->codec_id == AV_CODEC_ID_FLAC && parameters->extradata_size == 34) {
        d->declared_samples = AV_RB64(parameters->extradata + 10) & ((UINT64_C(1) << 36) - 1);
    }
    d->codec = avcodec_alloc_context3(codec);
    d->frame = av_frame_alloc();
    d->packet = av_packet_alloc();
    if (!d->codec || !d->frame || !d->packet) return AVERROR(ENOMEM);
    if ((ret = avcodec_parameters_to_context(d->codec, d->format->streams[d->stream]->codecpar)) < 0) return ret;
    d->codec->pkt_timebase = d->format->streams[d->stream]->time_base;
    d->codec->thread_count = 1;
    d->codec->err_recognition = AV_EF_CRCCHECK | AV_EF_BITSTREAM | AV_EF_BUFFER | AV_EF_EXPLODE;
    // Do not set AV_CODEC_FLAG2_SKIP_MANUAL: libavcodec applies skip/discard
    // metadata once, including Opus preskip and container edit-list priming.
    return avcodec_open2(d->codec, codec, NULL);
}
int book_pcm_observation(BookPCM *d, BookPCMObservation *out) {
    if (!d->observation_ready) return 0;
    *out = d->observation;
    return 1;
}
int book_pcm_next(BookPCM *d, float *out, int capacity) {
    d->observation_ready = 0;
    int ret;
    uint8_t *output = (uint8_t *)out;
    if (d->resampler_eof) return AVERROR_EOF;
    if (d->drain_pending) {
        // Drain after a full output buffer. An upper-bound size query can be
        // larger than capacity even with no buffered samples at low input rates.
        const uint8_t *empty[64] = {0};
        ret = swr_convert(d->swr, &output, capacity, empty, 0);
        d->drain_pending = ret == capacity;
        return ret;
    }
    if (d->frame->nb_samples > d->offset) {
        AVFrame *f = d->frame;
        if (!d->swr) {
            if (f->sample_rate <= 0 || f->ch_layout.nb_channels <= 0 || f->ch_layout.nb_channels > 64 || !av_channel_layout_check(&f->ch_layout)) return AVERROR_INVALIDDATA;
            if ((ret = swr_alloc_set_opts2(&d->swr, &d->output_layout, AV_SAMPLE_FMT_FLT, d->rate, &f->ch_layout, f->format, f->sample_rate, 0, NULL)) < 0) return ret;
            if ((ret = swr_init(d->swr)) < 0) return ret;
            if ((ret = av_channel_layout_copy(&d->input_layout, &f->ch_layout)) < 0) return ret;
            d->input_rate = f->sample_rate;
            d->input_format = f->format;
        } else if (d->input_rate != f->sample_rate || d->input_format != f->format || av_channel_layout_compare(&d->input_layout, &f->ch_layout)) return AVERROR_INVALIDDATA;
        const uint8_t *input[64];
        int planar = av_sample_fmt_is_planar(f->format);
        int planes = planar ? f->ch_layout.nb_channels : 1;
        int stride = av_get_bytes_per_sample(f->format) * (planar ? 1 : f->ch_layout.nb_channels);
        int count = FFMIN(1024, f->nb_samples - d->offset);
        for (int i = 0; i < planes; i++) input[i] = f->extended_data[i] + (int64_t)d->offset * stride;
        ret = swr_convert(d->swr, &output, capacity, input, count);
        d->drain_pending = ret == capacity;
        d->offset += count;
        if (d->offset == f->nb_samples) { av_frame_unref(f); d->offset = 0; }
        return ret;
    }
    if (d->decoder_eof) {
        if (!d->swr) return AVERROR_INVALIDDATA;
        ret = swr_convert(d->swr, &output, capacity, NULL, 0);
        if (ret == 0) { d->resampler_eof = 1; return AVERROR_EOF; }
        return ret;
    }
    ret = avcodec_receive_frame(d->codec, d->frame);
    if (ret >= 0) {
        if (d->declared_samples) d->decoded_samples += d->frame->nb_samples;
        // AAC's MOV final packet duration can carry the final audible length
        // without skip-samples side data. libavcodec preserves it in duration,
        // but returns a full codec frame. Respect the shorter duration after
        // libavcodec's automatic skip/discard processing (never subtract twice).
        // Coarse container timestamps (e.g. millisecond Matroska ticks) cannot
        // define sample-accurate trims. Only trust a sample-resolution clock.
        if (d->frame->duration > 0 && d->frame->sample_rate > 0 &&
            d->codec->pkt_timebase.num > 0 &&
            (int64_t)d->codec->pkt_timebase.num * d->frame->sample_rate <= d->codec->pkt_timebase.den) {
            int64_t audible = av_rescale_q(d->frame->duration, d->codec->pkt_timebase, (AVRational){1, d->frame->sample_rate});
            if (audible > 0 && audible < d->frame->nb_samples) d->frame->nb_samples = audible;
        }
        if (d->frame->sample_rate <= 0) return AVERROR_INVALIDDATA;
        if (d->seek_pending) {
            // After a seek the position comes from the container timestamp,
            // not from counting samples since the origin.
            if (d->frame->pts == AV_NOPTS_VALUE) return AVERROR_INVALIDDATA;
            int64_t at = av_rescale_q(d->frame->pts, d->codec->pkt_timebase, (AVRational){1, d->frame->sample_rate});
            if (at < 0) return AVERROR_INVALIDDATA;
            d->audible_samples = at;
            d->seek_pending = 0;
        }
        AVFrame *f=d->frame;
        AVRational samples={1,f->sample_rate};
        d->observation.start=av_rescale_q(d->audible_samples,samples,(AVRational){1,d->rate});
        d->audible_samples+=f->nb_samples;
        d->observation.end=av_rescale_q(d->audible_samples,samples,(AVRational){1,d->rate});
        d->observation.valid=f->pts!=AV_NOPTS_VALUE;
        d->observation.pts_us=d->observation.valid?av_rescale_q(f->pts,d->codec->pkt_timebase,AV_TIME_BASE_Q):0;
        d->observation.duration_us=av_rescale_q(f->nb_samples,samples,AV_TIME_BASE_Q);
        d->observation_ready=1;
        if (d->frame->flags & AV_FRAME_FLAG_CORRUPT || d->frame->decode_error_flags || d->frame->nb_samples > 1048576) return AVERROR_INVALIDDATA;
        return 0;
    }
    if (ret == AVERROR_EOF) {
        if (d->declared_samples && d->decoded_samples != d->declared_samples) return AVERROR_INVALIDDATA;
        d->decoder_eof = 1;
        return 0;
    }
    if (ret != AVERROR(EAGAIN)) return ret;
    if (d->demux_eof) return AVERROR_INVALIDDATA;
    ret = av_read_frame(d->format, d->packet);
    if (ret == AVERROR_EOF) {
        if (d->format->pb && d->format->pb->error < 0) return d->format->pb->error;
        d->demux_eof = 1;
        return avcodec_send_packet(d->codec, NULL);
    }
    if (ret < 0) return ret;
    if (d->packet->flags & AV_PKT_FLAG_CORRUPT) { av_packet_unref(d->packet); return AVERROR_INVALIDDATA; }
    ret = d->packet->stream_index == d->stream ? avcodec_send_packet(d->codec, d->packet) : 0;
    av_packet_unref(d->packet);
    return ret;
}

// Half a second of pre-roll lets every supported codec rebuild its overlap
// state before the first sample the caller keeps.
#define BOOK_PCM_PREROLL_MS 500
int book_pcm_seek(BookPCM *d, int64_t target, int64_t *position) {
    AVStream *stream = d->format->streams[d->stream];
    const char *name = d->format->iformat->name;
    // Only containers whose index yields sample-exact timestamps, and only
    // when timestamps count from the first audible sample.
    if (!strstr(name, "mp4") && strcmp(name, "flac") && strcmp(name, "wav")) return BOOK_PCM_NO_SEEK;
    // WAV reports no stream start time; its timestamps are byte positions.
    if (stream->start_time == AV_NOPTS_VALUE ? strcmp(name, "wav") != 0 : stream->start_time > 0) return BOOK_PCM_NO_SEEK;
    if (!d->format->pb || !(d->format->pb->seekable & AVIO_SEEKABLE_NORMAL)) return BOOK_PCM_NO_SEEK;
    int64_t preroll = (int64_t)d->rate * BOOK_PCM_PREROLL_MS / 1000;
    if (target <= 2 * preroll) return BOOK_PCM_NO_SEEK;
    int64_t timestamp = av_rescale_q(target - preroll, (AVRational){1, d->rate}, stream->time_base);
    int ret = av_seek_frame(d->format, d->stream, timestamp, AVSEEK_FLAG_BACKWARD);
    if (ret < 0) return ret;
    avcodec_flush_buffers(d->codec);
    swr_free(&d->swr);
    av_frame_unref(d->frame);
    av_packet_unref(d->packet);
    d->offset = d->demux_eof = d->decoder_eof = d->resampler_eof = d->drain_pending = 0;
    d->observation_ready = 0;
    // A partial decode cannot satisfy the whole-stream sample count check.
    d->declared_samples = d->decoded_samples = 0;
    d->seek_pending = 1;
    while (!d->frame->nb_samples) {
        if ((ret = book_pcm_next(d, NULL, 0)) < 0) return ret;
        if (d->decoder_eof) return AVERROR_EOF;
    }
    *position = d->observation.start;
    return 0;
}
