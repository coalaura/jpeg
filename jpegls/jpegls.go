// Package jpegls encodes and decodes baseline JPEG-LS using bundled CharLS.
// JPEG-LS does not imply RGB or alpha: ordinary decoding accepts one grayscale
// or three RGB components. Four components require an explicit RGBA interpretation.
// Samples preserves unsigned native precision and component order without scaling.
package jpegls

/*
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"image"
	"io"
	"slices"
	"unsafe"

	"github.com/coalaura/jpeg/internal/pixels"
)

const (
	Planar Interleave = iota
	Line
	Sample
)

const (
	NoTransform Transform = iota
	HP1
	HP2
	HP3
)

type Interleave int
type Transform int

// Preset overrides the JPEG-LS coding parameters. Nil uses ISO defaults.
type Preset struct {
	Maximum int
	Threshold1 int
	Threshold2 int
	Threshold3 int
	Reset int
}

// Options controls encoding. Nil is 8-bit, exact lossless, sample-interleaved
// color or planar grayscale. NEAR=0 means exact lossless, never a quality preset.
type Options struct {
	BitsPerSample int // Zero selects 8. Native sample encoding uses Samples.BitsPerSample.
	NEAR int // 0..min(255, maximum/2), in native sample units.
	Interleave *Interleave // Nil selects Sample for color and Planar for grayscale.
	Transform Transform // HP transforms require three 8/16-bit components and NEAR=0.
	Preset *Preset
	RGBA bool // Explicitly store four components as straight RGBA; this is not file metadata.
}

type DecodeOptions struct {
	RGBA bool // Interpret four components as straight RGBA by caller agreement.
	MaxPixels uint64 // Zero selects 16 million pixels.
	MaxInputBytes int64 // Zero selects 64 MiB; maximum 512 MiB.
}

// Samples stores pixel-interleaved values, irrespective of file interleave mode.
// Components have no implicit color semantics. Precision is 2..16 bits.
type Samples struct {
	Width int
	Height int
	Components int
	BitsPerSample int
	Values []uint16
	Interleave Interleave
	NEAR []int // Decoder-reported error bound for each component.
}

func Decode(reader io.Reader) (image.Image, error) {
	return DecodeWithOptions(reader, nil)
}

func DecodeWithOptions(reader io.Reader, options *DecodeOptions) (image.Image, error) {
	samples, err := DecodeSamples(reader, options)
	if err != nil {
		return nil, err
	}

	if samples.Components == 4 && (options == nil || !options.RGBA) {
		return nil, errors.New("jpegls: four components require explicit RGBA interpretation or DecodeSamples")
	}

	return pixels.Image(samples.Values, samples.Width, samples.Height, samples.Components, samples.BitsPerSample)
}

func DecodeConfig(reader io.Reader) (image.Config, error) {
	return DecodeConfigWithOptions(reader, nil)
}

func DecodeConfigWithOptions(reader io.Reader, options *DecodeOptions) (image.Config, error) {
	samples, err := decode(reader, options, true)
	if err != nil {
		return image.Config{}, err
	}

	if samples.Components != 1 && samples.Components != 3 && (samples.Components != 4 || options == nil || !options.RGBA) {
		return image.Config{}, errors.New("jpegls: ambiguous color components; use DecodeSamples")
	}

	return image.Config{ColorModel: pixels.Model(samples.Components, samples.BitsPerSample), Width: samples.Width, Height: samples.Height}, nil
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

	values, components, err := pixels.Extract(img, settings.BitsPerSample, settings.RGBA)
	if err != nil {
		return err
	}

	samples := Samples{Width: img.Bounds().Dx(), Height: img.Bounds().Dy(), Components: components, BitsPerSample: settings.BitsPerSample, Values: values}
	return EncodeSamples(writer, &samples, &settings)
}

func EncodeSamples(writer io.Writer, samples *Samples, options *Options) error {
	if samples == nil {
		return errors.New("jpegls: nil samples")
	}

	err := pixels.Check(samples.Width, samples.Height, samples.Components, 0)
	if err != nil {
		return err
	}

	if samples.BitsPerSample < 2 || samples.BitsPerSample > 16 || len(samples.Values) != samples.Width*samples.Height*samples.Components {
		return errors.New("jpegls: invalid precision or sample count")
	}

	settings := Options{}

	if options != nil {
		settings = *options
	}

	if settings.BitsPerSample != 0 && settings.BitsPerSample != samples.BitsPerSample {
		return errors.New("jpegls: conflicting precision")
	}

	maximum := 1<<samples.BitsPerSample - 1
	if settings.NEAR < 0 || settings.NEAR > min(255, maximum/2) || settings.Transform < NoTransform || settings.Transform > HP3 {
		return errors.New("jpegls: invalid NEAR or transform")
	}

	for _, value := range samples.Values {
		if int(value) > maximum {
			return errors.New("jpegls: sample exceeds precision")
		}
	}

	interleave := Sample

	if samples.Components == 1 {
		interleave = Planar
	}

	if settings.Interleave != nil {
		interleave = *settings.Interleave
	}

	if interleave < Planar || interleave > Sample || samples.Components == 1 && interleave != Planar {
		return errors.New("jpegls: invalid interleave")
	}

	var native C.cj_ls_options

	native.near_lossless = C.int(settings.NEAR)
	native.interleave = C.int(interleave)
	native.transform = C.int(settings.Transform)

	if settings.Preset != nil {
		preset := settings.Preset
		if preset.Maximum < 1 || preset.Maximum > maximum || settings.NEAR > min(255, preset.Maximum/2) || preset.Threshold1 < settings.NEAR+1 || preset.Threshold2 < preset.Threshold1 || preset.Threshold3 < preset.Threshold2 || preset.Threshold3 > preset.Maximum || preset.Reset < 3 || preset.Reset > max(255, preset.Maximum) {
			return errors.New("jpegls: invalid preset")
		}

		for _, value := range samples.Values {
			if int(value) > preset.Maximum {
				return errors.New("jpegls: sample exceeds preset maximum")
			}
		}

		native.preset = [5]C.int{C.int(preset.Maximum), C.int(preset.Threshold1), C.int(preset.Threshold2), C.int(preset.Threshold3), C.int(preset.Reset)}
	}

	result := C.cj_result{width: C.uint32_t(samples.Width), height: C.uint32_t(samples.Height), components: C.uint32_t(samples.Components), bits: C.uint32_t(samples.BitsPerSample)}
	defer C.cj_free(&result)

	status := C.cj_ls_encode((*C.uint16_t)(unsafe.Pointer(unsafe.SliceData(samples.Values))), &native, &result)
	if status == 0 {
		return errors.New("jpegls: " + C.GoString(&result.error[0]))
	}

	return pixels.Write(writer, unsafe.Slice((*byte)(unsafe.Pointer(result.data)), int(result.size)))
}

func decode(reader io.Reader, options *DecodeOptions, header bool) (*Samples, error) {
	settings := DecodeOptions{}

	if options != nil {
		settings = *options
	}

	data, err := pixels.Read(reader, settings.MaxInputBytes)
	if err != nil {
		return nil, err
	}

	var (
		result C.cj_result
		native C.cj_ls_options
		headerOnly C.int
	)

	if header {
		headerOnly = 1
	}

	defer C.cj_free(&result)

	status := C.cj_ls_decode((*C.uchar)(unsafe.Pointer(unsafe.SliceData(data))), C.size_t(len(data)), headerOnly, C.uint64_t(settings.MaxPixels), &native, &result)
	if status == 0 {
		return nil, errors.New("jpegls: " + C.GoString(&result.error[0]))
	}

	samples := &Samples{Width: int(result.width), Height: int(result.height), Components: int(result.components), BitsPerSample: int(result.bits), Interleave: Interleave(native.interleave)}
	samples.NEAR = make([]int, samples.Components)

	for index := range samples.NEAR {
		samples.NEAR[index] = int(native.near_components[index])
	}

	if !header {
		samples.Values = slices.Clone(unsafe.Slice((*uint16)(unsafe.Pointer(result.samples)), samples.Width*samples.Height*samples.Components))
	}

	return samples, nil
}
