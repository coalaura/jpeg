package jpeg_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"io"
	"os"
	"reflect"
	"testing"

	"github.com/coalaura/jpeg"
	"github.com/coalaura/jpeg/classic"
	"github.com/coalaura/jpeg/jpeg2000"
	"github.com/coalaura/jpeg/jpegls"
	"github.com/coalaura/jpeg/jpegxl"
)

type failedIO struct{}

type shortWriter struct{}

var errIO = errors.New("test I/O failure")

func (failedIO) Read([]byte) (int, error) {
	return 0, errIO
}

func (failedIO) Write([]byte) (int, error) {
	return 0, errIO
}

func (shortWriter) Write(data []byte) (int, error) {
	return len(data) - 1, nil
}

// The ten synthetic fixtures are specified by internal/fixtures. upstream.jpg
// is libjpeg-turbo 3.2.1 testimages/testorig.jpg (license in internal/native/turbo).
// upstream.jls is CharLS 2.4.2 test/test.jls (license in internal/native/charls).
func TestFixtures(t *testing.T) {
	names := []string{"gray16.jpg", "gray16.jls", "gray16.jp2", "gray16.j2k", "gray16.jxl"}
	values := []uint16{0, 1, 255, 256, 32768, 32769, 65535, 65534, 12345}
	options := jpeg.DecodeOptions{JPEG2000: &jpeg2000.DecodeOptions{RawColorSpace: jpeg2000.Gray}}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			data := fixtureBytes(t, name)

			config, err := jpeg.DecodeConfigWithOptions(bytes.NewReader(data), &options)
			if err != nil || config.Width != 3 || config.Height != 3 {
				t.Fatalf("config=%+v error=%v", config, err)
			}

			img, err := jpeg.DecodeWithOptions(bytes.NewReader(data), &options)
			if err != nil {
				t.Fatal(err)
			}

			for index, expected := range values {
				actual := color.Gray16Model.Convert(img.At(index%3, index/3)).(color.Gray16).Y
				if actual != expected {
					t.Fatalf("sample %d: %d != %d", index, actual, expected)
				}
			}
		})
	}

	_, err := jpeg.Decode(bytes.NewReader(fixtureBytes(t, "gray16.j2k")))
	if err == nil {
		t.Fatal("raw color must not be guessed")
	}
}

func TestPartialAlpha(t *testing.T) {
	names := []string{"rgba16.jp2", "rgba16.jls", "rgba16.jxl"}
	expected := []color.NRGBA64{{R: 32768, G: 32769, B: 65535, A: 32769}, {R: 257, G: 511, B: 12345, A: 65535}, {R: 17, G: 32769, B: 65534, A: 0}}
	options := jpeg.DecodeOptions{JPEGLS: &jpegls.DecodeOptions{RGBA: true}}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			data := fixtureBytes(t, name)

			config, err := jpeg.DecodeConfigWithOptions(bytes.NewReader(data), &options)
			if err != nil || config.Width != 3 || config.Height != 1 {
				t.Fatalf("config=%+v error=%v", config, err)
			}

			img, err := jpeg.DecodeWithOptions(bytes.NewReader(data), &options)
			if err != nil {
				t.Fatal(err)
			}

			for index, value := range expected {
				actual := color.NRGBA64Model.Convert(img.At(index, 0)).(color.NRGBA64)
				if actual != value {
					t.Fatalf("pixel %d: %+v != %+v", index, actual, value)
				}
			}
		})
	}

	_, err := jpeg.Decode(bytes.NewReader(fixtureBytes(t, "rgba16.jls")))
	if err == nil {
		t.Fatal("four components must require interpretation")
	}
}

