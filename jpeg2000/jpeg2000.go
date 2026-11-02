// Package jpeg2000 implements JPEG 2000 Part 1 with bundled OpenJPEG.
// NativeImage retains signed samples, component grids, color space and ICC data.
// Ordinary images use unsigned 8/16-bit paths. Profiles are retained, not applied;
// conversion from signed samples, CMYK, HDR, or subsampled YCC is caller-controlled.
package jpeg2000

/*
#include "bridge.h"
*/
import "C"

import (
	"bytes"
	"errors"
	"image"
	"io"
	"math"
	"math/bits"
	"slices"
	"unsafe"

	"github.com/coalaura/jpeg/internal/pixels"
)

const (
	JP2 Format = iota
	J2K
)

const (
	Unspecified ColorSpace = iota
	SRGB
	Gray
	SYCC
	EYCC
	CMYK
	ICCRGB // RGB channels whose primaries and transfer curves are defined by ICC.
)

const (
	LRCP Progression = iota
	RLCP
	RPCL
	PCRL
	CPRL
)

type Format int
type ColorSpace int
type Progression int

// Options defaults to reversible, exact lossless JP2. Explicit layer targets
// enable rate allocation; reversible wavelets alone do not imply losslessness.
// Ratios must decrease (last 1 means lossless), PSNR must increase (last 0 means
// lossless). Targets are approximate. Only one target list may be provided.
type Options struct {
	Format                  Format
	BitsPerSample           int       // Zero retains 16-bit Go images; otherwise selects 8.
	Irreversible            bool      // Use the lossy 9/7 wavelet rather than reversible 5/3.
	Ratios                  []float64 // Compression ratios >=1, at most 100 quality layers.
	PSNR                    []float64 // Decibels >=0, at most 100 quality layers.
	TileWidth               int       // Both tile dimensions must be specified together.
	TileHeight              int
	Resolutions             int // Zero selects min(6, floor(log2(smallest tile side))+1).
	Progression             Progression
	CodeBlockWidth          int // Zero selects 64; powers of two 4..1024, area <=4096.
	CodeBlockHeight         int
	MultiComponentTransform *bool  // Nil enables reversible/irreversible RGB transform for SRGB and ICCRGB.
	SOP                     bool   // Emit start-of-packet markers.
	EPH                     bool   // Emit end-of-packet-header markers.
	Threads                 int    // Zero selects 1, maximum 16.
	ICC                     []byte // JP2 only, at most 4 MiB; not color-converted.
}

type DecodeOptions struct {
	Reduce            int              // Discard this many highest-resolution levels, 0..30.
	Layers            int              // Zero decodes all quality layers.
	Region            *image.Rectangle // Full-resolution reference-grid coordinates.
	Threads           int              // Zero selects 1, maximum 16.
	RawColorSpace     ColorSpace       // Required for displaying raw codestreams lacking color metadata.
	RawAlphaComponent *int             // Explicit raw-codestream alpha interpretation; zero is meaningful.
	MaxPixels         uint64           // Zero selects 16 million; component buffers are also capped at 512 MiB.
	MaxInputBytes     int64            // Zero selects 64 MiB; maximum 512 MiB.
}

// Component contains planar native samples; Signed values are never reinterpreted
// as unsigned. Alpha is 0 for color, 1 for straight, or 2 for associated opacity.
type Component struct {
	Width         int
	Height        int
	X0            int
	Y0            int
	DX            int
	DY            int
	BitsPerSample int
	Signed        bool
	Alpha         int
	Values        []int32
}

type NativeImage struct {
	Bounds     image.Rectangle // Full-resolution reference grid, even with reduced decoding.
	ColorSpace ColorSpace
	Components []Component
	ICC        []byte
	Format     Format
}

// Image constructs a Go image without guessing color, resampling, clipping signed
// data or applying ICC profiles. Unsupported representations remain available in
// NativeImage. Associated alpha is rejected to avoid irreversible unpremultiplication.
func (native *NativeImage) Image() (image.Image, error) {
	return native.image(0)
}

