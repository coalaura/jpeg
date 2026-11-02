// Package detect identifies JPEG-family streams without cgo or codec dependencies.
package detect

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

const (
	Unknown Format = iota
	JPEG
	JPEGLS
	JP2
	J2K
	JXLCodestream
	JXLContainer
)

// DefaultHeaderLimit bounds JPEG marker scanning, including APP and COM data.
const DefaultHeaderLimit = 1 << 20

// Format distinguishes containers from raw codestreams.
type Format uint8

var (
	ErrUnknown     = errors.New("jpeg: unrecognized format")
	ErrHeaderLimit = errors.New("jpeg: header scan limit exceeded")
	ErrMalformed   = errors.New("jpeg: malformed marker sequence")
)

func (format Format) String() string {
	names := [...]string{"unknown", "jpeg", "jpeg-ls", "jp2", "j2k", "jxl-codestream", "jxl-container"}
	if int(format) >= len(names) {
		return names[0]
	}

	return names[format]
}

// Detect returns the format and a reader replaying every consumed byte, including
// on error. It does not require Seek. Use the returned reader for decoding.
func Detect(reader io.Reader) (Format, io.Reader, error) {
	return DetectLimit(reader, DefaultHeaderLimit)
}

// DetectLimit is Detect with an explicit maximum number of inspected bytes.
func DetectLimit(reader io.Reader, maxHeader int) (Format, io.Reader, error) {
	if maxHeader == 0 {
		maxHeader = DefaultHeaderLimit
	}

	prefix := make([]byte, 0, 512)
	format, err := identify(reader, &prefix, maxHeader)

	return format, io.MultiReader(bytes.NewReader(prefix), reader), err
}

func identify(reader io.Reader, prefix *[]byte, limit int) (Format, error) {
	data, err := take(reader, prefix, 2, limit)
	if err != nil {
		return Unknown, err
	}

	if bytes.Equal(data, []byte{0xff, 0x0a}) {
		return JXLCodestream, nil
	}

	if bytes.Equal(data, []byte{0xff, 0x4f}) {
		data, err = take(reader, prefix, 2, limit)
		if err != nil {
			return Unknown, err
		}

		if bytes.Equal(data, []byte{0xff, 0x51}) {
			return J2K, nil
		}

		return Unknown, ErrUnknown
	}

	if !bytes.Equal(data, []byte{0xff, 0xd8}) {
		_, err = take(reader, prefix, 10, limit)
		if err != nil {
			return Unknown, err
		}

		switch string(*prefix) {
		case "\x00\x00\x00\x0cjP  \r\n\x87\n":
			return JP2, nil
		case "\x00\x00\x00\x0cJXL \r\n\x87\n":
			return JXLContainer, nil
		}

		return Unknown, ErrUnknown
	}

	for {
		data, err = take(reader, prefix, 1, limit)
		if err != nil {
			return Unknown, err
		}

		if data[0] != 0xff {
			return Unknown, ErrMalformed
		}

		for data[0] == 0xff {
			data, err = take(reader, prefix, 1, limit)
			if err != nil {
				return Unknown, err
			}
		}

		marker := data[0]
		if marker == 0xf7 {
			return JPEGLS, nil
		}

		if marker >= 0xc0 && marker <= 0xcf && marker != 0xc4 && marker != 0xc8 && marker != 0xcc {
			return JPEG, nil
		}

		if marker == 0 || marker == 0xd8 || marker == 0xd9 || marker == 0xda || marker >= 0xd0 && marker <= 0xd7 {
			return Unknown, ErrMalformed
		}

		if marker == 0x01 {
			continue
		}

		data, err = take(reader, prefix, 2, limit)
		if err != nil {
			return Unknown, err
		}

		length := int(binary.BigEndian.Uint16(data))
		if length < 2 {
			return Unknown, ErrMalformed
		}

		_, err = take(reader, prefix, length-2, limit)
		if err != nil {
			return Unknown, err
		}
	}
}

func take(reader io.Reader, prefix *[]byte, count, limit int) ([]byte, error) {
	start := len(*prefix)
	if count > limit-start {
		return nil, ErrHeaderLimit
	}

	*prefix = append(*prefix, make([]byte, count)...)
	read, err := io.ReadFull(reader, (*prefix)[start:])
	*prefix = (*prefix)[:start+read]

	return (*prefix)[start:], err
}
