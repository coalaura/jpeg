package main

import (
	"bytes"
	"image"

	"github.com/coalaura/jpeg"
)

func main() {
	var encoded bytes.Buffer

	err := jpeg.Encode(&encoded, image.NewGray(image.Rect(0, 0, 4, 4)), nil)
	if err != nil {
		panic(err)
	}

	_, err = jpeg.Decode(&encoded)
	if err != nil {
		panic(err)
	}
}
