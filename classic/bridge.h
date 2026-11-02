#include "../internal/bridge.h"
typedef struct {
	int quality, subsampling, progressive, optimize, arithmetic;
	int predictor, point_transform, color, dct;
	int restart, restart_rows, scale_num, scale_den, fancy, smoothing, strict;
	int jfif, adobe, max_scans, save_markers;
	int source_color, progressive_input, lossless_input;
	const unsigned char *markers;
	size_t markers_size;
} cj_jpeg_options;
int cj_jpeg_decode(const unsigned char *, size_t, int, uint64_t, cj_jpeg_options *, cj_result *);
int cj_jpeg_encode(const uint16_t *, cj_jpeg_options *, cj_result *);
