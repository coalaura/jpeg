package jpegls

/*
#cgo CPPFLAGS: -DCHARLS_STATIC -DCHARLS_LIBRARY_BUILD -I${SRCDIR}/../internal/native/charls/include
#cgo CXXFLAGS: -O3 -std=c++17
#cgo linux LDFLAGS: -lstdc++ -lm
#cgo darwin LDFLAGS: -lc++ -lm
#cgo windows LDFLAGS: -lstdc++
*/
import "C"
