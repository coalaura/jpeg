// Package icc reads JPEG ICC segments without retaining unrelated metadata.
package icc

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const signature = "ICC_PROFILE\x00"

// JPEG returns the assembled profile and whether its segments occur in sequence.
// The caller retains responsibility for validating and decoding the JPEG stream.
func JPEG(data []byte) ([]byte, bool, error) {
	var (
		segments [255][]byte
		count    int
		found    int
		total    int
	)

	ordered := true
	position := 2

	for position < len(data) {
		next := bytes.IndexByte(data[position:], 0xff)
		if next < 0 {
			break
		}

		position += next + 1

		for position < len(data) && data[position] == 0xff {
			position++
		}

		if position == len(data) {
			break
		}

		marker := data[position]
		position++

		if marker == 0xd9 {
			break
		}

		if marker == 0 || marker == 1 || marker >= 0xd0 && marker <= 0xd8 {
			continue
		}

		if len(data)-position < 2 {
			break
		}

		length := int(binary.BigEndian.Uint16(data[position:]))
		if length < 2 || length > len(data)-position {
			break
		}

		payload := data[position+2 : position+length]
		position += length

		if marker != 0xe2 || !bytes.HasPrefix(payload, []byte(signature)) {
			continue
		}

		if len(payload) <= 14 {
			return nil, false, fmt.Errorf("ICC segment is truncated or empty")
		}

		sequence := int(payload[12])
		declared := int(payload[13])

		if sequence == 0 || declared == 0 || sequence > declared || count != 0 && declared != count {
			return nil, false, fmt.Errorf("ICC segments have contradictory sequence numbers or counts")
		}

		if segments[sequence-1] != nil {
			return nil, false, fmt.Errorf("ICC segment %d is duplicated", sequence)
		}

		count = declared
		found++
		ordered = ordered && sequence == found

		total += len(payload) - 14
		if total > 4<<20 {
			return nil, false, fmt.Errorf("ICC profile exceeds 4 MiB")
		}

		segments[sequence-1] = payload[14:]
	}

	if found != count {
		return nil, false, fmt.Errorf("ICC profile is incomplete: got %d of %d segments", found, count)
	}

	if count == 0 {
		return nil, true, nil
	}

	profile := make([]byte, 0, total)

	for _, segment := range segments[:count] {
		profile = append(profile, segment...)
	}

	return profile, ordered, nil
}
