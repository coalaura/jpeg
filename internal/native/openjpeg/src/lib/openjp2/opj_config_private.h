#define OPJ_PACKAGE_VERSION "2.5.4"
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
