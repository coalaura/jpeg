// Package classic implements JPEG with bundled libjpeg-turbo. Quality 100 is
// ordinary DCT coding; predictive lossless must be explicitly requested.
// Decode returns Gray, NRGBA, Gray16, NRGBA64, or CMYK. ICC/Exif are available
// through DecodeSamples and are not implicitly applied. Alpha is rejected on
// encoding unless the caller explicitly composites it beforehand.
package classic

/*
#include "bridge.h"
*/
import "C"

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"slices"
	"unsafe"

	"github.com/coalaura/jpeg/internal/icc"
	"github.com/coalaura/jpeg/internal/pixels"
)

const (
	Subsample420 Subsampling = iota
	Subsample444
	Subsample422
	Subsample440
	Subsample411
	Subsample441
)

const (
	DefaultColor ColorSpace = iota
	Grayscale
	RGB
	YCbCr
	CMYK
	YCCK
)

const (
	Accurate DCTMethod = iota
	Fast
	Float
)

type Subsampling int
type ColorSpace int
type DCTMethod int

// Lossless selects predictive JPEG. Predictor is 1..7 (zero selects 1).
// PointTransform discards that many least-significant bits; zero preserves all.
type Lossless struct {
	Predictor      int
	PointTransform int
}

// Marker is an APP0..APP15 (0xe0..0xef) or COM (0xfe) payload, without its length.
// ICC profiles may span multiple APP2 markers; Exif usually occupies APP1.
type Marker struct {
	ID   byte
	Data []byte
}

// Options uses defaults when nil. Pointer fields distinguish omission from zero.
type Options struct {
	Quality         *int        // Nil selects 90; 0..100 is clamped by libjpeg to 1..100.
	Subsampling     Subsampling // Default 4:2:0; ignored for grayscale/RGB/lossless.
	Progressive     bool
	OptimizeHuffman bool
	Arithmetic      bool // Arithmetic entropy coding instead of Huffman, DCT only.
	Lossless        *Lossless
	BitsPerSample   int        // Zero selects 8; DCT supports 8/12, lossless supports 2..16.
	ColorSpace      ColorSpace // Default YCbCr, grayscale, or CMYK according to input.
	RestartInterval int        // MCUs, 0..65535; mutually exclusive with RestartRows.
	RestartRows     int        // MCU rows, 0..65535.
	DCT             DCTMethod
	WriteJFIF       *bool
	WriteAdobe      *bool
	Markers         []Marker // At most 4 MiB total; each payload at most 65533 bytes.
}

type DecodeOptions struct {
	ScaleNumerator   int // Zero selects 1. Libjpeg rounds to a supported IDCT scale.
	ScaleDenominator int // Zero selects 1; each scale term must be 1..16.
	DCT              DCTMethod
	FancyUpsampling  *bool  // Nil selects true.
	BlockSmoothing   *bool  // Nil selects true.
	AllowWarnings    bool   // Default rejects truncation and other native warnings.
	ReadMetadata     bool   // Retain APP/COM markers, up to 4 MiB.
	ReadICC          bool   // Assemble ICC in Samples.ICC, up to 4 MiB of profile data; skip unrelated metadata unless ReadMetadata is set.
	MaxScans         int    // Zero selects 1000.
	MaxPixels        uint64 // Zero selects 16 million, checked before and after scaling.
	MaxInputBytes    int64  // Zero selects 64 MiB; maximum 512 MiB.
}

// Samples contains unsigned, pixel-interleaved native-precision samples.
// ColorSpace describes decoded samples (Gray/RGB/CMYK); SourceColorSpace describes
// the encoded components. CMYK uses conventional non-inverted Go semantics.
// This API preserves 9..16-bit samples without display-range expansion.
type Samples struct {
	Width            int
	Height           int
	BitsPerSample    int
	ColorSpace       ColorSpace
	SourceColorSpace ColorSpace
	Values           []uint16
	Progressive      bool
	Lossless         bool
	Markers          []Marker
	ICC              []byte
}

