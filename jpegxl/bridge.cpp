#include "bridge.h"
#include <jxl/decode.h>
#include <jxl/encode.h>
#include <jxl/thread_parallel_runner.h>
#include <atomic>
#include <exception>
#include <memory>
#include <vector>
#include <new>
#include <cstddef>

struct cj_xl_budget {
	std::atomic<size_t> used{0};
	size_t limit;
	explicit cj_xl_budget(size_t maximum) : limit(maximum) {}
};

struct alignas(std::max_align_t) cj_xl_allocation {
	size_t size;
};

static void *cj_xl_alloc(void *opaque, size_t bytes) {
	auto *budget = static_cast<cj_xl_budget *>(opaque);
	if (bytes > budget->limit - sizeof(cj_xl_allocation)) return nullptr;
	size_t total = bytes + sizeof(cj_xl_allocation);
	size_t previous = budget->used.fetch_add(total);
	if (previous > budget->limit - total) { budget->used.fetch_sub(total); return nullptr; }
	auto *allocation = static_cast<cj_xl_allocation *>(malloc(total));
	if (!allocation) { budget->used.fetch_sub(total); return nullptr; }
	allocation->size = total;
	return allocation + 1;
}

static void cj_xl_dealloc(void *opaque, void *address) {
	if (!address) return;
	auto *budget = static_cast<cj_xl_budget *>(opaque);
	auto *allocation = static_cast<cj_xl_allocation *>(address) - 1;
	budget->used.fetch_sub(allocation->size);
	free(allocation);
}

struct cj_xl_encoder {
	cj_xl_budget budget;
	JxlMemoryManager manager;
	JxlEncoder *encoder = nullptr;
	void *runner = nullptr;
	JxlEncoderFrameSettings *settings = nullptr;
	JxlPixelFormat format{};
	cj_xl_options options;
	explicit cj_xl_encoder(const cj_xl_options &source) : budget(source.max_memory_bytes), manager{&budget, cj_xl_alloc, cj_xl_dealloc}, options(source) {
		encoder = JxlEncoderCreate(&manager);
		try { runner = JxlThreadParallelRunnerCreate(&manager, source.threads); }
		catch (...) { JxlEncoderDestroy(encoder); throw; }
	}
	~cj_xl_encoder() {
		JxlEncoderDestroy(encoder);
		JxlThreadParallelRunnerDestroy(runner);
	}
};

struct cj_xl_decoder {
	cj_xl_budget budget;
	JxlMemoryManager manager;
	JxlDecoder *decoder = nullptr;
	void *runner = nullptr;
	explicit cj_xl_decoder(const cj_xl_options &options) : budget(options.max_memory_bytes), manager{&budget, cj_xl_alloc, cj_xl_dealloc} {
		decoder = JxlDecoderCreate(&manager);
		try { runner = JxlThreadParallelRunnerCreate(&manager, options.threads); }
		catch (...) { JxlDecoderDestroy(decoder); throw; }
	}
	~cj_xl_decoder() {
		JxlDecoderDestroy(decoder);
		JxlThreadParallelRunnerDestroy(runner);
	}
};

#define ENC(call) do { if ((call) != JXL_ENC_SUCCESS) return cj_fail(result, "libjxl rejected encoder input or exhausted memory"); } while (0)
#define DEC(call) do { if ((call) != JXL_DEC_SUCCESS) return cj_fail(&result->base, "libjxl rejected decoder input or exhausted memory"); } while (0)
#define CATCH_RESULT catch (const std::exception &error) { return cj_fail(result, error.what()); } catch (...) { return cj_fail(result, "unknown native exception"); }

