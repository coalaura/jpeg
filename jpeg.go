// Package jpeg dispatches JPEG-family images to four independently importable
// source-built codecs. It does not depend on image.RegisterFormat ordering;
// Decode works even when image/jpeg is imported. Use package detect to identify
// formats without CGO or native libraries, and use codec packages for advanced
// decoding, metadata, animations and native sample representations.
package jpeg

import (
	"errors"
	"image"
	"io"

	"github.com/coalaura/jpeg/classic"
	"github.com/coalaura/jpeg/detect"
	"github.com/coalaura/jpeg/jpeg2000"
	"github.com/coalaura/jpeg/jpegls"
	"github.com/coalaura/jpeg/jpegxl"
)

const (
	JPEG = detect.JPEG
	JPEGLS = detect.JPEGLS
	JP2 = detect.JP2
	J2K = detect.J2K
	JXLCodestream = detect.JXLCodestream
	JXLContainer = detect.JXLContainer
)

// Options selects an output format and that codec's typed settings. A nil
// Options, or an unspecified Format, writes ordinary JPEG. Format overrides
// JPEG2000.Format and JPEGXL.Container. Settings for other codecs are ignored.
type Options struct {
	Format detect.Format
	Classic *classic.Options
	JPEG2000 *jpeg2000.Options
	JPEGLS *jpegls.Options
	JPEGXL *jpegxl.Options
}

// DecodeOptions supplies explicit color interpretation for raw JPEG 2000 or
// four-component JPEG-LS, and format-specific resource and decoding controls.
type DecodeOptions struct {
	Classic *classic.DecodeOptions
	JPEG2000 *jpeg2000.DecodeOptions
	JPEGLS *jpegls.DecodeOptions
	JPEGXL *jpegxl.DecodeOptions
	MaxHeaderBytes int // Zero uses detect.DefaultHeaderLimit.
}

func Decode(reader io.Reader) (image.Image, error) {
	return DecodeWithOptions(reader, nil)
}

func DecodeWithOptions(reader io.Reader, options *DecodeOptions) (image.Image, error) {
	if options == nil {
		options = &DecodeOptions{}
	}

	format, replay, err := detect.DetectLimit(reader, options.MaxHeaderBytes)
	if err != nil {
		return nil, err
	}

	switch format {
	case JPEG:
		return classic.DecodeWithOptions(replay, options.Classic)
	case JPEGLS:
		return jpegls.DecodeWithOptions(replay, options.JPEGLS)
	case JP2, J2K:
		return jpeg2000.DecodeWithOptions(replay, options.JPEG2000)
	case JXLCodestream, JXLContainer:
		return jpegxl.DecodeWithOptions(replay, options.JPEGXL)
	default:
		return nil, image.ErrFormat
	}
}

// DecodeConfig reads image headers without decoding the pixel image. It reports
// ambiguous raw color layouts as errors rather than guessing their meaning.
func DecodeConfig(reader io.Reader) (image.Config, error) {
	return DecodeConfigWithOptions(reader, nil)
}

func DecodeConfigWithOptions(reader io.Reader, options *DecodeOptions) (image.Config, error) {
	if options == nil {
		options = &DecodeOptions{}
	}

	format, replay, err := detect.DetectLimit(reader, options.MaxHeaderBytes)
	if err != nil {
		return image.Config{}, err
	}

	switch format {
	case JPEG:
		return classic.DecodeConfigWithOptions(replay, options.Classic)
	case JPEGLS:
		return jpegls.DecodeConfigWithOptions(replay, options.JPEGLS)
	case JP2, J2K:
		return jpeg2000.DecodeConfigWithOptions(replay, options.JPEG2000)
	case JXLCodestream, JXLContainer:
		return jpegxl.DecodeConfigWithOptions(replay, options.JPEGXL)
	default:
		return image.Config{}, image.ErrFormat
	}
}

func Encode(writer io.Writer, img image.Image, options *Options) error {
	if options == nil {
		return classic.Encode(writer, img, nil)
	}

	switch options.Format {
	case detect.Unknown, JPEG:
		return classic.Encode(writer, img, options.Classic)
	case JPEGLS:
		return jpegls.Encode(writer, img, options.JPEGLS)
	case JP2, J2K:
		settings := jpeg2000.Options{}

		if options.JPEG2000 != nil {
			settings = *options.JPEG2000
		}

		if options.Format == J2K {
			settings.Format = jpeg2000.J2K
		} else {
			settings.Format = jpeg2000.JP2
		}

		return jpeg2000.Encode(writer, img, &settings)
	case JXLCodestream, JXLContainer:
		settings := jpegxl.Options{}

		if options.JPEGXL != nil {
			settings = *options.JPEGXL
		}

		settings.Container = options.Format == JXLContainer

		return jpegxl.Encode(writer, img, &settings)
	default:
		return errors.New("jpeg: unknown output format")
	}
}
