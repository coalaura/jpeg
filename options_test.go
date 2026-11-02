package jpeg_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"reflect"
	"slices"
	"testing"

	"github.com/coalaura/jpeg"
	"github.com/coalaura/jpeg/classic"
	"github.com/coalaura/jpeg/internal/pixels"
	"github.com/coalaura/jpeg/jpeg2000"
	"github.com/coalaura/jpeg/jpegls"
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
			samples.Values[index] = uint16(index % 4 * 50)
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
	if err != nil || !bytes.Equal(jp2.ICC, source.OutputICC) || jp2.Components[3].Alpha != 1 {
		t.Fatalf("JP2 ICC: %v", err)
	}

	config, err := jpeg2000.DecodeConfig(bytes.NewReader(encoded.Bytes()))
	if err != nil || config.ColorModel != color.NRGBA64Model || config.Width != img.Bounds().Dx() || config.Height != img.Bounds().Dy() {
		t.Fatalf("JP2 ICC config: %+v %v", config, err)
	}

	display, err := jpeg2000.Decode(bytes.NewReader(encoded.Bytes()))
	if err != nil || !reflect.DeepEqual(display, img) {
		t.Fatalf("JP2 ICC display: %v", err)
	}

	// Retain the valid matrix profile, but replace its RGB transfer curves
	// with gamma 1.0: these samples are linear RGB, not sRGB.
	profile := bytes.Clone(source.OutputICC)
	tags := int(binary.BigEndian.Uint32(profile[128:]))
	curves := 0

	for index := range tags {
		entry := profile[132+index*12 : 144+index*12]
		offset := int(binary.BigEndian.Uint32(entry[4:]))
		size := int(binary.BigEndian.Uint32(entry[8:]))

		switch string(entry[:4]) {
		case "rTRC", "gTRC", "bTRC":
			if size < 16 || offset+size > len(profile) {
				t.Fatal("fixture profile has an invalid transfer curve")
			}

			clear(profile[offset : offset+size])
			copy(profile[offset:], []byte{'c', 'u', 'r', 'v', 0, 0, 0, 0, 0, 0, 0, 1, 1, 0, 0, 0})
			binary.BigEndian.PutUint32(entry[8:], 16)
			curves++
		case "cicp":
			profile[offset+9] = 8 // Linear transfer characteristics.
		}
	}

	if curves != 3 {
		t.Fatal("fixture profile must have three RGB transfer curves")
	}

	clear(profile[84:100]) // An unset profile ID is valid after editing a profile.
	associations := []int{0, 1, 2}

	for _, association := range associations {
		channels := 3

		if association != 0 {
			channels = 4
		}

		native := *jp2
		native.ColorSpace = jpeg2000.ICCRGB
		native.ICC = profile
		native.Components = slices.Clone(jp2.Components[:channels])

		if association != 0 {
			native.Components[3].Alpha = association
		}

		encoded.Reset()

		err = jpeg2000.EncodeNative(&encoded, &native, nil)
		if err != nil {
			t.Fatal(err)
		}

		profiled, err := jpeg2000.DecodeNative(bytes.NewReader(encoded.Bytes()), nil)
		if err != nil || profiled.ColorSpace != jpeg2000.ICCRGB || !bytes.Equal(profiled.ICC, profile) || !reflect.DeepEqual(profiled.Components, native.Components) {
			t.Fatalf("linear RGB decode, alpha %d: %v", association, err)
		}

		encoded.Reset()

		err = jpeg2000.EncodeNative(&encoded, profiled, nil)
		if err != nil {
			t.Fatal(err)
		}

		roundtrip, err := jpeg2000.DecodeNative(bytes.NewReader(encoded.Bytes()), nil)
		if err != nil || !reflect.DeepEqual(roundtrip, profiled) {
			t.Fatalf("linear RGB roundtrip, alpha %d: %v", association, err)
		}

		if association == 2 {
			continue
		}

		config, err := jpeg2000.DecodeConfig(bytes.NewReader(encoded.Bytes()))
		if err != nil || config.ColorModel != color.NRGBA64Model || config.Width != img.Bounds().Dx() || config.Height != img.Bounds().Dy() {
			t.Fatalf("linear RGB config, alpha %d: %+v %v", association, config, err)
		}

		display, err := jpeg2000.Decode(bytes.NewReader(encoded.Bytes()))
		if err != nil {
			t.Fatal(err)
		}

		for index := range native.Components[0].Values {
			expected := color.NRGBA64{R: uint16(native.Components[0].Values[index]), G: uint16(native.Components[1].Values[index]), B: uint16(native.Components[2].Values[index]), A: 65535}

			if association == 1 {
				expected.A = uint16(native.Components[3].Values[index])
			}

			if display.At(index%config.Width, index/config.Width) != expected {
				t.Fatalf("linear RGB display changed sample %d, alpha %d", index, association)
			}
		}
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

func TestJP2ReversiblePrecision(t *testing.T) {
	signs := []bool{false, true}
	levels := []int{1, 5}
	transforms := []bool{false, true}
	precisions := []int{24, 25, 27}

	for _, signed := range signs {
		for _, resolutions := range levels {
			for _, transform := range transforms {
				for _, precision := range precisions {
					native := jpeg2000.NativeImage{Bounds: image.Rect(0, 0, 16, 16), ColorSpace: jpeg2000.SRGB, Components: make([]jpeg2000.Component, 3)}
					maximum := int32(1<<precision - 1)

					for channel := range native.Components {
						component := &native.Components[channel]
						*component = jpeg2000.Component{Width: 16, Height: 16, DX: 1, DY: 1, BitsPerSample: precision, Signed: signed, Values: make([]int32, 256)}

						for index := range component.Values {
							component.Values[index] = int32(int64(index) * int64(maximum) / 255)

							if signed {
								component.Values[index] -= 1 << (precision - 1)
							}
						}
					}

					var encoded bytes.Buffer

					err := jpeg2000.EncodeNative(&encoded, &native, &jpeg2000.Options{Resolutions: resolutions, MultiComponentTransform: new(transform)})

					if precision > 25 {
						if err == nil || encoded.Len() != 0 {
							t.Fatalf("%d-bit reversible input accepted: %v", precision, err)
						}

						continue
					}

					if err != nil {
						t.Fatalf("precision=%d signed=%v resolutions=%d transform=%v: %v", precision, signed, resolutions, transform, err)
					}

					decoded, err := jpeg2000.DecodeNative(bytes.NewReader(encoded.Bytes()), nil)
					if err != nil {
						t.Fatal(err)
					}

					for channel := range native.Components {
						if !reflect.DeepEqual(native.Components[channel].Values, decoded.Components[channel].Values) {
							t.Fatalf("inexact precision=%d signed=%v resolutions=%d transform=%v channel=%d", precision, signed, resolutions, transform, channel)
						}
					}
				}
			}
		}
	}

	// The reported unsigned 27-bit grayscale ramp must fail before output.
	native := jpeg2000.NativeImage{Bounds: image.Rect(0, 0, 16, 16), ColorSpace: jpeg2000.Gray, Components: []jpeg2000.Component{{Width: 16, Height: 16, DX: 1, DY: 1, BitsPerSample: 27, Values: make([]int32, 256)}}}

	for index := range native.Components[0].Values {
		native.Components[0].Values[index] = int32(int64(index) * ((1 << 27) - 1) / 255)
	}

	var encoded bytes.Buffer

	err := jpeg2000.EncodeNative(&encoded, &native, nil)
	if err == nil || encoded.Len() != 0 {
		t.Fatalf("27-bit grayscale ramp: %v", err)
	}

	// High-frequency data can exceed Tier 1's coefficient range after the
	// reversible transforms, even when the input precision itself is supported.
	native.ColorSpace = jpeg2000.SRGB
	native.Components = make([]jpeg2000.Component, 3)

	for channel := range native.Components {
		component := &native.Components[channel]
		*component = jpeg2000.Component{Width: 16, Height: 16, DX: 1, DY: 1, BitsPerSample: 25, Values: make([]int32, 256)}

		for index := range component.Values {
			if (index%16+index/16+channel)%2 != 0 {
				component.Values[index] = 1<<25 - 1
			}
		}
	}

	err = jpeg2000.EncodeNative(&encoded, &native, nil)
	if err == nil || encoded.Len() != 0 {
		t.Fatalf("out-of-range reversible coefficients: %v", err)
	}
}

func TestNativeAlphaAssociation(t *testing.T) {
	values := []uint16{64, 0, 0, 128}
	native := jpeg2000.NativeImage{Bounds: image.Rect(0, 0, 1, 1), ColorSpace: jpeg2000.SRGB, Components: make([]jpeg2000.Component, 4)}

	for channel, value := range values {
		native.Components[channel] = jpeg2000.Component{Width: 1, Height: 1, DX: 1, DY: 1, BitsPerSample: 8, Values: []int32{int32(value)}}
	}

	native.Components[3].Alpha = 2

	var encoded bytes.Buffer

	err := jpeg2000.EncodeNative(&encoded, &native, nil)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := jpeg2000.DecodeNative(bytes.NewReader(encoded.Bytes()), nil)
	if err != nil || decoded.Components[3].Alpha != 2 {
		t.Fatalf("JP2 associated alpha: %v", err)
	}

	for channel, value := range values {
		if decoded.Components[channel].Values[0] != int32(value) {
			t.Fatalf("JP2 channel %d changed", channel)
		}
	}

	encoded.Reset()

	samples := jpegxl.Samples{Width: 1, Height: 1, Channels: 4, BitsPerSample: 8, PremultipliedAlpha: true, Unsigned: values}

	err = jpegxl.EncodeSamples(&encoded, &samples, &jpegxl.Options{Lossless: true, Effort: 1})
	if err != nil {
		t.Fatal(err)
	}

	animation, err := jpegxl.DecodeAnimation(bytes.NewReader(encoded.Bytes()), nil)
	if err != nil {
		t.Fatal(err)
	}

	actual := animation.Frames[0].Samples
	if !actual.PremultipliedAlpha || !reflect.DeepEqual(actual.Unsigned, values) {
		t.Fatalf("JXL associated alpha: %+v", actual)
	}
}

func TestDefaultLosslessPrecision(t *testing.T) {
	gray := image.NewGray16(image.Rect(0, 0, 2, 2))
	gray.SetGray16(0, 0, color.Gray16{Y: 32769})

	rgba := image.NewRGBA64(gray.Bounds())
	rgba.SetRGBA64(0, 0, color.RGBA64{R: 32769, A: 65535})

	nrgba := image.NewNRGBA64(gray.Bounds())
	nrgba.SetNRGBA64(0, 0, color.NRGBA64{R: 32769, A: 65535})

	images := []image.Image{gray, rgba, nrgba}
	precisions := []int{0, 8}

	for _, precision := range precisions {
		formats := []jpeg.Options{
			{Format: jpeg.JP2, JPEG2000: &jpeg2000.Options{BitsPerSample: precision}},
			{Format: jpeg.JPEGLS, JPEGLS: &jpegls.Options{BitsPerSample: precision, RGBA: true}},
			{Format: jpeg.JXLContainer, JPEGXL: &jpegxl.Options{BitsPerSample: precision, Lossless: true, Effort: 1}},
		}

		for index := range formats {
			for _, img := range images {
				var encoded bytes.Buffer

				err := jpeg.Encode(&encoded, img, &formats[index])
				if err != nil {
					t.Fatalf("%s %T precision=%d: %v", formats[index].Format, img, precision, err)
				}

				decoded, err := jpeg.DecodeWithOptions(bytes.NewReader(encoded.Bytes()), &jpeg.DecodeOptions{JPEGLS: &jpegls.DecodeOptions{RGBA: true}})
				if err != nil {
					t.Fatal(err)
				}

				red, _, _, _ := decoded.At(0, 0).RGBA()
				expected := uint32(32769)

				if precision == 8 {
					expected = 128 * 257
				}

				if red != expected {
					t.Fatalf("%s %T precision=%d: got %d want %d", formats[index].Format, img, precision, red, expected)
				}
			}
		}
	}
}

func TestExplicitJXLCodestream(t *testing.T) {
	images := []image.Image{image.NewGray(image.Rect(0, 0, 2, 2)), image.NewGray16(image.Rect(0, 0, 2, 2))}

	for _, img := range images {
		var encoded bytes.Buffer

		err := jpeg.Encode(&encoded, img, &jpeg.Options{Format: jpeg.JXLCodestream, JPEGXL: &jpegxl.Options{Lossless: true, Effort: 1}})
		if err != nil {
			if encoded.Len() != 0 {
				t.Fatal("raw codestream error wrote output")
			}

			continue
		}

		if !bytes.HasPrefix(encoded.Bytes(), []byte{0xff, 0x0a}) {
			t.Fatalf("%T produced a container", img)
		}
	}
}

func TestLargeJPEGMaxPixels(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 4097, 4096))

	var encoded bytes.Buffer

	err := stdjpeg.Encode(&encoded, img, nil)
	if err != nil {
		t.Fatal(err)
	}

	options := &classic.DecodeOptions{MaxPixels: 20000000}

	config, err := classic.DecodeConfigWithOptions(bytes.NewReader(encoded.Bytes()), options)
	if err != nil || config.Width != 4097 || config.Height != 4096 {
		t.Fatalf("large config: %+v %v", config, err)
	}

	decoded, err := classic.DecodeWithOptions(bytes.NewReader(encoded.Bytes()), options)
	if err != nil || decoded.Bounds() != img.Bounds() {
		t.Fatalf("large decode: %v", err)
	}

	options.MaxInputBytes = 1

	_, err = classic.DecodeWithOptions(bytes.NewReader(encoded.Bytes()), options)
	if !errors.Is(err, pixels.ErrLimit) {
		t.Fatalf("independent input limit: %v", err)
	}
}