static int cj_xl_settings(cj_xl_encoder *state, cj_result *result) {
	const auto &options = state->options;
	ENC(JxlEncoderSetParallelRunner(state->encoder, JxlThreadParallelRunner, state->runner));
	ENC(JxlEncoderUseContainer(state->encoder, options.container));
	if (options.level) ENC(JxlEncoderSetCodestreamLevel(state->encoder, options.level));
	state->settings = JxlEncoderFrameSettingsCreate(state->encoder, nullptr);
	if (!state->settings) return cj_fail(result, "frame settings allocation failed");
	ENC(JxlEncoderFrameSettingsSetOption(state->settings, JXL_ENC_FRAME_SETTING_EFFORT, options.effort));
	ENC(JxlEncoderFrameSettingsSetOption(state->settings, JXL_ENC_FRAME_SETTING_DECODING_SPEED, options.decoding_speed));
	ENC(JxlEncoderSetFrameDistance(state->settings, options.distance));
	ENC(JxlEncoderSetFrameLossless(state->settings, options.lossless));
	// Invisible RGB samples are part of exact lossless pixel preservation.
	if (options.lossless) ENC(JxlEncoderFrameSettingsSetOption(state->settings, JXL_ENC_FRAME_SETTING_KEEP_INVISIBLE, 1));
	for (int index = 0; index < options.setting_count; ++index) {
		ENC(JxlEncoderFrameSettingsSetOption(state->settings, static_cast<JxlEncoderFrameSettingId>(options.setting_ids[index]), options.setting_values[index]));
	}
	return 1;
}

cj_xl_encoder *cj_xl_encoder_create(uint32_t width, uint32_t height, const cj_xl_options *options, const unsigned char *icc, size_t icc_size, cj_result *result) {
	try {
		auto state = std::make_unique<cj_xl_encoder>(*options);
		if (!state->encoder || !state->runner || !cj_xl_settings(state.get(), result)) return nullptr;
		JxlBasicInfo info;
		JxlEncoderInitBasicInfo(&info);
		info.xsize = width;
		info.ysize = height;
		info.bits_per_sample = options->bits;
		info.exponent_bits_per_sample = options->exponent_bits;
		info.num_color_channels = options->channels < 3 ? 1 : 3;
		info.num_extra_channels = options->channels % 2 == 0 ? 1 : 0;
		info.alpha_bits = info.num_extra_channels ? options->bits : 0;
		info.alpha_exponent_bits = info.num_extra_channels ? options->exponent_bits : 0;
		info.alpha_premultiplied = options->premultiplied;
		info.uses_original_profile = options->lossless;
		info.orientation = static_cast<JxlOrientation>(options->orientation);
		info.intensity_target = options->intensity_target;
		info.have_animation = options->have_animation;
		info.animation.tps_numerator = options->tps_numerator;
		info.animation.tps_denominator = options->tps_denominator;
		info.animation.num_loops = options->loops;
		info.animation.have_timecodes = options->have_animation;
		if (JxlEncoderSetBasicInfo(state->encoder, &info) != JXL_ENC_SUCCESS) { cj_fail(result, "invalid basic image info"); return nullptr; }
		if (icc_size) {
			if (JxlEncoderSetICCProfile(state->encoder, icc, icc_size) != JXL_ENC_SUCCESS) { cj_fail(result, "invalid ICC profile"); return nullptr; }
		} else {
			JxlColorEncoding color;
			if (options->linear) JxlColorEncodingSetToLinearSRGB(&color, info.num_color_channels == 1);
			else JxlColorEncodingSetToSRGB(&color, info.num_color_channels == 1);
			if (JxlEncoderSetColorEncoding(state->encoder, &color) != JXL_ENC_SUCCESS) { cj_fail(result, "invalid color encoding"); return nullptr; }
		}
		if (info.num_extra_channels && JxlEncoderSetExtraChannelDistance(state->settings, 0, options->alpha_distance) != JXL_ENC_SUCCESS) { cj_fail(result, "invalid alpha distance"); return nullptr; }
		state->format = {static_cast<uint32_t>(options->channels), options->exponent_bits ? JXL_TYPE_FLOAT : (options->bits <= 8 ? JXL_TYPE_UINT8 : JXL_TYPE_UINT16), JXL_NATIVE_ENDIAN, 0};
		if (!options->exponent_bits) {
			JxlBitDepth depth{JXL_BIT_DEPTH_FROM_CODESTREAM, 0, 0};
			if (JxlEncoderSetFrameBitDepth(state->settings, &depth) != JXL_ENC_SUCCESS) { cj_fail(result, "invalid sample precision"); return nullptr; }
		}
		if (options->container && JxlEncoderUseBoxes(state->encoder) != JXL_ENC_SUCCESS) { cj_fail(result, "metadata initialization failed"); return nullptr; }
		return state.release();
	} catch (const std::exception &error) { cj_fail(result, error.what()); return nullptr; }
	catch (...) { cj_fail(result, "unknown native exception"); return nullptr; }
}