func Decode(reader io.Reader) (image.Image, error) {
	return DecodeWithOptions(reader, nil)
}

func DecodeWithOptions(reader io.Reader, options *DecodeOptions) (image.Image, error) {
	samples, err := DecodeSamples(reader, options)
	if err != nil {
		return nil, err
	}

	if samples.ColorSpace == CMYK {
		if samples.BitsPerSample != 8 {
			return nil, errors.New("classic: high-precision CMYK requires DecodeSamples")
		}

		img := image.NewCMYK(image.Rect(0, 0, samples.Width, samples.Height))

		for index, value := range samples.Values {
			img.Pix[index] = byte(value)
		}

		return img, nil
	}

	maxPixels := uint64(0)

	if options != nil {
		maxPixels = options.MaxPixels
	}

	return pixels.Image(samples.Values, samples.Width, samples.Height, components(samples.ColorSpace), samples.BitsPerSample, maxPixels)
}

func DecodeConfig(reader io.Reader) (image.Config, error) {
	return DecodeConfigWithOptions(reader, nil)
}

func DecodeConfigWithOptions(reader io.Reader, options *DecodeOptions) (image.Config, error) {
	samples, err := decode(reader, options, true)
	if err != nil {
		return image.Config{}, err
	}

	model := pixels.Model(components(samples.ColorSpace), samples.BitsPerSample)

	if samples.ColorSpace == CMYK {
		model = color.CMYKModel
	}

	return image.Config{ColorModel: model, Width: samples.Width, Height: samples.Height}, nil
}

func DecodeSamples(reader io.Reader, options *DecodeOptions) (*Samples, error) {
	return decode(reader, options, false)
}

func Encode(writer io.Writer, img image.Image, options *Options) error {
	settings := Options{}

	if options != nil {
		settings = *options
	}

	if settings.BitsPerSample == 0 {
		settings.BitsPerSample = 8
	}

	if img == nil {
		return errors.New("classic: nil image")
	}

	bounds := img.Bounds()

	if cmyk, ok := img.(*image.CMYK); ok {
		err := pixels.Check(bounds.Dx(), bounds.Dy(), 4, 0)
		if err != nil {
			return err
		}

		if settings.BitsPerSample != 8 {
			return errors.New("classic: use EncodeSamples for high-precision CMYK")
		}

		values := make([]uint16, bounds.Dx()*bounds.Dy()*4)
		index := 0

		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				value := cmyk.CMYKAt(x, y)
				values[index] = uint16(value.C)
				values[index+1] = uint16(value.M)
				values[index+2] = uint16(value.Y)
				values[index+3] = uint16(value.K)
				index += 4
			}
		}

		samples := Samples{Width: bounds.Dx(), Height: bounds.Dy(), BitsPerSample: 8, ColorSpace: CMYK, Values: values}
		return EncodeSamples(writer, &samples, &settings)
	}

	values, count, err := pixels.Extract(img, settings.BitsPerSample, false)
	if err != nil {
		return err
	}

	space := RGB

	if count == 1 {
		space = Grayscale
	}

	samples := Samples{Width: bounds.Dx(), Height: bounds.Dy(), BitsPerSample: settings.BitsPerSample, ColorSpace: space, Values: values}
	return EncodeSamples(writer, &samples, &settings)
}

