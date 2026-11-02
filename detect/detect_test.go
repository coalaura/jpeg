package detect_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/coalaura/jpeg/detect"
)

type signatureCase struct {
	name string
	input []byte
	format detect.Format
	bad bool
}

type singleByteReader struct {
	reader *bytes.Reader
}

func (reader singleByteReader) Read(buffer []byte) (int, error) {
	return reader.reader.Read(buffer[:min(1, len(buffer))])
}

func TestSignaturesAndReplay(t *testing.T) {
	cases := []signatureCase{
		{name: "jpeg", input: []byte{255, 216, 255, 224, 0, 4, 255, 247, 255, 192, 0, 8}, format: detect.JPEG},
		{name: "jls after app and comment", input: []byte{255, 216, 255, 224, 0, 4, 255, 192, 255, 254, 0, 3, 42, 255, 247, 0, 8}, format: detect.JPEGLS},
		{name: "jp2", input: []byte("\x00\x00\x00\x0cjP  \r\n\x87\nrest"), format: detect.JP2},
		{name: "j2k", input: []byte{255, 79, 255, 81, 0, 1}, format: detect.J2K},
		{name: "jxl raw", input: []byte{255, 10, 1, 2}, format: detect.JXLCodestream},
		{name: "jxl container", input: []byte("\x00\x00\x00\x0cJXL \r\n\x87\nrest"), format: detect.JXLContainer},
		{name: "empty", bad: true},
		{name: "truncated app", input: []byte{255, 216, 255, 225, 0, 8, 1}, bad: true},
		{name: "invalid length", input: []byte{255, 216, 255, 225, 0, 1}, bad: true},
		{name: "missing frame", input: []byte{255, 216, 255, 217}, bad: true},
	}

	for index := range cases {
		item := &cases[index]

		t.Run(item.name, func(t *testing.T) {
			format, replay, err := detect.Detect(singleByteReader{reader: bytes.NewReader(item.input)})
			if (err != nil) != item.bad || !item.bad && format != item.format {
				t.Fatalf("format=%v error=%v", format, err)
			}

			data, err := io.ReadAll(replay)
			if err != nil || !bytes.Equal(data, item.input) {
				t.Fatalf("replay=%x error=%v", data, err)
			}
		})
	}

	_, _, err := detect.DetectLimit(bytes.NewReader(cases[1].input), 8)
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("scan limit: %v", err)
	}
}
