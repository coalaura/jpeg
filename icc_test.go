package jpeg_test

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/coalaura/jpeg/classic"
	"github.com/coalaura/jpeg/jpegxl"
)

func TestClassicICC(t *testing.T) {
	profile := repetitiveICC(t, 65540)
	source := iccJPEG(t)

	want, err := classic.DecodeSamples(bytes.NewReader(source), nil)
	if err != nil {
		t.Fatal(err)
	}

	segments := iccSegments(profile)
	profiles := [][]byte{nil, append(bytes.Clone(segments[1]), segments[0]...)}

	comment := jpegMarker(0xfe, bytes.Repeat([]byte("c"), 65533))
	comments := bytes.Repeat(comment, 65)

	// Inject after SOF and before SOS so sniffing cannot mask decode behavior.
	scan := bytes.Index(source, []byte{0xff, 0xda})

	for index, markers := range profiles {
		data := append(bytes.Clone(source[:scan]), comments...)
		data = append(data, markers...)
		data = append(data, source[scan:]...)
		path := filepath.Join(t.TempDir(), "input.jpg")

		err = os.WriteFile(path, data, 0o600)
		if err != nil {
			t.Fatal(err)
		}

		file, openError := os.Open(path)
		if openError != nil {
			t.Fatal(openError)
		}

		t.Cleanup(func() {
			file.Close()
		})

		readers := []io.Reader{file, io.LimitReader(bytes.NewReader(data), int64(len(data)))}

		for _, reader := range readers {
			got, decodeError := classic.DecodeSamples(reader, &classic.DecodeOptions{ReadICC: true})
			if decodeError != nil {
				t.Fatal(decodeError)
			}

			expected := profile

			if index == 0 {
				expected = nil
			}

			if !reflect.DeepEqual(got.Values, want.Values) || !bytes.Equal(got.ICC, expected) || len(got.Markers) != 0 {
				t.Fatal("selective ICC decode changed pixels, profile, or retained unrelated markers")
			}
		}

		_, err = classic.DecodeSamples(bytes.NewReader(data), &classic.DecodeOptions{ReadMetadata: true})
		if err == nil || !strings.Contains(err.Error(), "metadata limit") {
			t.Fatalf("all-marker limit: %v", err)
		}

		limits := []classic.DecodeOptions{{ReadICC: true, MaxInputBytes: int64(len(data) - 1)}, {ReadICC: true, MaxPixels: 1}}

		for _, options := range limits {
			_, err = classic.DecodeSamples(io.LimitReader(bytes.NewReader(data), int64(len(data))), &options)
			if err == nil {
				t.Fatal("stream decode ignored a resource limit")
			}
		}
	}

	contradictory := bytes.Clone(segments[1])
	contradictory[17] = 3

	invalid := [][]byte{segments[0], append(bytes.Clone(segments[0]), segments[0]...), append(bytes.Clone(segments[0]), contradictory...), bytes.Join(iccSegments(make([]byte, (4<<20)+1)), nil)}
	messages := []string{"incomplete", "duplicated", "contradictory", "exceeds 4 MiB"}

	for index, markers := range invalid {
		data := append(bytes.Clone(source[:2]), markers...)
		data = append(data, source[2:]...)

		_, err = classic.DecodeSamples(bytes.NewReader(data), &classic.DecodeOptions{ReadICC: true})
		if err == nil || !strings.Contains(err.Error(), messages[index]) {
			t.Fatalf("invalid ICC %d: %v", index, err)
		}
	}

	data := append(bytes.Clone(source[:2]), bytes.Join(segments, nil)...)
	data = append(data, source[2:]...)

	got, err := classic.DecodeSamples(bytes.NewReader(data), &classic.DecodeOptions{ReadICC: true, ReadMetadata: true})
	if err != nil || !bytes.Equal(got.ICC, profile) || len(got.Markers) < 2 {
		t.Fatalf("combined metadata APIs: %v", err)
	}
}

func TestJXLICCReconstruction(t *testing.T) {
	sizes := []int{65540, 1155588}

	for _, size := range sizes {
		checkJXLICCReconstruction(t, repetitiveICC(t, size))
	}
}