func EncodeSamples(writer io.Writer, samples *Samples, options *Options) error {
	if samples == nil {
		return errors.New("classic: nil samples")
	}

	if samples.ColorSpace != Grayscale && samples.ColorSpace != RGB && samples.ColorSpace != CMYK {
		return errors.New("classic: input samples must be grayscale, RGB, or conventional CMYK")
	}

	count := components(samples.ColorSpace)

	err := pixels.Check(samples.Width, samples.Height, count, 0)
	if err != nil {
		return err
	}

	if len(samples.Values) != samples.Width*samples.Height*count || samples.BitsPerSample < 2 || samples.BitsPerSample > 16 {
		return errors.New("classic: invalid sample count or precision")
	}

	for _, value := range samples.Values {
		if uint32(value) >= 1<<samples.BitsPerSample {
			return errors.New("classic: sample exceeds precision")
		}
	}

	settings := Options{}

	if options != nil {
		settings = *options
	}

	native, markers, err := encoderOptions(&settings, samples.BitsPerSample)
	if err != nil {
		return err
	}

	if settings.ColorSpace != DefaultColor && components(settings.ColorSpace) != count {
		return errors.New("classic: incompatible output color space")
	}

	if len(markers) != 0 {
		native.markers = (*C.uchar)(C.CBytes(markers))
		defer C.free(unsafe.Pointer(native.markers))
	}

	native.markers_size = C.size_t(len(markers))

	result := C.cj_result{width: C.uint32_t(samples.Width), height: C.uint32_t(samples.Height), components: C.uint32_t(count), bits: C.uint32_t(samples.BitsPerSample)}
	defer C.cj_free(&result)

	status := C.cj_jpeg_encode((*C.uint16_t)(unsafe.Pointer(unsafe.SliceData(samples.Values))), &native, &result)
	if status == 0 {
		return errors.New("classic: " + C.GoString(&result.error[0]))
	}

	return pixels.Write(writer, unsafe.Slice((*byte)(unsafe.Pointer(result.data)), int(result.size)))
}

func decode(reader io.Reader, options *DecodeOptions, header bool) (*Samples, error) {
	settings := DecodeOptions{}

	if options != nil {
		settings = *options
	}

	if settings.ScaleNumerator == 0 {
		settings.ScaleNumerator = 1
	}

	if settings.ScaleDenominator == 0 {
		settings.ScaleDenominator = 1
	}

	if settings.MaxScans == 0 {
		settings.MaxScans = 1000
	}

	if settings.ScaleNumerator < 1 || settings.ScaleNumerator > 16 || settings.ScaleDenominator < 1 || settings.ScaleDenominator > 16 || settings.DCT < Accurate || settings.DCT > Float || settings.MaxScans < 1 || settings.MaxScans > 10000 {
		return nil, errors.New("classic: invalid decoder options")
	}

	data, err := pixels.Read(reader, settings.MaxInputBytes)
	if err != nil {
		return nil, err
	}

	var profile []byte

	if settings.ReadICC {
		profile, _, err = icc.JPEG(data)
		if err != nil {
			return nil, fmt.Errorf("classic: %w", err)
		}
	}

	var native C.cj_jpeg_options

	native.scale_num = C.int(settings.ScaleNumerator)
	native.scale_den = C.int(settings.ScaleDenominator)
	native.dct = C.int(settings.DCT)
	native.fancy = optionalBool(settings.FancyUpsampling, 1)
	native.smoothing = optionalBool(settings.BlockSmoothing, 1)
	native.strict = flag(!settings.AllowWarnings)
	native.save_markers = flag(settings.ReadMetadata)
	native.max_scans = C.int(settings.MaxScans)

	var result C.cj_result

	defer C.cj_free(&result)

	status := C.cj_jpeg_decode((*C.uchar)(unsafe.Pointer(unsafe.SliceData(data))), C.size_t(len(data)), flag(header), C.uint64_t(settings.MaxPixels), &native, &result)
	if status == 0 {
		return nil, errors.New("classic: " + C.GoString(&result.error[0]))
	}

	space := RGB

	switch result.components {
	case 1:
		space = Grayscale
	case 4:
		space = CMYK
	}

	samples := &Samples{Width: int(result.width), Height: int(result.height), BitsPerSample: int(result.bits), ColorSpace: space, SourceColorSpace: ColorSpace(native.source_color), Progressive: native.progressive_input != 0, Lossless: native.lossless_input != 0}
	samples.ICC = profile

	if !header {
		samples.Values = slices.Clone(unsafe.Slice((*uint16)(unsafe.Pointer(result.samples)), samples.Width*samples.Height*int(result.components)))
	}

	metadata := unsafe.Slice((*byte)(unsafe.Pointer(result.data)), int(result.size))

	for len(metadata) >= 3 {
		length := int(binary.BigEndian.Uint16(metadata[1:3]))
		samples.Markers = append(samples.Markers, Marker{ID: metadata[0], Data: slices.Clone(metadata[3 : 3+length])})

		metadata = metadata[3+length:]
	}

	return samples, nil
}

