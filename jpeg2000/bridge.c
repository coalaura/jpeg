#include "bridge.h"

extern OPJ_BOOL cj_jp2_header_config(opj_codec_t *codec, opj_image_t *image, OPJ_BOOL header_only, OPJ_UINT32 max_components, OPJ_UINT64 max_bytes);

typedef struct {
	const unsigned char *input;
	unsigned char *output;
	size_t size, position, capacity;
	cj_result *result;
} cj_j2k_stream;

static OPJ_SIZE_T cj_j2k_read(void *buffer, OPJ_SIZE_T bytes, void *user) {
	cj_j2k_stream *stream = user;
	if (stream->position >= stream->size) return (OPJ_SIZE_T)-1;
	if (bytes > stream->size - stream->position) bytes = stream->size - stream->position;
	memcpy(buffer, stream->input + stream->position, bytes);
	stream->position += bytes;
	return bytes;
}

static int cj_j2k_grow(cj_j2k_stream *stream, size_t needed) {
	if (needed > (512u << 20)) return cj_fail(stream->result, "encoded output limit exceeded");
	if (needed <= stream->capacity) return 1;
	size_t capacity = stream->capacity ? stream->capacity : 4096;
	while (capacity < needed) capacity *= 2;
	unsigned char *output = realloc(stream->output, capacity);
	if (!output) return cj_fail(stream->result, "output allocation failed");
	stream->output = output;
	stream->capacity = capacity;
	return 1;
}

static OPJ_SIZE_T cj_j2k_write(void *buffer, OPJ_SIZE_T bytes, void *user) {
	cj_j2k_stream *stream = user;
	if (bytes > (512u << 20) || !cj_j2k_grow(stream, stream->position + bytes)) return (OPJ_SIZE_T)-1;
	memcpy(stream->output + stream->position, buffer, bytes);
	stream->position += bytes;
	if (stream->size < stream->position) stream->size = stream->position;
	return bytes;
}

static OPJ_BOOL cj_j2k_seek(OPJ_OFF_T position, void *user) {
	cj_j2k_stream *stream = user;
	if (position < 0 || (uint64_t)position > (512u << 20)) return OPJ_FALSE;
	if (stream->input) {
		if ((size_t)position > stream->size) return OPJ_FALSE;
	} else {
		if (!cj_j2k_grow(stream, position)) return OPJ_FALSE;
		if ((size_t)position > stream->size) {
			memset(stream->output + stream->size, 0, position - stream->size);
			stream->size = position;
		}
	}
	stream->position = position;
	return OPJ_TRUE;
}

static OPJ_OFF_T cj_j2k_skip(OPJ_OFF_T bytes, void *user) {
	cj_j2k_stream *stream = user;
	if (bytes < -(OPJ_OFF_T)stream->position || bytes > (512u << 20)) return -1;
	return cj_j2k_seek((OPJ_OFF_T)stream->position + bytes, user) ? bytes : -1;
}

static void cj_j2k_error(const char *message, void *user) {
	cj_fail(user, message);
}

void cj_j2k_free(cj_j2k_result *result) {
	if (result->image) opj_image_destroy(result->image);
	cj_free(&result->base);
}

static int cj_j2k_check_image(cj_j2k_result *result, uint64_t max_pixels) {
	opj_image_t *image = result->image;
	result->base.width = image->x1 - image->x0;
	result->base.height = image->y1 - image->y0;
	result->base.components = image->numcomps;
	if (!cj_check(&result->base, max_pixels)) return 0;
	uint64_t total = 0;
	for (uint32_t index = 0; index < image->numcomps; ++index) {
		opj_image_comp_t *component = &image->comps[index];
		uint64_t count = (uint64_t)component->w * component->h;
		if (!component->w || !component->h || !component->prec || component->prec > 31 || !component->dx || !component->dy || count > (128u << 20)) return cj_fail(&result->base, "invalid component dimensions or precision");
		total += count;
	}
	if (total > (128u << 20)) return cj_fail(&result->base, "sample allocation limit exceeded");
	return 1;
}