func checkJXLICCReconstruction(t *testing.T, profile []byte) {
	t.Helper()

	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))

	for index := 0; index < len(img.Pix); index += 4 {
		copy(img.Pix[index:], []byte{64, 128, 192, 255})
	}

	modes := []bool{false, true}

	for _, lossless := range modes {
		var encoded bytes.Buffer

		err := jpegxl.Encode(&encoded, img, &jpegxl.Options{Lossless: lossless, ICC: profile})
		if err != nil {
			t.Fatalf("image encode: %v (published %d bytes)", err, encoded.Len())
		}

		got, err := jpegxl.DecodeAnimation(bytes.NewReader(encoded.Bytes()), nil)
		if err != nil || !bytes.Equal(got.OriginalICC, profile) {
			t.Fatalf("image ICC round trip: %v", err)
		}

		if lossless {
			pixels, imageError := got.Frames[0].Samples.Image()
			if imageError != nil || !bytes.Equal(got.OutputICC, profile) {
				t.Fatalf("lossless output: %v", imageError)
			}

			for y := range 8 {
				for x := range 8 {
					if color.NRGBAModel.Convert(pixels.At(x, y)) != img.NRGBAAt(x, y) {
						t.Fatal("lossless pixels changed")
					}
				}
			}
		}
	}

	jpeg := iccJPEG(t)
	segments := iccSegments(profile)

	reversed := append(bytes.Clone(segments[1]), segments[0]...)
	reversed = append(reversed, bytes.Join(segments[2:], nil)...)

	orders := [][]byte{bytes.Join(segments, nil), reversed}

	for index, markers := range orders {
		source := append(bytes.Clone(jpeg[:2]), markers...)
		source = append(source, jpeg[2:]...)

		var (
			encoded  bytes.Buffer
			restored bytes.Buffer
		)

		err := jpegxl.RecompressJPEG(&encoded, bytes.NewReader(source), nil)
		if index == 1 && err != nil {
			if encoded.Len() != 0 || !strings.Contains(err.Error(), "out-of-order ICC segments") {
				t.Fatalf("layout error: %v, output=%d", err, encoded.Len())
			}

			continue
		}

		if err != nil {
			t.Fatal(err)
		}

		err = jpegxl.RestoreJPEG(&restored, bytes.NewReader(encoded.Bytes()), nil)
		if err != nil || !bytes.Equal(source, restored.Bytes()) {
			t.Fatalf("JPEG restoration changed original bytes/marker order: %v", err)
		}

		got, err := jpegxl.DecodeAnimation(bytes.NewReader(encoded.Bytes()), nil)
		if err != nil || !bytes.Equal(got.OriginalICC, profile) {
			t.Fatalf("recompressed profile: %v", err)
		}
	}
}

func repetitiveICC(t *testing.T, size int) []byte {
	t.Helper()

	source, err := jpegxl.DecodeAnimation(bytes.NewReader(fixtureBytes(t, "rgba16.jxl")), nil)
	if err != nil {
		t.Fatal(err)
	}

	base := source.OutputICC
	count := int(binary.BigEndian.Uint32(base[128:]))
	end := 132 + count*12

	profile := make([]byte, size)
	copy(profile, base[:end])
	copy(profile[end+12:], base[end:])
	binary.BigEndian.PutUint32(profile, uint32(size))
	binary.BigEndian.PutUint32(profile[128:], uint32(count+1))
	clear(profile[84:100]) // The profile ID no longer describes the augmented profile.

	for index := range count {
		entry := profile[132+index*12:]
		binary.BigEndian.PutUint32(entry[4:], binary.BigEndian.Uint32(entry[4:])+12)
	}

	start := len(base) + 12

	copy(profile[end:], "priv")
	binary.BigEndian.PutUint32(profile[end+4:], uint32(start))
	binary.BigEndian.PutUint32(profile[end+8:], uint32(size-start))
	copy(profile[start:], "data")
	profile[start+11] = 1

	return profile
}

func iccJPEG(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 8, 8))

	for index := 0; index < len(img.Pix); index += 4 {
		copy(img.Pix[index:], []byte{64, 128, 192, 255})
	}

	var encoded bytes.Buffer

	err := stdjpeg.Encode(&encoded, img, &stdjpeg.Options{Quality: 95})
	if err != nil {
		t.Fatal(err)
	}

	return encoded.Bytes()
}

func iccSegments(profile []byte) [][]byte {
	count := (len(profile) + 65518) / 65519
	segments := make([][]byte, count)

	for index := range segments {
		length := min(len(profile), 65519)

		payload := make([]byte, 14+length)
		copy(payload, "ICC_PROFILE\x00")
		payload[12] = byte(index + 1)
		payload[13] = byte(count)
		copy(payload[14:], profile[:length])

		segments[index] = jpegMarker(0xe2, payload)
		profile = profile[length:]
	}

	return segments
}

func jpegMarker(marker byte, payload []byte) []byte {
	result := make([]byte, 4+len(payload))
	result[0] = 0xff
	result[1] = marker
	binary.BigEndian.PutUint16(result[2:], uint16(len(payload)+2))
	copy(result[4:], payload)

	return result
}