func encoderOptions(settings *Options, bits int) (C.cj_jpeg_options, []byte, error) {
	var native C.cj_jpeg_options

	native.quality = 90

	if settings.Quality != nil {
		if *settings.Quality < 0 || *settings.Quality > 100 {
			return native, nil, errors.New("classic: quality must be 0..100")
		}

		native.quality = C.int(*settings.Quality)
	}

	if settings.Subsampling < Subsample420 || settings.Subsampling > Subsample441 || settings.DCT < Accurate || settings.DCT > Float || settings.ColorSpace < DefaultColor || settings.ColorSpace > YCCK {
		return native, nil, errors.New("classic: invalid subsampling, DCT, or color space")
	}

	if settings.RestartInterval < 0 || settings.RestartInterval > 65535 || settings.RestartRows < 0 || settings.RestartRows > 65535 || settings.RestartInterval != 0 && settings.RestartRows != 0 {
		return native, nil, errors.New("classic: invalid restart interval")
	}

	if settings.BitsPerSample != 0 && settings.BitsPerSample != bits {
		return native, nil, errors.New("classic: conflicting sample precision")
	}

	if settings.Lossless == nil && bits != 8 && bits != 12 {
		return native, nil, errors.New("classic: DCT coding requires 8 or 12 bits")
	}

	if settings.Lossless != nil {
		lossless := *settings.Lossless
		if lossless.Predictor == 0 {
			lossless.Predictor = 1
		}

		if lossless.Predictor < 1 || lossless.Predictor > 7 || lossless.PointTransform < 0 || lossless.PointTransform >= bits || settings.Progressive || settings.Arithmetic {
			return native, nil, errors.New("classic: invalid predictive-lossless options")
		}

		native.predictor = C.int(lossless.Predictor)
		native.point_transform = C.int(lossless.PointTransform)
	}

	native.subsampling = C.int(settings.Subsampling)
	native.progressive = flag(settings.Progressive)
	native.optimize = flag(settings.OptimizeHuffman)
	native.arithmetic = flag(settings.Arithmetic)
	native.color = C.int(settings.ColorSpace)
	native.restart = C.int(settings.RestartInterval)
	native.restart_rows = C.int(settings.RestartRows)
	native.dct = C.int(settings.DCT)
	native.jfif = optionalBool(settings.WriteJFIF, -1)
	native.adobe = optionalBool(settings.WriteAdobe, -1)

	length := 0

	for _, marker := range settings.Markers {
		if marker.ID != 0xfe && (marker.ID < 0xe0 || marker.ID > 0xef) || len(marker.Data) > 65533 {
			return native, nil, errors.New("classic: invalid metadata marker")
		}

		length += len(marker.Data) + 3
		if length > 4<<20 {
			return native, nil, errors.New("classic: metadata exceeds 4 MiB")
		}
	}

	markers := make([]byte, 0, length)

	for _, marker := range settings.Markers {
		markers = append(markers, marker.ID, byte(len(marker.Data)>>8), byte(len(marker.Data)))
		markers = append(markers, marker.Data...)
	}

	return native, markers, nil
}

func components(space ColorSpace) int {
	switch space {
	case Grayscale:
		return 1
	case RGB, YCbCr:
		return 3
	case CMYK, YCCK:
		return 4
	}

	return 0
}

func flag(value bool) C.int {
	if value {
		return 1
	}

	return 0
}

func optionalBool(value *bool, fallback C.int) C.int {
	if value == nil {
		return fallback
	}

	return flag(*value)
}
