// Package pixels contains Go-only buffer and image conversion helpers.
package pixels

import (
	"errors"
	"image"
	"image/color"
	"io"
)

const (
	MaxPixels = 16 << 20
	MaxBytes  = 512 << 20
	MaxInput  = 64 << 20
)

var ErrLimit = errors.New("jpeg: resource limit exceeded")

// Read bounds encoded input and propagates underlying errors.
func Read(reader io.Reader, limit int64) ([]byte, error) {
	if limit == 0 {
		limit = MaxInput
	}

	if limit < 0 || limit > MaxBytes {
		return nil, ErrLimit
	}

	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}

	if int64(len(data)) > limit {
		return nil, ErrLimit
	}

	if len(data) == 0 {
		return nil, io.ErrUnexpectedEOF
	}

	return data, nil
}

func Write(writer io.Writer, data []byte) error {
	count, err := writer.Write(data)
	if err == nil && count != len(data) {
		return io.ErrShortWrite
	}

	return err
}

func Check(width, height, components int, maxPixels uint64) error {
	if maxPixels == 0 {
		maxPixels = MaxPixels
	}

	if width <= 0 || height <= 0 || components < 1 || components > 16 {
		return errors.New("jpeg: invalid image dimensions or component count")
	}

	count := uint64(width) * uint64(height)
	if uint64(width) > MaxBytes || uint64(height) > MaxBytes || count > maxPixels || count > MaxBytes/uint64(components)/2 {
		return ErrLimit
	}

	return nil
}

// Extract returns straight-alpha interleaved unsigned samples. Bits must be 2..16.
// Alpha is never silently discarded. RGB-only codecs reject translucent inputs.
func Extract(img image.Image, bits int, alpha bool) ([]uint16, int, error) {
	if img == nil || bits < 2 || bits > 16 {
		return nil, 0, errors.New("jpeg: invalid image or sample precision")
	}

	bounds := img.Bounds()
	components := 3

	switch img.ColorModel() {
	case color.GrayModel, color.Gray16Model:
		components = 1
	default:
		if alpha {
			components = 4
		}
	}

	err := Check(bounds.Dx(), bounds.Dy(), components, 0)
	if err != nil {
		return nil, 0, err
	}

	output := make([]uint16, bounds.Dx()*bounds.Dy()*components)
	index := 0

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pixel := sampleAt(img, x, y)
			if !alpha && pixel.A != 65535 {
				return nil, 0, errors.New("jpeg: alpha is unsupported; composite explicitly before encoding")
			}

			output[index] = pixel.R >> (16 - bits)

			if components >= 3 {
				output[index+1] = pixel.G >> (16 - bits)
				output[index+2] = pixel.B >> (16 - bits)
			}

			if components == 4 {
				output[index+3] = pixel.A >> (16 - bits)
			}

			index += components
		}
	}

	return output, components, nil
}

// Typed image access avoids boxing a color and allocating for every pixel.
// Straight-alpha access also preserves the invisible RGB of NRGBA images.
func sampleAt(img image.Image, x, y int) color.NRGBA64 {
	switch source := img.(type) {
	case *image.NRGBA64:
		return source.NRGBA64At(x, y)
	case *image.NRGBA:
		pixel := source.NRGBAAt(x, y)

		return color.NRGBA64{R: uint16(pixel.R) * 257, G: uint16(pixel.G) * 257, B: uint16(pixel.B) * 257, A: uint16(pixel.A) * 257}
	case image.RGBA64Image:
		pixel := source.RGBA64At(x, y)
		if pixel.A == 0 {
			return color.NRGBA64{}
		}

		if pixel.A == 65535 {
			return color.NRGBA64(pixel)
		}

		alpha := uint32(pixel.A)
		return color.NRGBA64{R: uint16(uint32(pixel.R) * 65535 / alpha), G: uint16(uint32(pixel.G) * 65535 / alpha), B: uint16(uint32(pixel.B) * 65535 / alpha), A: pixel.A}
	default:
		return color.NRGBA64Model.Convert(img.At(x, y)).(color.NRGBA64)
	}
}

// Precision preserves the sample depth of standard 16-bit Go images.
func Precision(img image.Image) int {
	if img != nil {
		switch img.ColorModel() {
		case color.Gray16Model, color.RGBA64Model, color.NRGBA64Model:
			return 16
		}
	}

	return 8
}

// Image expands the sample range to the corresponding Go color range.
func Image(samples []uint16, width, height, components, bits int, maxPixels uint64) (image.Image, error) {
	if components != 1 && components != 3 && components != 4 || bits < 2 || bits > 16 {
		return nil, errors.New("jpeg: samples require explicit color interpretation")
	}

	err := Check(width, height, components, maxPixels)
	if err != nil {
		return nil, err
	}

	if len(samples) != width*height*components {
		return nil, errors.New("jpeg: incorrect sample count")
	}

	maximum := uint32(1<<bits) - 1
	rectangle := image.Rect(0, 0, width, height)

	if components == 1 && bits <= 8 {
		img := image.NewGray(rectangle)

		for index, sample := range samples {
			img.Pix[index] = byte(uint32(sample) * 255 / maximum)
		}

		return img, nil
	}

	if components == 1 {
		img := image.NewGray16(rectangle)

		for index, sample := range samples {
			value := uint32(sample) * 65535 / maximum
			img.Pix[index*2] = byte(value >> 8)
			img.Pix[index*2+1] = byte(value)
		}

		return img, nil
	}

	if bits <= 8 {
		img := image.NewNRGBA(rectangle)

		for index := 0; index < width*height; index++ {
			for channel := range 3 {
				img.Pix[index*4+channel] = byte(uint32(samples[index*components+channel]) * 255 / maximum)
			}

			img.Pix[index*4+3] = 255

			if components == 4 {
				img.Pix[index*4+3] = byte(uint32(samples[index*4+3]) * 255 / maximum)
			}
		}

		return img, nil
	}

	img := image.NewNRGBA64(rectangle)

	for index := 0; index < width*height; index++ {
		for channel := range 4 {
			value := uint32(65535)

			if channel < components {
				value = uint32(samples[index*components+channel]) * 65535 / maximum
			}

			img.Pix[index*8+channel*2] = byte(value >> 8)
			img.Pix[index*8+channel*2+1] = byte(value)
		}
	}

	return img, nil
}

func Model(components, bits int) color.Model {
	if components == 1 {
		if bits > 8 {
			return color.Gray16Model
		}

		return color.GrayModel
	}

	if bits > 8 {
		return color.NRGBA64Model
	}

	return color.NRGBAModel
}