func TestJP2PaletteConfig(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 2, 2))
	copy(img.Pix, []byte{0, 1, 1, 0})

	var encoded bytes.Buffer

	err := jpeg2000.Encode(&encoded, img, nil)
	if err != nil {
		t.Fatal(err)
	}

	palette := make([]byte, 0, encoded.Len()+64)
	data := encoded.Bytes()

	for len(data) >= 8 {
		length := int(binary.BigEndian.Uint32(data))
		box := data[:length]
		data = data[length:]

		if string(box[4:8]) != "jp2h" {
			palette = append(palette, box...)

			continue
		}

		header := make([]byte, 0, length+64)
		children := box[8:]

		for len(children) >= 8 {
			childLength := int(binary.BigEndian.Uint32(children))

			if string(children[4:8]) != "colr" {
				header = append(header, children[:childLength]...)
			}

			children = children[childLength:]
		}

		header = appendJP2Box(header, "colr", []byte{1, 0, 0, 0, 0, 0, 16})
		header = appendJP2Box(header, "pclr", []byte{0, 2, 3, 7, 7, 7, 255, 0, 0, 0, 255, 0})
		header = appendJP2Box(header, "cmap", []byte{0, 0, 1, 0, 0, 0, 1, 1, 0, 0, 1, 2})
		palette = appendJP2Box(palette, "jp2h", header)
	}

	config, err := jpeg2000.DecodeConfig(bytes.NewReader(palette))
	if err != nil || config.Width != 2 || config.Height != 2 || config.ColorModel != color.NRGBAModel {
		t.Fatalf("expanded palette config: %+v %v", config, err)
	}

	decoded, err := jpeg2000.Decode(bytes.NewReader(palette))
	if err != nil {
		t.Fatal(err)
	}

	if decoded.ColorModel() != config.ColorModel || decoded.At(0, 0) != (color.NRGBA{R: 255, A: 255}) || decoded.At(1, 0) != (color.NRGBA{G: 255, A: 255}) {
		t.Fatal("palette expansion differs from config or entries")
	}

	// Keep a valid header, but make the tile undecodable. The palette limit
	// must take precedence over any error encountered while decoding pixels.
	oversized := make([]byte, 0, len(palette)+512)
	data = palette

	for len(data) >= 8 {
		length := int(binary.BigEndian.Uint32(data))
		box := data[:length]
		data = data[length:]

		switch string(box[4:8]) {
		case "jp2h":
			header := make([]byte, 0, length+512)
			children := box[8:]

			for len(children) >= 8 {
				childLength := int(binary.BigEndian.Uint32(children))
				kind := string(children[4:8])

				if kind != "pclr" && kind != "cmap" {
					header = append(header, children[:childLength]...)
				}

				children = children[childLength:]
			}

			entries := make([]byte, 3+64*3)
			copy(entries, []byte{0, 2, 64})
			mapping := make([]byte, 0, 64*4)

			for channel := range 64 {
				entries[3+channel] = 7
				mapping = append(mapping, 0, 0, 1, byte(channel))
			}

			header = appendJP2Box(header, "pclr", entries)
			header = appendJP2Box(header, "cmap", mapping)
			oversized = appendJP2Box(oversized, "jp2h", header)
		case "jp2c":
			tile := bytes.Index(box[8:], []byte{0xff, 0x90})
			if tile < 0 {
				t.Fatal("missing JPEG 2000 tile")
			}

			oversized = appendJP2Box(oversized, "jp2c", box[8:8+tile+2])
		default:
			oversized = append(oversized, box...)
		}
	}

	_, err = jpeg2000.DecodeConfig(bytes.NewReader(oversized))
	if err == nil || err.Error() != "jpeg2000: resource limit exceeded" {
		t.Fatalf("oversized palette config must reject before tile decoding: %v", err)
	}

	_, err = jpeg2000.DecodeNative(bytes.NewReader(oversized), nil)
	if err == nil || err.Error() != "jpeg2000: resource limit exceeded" {
		t.Fatalf("oversized palette decode must reject before tile decoding: %v", err)
	}
}