func (native *NativeImage) image(maxPixels uint64) (image.Image, error) {
	width, height, precision, channels, alpha, err := displayLayout(native)
	if err != nil {
		return nil, err
	}

	err = pixels.Check(width, height, channels, maxPixels)
	if err != nil {
		return nil, err
	}

	for index := range native.Components {
		component := &native.Components[index]
		if len(component.Values) != width*height {
			return nil, errors.New("jpeg2000: invalid component sample count")
		}

		for _, value := range component.Values {
			if value < 0 || uint32(value) >= 1<<precision {
				return nil, errors.New("jpeg2000: sample exceeds component precision")
			}
		}
	}

	values := make([]uint16, width*height*channels)

	for index := 0; index < width*height; index++ {
		for channel := range channels {
			source := channel

			if channel == channels-1 && alpha >= 0 {
				source = alpha
			}

			values[index*channels+channel] = uint16(native.Components[source].Values[index])
		}
	}

	if channels == 2 {
		rgba := make([]uint16, width*height*4)

		for index := 0; index < width*height; index++ {
			value := values[index*2]

			rgba[index*4] = value
			rgba[index*4+1] = value
			rgba[index*4+2] = value
			rgba[index*4+3] = values[index*2+1]
		}

		return pixels.Image(rgba, width, height, 4, precision, maxPixels)
	}

	return pixels.Image(values, width, height, channels, precision, maxPixels)
}

func Decode(reader io.Reader) (image.Image, error) {
	return DecodeWithOptions(reader, nil)
}

func DecodeWithOptions(reader io.Reader, options *DecodeOptions) (image.Image, error) {
	native, err := DecodeNative(reader, options)
	if err != nil {
		return nil, err
	}

	maxPixels := uint64(0)

	if options != nil {
		maxPixels = options.MaxPixels
	}

	return native.image(maxPixels)
}

func DecodeConfig(reader io.Reader) (image.Config, error) {
	return DecodeConfigWithOptions(reader, nil)
}

func DecodeConfigWithOptions(reader io.Reader, options *DecodeOptions) (image.Config, error) {
	native, err := decode(reader, options, true)
	if err != nil {
		return image.Config{}, err
	}

	width, height, precision, channels, _, err := displayLayout(native)
	if err != nil {
		return image.Config{}, err
	}

	return image.Config{ColorModel: pixels.Model(channels, precision), Width: width, Height: height}, nil
}

func DecodeNative(reader io.Reader, options *DecodeOptions) (*NativeImage, error) {
	return decode(reader, options, false)
}

func Encode(writer io.Writer, img image.Image, options *Options) error {
	settings := Options{}

	if options != nil {
		settings = *options
	}

	if settings.BitsPerSample == 0 {
		settings.BitsPerSample = pixels.Precision(img)
	}

	values, channels, err := pixels.Extract(img, settings.BitsPerSample, settings.Format != J2K)
	if err != nil {
		return err
	}

	width := img.Bounds().Dx()
	height := img.Bounds().Dy()

	native := NativeImage{Bounds: image.Rect(0, 0, width, height), ColorSpace: SRGB, Components: make([]Component, channels), ICC: settings.ICC}

	if channels == 1 {
		native.ColorSpace = Gray
	}

	for channel := range native.Components {
		component := &native.Components[channel]
		*component = Component{Width: width, Height: height, DX: 1, DY: 1, BitsPerSample: settings.BitsPerSample, Values: make([]int32, width*height)}

		if channel == 3 {
			component.Alpha = 1
		}

		for index := range component.Values {
			component.Values[index] = int32(values[index*channels+channel])
		}
	}

	return EncodeNative(writer, &native, &settings)
}

