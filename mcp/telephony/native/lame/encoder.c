// Thin streaming ABI around LAME 3.100. Each WASM instance owns one encoder.
#include <lame.h>
#include <stdint.h>
static lame_t encoder;
static short pcm[8192];
static unsigned char encoded[65536];
int start(int rate, int channels) {
    if ((channels != 1 && channels != 2) || rate < 8000 || rate > 48000) return -1;
    encoder = lame_init();
    if (!encoder) return -1;
    lame_set_num_channels(encoder, channels);
    lame_set_in_samplerate(encoder, rate);
    // MPEG-2 (16 kHz minimum) is supported by the app streaming decoder.
    lame_set_out_samplerate(encoder, rate < 16000 ? 16000 : rate);
    lame_set_brate(encoder, 128);
    lame_set_mode(encoder, channels == 1 ? MONO : STEREO);
    lame_set_quality(encoder, 5);
    lame_set_bWriteVbrTag(encoder, 1);
    return lame_init_params(encoder);
}
uintptr_t input_ptr(void) { return (uintptr_t)pcm; }
uintptr_t output_ptr(void) { return (uintptr_t)encoded; }
int encode(int samples, int channels) {
    if (samples < 0 || samples * channels > 8192) return -1;
    if (channels == 1) return lame_encode_buffer(encoder, pcm, pcm, samples, encoded, sizeof(encoded));
    return lame_encode_buffer_interleaved(encoder, pcm, samples, encoded, sizeof(encoded));
}
int finish(void) { return lame_encode_flush(encoder, encoded, sizeof(encoded)); }
int tag(void) { return (int)lame_get_lametag_frame(encoder, encoded, sizeof(encoded)); }
