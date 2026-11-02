# jpeg

Self-contained Go codecs for classic JPEG (baseline, progressive, 2..16-bit predictive lossless), JPEG 2000 Part 1 (JP2/J2K), JPEG-LS, and JPEG XL (raw/container, animation, JPEG reconstruction). Targets: Linux, Windows, and Darwin on amd64 and arm64.

```sh
go get github.com/coalaura/jpeg
```

Requires Go 1.27.3+, `CGO_ENABLED=1`, a configured C/C++17 toolchain, and the target SDK. Native sources and dependencies are bundled; consumer builds need no network, generator, CMake, pkg-config, NASM, or installed codec libraries. Import `classic`, `jpeg2000`, `jpegls`, or `jpegxl` independently to compile only that codec. `detect` is Go-only.

```go
import "github.com/coalaura/jpeg"

img, err := jpeg.Decode(reader)
```

```go
import (
	"github.com/coalaura/jpeg"
	"github.com/coalaura/jpeg/jpegxl"
)

err := jpeg.Encode(writer, img, &jpeg.Options{
	Format: jpeg.JXLContainer,
	JPEGXL: &jpegxl.Options{Lossless: true, BitsPerSample: 16},
})
```

Nil encoding options write ordinary JPEG. Root decoding dispatches explicitly, including when `image/jpeg` is imported. See package Go documentation for defaults, limits, precision, alpha/color interpretation, native sample APIs, animation timing, metadata, and JPEG reconstruction. Profiles are preserved but not implicitly applied; signed/HDR and ambiguous component layouts require explicit interpretation. Input is buffered within configurable limits; `DecodeConfig` parses headers without reconstructing pixels.

## Maintainers

```sh
go generate ./...
go run ./internal/fixtures
go test ./...
vet --cgo
go run ./internal/verify
```

Regeneration requires Git and initial network access; exact checkouts are cached in `.cache`. Pinned revisions and bundled notices are in `internal/native/REVISIONS` and each dependency directory. `internal/verify` creates a module zip and links offline root and individual-codec consumers on all six targets using builder/Zig; Darwin requires a locally configured SDK. Source SIMD uses libjpeg-turbo's experimental SIMDe path on amd64 and native NEON on arm64. This avoids an assembler dependency at a substantial amd64 encoding-throughput cost relative to upstream NASM.
