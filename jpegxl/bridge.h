#ifndef CJ_XL_BRIDGE_H
#define CJ_XL_BRIDGE_H
#include "../internal/bridge.h"
#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
	int lossless, container, bits, exponent_bits, channels, premultiplied;
	int threads, effort, decoding_speed, orientation, keep_orientation;
	int unpremultiply, render_spotcolors, read_boxes, max_frames;
	int have_animation, linear, level;
	uint32_t tps_numerator, tps_denominator, loops;
	float distance, alpha_distance, intensity_target;
	uint64_t max_pixels, max_decoded_bytes, max_memory_bytes;
	int setting_count, setting_ids[40], setting_values[40];
} cj_xl_options;

typedef struct {
	void *pixels;
	size_t size;
	uint32_t duration, timecode;
	char *name;
} cj_xl_frame;

typedef struct {
	cj_result base;
	cj_xl_options info;
	cj_xl_frame *frames;
	int frame_count;
	unsigned char *icc, *original_icc;
	size_t icc_size, original_icc_size;
} cj_xl_result;

typedef struct cj_xl_encoder cj_xl_encoder;
cj_xl_encoder *cj_xl_encoder_create(uint32_t, uint32_t, const cj_xl_options *, const unsigned char *, size_t, cj_result *);
int cj_xl_encoder_frame(cj_xl_encoder *, const void *, size_t, uint32_t, uint32_t, const char *, cj_result *);
int cj_xl_encoder_box(cj_xl_encoder *, const char *, const unsigned char *, size_t, int, cj_result *);
int cj_xl_encoder_finish(cj_xl_encoder *, cj_result *);
void cj_xl_encoder_destroy(cj_xl_encoder *);
int cj_xl_decode(const unsigned char *, size_t, int, const cj_xl_options *, cj_xl_result *);
int cj_xl_recompress(const unsigned char *, size_t, const cj_xl_options *, cj_result *);
int cj_xl_restore(const unsigned char *, size_t, const cj_xl_options *, cj_result *);
void cj_xl_free(cj_xl_result *);
#ifdef __cplusplus
}
#endif
#endif
