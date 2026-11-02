package jpegxl

/*
#cgo CPPFLAGS: -I${SRCDIR}/../internal/native/libjxl -I${SRCDIR}/../internal/native/libjxl/lib/include -I${SRCDIR}/../internal/native/highway -I${SRCDIR}/../internal/native/brotli/c/include -I${SRCDIR}/../internal/native/lcms/include -DJXL_STATIC_DEFINE -DJXL_THREADS_STATIC_DEFINE -DJXL_CMS_STATIC_DEFINE -DJXL_INTERNAL_LIBRARY_BUILD -DHWY_STATIC_DEFINE -DJPEGXL_ENABLE_TRANSCODE_JPEG=1 -DJPEGXL_ENABLE_BOXES=1 -DNDEBUG
#cgo CFLAGS: -O3
#cgo CXXFLAGS: -O3 -std=c++17
#cgo linux LDFLAGS: -lstdc++ -lm -lpthread
#cgo darwin LDFLAGS: -lc++ -lm -lpthread
#cgo windows LDFLAGS: -lstdc++
*/
import "C"
