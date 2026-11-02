// Command fixtures regenerates the small, analytically specified test images.
// Run after internal/update; the two upstream fixtures come from its pinned caches.
package main

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"path/filepath"

	"github.com/coalaura/jpeg"
	"github.com/coalaura/jpeg/classic"
	"github.com/coalaura/jpeg/jpeg2000"
	"github.com/coalaura/jpeg/jpegls"
	"github.com/coalaura/jpeg/jpegxl"
)

type fixture struct {
	name    string
	options jpeg.Options
	img     image.Image
}

func main() {
	err := os.MkdirAll("testdata", 0755)
	check(err)

	gray := image.NewGray16(image.Rect(0, 0, 3, 3))
	values := []uint16{0, 1, 255, 256, 32768, 32769, 65535, 65534, 12345}

	for index, value := range values {
		gray.SetGray16(index%3, index/3, color.Gray16{Y: value})
	}

	rgba := image.NewNRGBA64(image.Rect(0, 0, 3, 1))
	rgba.SetNRGBA64(0, 0, color.NRGBA64{R: 32768, G: 32769, B: 65535, A: 32769})
	rgba.SetNRGBA64(1, 0, color.NRGBA64{R: 257, G: 511, B: 12345, A: 65535})
	rgba.SetNRGBA64(2, 0, color.NRGBA64{R: 17, G: 32769, B: 65534, A: 0})

	fixtures := []fixture{
		{name: "gray16.jpg", img: gray, options: jpeg.Options{Format: jpeg.JPEG, Classic: &classic.Options{BitsPerSample: 16, Lossless: &classic.Lossless{}}}},
		{name: "gray16.jls", img: gray, options: jpeg.Options{Format: jpeg.JPEGLS, JPEGLS: &jpegls.Options{BitsPerSample: 16}}},
		{name: "gray16.jp2", img: gray, options: jpeg.Options{Format: jpeg.JP2, JPEG2000: &jpeg2000.Options{BitsPerSample: 16}}},
		{name: "gray16.j2k", img: gray, options: jpeg.Options{Format: jpeg.J2K, JPEG2000: &jpeg2000.Options{BitsPerSample: 16}}},
		{name: "gray16.jxl", img: gray, options: jpeg.Options{Format: jpeg.JXLCodestream, JPEGXL: &jpegxl.Options{BitsPerSample: 16, Lossless: true, Effort: 1}}},
		{name: "rgba16.jls", img: rgba, options: jpeg.Options{Format: jpeg.JPEGLS, JPEGLS: &jpegls.Options{BitsPerSample: 16, RGBA: true}}},
		{name: "rgba16.jp2", img: rgba, options: jpeg.Options{Format: jpeg.JP2, JPEG2000: &jpeg2000.Options{BitsPerSample: 16}}},
		{name: "rgba16.jxl", img: rgba, options: jpeg.Options{Format: jpeg.JXLContainer, JPEGXL: &jpegxl.Options{BitsPerSample: 16, Lossless: true, Effort: 1}}},
		{name: "progressive.jpg", img: gray, options: jpeg.Options{Format: jpeg.JPEG, Classic: &classic.Options{Progressive: true}}},
	}

	for index := range fixtures {
		item := &fixtures[index]

		var encoded bytes.Buffer

		err = jpeg.Encode(&encoded, item.img, &item.options)
		check(err)

		err = os.WriteFile(filepath.Join("testdata", item.name), encoded.Bytes(), 0644)
		check(err)
	}

	animation := jpegxl.Animation{
		Animated:         true,
		TicksNumerator:   30000,
		TicksDenominator: 1001,
		LoopCount:        3,
		Frames:           []jpegxl.Frame{{Image: rgba, Duration: 1, Name: "first"}, {Image: rgba, Duration: 7, Name: "last"}},
	}

	var encoded bytes.Buffer

	err = jpegxl.EncodeAnimation(&encoded, &animation, &jpegxl.Options{BitsPerSample: 16, Lossless: true, Effort: 1, Container: true})
	check(err)

	err = os.WriteFile("testdata/animation.jxl", encoded.Bytes(), 0644)
	check(err)

	copyFixture(".cache/turbo/testimages/testorig.jpg", "testdata/upstream.jpg")
	copyFixture(".cache/charls/test/test.jls", "testdata/upstream.jls")
}

func copyFixture(source, destination string) {
	data, err := os.ReadFile(source)
	check(err)

	err = os.WriteFile(destination, data, 0644)
	check(err)
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