func TestJPEGLSScanErrorBounds(t *testing.T) {
	var combined []byte

	bounds := []int{0, 1, 3}

	for index, bound := range bounds {
		samples := jpegls.Samples{Width: 2, Height: 2, Components: 1, BitsPerSample: 8, Values: []uint16{0, 64, 128, 255}}

		var encoded bytes.Buffer

		err := jpegls.EncodeSamples(&encoded, &samples, &jpegls.Options{NEAR: bound})
		if err != nil {
			t.Fatal(err)
		}

		data := encoded.Bytes()

		frame := bytes.Index(data, []byte{0xff, 0xf7})
		scan := bytes.Index(data, []byte{0xff, 0xda})

		if frame < 0 || scan < 0 {
			t.Fatal("missing JPEG-LS frame/scan")
		}

		if index == 0 {
			combined = append(combined, data[:frame+2]...)
			combined = append(combined, 0, 17)
			combined = append(combined, data[frame+4:frame+9]...)
			combined = append(combined, 3, 1, 0x11, 0, 2, 0x11, 0, 3, 0x11, 0)
		}

		data[scan+5] = byte(index + 1)
		combined = append(combined, data[scan:len(data)-2]...)
	}

	combined = append(combined, 0xff, 0xd9)

	decoded, err := jpegls.DecodeSamples(bytes.NewReader(combined), nil)
	if err != nil || !reflect.DeepEqual(decoded.NEAR, bounds) {
		t.Fatalf("per-scan NEAR: %+v %v", decoded, err)
	}
}

func appendJP2Box(dst []byte, kind string, payload []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(payload)+8))
	dst = append(dst, kind...)

	return append(dst, payload...)
}