func EncodeNative(writer io.Writer, img *NativeImage, options *Options) error {
	if img == nil || img.Bounds.Min != (image.Point{}) {
		return errors.New("jpeg2000: encoding requires a non-nil, zero-origin reference grid")
	}

	err := pixels.Check(img.Bounds.Dx(), img.Bounds.Dy(), len(img.Components), 0)
	if err != nil {
		return err
	}

	settings := Options{}

	if options != nil {
		settings = *options
	}

	native, err := encoderOptions(img, &settings)
	if err != nil {
		return err
	}

	count := 0

	for index := range img.Components {
		component := &img.Components[index]
		if settings.Format == J2K && component.Alpha != 0 {
			return errors.New("jpeg2000: raw codestreams cannot declare alpha; use JP2 or explicitly unlabel native components")
		}

		err = pixels.Check(component.Width, component.Height, 2, 0)
		if err != nil {
			return err
		}

		if component.DX < 1 || component.DY < 1 || component.DX > 255 || component.DY > 255 || component.X0 != 0 || component.Y0 != 0 || component.Width != (img.Bounds.Dx()+component.DX-1)/component.DX || component.Height != (img.Bounds.Dy()+component.DY-1)/component.DY {
			return errors.New("jpeg2000: invalid component grid")
		}

		if component.BitsPerSample < 1 || component.BitsPerSample > 31 || component.Alpha < 0 || component.Alpha > 2 || len(component.Values) != component.Width*component.Height {
			return errors.New("jpeg2000: invalid component precision, alpha, or sample count")
		}

		// Keep transform intermediates within int32. The backend also checks
		// actual reversible coefficients before Tier 1's six-bit left shift.
		if !settings.Irreversible && component.BitsPerSample > 25 {
			return errors.New("jpeg2000: reversible encoding supports at most 25-bit components")
		}

		if component.Alpha != 0 {
			colorChannels := 3

			if img.ColorSpace == Gray {
				colorChannels = 1
			}

			if img.ColorSpace != SRGB && img.ColorSpace != ICCRGB && img.ColorSpace != Gray && img.ColorSpace != SYCC || index != colorChannels || len(img.Components) != colorChannels+1 {
				return errors.New("jpeg2000: alpha requires a final opacity component after gray or RGB/YCC color channels")
			}
		}

		minimum := int64(0)
		maximum := int64(1)<<component.BitsPerSample - 1

		if component.Signed {
			minimum = -(int64(1) << (component.BitsPerSample - 1))
			maximum = -minimum - 1
		}

		for _, value := range component.Values {
			if int64(value) < minimum || int64(value) > maximum {
				return errors.New("jpeg2000: sample exceeds component precision")
			}
		}

		count += len(component.Values)
		if count > pixels.MaxBytes/4 {
			return pixels.ErrLimit
		}

		native.components[index] = C.opj_image_cmptparm_t{dx: C.OPJ_UINT32(component.DX), dy: C.OPJ_UINT32(component.DY), w: C.OPJ_UINT32(component.Width), h: C.OPJ_UINT32(component.Height), prec: C.OPJ_UINT32(component.BitsPerSample)}

		if component.Signed {
			native.components[index].sgnd = 1
		}

		native.alpha[index] = C.int(component.Alpha)
	}

	values := make([]int32, 0, count)

	for index := range img.Components {
		values = append(values, img.Components[index].Values...)
	}

	if len(img.ICC) > 4<<20 || settings.Format == J2K && len(img.ICC) != 0 {
		return errors.New("jpeg2000: ICC requires JP2 and at most 4 MiB")
	}

	var result C.cj_j2k_result

	result.base.width = C.uint32_t(img.Bounds.Dx())
	result.base.height = C.uint32_t(img.Bounds.Dy())
	result.base.components = C.uint32_t(len(img.Components))
	defer C.cj_j2k_free(&result)

	status := C.cj_j2k_encode((*C.int32_t)(unsafe.Pointer(unsafe.SliceData(values))), (*C.uchar)(unsafe.Pointer(unsafe.SliceData(img.ICC))), C.size_t(len(img.ICC)), &native, &result)
	if status == 0 {
		return errors.New("jpeg2000: " + C.GoString(&result.base.error[0]))
	}

	return pixels.Write(writer, unsafe.Slice((*byte)(unsafe.Pointer(result.base.data)), int(result.base.size)))
}

