#include "bridge.h"
#include <charls/charls.h>

#define CHECK(call) do { status = (call); if (status) { cj_fail(result, charls_get_error_message(status)); goto cleanup; } } while (0)

int cj_ls_decode(const unsigned char *data, size_t size, int header_only, uint64_t max_pixels, cj_ls_options *options, cj_result *result) {
	charls_jpegls_decoder *decoder = charls_jpegls_decoder_create();
	charls_jpegls_errc status;
	charls_frame_info info;
	charls_interleave_mode interleave;
	unsigned char *buffer = NULL;
	size_t bytes = 0, count, plane;
	int success = 0;
	if (!decoder) return cj_fail(result, "decoder allocation failed");
	CHECK(charls_jpegls_decoder_set_source_buffer(decoder, data, size));
	CHECK(charls_jpegls_decoder_read_header(decoder));
	CHECK(charls_jpegls_decoder_get_frame_info(decoder, &info));
	result->width = info.width;
	result->height = info.height;
	result->bits = info.bits_per_sample;
	result->components = info.component_count;
	if (!cj_check(result, max_pixels)) goto cleanup;
	CHECK(charls_jpegls_decoder_get_interleave_mode(decoder, &interleave));
	options->interleave = interleave;
	for (uint32_t component = 0; component < result->components; ++component) {
		CHECK(charls_jpegls_decoder_get_near_lossless(decoder, component, &options->near_components[component]));
	}
	if (header_only) { success = 1; goto cleanup; }
	CHECK(charls_jpegls_decoder_get_destination_size(decoder, 0, &bytes));
	if (bytes > (512u << 20)) { cj_fail(result, "decoded buffer limit exceeded"); goto cleanup; }
	buffer = malloc(bytes);
	plane = (size_t)result->width * result->height;
	count = plane * result->components;
	result->samples = malloc(count * 2);
	if (!buffer || !result->samples) { cj_fail(result, "sample allocation failed"); goto cleanup; }
	CHECK(charls_jpegls_decoder_decode_to_buffer(decoder, buffer, bytes, 0));
	for (size_t index = 0; index < count; ++index) {
		size_t source = interleave == CHARLS_INTERLEAVE_MODE_NONE ? (index % result->components) * plane + index / result->components : index;
		result->samples[index] = result->bits > 8 ? ((uint16_t *)buffer)[source] : buffer[source];
	}
	success = 1;
cleanup:
	free(buffer);
	charls_jpegls_decoder_destroy(decoder);
	return success;
}

int cj_ls_encode(const uint16_t *samples, cj_ls_options *options, cj_result *result) {
	charls_jpegls_encoder *encoder = charls_jpegls_encoder_create();
	charls_jpegls_errc status;
	charls_frame_info info = {result->width, result->height, result->bits, result->components};
	charls_jpegls_pc_parameters preset;
	size_t plane = (size_t)result->width * result->height;
	size_t count = plane * result->components;
	size_t bytes = count * (result->bits > 8 ? 2 : 1);
	unsigned char *buffer = NULL;
	int success = 0;
	if (!encoder) return cj_fail(result, "encoder allocation failed");
	CHECK(charls_jpegls_encoder_set_frame_info(encoder, &info));
	CHECK(charls_jpegls_encoder_set_near_lossless(encoder, options->near_lossless));
	CHECK(charls_jpegls_encoder_set_interleave_mode(encoder, options->interleave));
	CHECK(charls_jpegls_encoder_set_color_transformation(encoder, options->transform));
	if (options->preset[0]) {
		preset.maximum_sample_value = options->preset[0];
		preset.threshold1 = options->preset[1];
		preset.threshold2 = options->preset[2];
		preset.threshold3 = options->preset[3];
		preset.reset_value = options->preset[4];
		CHECK(charls_jpegls_encoder_set_preset_coding_parameters(encoder, &preset));
	}
	CHECK(charls_jpegls_encoder_get_estimated_destination_size(encoder, &result->size));
	if (result->size > (512u << 20)) { cj_fail(result, "encoded buffer limit exceeded"); goto cleanup; }
	result->data = malloc(result->size);
	buffer = malloc(bytes);
	if (!buffer || !result->data) { cj_fail(result, "buffer allocation failed"); goto cleanup; }
	for (size_t index = 0; index < count; ++index) {
		size_t target = options->interleave == CHARLS_INTERLEAVE_MODE_NONE ? (index % result->components) * plane + index / result->components : index;
		if (result->bits > 8) ((uint16_t *)buffer)[target] = samples[index];
		else buffer[target] = samples[index];
	}
	CHECK(charls_jpegls_encoder_set_destination_buffer(encoder, result->data, result->size));
	CHECK(charls_jpegls_encoder_encode_from_buffer(encoder, buffer, bytes, 0));
	CHECK(charls_jpegls_encoder_get_bytes_written(encoder, &result->size));
	success = 1;
cleanup:
	free(buffer);
	charls_jpegls_encoder_destroy(encoder);
	return success;
}
