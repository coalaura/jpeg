package jpeg_test

import (
	"bytes"
	"errors"
	"image"
	stdjpeg "image/jpeg"
	"reflect"
	"testing"

	"github.com/coalaura/jpeg/classic"
	"github.com/coalaura/jpeg/internal/pixels"
	"github.com/coalaura/jpeg/jpeg2000"
	"github.com/coalaura/jpeg/jpegxl"
)

func TestClassicPrecisionAndCMYK(t *testing.T) {
	for precision := 2; precision <= 16; precision++ {
		maximum := uint16(1<<precision - 1)
		samples := classic.Samples{Width: 3, Height: 1, BitsPerSample: precision, ColorSpace: classic.Grayscale, Values: []uint16{0, maximum / 2, maximum}}

		var encoded bytes.Buffer

		err := classic.EncodeSamples(&encoded, &samples, &classic.Options{Lossless: &classic.Lossless{Predictor: 7}})
		if err != nil {
			t.Fatalf("%d-bit encoding: %v", precision, err)
		}

		decoded, err := classic.DecodeSamples(bytes.NewReader(encoded.Bytes()), nil)
		if err != nil || !reflect.DeepEqual(samples.Values, decoded.Values) {
			t.Fatalf("%d-bit lossless: %v", precision, err)
		}
	}

	spaces := []classic.ColorSpace{classic.CMYK, classic.YCCK}

	for _, space := range spaces {
		samples := classic.Samples{Width: 8, Height: 8, BitsPerSample: 8, ColorSpace: classic.CMYK, Values: make([]uint16, 8*8*4)}

		for index := range samples.Values {
			samples.Values[index] = uint16(index%4 * 50)
		}

		var encoded bytes.Buffer

		err := classic.EncodeSamples(&encoded, &samples, &classic.Options{ColorSpace: space, Quality: new(100)})
		if err != nil {
			t.Fatal(err)
		}

		decoded, err := classic.DecodeSamples(bytes.NewReader(encoded.Bytes()), nil)
		if err != nil || decoded.SourceColorSpace != space {
			t.Fatalf("CMYK/YCCK %d: %v", space, err)
		}
		// DCT/YCCK involves rounding: compare exact decoded components with the
		// independent standard library, rather than assuming quality 100 is lossless.
		independent, err := stdjpeg.Decode(bytes.NewReader(encoded.Bytes()))
		if err != nil {
			t.Fatal(err)
		}

		cmyk, ok := independent.(*image.CMYK)
		if !ok {
			t.Fatalf("independent decoder returned %T", independent)
		}

		for index, value := range decoded.Values {
			if value != uint16(cmyk.Pix[index]) {
				t.Fatalf("CMYK/YCCK %d component %d: %d != %d", space, index, value, cmyk.Pix[index])
			}
		}
	}

	var encoded bytes.Buffer

	err := classic.Encode(&encoded, image.NewGray16(image.Rect(0, 0, 8, 8)), &classic.Options{BitsPerSample: 12, Progressive: true})
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := classic.DecodeSamples(bytes.NewReader(encoded.Bytes()), nil)
	if err != nil || decoded.BitsPerSample != 12 || !decoded.Progressive {
		t.Fatalf("12-bit progressive: %v", err)
	}
}

func TestJP2ResolutionAndRegion(t *testing.T) {
	var encoded bytes.Buffer

	err := jpeg2000.Encode(&encoded, image.NewGray(image.Rect(0, 0, 32, 32)), &jpeg2000.Options{TileWidth: 16, TileHeight: 16, Ratios: []float64{8, 1}, Progression: jpeg2000.RPCL})
	if err != nil {
		t.Fatal(err)
	}

	options := []*jpeg2000.DecodeOptions{{Reduce: 1}, {Region: new(image.Rect(4, 6, 16, 20))}}
	expected := []image.Point{{X: 16, Y: 16}, {X: 12, Y: 14}}

	for index, option := range options {
		config, err := jpeg2000.DecodeConfigWithOptions(bytes.NewReader(encoded.Bytes()), option)
		if err != nil || config.Width != expected[index].X || config.Height != expected[index].Y {
			t.Fatalf("config %d: %+v %v", index, config, err)
		}

		img, err := jpeg2000.DecodeWithOptions(bytes.NewReader(encoded.Bytes()), option)
		if err != nil || img.Bounds().Size() != expected[index] {
			t.Fatalf("decode %d: %v", index, err)
		}
	}
}

func TestProfilesAndBoxes(t *testing.T) {
	source, err := jpegxl.DecodeAnimation(bytes.NewReader(fixtureBytes(t, "rgba16.jxl")), nil)
	if err != nil {
		t.Fatal(err)
	}

	box := jpegxl.Box{Type: "xml ", Data: bytes.Repeat([]byte("metadata "), 10000), Compress: true}

	var encoded bytes.Buffer

	err = jpegxl.EncodeSamples(&encoded, source.Frames[0].Samples, &jpegxl.Options{Lossless: true, Effort: 1, Container: true, ICC: source.OutputICC, Boxes: []jpegxl.Box{box}})
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := jpegxl.DecodeAnimation(bytes.NewReader(encoded.Bytes()), &jpegxl.DecodeOptions{ReadBoxes: true})
	if err != nil || !bytes.Equal(decoded.OutputICC, source.OutputICC) || len(decoded.Boxes) != 1 || !bytes.Equal(decoded.Boxes[0].Data, box.Data) {
		t.Fatalf("JXL ICC/boxes: %v", err)
	}

	encoded.Reset()

	img, err := source.Frames[0].Samples.Image()
	if err != nil {
		t.Fatal(err)
	}

	err = jpeg2000.Encode(&encoded, img, &jpeg2000.Options{BitsPerSample: 16, ICC: source.OutputICC})
	if err != nil {
		t.Fatal(err)
	}

	jp2, err := jpeg2000.DecodeNative(bytes.NewReader(encoded.Bytes()), nil)
	if err != nil || !bytes.Equal(jp2.ICC, source.OutputICC) {
		t.Fatalf("JP2 ICC: %v", err)
	}
}

func TestJPEGXLResourceAndLosslessOptions(t *testing.T) {
	source := fixtureBytes(t, "upstream.jpg")

	frame := bytes.Index(source, []byte{0xff, 0xc0})
	if frame < 0 {
		t.Fatal("fixture has no baseline frame")
	}

	copy(source[frame+5:frame+9], []byte{0xff, 0xff, 0xff, 0xff})

	config, err := stdjpeg.DecodeConfig(bytes.NewReader(source))
	if err != nil || config.Width != 65535 || config.Height != 65535 {
		t.Fatalf("large header: %+v %v", config, err)
	}

	var encoded bytes.Buffer

	err = jpegxl.RecompressJPEG(&encoded, bytes.NewReader(source), nil)
	if !errors.Is(err, pixels.ErrLimit) {
		t.Fatalf("recompression dimension limit: %v", err)
	}

	err = jpegxl.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 3, 3)), &jpegxl.Options{Lossless: true, AlphaDistance: new(float32(1))})
	if err == nil {
		t.Fatal("lossless coding accepted lossy alpha")
	}

	_, err = jpegxl.DecodeWithOptions(bytes.NewReader(fixtureBytes(t, "gray16.jxl")), &jpegxl.DecodeOptions{MaxMemoryBytes: 1})
	if err == nil {
		t.Fatal("invalid native memory limit accepted")
	}
}