func decode(reader io.Reader, options *DecodeOptions, header bool) (*NativeImage, error) {
	settings := DecodeOptions{}

	if options != nil {
		settings = *options
	}

	if settings.Threads == 0 {
		settings.Threads = 1
	}

	if settings.Threads < 1 || settings.Threads > 16 || settings.Reduce < 0 || settings.Reduce > 30 || settings.Layers < 0 || settings.Layers > 65535 || settings.RawColorSpace < Unspecified || settings.RawColorSpace > CMYK {
		return nil, errors.New("jpeg2000: invalid decoder options")
	}

	data, err := pixels.Read(reader, settings.MaxInputBytes)
	if err != nil {
		return nil, err
	}

	var native C.cj_j2k_options

	if bytes.HasPrefix(data, []byte{0xff, 0x4f, 0xff, 0x51}) {
		native.format = 1
	} else if !bytes.HasPrefix(data, []byte("\x00\x00\x00\x0cjP  \r\n\x87\n")) {
		return nil, errors.New("jpeg2000: invalid signature")
	}

	native.threads = C.int(settings.Threads)
	native.reduce = C.int(settings.Reduce)
	native.layers = C.int(settings.Layers)

	if settings.Region != nil {
		region := *settings.Region
		if region.Empty() || region.Min.X < 0 || region.Min.Y < 0 || region.Max.X > math.MaxInt32 || region.Max.Y > math.MaxInt32 {
			return nil, errors.New("jpeg2000: invalid decode region")
		}

		native.has_region = 1
		native.region = [4]C.int{C.int(region.Min.X), C.int(region.Min.Y), C.int(region.Max.X), C.int(region.Max.Y)}
	}

	var (
		result     C.cj_j2k_result
		headerOnly C.int
	)

	if header {
		headerOnly = 1
	}

	defer C.cj_j2k_free(&result)

	status := C.cj_j2k_decode((*C.uchar)(unsafe.Pointer(unsafe.SliceData(data))), C.size_t(len(data)), headerOnly, C.uint64_t(settings.MaxPixels), &native, &result)
	if status == 0 {
		return nil, errors.New("jpeg2000: " + C.GoString(&result.base.error[0]))
	}

	source := result.image
	img := &NativeImage{Bounds: image.Rect(int(source.x0), int(source.y0), int(source.x1), int(source.y1)), ColorSpace: ColorSpace(source.color_space), Components: make([]Component, int(source.numcomps)), Format: Format(native.format)}

	if source.icc_profile_len > 4<<20 {
		return nil, errors.New("jpeg2000: ICC exceeds 4 MiB")
	}

	img.ICC = C.GoBytes(unsafe.Pointer(source.icc_profile_buf), C.int(source.icc_profile_len))

	components := unsafe.Slice(source.comps, len(img.Components))

	for index := range components {
		component := &components[index]

		img.Components[index] = Component{Width: int(component.w), Height: int(component.h), X0: int(component.x0), Y0: int(component.y0), DX: int(component.dx), DY: int(component.dy), BitsPerSample: int(component.prec), Signed: component.sgnd != 0, Alpha: int(component.alpha)}

		if !header {
			img.Components[index].Values = slices.Clone(unsafe.Slice((*int32)(unsafe.Pointer(component.data)), int(component.w)*int(component.h)))
		}
	}

	if native.format == 1 {
		img.ColorSpace = settings.RawColorSpace

		if settings.RawAlphaComponent != nil {
			index := *settings.RawAlphaComponent
			if index < 0 || index >= len(img.Components) {
				return nil, errors.New("jpeg2000: alpha component index out of range")
			}

			img.Components[index].Alpha = 1
		}
	} else if len(img.ICC) >= 128 && string(img.ICC[36:40]) == "acsp" {
		switch string(img.ICC[16:20]) {
		case "RGB ":
			img.ColorSpace = ICCRGB
		case "GRAY":
			img.ColorSpace = Gray
		case "CMYK":
			img.ColorSpace = CMYK
		}
	}

	return img, nil
}