int cj_j2k_decode(const unsigned char *data, size_t size, int header_only, uint64_t max_pixels, cj_j2k_options *options, cj_j2k_result *result) {
	opj_dparameters_t parameters;
	opj_set_default_decoder_parameters(&parameters);
	parameters.cp_reduce = options->reduce;
	parameters.cp_layer = options->layers;
	opj_codec_t *codec = opj_create_decompress(options->format ? OPJ_CODEC_J2K : OPJ_CODEC_JP2);
	opj_stream_t *stream = opj_stream_default_create(OPJ_TRUE);
	cj_j2k_stream memory = {0};
	memory.input = data;
	memory.size = size;
	memory.result = &result->base;
	int success = 0;
	if (!codec || !stream) { cj_fail(&result->base, "decoder allocation failed"); goto cleanup; }
	opj_set_error_handler(codec, cj_j2k_error, &result->base);
	opj_stream_set_user_data(stream, &memory, NULL);
	opj_stream_set_user_data_length(stream, size);
	opj_stream_set_read_function(stream, cj_j2k_read);
	opj_stream_set_skip_function(stream, cj_j2k_skip);
	opj_stream_set_seek_function(stream, cj_j2k_seek);
	if (!opj_setup_decoder(codec, &parameters) || !opj_codec_set_threads(codec, options->threads)) goto cleanup;
	if (!opj_read_header(stream, codec, &result->image) || !cj_j2k_check_image(result, max_pixels)) goto cleanup;
	if (options->has_region && !opj_set_decode_area(codec, result->image, options->region[0], options->region[1], options->region[2], options->region[3])) goto cleanup;
	if (!options->format && !cj_jp2_header_config(codec, result->image, header_only, 16, 512u << 20)) goto cleanup;
	if (!header_only) {
		if (!opj_decode(codec, stream, result->image) || !opj_end_decompress(codec, stream)) goto cleanup;
	}
	if (!cj_j2k_check_image(result, max_pixels)) goto cleanup;
	success = 1;
cleanup:
	if (!success && !result->base.error[0]) cj_fail(&result->base, "invalid JPEG 2000 stream");
	if (stream) opj_stream_destroy(stream);
	if (codec) opj_destroy_codec(codec);
	return success;
}

int cj_j2k_encode(const int32_t *samples, const unsigned char *icc, size_t icc_size, cj_j2k_options *options, cj_j2k_result *result) {
	opj_cparameters_t parameters;
	opj_set_default_encoder_parameters(&parameters);
	parameters.irreversible = !options->reversible;
	parameters.numresolution = options->resolutions;
	parameters.prog_order = options->progression;
	parameters.tcp_numlayers = options->layer_count;
	parameters.cp_disto_alloc = !options->psnr;
	parameters.cp_fixed_quality = options->psnr;
	parameters.tcp_mct = options->mct;
	parameters.cblockw_init = options->codeblock_width;
	parameters.cblockh_init = options->codeblock_height;
	parameters.csty = (options->sop ? 2 : 0) | (options->eph ? 4 : 0);
	for (int index = 0; index < options->layer_count; ++index) {
		if (options->psnr) parameters.tcp_distoratio[index] = options->rates[index];
		else parameters.tcp_rates[index] = options->rates[index];
	}
	if (options->tile_width) {
		parameters.tile_size_on = OPJ_TRUE;
		parameters.cp_tdx = options->tile_width;
		parameters.cp_tdy = options->tile_height;
	}
	opj_codec_t *codec = opj_create_compress(options->format ? OPJ_CODEC_J2K : OPJ_CODEC_JP2);
	opj_stream_t *stream = opj_stream_default_create(OPJ_FALSE);
	cj_j2k_stream memory = {0};
	memory.result = &result->base;
	int success = 0;
	result->image = opj_image_create(result->base.components, options->components, options->color);
	if (!codec || !stream || !result->image) { cj_fail(&result->base, "encoder allocation failed"); goto cleanup; }
	result->image->x1 = result->base.width;
	result->image->y1 = result->base.height;
	size_t offset = 0;
	for (uint32_t index = 0; index < result->base.components; ++index) {
		opj_image_comp_t *component = &result->image->comps[index];
		size_t count = (size_t)component->w * component->h;
		memcpy(component->data, samples + offset, count * sizeof(*samples));
		component->alpha = options->alpha[index];
		offset += count;
	}
	if (icc_size) {
		result->image->icc_profile_buf = malloc(icc_size);
		if (!result->image->icc_profile_buf) { cj_fail(&result->base, "ICC allocation failed"); goto cleanup; }
		memcpy(result->image->icc_profile_buf, icc, icc_size);
		result->image->icc_profile_len = icc_size;
	}
	opj_set_error_handler(codec, cj_j2k_error, &result->base);
	opj_stream_set_user_data(stream, &memory, NULL);
	opj_stream_set_write_function(stream, cj_j2k_write);
	opj_stream_set_skip_function(stream, cj_j2k_skip);
	opj_stream_set_seek_function(stream, cj_j2k_seek);
	if (!opj_setup_encoder(codec, &parameters, result->image) || !opj_codec_set_threads(codec, options->threads)) goto cleanup;
	if (!opj_start_compress(codec, result->image, stream) || !opj_encode(codec, stream) || !opj_end_compress(codec, stream)) goto cleanup;
	result->base.data = memory.output;
	result->base.size = memory.size;
	memory.output = NULL;
	success = 1;
cleanup:
	free(memory.output);
	if (!success && !result->base.error[0]) cj_fail(&result->base, "JPEG 2000 encoding failed");
	if (stream) opj_stream_destroy(stream);
	if (codec) opj_destroy_codec(codec);
	return success;
}