int cj_xl_encoder_frame(cj_xl_encoder *state, const void *pixels, size_t size, uint32_t duration, uint32_t timecode, const char *name, cj_result *result) {
	try {
		JxlFrameHeader header;
		JxlEncoderInitFrameHeader(&header);
		header.duration = duration;
		header.timecode = timecode;
		ENC(JxlEncoderSetFrameHeader(state->settings, &header));
		if (name) ENC(JxlEncoderSetFrameName(state->settings, name));
		std::vector<unsigned char> bytes;
		if (state->format.data_type == JXL_TYPE_UINT8) {
			// libjxl's lossy input path requires byte storage for <=8-bit samples.
			auto *samples = static_cast<const uint16_t *>(pixels);
			bytes.resize(size / 2);
			for (size_t index = 0; index < bytes.size(); ++index) bytes[index] = samples[index];
			pixels = bytes.data();
			size = bytes.size();
		}
		ENC(JxlEncoderAddImageFrame(state->settings, &state->format, pixels, size));
		return 1;
	} CATCH_RESULT
}

int cj_xl_encoder_box(cj_xl_encoder *state, const char *type, const unsigned char *data, size_t size, int compress, cj_result *result) {
	try { ENC(JxlEncoderAddBox(state->encoder, type, data, size, compress)); return 1; } CATCH_RESULT
}

int cj_xl_encoder_finish(cj_xl_encoder *state, cj_result *result) {
	try {
		JxlEncoderCloseInput(state->encoder);
		size_t capacity = 65536;
		result->data = static_cast<unsigned char *>(malloc(capacity));
		if (!result->data) return cj_fail(result, "output allocation failed");
		for (;;) {
			unsigned char *next = result->data + result->size;
			size_t available = capacity - result->size;
			auto status = JxlEncoderProcessOutput(state->encoder, &next, &available);
			result->size = capacity - available;
			if (status == JXL_ENC_SUCCESS) return 1;
			if (status != JXL_ENC_NEED_MORE_OUTPUT) return cj_fail(result, "JPEG XL encoding failed");
			if (capacity >= (512u << 20)) return cj_fail(result, "encoded output limit exceeded");
			capacity *= 2;
			auto *output = static_cast<unsigned char *>(realloc(result->data, capacity));
			if (!output) return cj_fail(result, "output allocation failed");
			result->data = output;
		}
	} CATCH_RESULT
}

void cj_xl_encoder_destroy(cj_xl_encoder *state) { delete state; }

void cj_xl_free(cj_xl_result *result) {
	for (int index = 0; index < result->frame_count; ++index) {
		free(result->frames[index].pixels);
		free(result->frames[index].name);
	}
	free(result->frames);
	free(result->icc);
	free(result->original_icc);
	cj_free(&result->base);
}

static int cj_xl_profile(JxlDecoder *decoder, JxlColorProfileTarget target, unsigned char **data, size_t *size, cj_result *result) {
	if (JxlDecoderGetICCProfileSize(decoder, target, size) != JXL_DEC_SUCCESS) return cj_fail(result, "ICC profile is unavailable");
	if (*size > (4u << 20)) return cj_fail(result, "ICC profile limit exceeded");
	*data = static_cast<unsigned char *>(malloc(*size));
	if (!*data) return cj_fail(result, "ICC allocation failed");
	if (JxlDecoderGetColorAsICCProfile(decoder, target, *data, *size) != JXL_DEC_SUCCESS) return cj_fail(result, "ICC extraction failed");
	return 1;
}

