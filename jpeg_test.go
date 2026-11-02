package jpeg_test

import (
	"bytes"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"testing"

	"github.com/coalaura/jpeg"
	"github.com/coalaura/jpeg/classic"
	"github.com/coalaura/jpeg/detect"
	"github.com/coalaura/jpeg/jpeg2000"
	"github.com/coalaura/jpeg/jpegls"
	"github.com/coalaura/jpeg/jpegxl"
)

func TestLosslessSamples(t *testing.T) {
	values := []uint16{0, 1, 255, 256, 32768, 32769, 65535, 65534, 12345}
	img := image.NewGray16(image.Rect(0, 0, 3, 3))

	for index, value := range values {
		img.SetGray16(index%3, index/3, color.Gray16{Y: value})
	}

	formats := []jpeg.Options{
		{Format: jpeg.JPEG, Classic: &classic.Options{BitsPerSample: 16, Lossless: &classic.Lossless{}}},
		{Format: jpeg.JPEGLS, JPEGLS: &jpegls.Options{BitsPerSample: 16}},
		{Format: jpeg.JP2, JPEG2000: &jpeg2000.Options{BitsPerSample: 16}},
		{Format: jpeg.JXLContainer, JPEGXL: &jpegxl.Options{BitsPerSample: 16, Lossless: true, Effort: 1}},
	}

	for index := range formats {
		options := &formats[index]

		t.Run(options.Format.String(), func(t *testing.T) {
			var encoded bytes.Buffer

			err := jpeg.Encode(&encoded, img, options)
			if err != nil {
				t.Fatal(err)
			}

			config, err := jpeg.DecodeConfig(bytes.NewReader(encoded.Bytes()))
			if err != nil || config.Width != 3 || config.Height != 3 {
				t.Fatalf("config: %+v, %v", config, err)
			}

			decoded, err := jpeg.Decode(bytes.NewReader(encoded.Bytes()))
			if err != nil {
				t.Fatal(err)
			}

			for sample, expected := range values {
				actual := color.Gray16Model.Convert(decoded.At(sample%3, sample/3)).(color.Gray16).Y
				if actual != expected {
					t.Fatalf("sample %d: got %d, want %d", sample, actual, expected)
				}
			}
		})
	}
}

func TestDefaultJPEG(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 8, 8))

	var encoded bytes.Buffer

	err := jpeg.Encode(&encoded, img, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = stdjpeg.Decode(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}

	format, _, err := detect.Detect(bytes.NewReader(encoded.Bytes()))
	if err != nil || format != detect.JPEG {
		t.Fatalf("format=%v error=%v", format, err)
	}
}
