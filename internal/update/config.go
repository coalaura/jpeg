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
