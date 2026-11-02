#include "bridge.h"
#include <jpeglib.h>
#include <jpegint.h>
#include <jerror.h>
#include <setjmp.h>

typedef struct {
	struct jpeg_error_mgr base;
	jmp_buf jump;
	cj_result *result;
	int strict;
} cj_jpeg_error;

typedef struct {
	struct jpeg_progress_mgr base;
	int max_scans;
} cj_jpeg_progress;

typedef struct {
	struct jpeg_decompress_struct decoder;
	struct jpeg_compress_struct encoder;
	cj_jpeg_error error;
	cj_jpeg_progress progress;
	unsigned char *row;
	unsigned char *encoded;
	unsigned long encoded_size;
} cj_jpeg_state;

static void cj_jpeg_error_exit(j_common_ptr codec) {
	cj_jpeg_error *error = (cj_jpeg_error *)codec->err;
	(*codec->err->format_message)(codec, error->result->error);
	longjmp(error->jump, 1);
}

static void cj_jpeg_message(j_common_ptr codec, int level) {
	cj_jpeg_error *error = (cj_jpeg_error *)codec->err;
	if (level < 0 && error->strict) cj_jpeg_error_exit(codec);
}

static void cj_jpeg_progress_monitor(j_common_ptr codec) {
	if (codec->is_decompressor) {
		j_decompress_ptr decoder = (j_decompress_ptr)codec;
		cj_jpeg_progress *progress = (cj_jpeg_progress *)codec->progress;
		if (decoder->input_scan_number > progress->max_scans) {
			cj_jpeg_error *error = (cj_jpeg_error *)codec->err;
			cj_fail(error->result, "scan limit exceeded");
			longjmp(error->jump, 1);
		}
	}
}

int cj_jpeg_decode(const unsigned char *data, size_t size, int header_only, uint64_t max_pixels, cj_jpeg_options *options, cj_result *result) {
	cj_jpeg_state *state = calloc(1, sizeof(*state));
	if (!state) return cj_fail(result, "decoder allocation failed");
	j_decompress_ptr codec = &state->decoder;
	codec->err = jpeg_std_error(&state->error.base);
	state->error.base.error_exit = cj_jpeg_error_exit;
	state->error.base.emit_message = cj_jpeg_message;
	state->error.result = result;
	state->error.strict = options->strict;
	if (setjmp(state->error.jump)) {
		jpeg_destroy_decompress(codec);
		free(state->row);
		free(state);
		return 0;
	}
	jpeg_create_decompress(codec);
	codec->mem->max_memory_to_use = 256u << 20;
	state->progress.base.progress_monitor = cj_jpeg_progress_monitor;
	state->progress.max_scans = options->max_scans;
	codec->progress = &state->progress.base;
	jpeg_mem_src(codec, data, size);
	if (options->save_markers) {
		for (int marker = 0xe0; marker <= 0xef; ++marker) jpeg_save_markers(codec, marker, 65535);
		jpeg_save_markers(codec, JPEG_COM, 65535);
	}
	jpeg_read_header(codec, TRUE);
	result->width = codec->image_width;
	result->height = codec->image_height;
	result->components = codec->num_components;
	result->bits = codec->data_precision;
	if (!cj_check(result, max_pixels)) longjmp(state->error.jump, 1);
	options->source_color = codec->jpeg_color_space;
	options->progressive_input = codec->progressive_mode;
	options->lossless_input = codec->master->lossless;
	codec->out_color_space = codec->num_components == 1 ? JCS_GRAYSCALE : codec->num_components == 4 ? JCS_CMYK : JCS_RGB;
	codec->scale_num = options->scale_num;
	codec->scale_denom = options->scale_den;
	codec->dct_method = options->dct;
	codec->do_fancy_upsampling = options->fancy;
	codec->do_block_smoothing = options->smoothing;
	jpeg_calc_output_dimensions(codec);
	result->width = codec->output_width;
	result->height = codec->output_height;
	result->components = codec->out_color_components;
	if (!cj_check(result, max_pixels)) longjmp(state->error.jump, 1);
	if (options->save_markers) {
		for (jpeg_saved_marker_ptr marker = codec->marker_list; marker; marker = marker->next) result->size += marker->data_length + 3;
		if (result->size > (4u << 20)) { cj_fail(result, "metadata limit exceeded"); longjmp(state->error.jump, 1); }
		if (result->size) {
			result->data = malloc(result->size);
			if (!result->data) { cj_fail(result, "metadata allocation failed"); longjmp(state->error.jump, 1); }
			unsigned char *target = result->data;
			for (jpeg_saved_marker_ptr marker = codec->marker_list; marker; marker = marker->next) {
				*target++ = marker->marker;
				*target++ = marker->data_length >> 8;
				*target++ = marker->data_length;
				memcpy(target, marker->data, marker->data_length);
				target += marker->data_length;
			}
		}
	}
	if (!header_only) {
		jpeg_start_decompress(codec);
		size_t stride = (size_t)result->width * result->components;
		result->samples = malloc(stride * result->height * 2);
		state->row = malloc(stride * 2);
		if (!result->samples || !state->row) { cj_fail(result, "sample allocation failed"); longjmp(state->error.jump, 1); }
		while (codec->output_scanline < codec->output_height) {
			uint16_t *target = result->samples + (size_t)codec->output_scanline * stride;
			if (result->bits <= 8) {
				JSAMPROW row = state->row;
				jpeg_read_scanlines(codec, &row, 1);
				for (size_t index = 0; index < stride; ++index) target[index] = row[index];
			} else if (result->bits <= 12) {
				J12SAMPROW row = (J12SAMPROW)target;
				jpeg12_read_scanlines(codec, &row, 1);
			} else {
				J16SAMPROW row = (J16SAMPROW)target;
				jpeg16_read_scanlines(codec, &row, 1);
			}
			if (result->components == 4 && codec->saw_Adobe_marker) {
				uint32_t maximum = (1u << result->bits) - 1;
				for (size_t index = 0; index < stride; ++index) target[index] = maximum - target[index];
			}
		}
		jpeg_finish_decompress(codec);
	}
	jpeg_destroy_decompress(codec);
	free(state->row);
	free(state);
	return 1;
}