func TestJLSInterleaveAndNEAR(t *testing.T) {
	samples := jpegls.Samples{Width: 19, Height: 7, Components: 3, BitsPerSample: 16, Values: make([]uint16, 19*7*3)}

	for index := range samples.Values {
		samples.Values[index] = uint16(index*257 + index%17)
	}

	modes := []jpegls.Interleave{jpegls.Planar, jpegls.Line, jpegls.Sample}
	bounds := []int{0, 3, 255}

	for _, mode := range modes {
		for _, bound := range bounds {
			var encoded bytes.Buffer

			err := jpegls.EncodeSamples(&encoded, &samples, &jpegls.Options{Interleave: new(mode), NEAR: bound})
			if err != nil {
				t.Fatal(err)
			}

			decoded, err := jpegls.DecodeSamples(bytes.NewReader(encoded.Bytes()), nil)
			if err != nil {
				t.Fatal(err)
			}

			for index, expected := range samples.Values {
				difference := int(decoded.Values[index]) - int(expected)
				if difference < -bound || difference > bound {
					t.Fatalf("mode=%d near=%d sample=%d difference=%d", mode, bound, index, difference)
				}
			}

			for _, reported := range decoded.NEAR {
				if reported != bound {
					t.Fatalf("NEAR=%d, want %d", reported, bound)
				}
			}
		}
	}

	_, err := jpegls.DecodeSamples(bytes.NewReader(fixtureBytes(t, "upstream.jls")), nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestClassicModesAndIndependentDecode(t *testing.T) {
	data := fixtureBytes(t, "upstream.jpg")

	config, err := classic.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != 227 || config.Height != 149 {
		t.Fatalf("config=%+v error=%v", config, err)
	}

	img, err := stdjpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	var encoded bytes.Buffer

	err = classic.Encode(&encoded, img, &classic.Options{Quality: new(100), Progressive: true, OptimizeHuffman: true, RestartInterval: 5, Subsampling: classic.Subsample444, Markers: []classic.Marker{{ID: 0xfe, Data: []byte("comment")}}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = stdjpeg.Decode(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}

	samples, err := classic.DecodeSamples(bytes.NewReader(encoded.Bytes()), &classic.DecodeOptions{ReadMetadata: true, ScaleDenominator: 2})
	if err != nil || !samples.Progressive || samples.Lossless || samples.Width != 114 || samples.Height != 75 {
		t.Fatalf("scaled progressive decode: %v", err)
	}

	found := false

	for _, marker := range samples.Markers {
		if marker.ID == 0xfe && string(marker.Data) == "comment" {
			found = true
		}
	}

	if !found {
		t.Fatal("missing comment")
	}

	_, err = jpeg.Decode(bytes.NewReader(fixtureBytes(t, "progressive.jpg")))
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnimationAndReconstruction(t *testing.T) {
	animation, err := jpegxl.DecodeAnimation(bytes.NewReader(fixtureBytes(t, "animation.jxl")), nil)
	if err != nil {
		t.Fatal(err)
	}

	if !animation.Animated || len(animation.Frames) != 2 || animation.TicksNumerator != 30000 || animation.TicksDenominator != 1001 || animation.LoopCount != 3 || animation.Frames[0].Duration != 1 || animation.Frames[1].Duration != 7 || animation.Frames[1].Name != "last" {
		t.Fatalf("animation timing: %+v", animation)
	}

	data := fixtureBytes(t, "upstream.jpg")

	var compressed bytes.Buffer

	err = jpegxl.RecompressJPEG(&compressed, bytes.NewReader(data), nil)
	if err != nil {
		t.Fatal(err)
	}

	var restored bytes.Buffer

	err = jpegxl.RestoreJPEG(&restored, bytes.NewReader(compressed.Bytes()), nil)
	if err != nil || !bytes.Equal(restored.Bytes(), data) {
		t.Fatalf("JPEG reconstruction differs: %v", err)
	}

	_, err = jpegxl.DecodeAnimation(bytes.NewReader(fixtureBytes(t, "animation.jxl")), &jpegxl.DecodeOptions{MaxFrames: 1})
	if err == nil {
		t.Fatal("frame limit ignored")
	}
}

func TestNativeSignedAndHDR(t *testing.T) {
	component := jpeg2000.Component{Width: 3, Height: 1, DX: 1, DY: 1, BitsPerSample: 16, Signed: true, Values: []int32{-32768, -1, 32767}}
	native := jpeg2000.NativeImage{Bounds: image.Rect(0, 0, 3, 1), ColorSpace: jpeg2000.Gray, Components: []jpeg2000.Component{component}}

	var encoded bytes.Buffer

	err := jpeg2000.EncodeNative(&encoded, &native, nil)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := jpeg2000.DecodeNative(bytes.NewReader(encoded.Bytes()), nil)
	if err != nil || !reflect.DeepEqual(decoded.Components[0].Values, component.Values) {
		t.Fatalf("signed samples: %v", err)
	}

	_, err = decoded.Image()
	if err == nil {
		t.Fatal("signed display conversion must be explicit")
	}

	encoded.Reset()
	hdr := jpegxl.Samples{Width: 3, Height: 1, Channels: 1, BitsPerSample: 32, ExponentBits: 8, Float: []float32{-0.5, 1.25, 100}}

	err = jpegxl.EncodeSamples(&encoded, &hdr, &jpegxl.Options{Lossless: true, Effort: 1, Linear: true})
	if err != nil {
		t.Fatal(err)
	}

	animation, err := jpegxl.DecodeAnimation(bytes.NewReader(encoded.Bytes()), nil)
	if err != nil || !reflect.DeepEqual(animation.Frames[0].Samples.Float, hdr.Float) {
		t.Fatalf("HDR samples: %v", err)
	}

	_, err = animation.Frames[0].Samples.Image()
	if err == nil {
		t.Fatal("HDR display conversion must be explicit")
	}
}

func TestFailuresAndLimits(t *testing.T) {
	formats := []jpeg.Options{{Format: jpeg.JPEG}, {Format: jpeg.JPEGLS}, {Format: jpeg.JP2}, {Format: jpeg.JXLCodestream, JPEGXL: &jpegxl.Options{Effort: 1}}}
	img := image.NewGray(image.Rect(0, 0, 3, 3))

	for index := range formats {
		err := jpeg.Encode(failedIO{}, img, &formats[index])
		if !errors.Is(err, errIO) {
			t.Fatalf("writer failure: %v", err)
		}

		err = jpeg.Encode(shortWriter{}, img, &formats[index])
		if !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("short writer: %v", err)
		}
	}

	_, err := jpeg.Decode(failedIO{})
	if !errors.Is(err, errIO) {
		t.Fatalf("reader failure: %v", err)
	}

	names := []string{"gray16.jpg", "gray16.jls", "gray16.jp2", "gray16.jxl"}
	options := jpeg.DecodeOptions{Classic: &classic.DecodeOptions{MaxPixels: 2}, JPEGLS: &jpegls.DecodeOptions{MaxPixels: 2}, JPEG2000: &jpeg2000.DecodeOptions{MaxPixels: 2}, JPEGXL: &jpegxl.DecodeOptions{MaxPixels: 2}}

	for _, name := range names {
		data := fixtureBytes(t, name)

		_, err = jpeg.DecodeWithOptions(bytes.NewReader(data), &options)
		if err == nil {
			t.Fatalf("%s ignored pixel limit", name)
		}

		_, err = jpeg.Decode(bytes.NewReader(data[:len(data)/2]))
		if err == nil {
			t.Fatalf("%s accepted truncated image", name)
		}

		_, err = jpeg.Decode(io.MultiReader(bytes.NewReader(data), failedIO{}))
		if !errors.Is(err, errIO) {
			t.Fatalf("%s lost reader error: %v", name, err)
		}
	}
}

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}

	return data
}