int cj_xl_decode(const unsigned char *data, size_t size, int mode, const cj_xl_options *options, cj_xl_result *result) {
	try {
		cj_xl_decoder state(*options);
		auto *decoder = state.decoder;
		if (!decoder || !state.runner) return cj_fail(&result->base, "decoder allocation failed");
		DEC(JxlDecoderSetParallelRunner(decoder, JxlThreadParallelRunner, state.runner));
		DEC(JxlDecoderSetKeepOrientation(decoder, options->keep_orientation));
		DEC(JxlDecoderSetUnpremultiplyAlpha(decoder, options->unpremultiply));
		DEC(JxlDecoderSetRenderSpotcolors(decoder, options->render_spotcolors));
		DEC(JxlDecoderSetDecompressBoxes(decoder, JXL_TRUE));
		if (options->intensity_target > 0) DEC(JxlDecoderSetDesiredIntensityTarget(decoder, options->intensity_target));
		int events = JXL_DEC_BASIC_INFO | JXL_DEC_COLOR_ENCODING;
		if (mode != 1) events |= JXL_DEC_FRAME | JXL_DEC_FULL_IMAGE;
		if (options->read_boxes) events |= JXL_DEC_BOX | JXL_DEC_BOX_COMPLETE;
		DEC(JxlDecoderSubscribeEvents(decoder, events));
		DEC(JxlDecoderSetInput(decoder, data, size));
		JxlDecoderCloseInput(decoder);
		JxlBasicInfo info{};
		JxlPixelFormat format{};
		size_t decoded_bytes = 0;
		std::vector<unsigned char> metadata;
		unsigned char box_buffer[65536];
		bool box_active = false;
		size_t box_start = 0;
		for (;;) {
			auto status = JxlDecoderProcessInput(decoder);
			if (status == JXL_DEC_ERROR || status == JXL_DEC_NEED_MORE_INPUT) return cj_fail(&result->base, "invalid or truncated JPEG XL stream");
			if (status == JXL_DEC_BASIC_INFO) {
				DEC(JxlDecoderGetBasicInfo(decoder, &info));
				result->base.width = info.xsize;
				result->base.height = info.ysize;
				result->base.components = info.num_color_channels + (info.alpha_bits != 0);
				result->base.bits = info.bits_per_sample;
				if (!cj_check(&result->base, options->max_pixels)) return 0;
				if (info.num_extra_channels > 16) return cj_fail(&result->base, "extra channel limit exceeded");
				for (uint32_t index = 0; index < info.num_extra_channels; ++index) {
					JxlExtraChannelInfo channel;
					DEC(JxlDecoderGetExtraChannelInfo(decoder, index, &channel));
					if (channel.type == JXL_CHANNEL_ALPHA && index == 0) continue;
					if (channel.type == JXL_CHANNEL_SPOT_COLOR && options->render_spotcolors) continue;
					return cj_fail(&result->base, "extra channels require explicit spot rendering or an unsupported native channel API");
				}
				if ((!info.exponent_bits_per_sample && info.bits_per_sample > 16) || (info.alpha_bits && (info.alpha_bits != info.bits_per_sample || info.alpha_exponent_bits != info.exponent_bits_per_sample))) return cj_fail(&result->base, "unsupported mixed channel precision");
				result->info.bits = info.bits_per_sample;
				result->info.exponent_bits = info.exponent_bits_per_sample;
				result->info.channels = result->base.components;
				result->info.orientation = info.orientation;
				result->info.premultiplied = info.alpha_premultiplied && !options->unpremultiply;
				result->info.have_animation = info.have_animation;
				result->info.tps_numerator = info.animation.tps_numerator;
				result->info.tps_denominator = info.animation.tps_denominator;
				result->info.loops = info.animation.num_loops;
				result->info.intensity_target = info.intensity_target;
				format = {result->base.components, info.exponent_bits_per_sample ? JXL_TYPE_FLOAT : JXL_TYPE_UINT16, JXL_NATIVE_ENDIAN, 0};
				if (mode != 1) {
					result->frames = static_cast<cj_xl_frame *>(calloc(options->max_frames, sizeof(cj_xl_frame)));
					if (!result->frames) return cj_fail(&result->base, "frame allocation failed");
				}
			} else if (status == JXL_DEC_COLOR_ENCODING) {
				if (!cj_xl_profile(decoder, JXL_COLOR_PROFILE_TARGET_ORIGINAL, &result->original_icc, &result->original_icc_size, &result->base)) return 0;
				if (!cj_xl_profile(decoder, JXL_COLOR_PROFILE_TARGET_DATA, &result->icc, &result->icc_size, &result->base)) return 0;
				if (mode == 1) return 1;
			} else if (status == JXL_DEC_FRAME) {
				if (result->frame_count >= options->max_frames) return cj_fail(&result->base, "frame limit exceeded");
				JxlFrameHeader header;
				DEC(JxlDecoderGetFrameHeader(decoder, &header));
				auto &frame = result->frames[result->frame_count++];
				frame.duration = header.duration;
				frame.timecode = header.timecode;
				if (header.name_length > 65535) return cj_fail(&result->base, "frame name limit exceeded");
				frame.name = static_cast<char *>(malloc(header.name_length + 1));
				if (!frame.name) return cj_fail(&result->base, "frame name allocation failed");
				DEC(JxlDecoderGetFrameName(decoder, frame.name, header.name_length + 1));
			} else if (status == JXL_DEC_NEED_IMAGE_OUT_BUFFER) {
				if (!result->frame_count) return cj_fail(&result->base, "missing frame header");
				auto &frame = result->frames[result->frame_count-1];
				DEC(JxlDecoderImageOutBufferSize(decoder, &format, &frame.size));
				if (frame.size > options->max_decoded_bytes - decoded_bytes) return cj_fail(&result->base, "decoded allocation limit exceeded");
				decoded_bytes += frame.size;
				frame.pixels = malloc(frame.size);
				if (!frame.pixels) return cj_fail(&result->base, "sample allocation failed");
				DEC(JxlDecoderSetImageOutBuffer(decoder, &format, frame.pixels, frame.size));
				if (!info.exponent_bits_per_sample) {
					JxlBitDepth depth{JXL_BIT_DEPTH_FROM_CODESTREAM, 0, 0};
					DEC(JxlDecoderSetImageOutBitDepth(decoder, &depth));
				}
			} else if (status == JXL_DEC_FULL_IMAGE && mode == 2) {
				return 1;
			} else if (status == JXL_DEC_BOX) {
				JxlBoxType type;
				DEC(JxlDecoderGetBoxType(decoder, type, JXL_TRUE));
				box_active = memcmp(type, "Exif", 4) == 0 || memcmp(type, "xml ", 4) == 0 || memcmp(type, "jumb", 4) == 0;
				if (box_active) {
					box_start = metadata.size();
					metadata.insert(metadata.end(), type, type+4);
					metadata.resize(metadata.size()+4);
					DEC(JxlDecoderSetBoxBuffer(decoder, box_buffer, sizeof(box_buffer)));
				}
			} else if ((status == JXL_DEC_BOX_NEED_MORE_OUTPUT || status == JXL_DEC_BOX_COMPLETE || status == JXL_DEC_SUCCESS) && box_active) {
				size_t used = sizeof(box_buffer) - JxlDecoderReleaseBoxBuffer(decoder);
				if (metadata.size() + used > (4u << 20)) return cj_fail(&result->base, "metadata limit exceeded");
				metadata.insert(metadata.end(), box_buffer, box_buffer+used);
				if (status == JXL_DEC_BOX_NEED_MORE_OUTPUT) {
					DEC(JxlDecoderSetBoxBuffer(decoder, box_buffer, sizeof(box_buffer)));
				} else {
					uint32_t length = metadata.size() - box_start - 8;
					for (int index = 0; index < 4; ++index) metadata[box_start+4+index] = length >> (24-index*8);
					box_active = false;
				}
			}
			if (status == JXL_DEC_SUCCESS) {
				if (!result->frame_count) return cj_fail(&result->base, "missing image frame");
				if (!metadata.empty()) {
					result->base.data = static_cast<unsigned char *>(malloc(metadata.size()));
					if (!result->base.data) return cj_fail(&result->base, "metadata allocation failed");
					memcpy(result->base.data, metadata.data(), metadata.size());
					result->base.size = metadata.size();
				}
				return 1;
			}
		}
	} catch (const std::exception &error) { return cj_fail(&result->base, error.what()); }
	catch (...) { return cj_fail(&result->base, "unknown native exception"); }
}