int cj_jpeg_encode(const uint16_t *samples, cj_jpeg_options *options, cj_result *result) {
	cj_jpeg_state *state = calloc(1, sizeof(*state));
	if (!state) return cj_fail(result, "encoder allocation failed");
	j_compress_ptr codec = &state->encoder;
	codec->err = jpeg_std_error(&state->error.base);
	state->error.base.error_exit = cj_jpeg_error_exit;
	state->error.base.emit_message = cj_jpeg_message;
	state->error.result = result;
	state->error.strict = 1;
	if (setjmp(state->error.jump)) {
		jpeg_destroy_compress(codec);
		free(state->row);
		free(state->encoded);
		free(state);
		return 0;
	}
	jpeg_create_compress(codec);
	codec->mem->max_memory_to_use = 256u << 20;
	jpeg_mem_dest(codec, &state->encoded, &state->encoded_size);
	codec->image_width = result->width;
	codec->image_height = result->height;
	codec->input_components = result->components;
	codec->in_color_space = result->components == 1 ? JCS_GRAYSCALE : result->components == 4 ? JCS_CMYK : JCS_RGB;
	jpeg_set_defaults(codec);
	codec->data_precision = result->bits;
	if (options->predictor) {
		jpeg_set_colorspace(codec, codec->in_color_space);
		jpeg_enable_lossless(codec, options->predictor, options->point_transform);
	} else {
		jpeg_set_quality(codec, options->quality, TRUE);
		if (options->color) jpeg_set_colorspace(codec, options->color);
		if (codec->jpeg_color_space == JCS_YCbCr || codec->jpeg_color_space == JCS_YCCK) {
			static const int horizontal[] = {2, 1, 2, 1, 4, 1};
			static const int vertical[] = {2, 1, 1, 2, 1, 4};
			codec->comp_info[0].h_samp_factor = horizontal[options->subsampling];
			codec->comp_info[0].v_samp_factor = vertical[options->subsampling];
		}
		if (options->progressive) jpeg_simple_progression(codec);
	}
	codec->optimize_coding = options->optimize || result->bits == 12;
	codec->arith_code = options->arithmetic;
	codec->restart_interval = options->restart;
	codec->restart_in_rows = options->restart_rows;
	codec->dct_method = options->dct;
	codec->write_JFIF_header = options->jfif < 0 ? codec->write_JFIF_header : options->jfif;
	codec->write_Adobe_marker = options->adobe < 0 ? codec->write_Adobe_marker : options->adobe;
	jpeg_start_compress(codec, TRUE);
	for (size_t position = 0; position < options->markers_size;) {
		int marker = options->markers[position];
		unsigned length = ((unsigned)options->markers[position+1] << 8) | options->markers[position+2];
		jpeg_write_marker(codec, marker, options->markers + position + 3, length);
		position += length + 3;
	}
	size_t stride = (size_t)result->width * result->components;
	state->row = malloc(stride * 2);
	if (!state->row) { cj_fail(result, "row allocation failed"); longjmp(state->error.jump, 1); }
	while (codec->next_scanline < codec->image_height) {
		const uint16_t *source = samples + (size_t)codec->next_scanline * stride;
		uint32_t maximum = (1u << result->bits) - 1;
		for (size_t index = 0; index < stride; ++index) {
			uint16_t value = result->components == 4 && codec->write_Adobe_marker ? maximum - source[index] : source[index];
			if (result->bits <= 8) state->row[index] = value;
			else ((uint16_t *)state->row)[index] = value;
		}
		if (result->bits <= 8) { JSAMPROW row = state->row; jpeg_write_scanlines(codec, &row, 1); }
		else if (result->bits <= 12) { J12SAMPROW row = (J12SAMPROW)state->row; jpeg12_write_scanlines(codec, &row, 1); }
		else { J16SAMPROW row = (J16SAMPROW)state->row; jpeg16_write_scanlines(codec, &row, 1); }
	}
	jpeg_finish_compress(codec);
	result->data = state->encoded;
	result->size = state->encoded_size;
	jpeg_destroy_compress(codec);
	free(state->row);
	free(state);
	return 1;
}
