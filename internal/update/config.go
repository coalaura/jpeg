package main

const turboConfig = `#pragma once
#define JPEG_LIB_VERSION 62
#define LIBJPEG_TURBO_VERSION 3.2.1
#define LIBJPEG_TURBO_VERSION_NUMBER 3002001
#define C_ARITH_CODING_SUPPORTED 1
#define D_ARITH_CODING_SUPPORTED 1
#define MEM_SRCDST_SUPPORTED 1
#define WITH_SIMD 1
#ifndef BITS_IN_JSAMPLE
#define BITS_IN_JSAMPLE 8
#endif
`

const turboInternalConfig = `#define BUILD "coalaura"
#define HIDDEN __attribute__((visibility("hidden")))
#define INLINE __inline__ __attribute__((always_inline))
#define THREAD_LOCAL __thread
#define PACKAGE_NAME "libjpeg-turbo"
#define VERSION "3.2.1"
#define SIZEOF_SIZE_T 8
#ifndef _WIN32
#define HAVE_BUILTIN_CTZL
#endif
#define FALLTHROUGH __attribute__((fallthrough));
#ifndef BITS_IN_JSAMPLE
#define BITS_IN_JSAMPLE 8
#endif
#undef C_ARITH_CODING_SUPPORTED
#undef D_ARITH_CODING_SUPPORTED
#undef WITH_SIMD
#if BITS_IN_JSAMPLE == 8
#define C_ARITH_CODING_SUPPORTED 1
#define D_ARITH_CODING_SUPPORTED 1
#define WITH_SIMD 1
#define SIMD_ARCHITECTURE ARM64
#if defined(__x86_64__) || defined(_M_X64)
#define WITH_SIMDE 1
#endif
#endif
`

const openjpegConfig = `#define OPJ_PACKAGE_VERSION "2.5.4"
#define _FILE_OFFSET_BITS 64
#ifdef _WIN32
#define OPJ_HAVE_MALLOC_H
#define OPJ_HAVE__ALIGNED_MALLOC
#define MUTEX_win32 1
#else
#define OPJ_HAVE_FSEEKO 1
#define OPJ_HAVE_POSIX_MEMALIGN
#define MUTEX_pthread 1
#endif
`

const highwayMinGWConfig = `// MinGW GCC can misalign stack temporaries for wide vector arguments passed
// by value (GCC PR 54412, reproduced with GCC 15.2). libjxl passes such
// arguments outside Highway's VecArg wrappers. Retain SSE dispatch, but
// exclude AVX2 and wider targets for this compiler/ABI combination.
#if HWY_ARCH_X86_64 && HWY_OS_WIN && HWY_COMPILER_GCC_ACTUAL
#define HWY_BROKEN_MINGW (HWY_AVX2 | (HWY_AVX2 - 1))
#else
#define HWY_BROKEN_MINGW 0
#endif

`

const jxlFastLosslessMinGWConfig = `// This encoder has its own SIMD dispatch, independent of Highway. Avoid
// MinGW GCC's misaligned wide-vector stack stores here too (GCC PR 54412).
#if FJXL_ARCH_IS_X86_64 && defined(_WIN32) && defined(__GNUC__) && !defined(__clang__)
#define FJXL_ENABLE_AVX2 0
#define FJXL_ENABLE_AVX512 0
#endif

`

const openjpegCoefficientCheck = `		/* Tier 1 reserves six fractional bits and a sign bit. Reject coefficients
		 * that would be truncated by its left shift, before emitting tile data. */
		for (OPJ_UINT32 component = 0; component < p_tcd->image->numcomps; ++component) {
			if (p_tcd->tcp->tccps[component].qmfbid != 1) continue;
			opj_tcd_tilecomp_t *tile = &p_tcd->tcd_image->tiles->comps[component];
			size_t count = (size_t)(tile->x1 - tile->x0) * (tile->y1 - tile->y0);
			for (size_t sample = 0; sample < count; ++sample) {
				if (tile->data[sample] < -33554431 || tile->data[sample] > 33554431) {
					opj_event_msg(p_manager, EVT_ERROR, "Reversible coefficients exceed the exact 25-bit magnitude limit\n");
					return OPJ_FALSE;
				}
			}
		}

`

const openjpegHeaderConfig = `
/* Preflight the decoded layout without allocating samples or changing decoder
 * mapping state. Only header-only callers apply the projected layout. */
OPJ_BOOL cj_jp2_header_config(opj_codec_t *codec, opj_image_t *image, OPJ_BOOL header_only, OPJ_UINT32 max_components, OPJ_UINT64 max_bytes) {
	opj_codec_private_t *private_codec = (opj_codec_private_t *)codec;
	opj_jp2_t *jp2 = (opj_jp2_t *)private_codec->m_codec;
	opj_event_mgr_t *manager = &private_codec->m_event_mgr;
	opj_jp2_color_t color = jp2->color;
	opj_jp2_pclr_t palette_copy;
	opj_jp2_cmap_comp_t mappings[256];
	opj_jp2_pclr_t *palette = color.jp2_pclr;
	if (palette && palette->cmap) {
		/* NC is an eight-bit field. The validator may repair cmap in place. */
		palette_copy = *palette;
		memcpy(mappings, palette->cmap, palette->nr_channels * sizeof(*mappings));
		palette_copy.cmap = mappings;
		color.jp2_pclr = palette = &palette_copy;
	}
	if (!opj_jp2_check_color(image, &color, manager)) return OPJ_FALSE;
	OPJ_UINT32 channels = palette && palette->cmap ? palette->nr_channels : image->numcomps;
	if (!channels || channels > max_components) {
		opj_event_msg(manager, EVT_ERROR, "resource limit exceeded");
		return OPJ_FALSE;
	}
	OPJ_UINT64 remaining = max_bytes;
	for (OPJ_UINT32 channel = 0; channel < channels; ++channel) {
		OPJ_UINT32 source = palette && palette->cmap ? palette->cmap[channel].cmp : channel;
		const opj_image_comp_t *component = &image->comps[source];
		OPJ_UINT64 count = (OPJ_UINT64)component->w * component->h;
		if (count > remaining / sizeof(OPJ_INT32)) {
			opj_event_msg(manager, EVT_ERROR, "resource limit exceeded");
			return OPJ_FALSE;
		}
		remaining -= count * sizeof(OPJ_INT32);
	}
	if (!header_only) return OPJ_TRUE;
	if (palette && palette->cmap) {
		opj_image_comp_t *components = opj_calloc(palette->nr_channels, sizeof(*components));
		if (!components) return OPJ_FALSE;
		for (OPJ_UINT32 channel = 0; channel < palette->nr_channels; ++channel) {
			opj_jp2_cmap_comp_t *mapping = &palette->cmap[channel];
			components[channel] = image->comps[mapping->cmp];
			components[channel].data = NULL;
			components[channel].prec = palette->channel_size[channel];
			components[channel].sgnd = palette->channel_sign[channel];
		}
		opj_free(image->comps);
		image->comps = components;
		image->numcomps = palette->nr_channels;
	}
	if (jp2->color.jp2_cdef) opj_jp2_apply_cdef(image, &jp2->color, manager);
	return OPJ_TRUE;
}
`
