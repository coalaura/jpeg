# jpeg

Self-contained Go codecs for the JPEG family. Decode to `image.Image`, encode with format-specific options or work with native samples, metadata and animation through the individual codec packages.

| Package | Formats | Backend |
| --- | --- | --- |
| [`classic`](https://pkg.go.dev/github.com/coalaura/jpeg/classic) | Baseline, progressive and 2..16-bit predictive lossless JPEG | libjpeg-turbo |
| [`jpeg2000`](https://pkg.go.dev/github.com/coalaura/jpeg/jpeg2000) | JPEG 2000 Part 1: boxed JP2 and raw J2K/J2C | OpenJPEG |
| [`jpegls`](https://pkg.go.dev/github.com/coalaura/jpeg/jpegls) | Lossless and near-lossless JPEG-LS | CharLS |
| [`jpegxl`](https://pkg.go.dev/github.com/coalaura/jpeg/jpegxl) | JPEG XL codestreams and containers, animation, JPEG reconstruction | libjxl |

## Install

```sh
go get github.com/coalaura/jpeg
```

Requires Go 1.27.3 or later and **CGO with a working C/C++17 toolchain** and its normal platform headers/libraries. GCC/MinGW, Clang and Zig-based toolchains are suitable; macOS needs its platform SDK.

Native sources and dependencies are bundled. Once the module is downloaded, builds work offline without generation, CMake, pkg-config, NASM or installed codec libraries.

Import `github.com/coalaura/jpeg` for automatic format dispatch or import an individual codec package to compile only that backend. The [`detect`](https://pkg.go.dev/github.com/coalaura/jpeg/detect) package is Go-only and does not require CGO.

## Images

```go
import "github.com/coalaura/jpeg"

img, err := jpeg.Decode(reader)
if err != nil {
	return err
}
// img is an image.Image.
```

`Decode` accepts an `io.Reader`, including non-seekable streams. It detects the format directly and works reliably even when the standard library's `image/jpeg` is also imported; dispatch does not depend on global image registration order.

Use `DecodeConfig` to obtain dimensions and a color model without reconstructing pixels. `DecodeWithOptions` and `DecodeConfigWithOptions` accept codec-specific controls such as scaled decoding, decode regions, resource limits and explicit color interpretation. Compressed input is buffered within configurable limits.

Nil options write ordinary JPEG:

```go
err := jpeg.Encode(writer, img, nil)
```

Select another output format explicitly and supply its typed options:

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

Available output formats are `jpeg.JPEG`, `jpeg.JP2`, `jpeg.J2K`, `jpeg.JPEGLS`, `jpeg.JXLCodestream` and `jpeg.JXLContainer`. The root `Format` setting determines the container even when a codec's options specify otherwise.

Each codec also exposes its own `Encode`, `Decode` and `DecodeConfig` functions. For example, applications that only need JPEG-LS can import `github.com/coalaura/jpeg/jpegls` directly.

## Format detection

Inspect a stream without linking any native codecs:

```go
import "github.com/coalaura/jpeg/detect"

format, replay, err := detect.Detect(reader)
```

Pass `replay` to the decoder rather than reusing `reader`: it includes every byte consumed during detection, including on errors. Detection distinguishes boxed JP2 from raw J2K, both JXL representations and classic JPEG from JPEG-LS even when APP or COM segments precede the frame header. Header scanning is bounded; `DetectLimit` lets you set the limit.

## Native samples, metadata and animation

The individual codecs expose APIs for data that an `image.Image` cannot carry: `classic.DecodeSamples`, `jpegls.DecodeSamples`, `jpeg2000.DecodeNative` and `jpegxl.DecodeAnimation`. Use these to retain sample precision, component layouts and the metadata supported by each format. ICC profiles are not automatically applied to the pixels.

`jpegxl.DecodeAnimation` and `jpegxl.EncodeAnimation` expose full-canvas frames, frame durations in the file's time base and loop counts. `jpegxl.RecompressJPEG` stores eligible classic JPEGs losslessly in a JXL container; `jpegxl.RestoreJPEG` restores the original JPEG bytes.

## Coverage and limits

- **Lossless encoding:** Classic JPEG requires the explicit `Lossless` option; quality 100 still uses DCT coding. JPEG-LS uses `NEAR=0` for exact samples. JPEG 2000 defaults to reversible lossless JP2. JPEG XL requires `Lossless: true`; distance zero alone does not guarantee exact samples.
- **Sample precision:** Ordinary image encoding defaults to 8-bit samples. Set `BitsPerSample` when retaining higher precision. Native sample APIs support signed JPEG 2000 and floating-point JPEG XL data; display conversion requires explicit handling by the caller.
- **Alpha and color:** Classic JPEG encoding rejects transparency. Raw J2K needs explicit color interpretation and four-component JPEG-LS needs an explicit RGBA interpretation. Unsupported or ambiguous display conversions return errors rather than silently guessing or clipping values.
- **JPEG XL animation:** Encoding accepts full-canvas frames, rather than arbitrary layers or cropped-frame blending. General extra-channel preservation beyond alpha is not exposed.

Decoders default to a 64 MiB compressed-input limit and a 16M-pixel limit. Codec-specific options control resource limits and native threading where supported. Allocation limits are not a cap on total process memory: decoding also needs Go-owned pixels and native working storage.

## Platforms

Linux, Windows and macOS on amd64 and arm64, with portable CPU baselines. Classic JPEG uses source-based SIMDe on amd64 and native NEON on arm64. The amd64 path avoids an assembler dependency but has lower encoding throughput than upstream's NASM implementation.

See the [Go documentation](https://pkg.go.dev/github.com/coalaura/jpeg) and individual package references for option defaults, validation, resource limits and native sample representations.