int cj_xl_recompress(const unsigned char *data, size_t size, const cj_xl_options *options, cj_result *result) {
	try {
		cj_xl_encoder state(*options);
		if (!state.encoder || !state.runner || !cj_xl_settings(&state, result)) return cj_fail(result, "encoder initialization failed");
		ENC(JxlEncoderStoreJPEGMetadata(state.encoder, JXL_TRUE));
		ENC(JxlEncoderAddJPEGFrame(state.settings, data, size));
		return cj_xl_encoder_finish(&state, result);
	} CATCH_RESULT
}

int cj_xl_restore(const unsigned char *data, size_t size, const cj_xl_options *options, cj_result *result) {
	try {
		cj_xl_decoder state(*options);
		auto *decoder = state.decoder;
		if (!decoder || !state.runner) return cj_fail(result, "decoder initialization failed");
		if (JxlDecoderSetParallelRunner(decoder, JxlThreadParallelRunner, state.runner) != JXL_DEC_SUCCESS || JxlDecoderSubscribeEvents(decoder, JXL_DEC_BASIC_INFO | JXL_DEC_JPEG_RECONSTRUCTION | JXL_DEC_FULL_IMAGE) != JXL_DEC_SUCCESS || JxlDecoderSetInput(decoder, data, size) != JXL_DEC_SUCCESS) return cj_fail(result, "decoder initialization failed");
		JxlDecoderCloseInput(decoder);
		size_t capacity = 65536;
		bool reconstructing = false;
		for (;;) {
			auto status = JxlDecoderProcessInput(decoder);
			if (status == JXL_DEC_BASIC_INFO) {
				JxlBasicInfo info;
				if (JxlDecoderGetBasicInfo(decoder, &info) != JXL_DEC_SUCCESS) return cj_fail(result, "invalid image header");
				result->width = info.xsize; result->height = info.ysize; result->components = info.num_color_channels;
				if (!cj_check(result, options->max_pixels)) return 0;
			} else if (status == JXL_DEC_JPEG_RECONSTRUCTION) {
				result->data = static_cast<unsigned char *>(malloc(capacity));
				if (!result->data) return cj_fail(result, "output allocation failed");
				if (JxlDecoderSetJPEGBuffer(decoder, result->data, capacity) != JXL_DEC_SUCCESS) return cj_fail(result, "JPEG buffer failed");
				reconstructing = true;
			} else if (status == JXL_DEC_JPEG_NEED_MORE_OUTPUT) {
				result->size = capacity - JxlDecoderReleaseJPEGBuffer(decoder);
				if (capacity >= (512u << 20)) return cj_fail(result, "JPEG output limit exceeded");
				capacity *= 2;
				auto *output = static_cast<unsigned char *>(realloc(result->data, capacity));
				if (!output) return cj_fail(result, "output allocation failed");
				result->data = output;
				if (JxlDecoderSetJPEGBuffer(decoder, output + result->size, capacity - result->size) != JXL_DEC_SUCCESS) return cj_fail(result, "JPEG buffer failed");
			} else if (status == JXL_DEC_SUCCESS) {
				if (!reconstructing) return cj_fail(result, "stream has no JPEG reconstruction data");
				result->size = capacity - JxlDecoderReleaseJPEGBuffer(decoder);
				return 1;
			} else if (status == JXL_DEC_ERROR || status == JXL_DEC_NEED_MORE_INPUT || status == JXL_DEC_NEED_IMAGE_OUT_BUFFER) {
				return cj_fail(result, "invalid stream or missing JPEG reconstruction data");
			}
		}
	} CATCH_RESULT
}
