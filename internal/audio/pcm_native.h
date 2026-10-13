#ifndef BOOK_PCM_NATIVE_H
#define BOOK_PCM_NATIVE_H
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libswresample/swresample.h>
typedef struct BookPCM BookPCM;
int book_pcm_open(BookPCM **out, const char *path, int rate, const char *layout, uintptr_t input);
typedef struct { int64_t start,end,pts_us,duration_us; int valid; } BookPCMObservation;
int book_pcm_observation(BookPCM *d, BookPCMObservation *out);
int book_pcm_next(BookPCM *d, float *out, int capacity);
// Seeks shortly before output frame `target` and reports the output frame of
// the next sample. Returns BOOK_PCM_NO_SEEK, changing nothing, when the
// container cannot seek sample-exactly; after a negative error the decoder
// must be closed.
#define BOOK_PCM_NO_SEEK 1
int book_pcm_seek(BookPCM *d, int64_t target, int64_t *position);
void book_pcm_close(BookPCM **d);
#endif
