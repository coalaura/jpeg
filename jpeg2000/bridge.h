#include "../internal/bridge.h"
#include <openjpeg.h>

typedef struct {
	int format, color, threads, reduce, layers;
	int region[4], has_region;
	int reversible, resolutions, progression, tile_width, tile_height;
	int codeblock_width, codeblock_height, mct, sop, eph;
	int layer_count, psnr;
	float rates[100];
	opj_image_cmptparm_t components[16];
	int alpha[16];
} cj_j2k_options;

typedef struct {
	cj_result base;
	opj_image_t *image;
} cj_j2k_result;

int cj_j2k_decode(const unsigned char *, size_t, int, uint64_t, cj_j2k_options *, cj_j2k_result *);
int cj_j2k_encode(const int32_t *, const unsigned char *, size_t, cj_j2k_options *, cj_j2k_result *);
void cj_j2k_free(cj_j2k_result *);