func encoderOptions(img *NativeImage, settings *Options) (C.cj_j2k_options, error) {
	var native C.cj_j2k_options

	if settings.Format < JP2 || settings.Format > J2K || img.ColorSpace < Unspecified || img.ColorSpace > ICCRGB || settings.Progression < LRCP || settings.Progression > CPRL {
		return native, errors.New("jpeg2000: invalid format, color space, or progression")
	}

	if img.ColorSpace == ICCRGB && (settings.Format != JP2 || len(img.ICC) < 128 || string(img.ICC[36:40]) != "acsp" || string(img.ICC[16:20]) != "RGB ") {
		return native, errors.New("jpeg2000: ICC-defined RGB requires an RGB ICC profile and JP2")
	}

	if settings.Threads == 0 {
		settings.Threads = 1
	}

	if settings.Threads < 1 || settings.Threads > 16 || settings.TileWidth < 0 || settings.TileHeight < 0 || settings.TileWidth > img.Bounds.Dx() || settings.TileHeight > img.Bounds.Dy() || (settings.TileWidth == 0) != (settings.TileHeight == 0) {
		return native, errors.New("jpeg2000: invalid thread count or tiling")
	}

	smallest := min(img.Bounds.Dx(), img.Bounds.Dy())

	if settings.TileWidth > 0 {
		smallest = min(smallest, settings.TileWidth, settings.TileHeight)
	}

	if settings.Resolutions == 0 {
		settings.Resolutions = min(6, bits.Len(uint(smallest)))
	}

	if settings.Resolutions < 1 || settings.Resolutions > bits.Len(uint(smallest)) {
		return native, errors.New("jpeg2000: too many resolutions for tile dimensions")
	}

	if settings.CodeBlockWidth == 0 {
		settings.CodeBlockWidth = 64
	}

	if settings.CodeBlockHeight == 0 {
		settings.CodeBlockHeight = 64
	}

	width := settings.CodeBlockWidth
	height := settings.CodeBlockHeight

	if width < 4 || height < 4 || width > 1024 || height > 1024 || width*height > 4096 || width&(width-1) != 0 || height&(height-1) != 0 {
		return native, errors.New("jpeg2000: invalid code block size")
	}

	if len(settings.Ratios) > 0 && len(settings.PSNR) > 0 || len(settings.Ratios) > 100 || len(settings.PSNR) > 100 {
		return native, errors.New("jpeg2000: invalid quality layers")
	}

	rates := settings.Ratios

	if len(settings.PSNR) != 0 {
		rates = settings.PSNR
		native.psnr = 1
	}

	native.layer_count = C.int(max(1, len(rates)))

	for index, rate := range rates {
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate > math.MaxFloat32 || rate < 0 || native.psnr == 0 && rate < 1 {
			return native, errors.New("jpeg2000: invalid rate target")
		}

		if native.psnr == 0 && index > 0 && rate >= rates[index-1] || native.psnr != 0 && index > 0 && rate != 0 && rate <= rates[index-1] || native.psnr != 0 && rate == 0 && index != len(rates)-1 {
			return native, errors.New("jpeg2000: quality targets are not ordered")
		}

		native.rates[index] = C.float(rate)
	}

	native.format = C.int(settings.Format)
	native.color = C.int(img.ColorSpace)

	if img.ColorSpace == ICCRGB {
		// OpenJPEG uses this for channel definitions; the ICC profile takes
		// precedence over enumerated color when writing the JP2 color box.
		native.color = C.OPJ_CLRSPC_SRGB
	}

	native.threads = C.int(settings.Threads)
	native.reversible = 1

	if settings.Irreversible {
		native.reversible = 0
	}

	native.resolutions = C.int(settings.Resolutions)
	native.progression = C.int(settings.Progression)
	native.tile_width = C.int(settings.TileWidth)
	native.tile_height = C.int(settings.TileHeight)
	native.codeblock_width = C.int(width)
	native.codeblock_height = C.int(height)

	if (img.ColorSpace == SRGB || img.ColorSpace == ICCRGB) && len(img.Components) >= 3 {
		native.mct = 1
	}

	if settings.MultiComponentTransform != nil {
		native.mct = 0

		if *settings.MultiComponentTransform {
			native.mct = 1
		}
	}

	if native.mct != 0 && len(img.Components) < 3 {
		return native, errors.New("jpeg2000: color transform requires at least three components")
	}

	if settings.SOP {
		native.sop = 1
	}

	if settings.EPH {
		native.eph = 1
	}

	return native, nil
}

func displayLayout(img *NativeImage) (int, int, int, int, int, error) {
	failure := errors.New("jpeg2000: native samples require explicit color, alpha, signed, precision, or grid conversion")

	if img == nil || len(img.Components) == 0 || img.ColorSpace != SRGB && img.ColorSpace != ICCRGB && img.ColorSpace != Gray {
		return 0, 0, 0, 0, -1, failure
	}

	first := &img.Components[0]
	channels := 3

	if img.ColorSpace == Gray {
		channels = 1
	}

	alpha := -1

	for index := range img.Components {
		component := &img.Components[index]
		if component.Signed || component.BitsPerSample < 2 || component.BitsPerSample > 16 || component.BitsPerSample != first.BitsPerSample || component.Width != first.Width || component.Height != first.Height || component.Alpha == 2 {
			return 0, 0, 0, 0, -1, failure
		}

		if component.Alpha != 0 {
			if alpha != -1 || index != channels {
				return 0, 0, 0, 0, -1, failure
			}

			alpha = index
		}
	}

	if alpha >= 0 {
		channels++
	}

	if channels != len(img.Components) {
		return 0, 0, 0, 0, -1, failure
	}

	return first.Width, first.Height, first.BitsPerSample, channels, alpha, nil
}
