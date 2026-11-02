// Package jpegxl encodes and decodes JPEG XL using bundled libjxl, Highway,
// Brotli and LittleCMS sources. Decode returns the first displayed frame.
// DecodeAnimation preserves coalesced frames, integer tick durations and loop
// counts. Samples retain the decoder's color space; ICC profiles are returned,
// not silently transformed to a display profile. Floating-point HDR data is
// available through Samples and requires explicit display conversion.
package jpegxl

/*
#include "bridge.h"
*/
import "C"

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	stdjpeg "image/jpeg"
	"io"
	"math"
	"strings"
	"unicode/utf8"
	"unsafe"

	"github.com/coalaura/jpeg/internal/pixels"
)

const (
	Resampling                   SettingID = 2 // -1, 1, 2, 4 or 8.
	ExtraChannelResampling       SettingID = 3
	Noise                        SettingID = 6
	Dots                         SettingID = 7
	Patches                      SettingID = 8
	EdgePreservingFilter         SettingID = 9 // -1 or 0..3.
	Gaborish                     SettingID = 10
	Modular                      SettingID = 11
	KeepInvisible                SettingID = 12
	GroupOrder                   SettingID = 13 // -1/default, 0/scanline, 1/center-first.
	GroupOrderCenterX            SettingID = 14
	GroupOrderCenterY            SettingID = 15
	Responsive                   SettingID = 16
	ProgressiveAC                SettingID = 17
	QuantizedProgressiveAC       SettingID = 18
	ProgressiveDC                SettingID = 19 // -1 or 0..2.
	ChannelColorsGlobalPercent   SettingID = 20
	ChannelColorsGroupPercent    SettingID = 21
	PaletteColors                SettingID = 22
	LossyPalette                 SettingID = 23
	ColorTransform               SettingID = 24 // -1/default, 0/XYB, 1/none, 2/YCbCr.
	ModularColorSpace            SettingID = 25
	ModularGroupSize             SettingID = 26
	ModularPredictor             SettingID = 27
	ModularMATreeLearningPercent SettingID = 28
	ModularPreviousChannels      SettingID = 29
	JPEGReconstructionCFL        SettingID = 30
	BrotliEffort                 SettingID = 32 // -1 or 0..11.
	JPEGCompressBoxes            SettingID = 33
	Buffering                    SettingID = 34
	FullImageHeuristics          SettingID = 38
	DisablePerceptualHeuristics  SettingID = 39
)

// SettingID identifies an advanced libjxl integer encoder control. Most use -1
// for the backend default and 0/1 for off/on; invalid values are rejected by
// libjxl. These names and values follow libjxl 0.12.0's public encoder API.
type SettingID int

type Setting struct {
	ID    SettingID
	Value int
}

// Options controls encoding. Nil uses distance 1, effort 7, one native worker,
// source precision and a raw codestream. Lossless explicitly enables exact coding;
// Distance=0 alone does not promise exact samples. AlphaDistance defaults to 0.
type Options struct {
	Lossless        bool
	Distance        *float32 // 0..25; nil means 1. Ignored when Lossless is true.
	AlphaDistance   *float32 // 0..25; nil means exact alpha. Must be zero for Lossless.
	Effort          int      // 1..10; zero means 7.
	DecodingSpeed   int      // 0..4, trading compression for decoding speed.
	Threads         int      // 1..16; zero means 1.
	BitsPerSample   int      // 2..16 for image.Image; zero retains 16-bit Go images, otherwise 8.
	Container       bool     // false requires a raw codestream; metadata and level 10 require true.
	ICC             []byte   // At most 4 MiB; nil means sRGB (linear sRGB if Linear is true).
	Linear          bool
	IntensityTarget float32 // Nits; zero selects the backend's profile default.
	Orientation     int     // Exif values 1..8; zero means identity.
	CodestreamLevel int     // 0/automatic, 5 or 10. Level 10 requires Container.
	Settings        []Setting
	Boxes           []Box
	MaxMemoryBytes  uint64 // Native working memory budget; zero means 512 MiB.
}

// Box stores the uncompressed contents of an Exif, "xml " or jumb box. Exif
// contents include the four-byte TIFF offset required by the JPEG XL container.
type Box struct {
	Type     string
	Data     []byte
	Compress bool
}

// DecodeOptions bounds input, dimensions, native working memory and the total
// returned frame buffers. Zero limits mean 64 MiB input, 16M pixels, 512 MiB
// native memory, 512 MiB frame samples and 256 frames. Threads defaults to 1.
type DecodeOptions struct {
	Threads            int
	MaxInputBytes      int64
	MaxPixels          uint64
	MaxMemoryBytes     uint64
	MaxDecodedBytes    uint64
	MaxFrames          int
	KeepOrientation    bool
	UnpremultiplyAlpha bool // Explicitly allows a potentially lossy conversion.
	RenderSpotColors   bool // Explicitly render spot channels into color; other extra channels are rejected.
	ReadBoxes          bool
	IntensityTarget    float32 // Explicit HDR tone-mapping target in nits; zero disables it.
}

// Samples contains interleaved gray, gray-alpha, RGB or RGBA channels. Unsigned
// holds native unsigned integers, not values scaled to 16 bits. Float holds
// float32 values, including HDR values outside [0,1]. Exactly one slice is used.
// Floating-point encoding supports binary16 (16/5) and binary32 (32/8).
type Samples struct {
	Width              int
	Height             int
	Channels           int
	BitsPerSample      int
	ExponentBits       int
	PremultipliedAlpha bool
	Unsigned           []uint16
	Float              []float32
}

// Frame is a coalesced full-canvas frame. Decoding fills Samples. Encoding takes
// either Samples or Image, never both. Duration is measured in animation ticks,
// not milliseconds. Name is UTF-8 and Timecode is the optional packed timecode.
type Frame struct {
	Samples  *Samples
	Image    image.Image
	Duration uint32
	Timecode uint32
	Name     string
}

// Animation retains the file time base: a frame lasts
// Duration*TicksDenominator/TicksNumerator seconds. LoopCount=0 means infinite.
// Still images have Animated=false. OutputICC describes decoded samples;
// OriginalICC describes the encoded source and can differ after lossy coding.
type Animation struct {
	Frames           []Frame
	Animated         bool
	TicksNumerator   uint32
	TicksDenominator uint32
	LoopCount        uint32
	Orientation      int
	IntensityTarget  float32
	OutputICC        []byte
	OriginalICC      []byte
	Boxes            []Box
}

// Image returns an ordinary Go image with straight alpha and range-expanded
// unsigned samples. Floating point and premultiplied samples require an
// explicit conversion by the caller. ICC color conversion is not performed.
func (samples *Samples) Image() (image.Image, error) {
	return samples.image(0)
}

func (samples *Samples) image(maxPixels uint64) (image.Image, error) {
	err := samples.validate(maxPixels)
	if err != nil {
		return nil, err
	}

	if samples.ExponentBits != 0 || samples.PremultipliedAlpha {
		return nil, errors.New("jpegxl: floating point or premultiplied samples require explicit display conversion")
	}

	if samples.Channels != 2 {
		return pixels.Image(samples.Unsigned, samples.Width, samples.Height, samples.Channels, samples.BitsPerSample, maxPixels)
	}

	values := make([]uint16, samples.Width*samples.Height*4)

	for index := 0; index < samples.Width*samples.Height; index++ {
		gray := samples.Unsigned[index*2]
		values[index*4] = gray
		values[index*4+1] = gray
		values[index*4+2] = gray
		values[index*4+3] = samples.Unsigned[index*2+1]
	}

	return pixels.Image(values, samples.Width, samples.Height, 4, samples.BitsPerSample, maxPixels)
}

func (samples *Samples) validate(maxPixels uint64) error {
	if samples == nil {
		return errors.New("jpegxl: nil samples")
	}

	err := pixels.Check(samples.Width, samples.Height, samples.Channels, maxPixels)
	if err != nil {
		return err
	}

	if samples.Channels > 4 {
		return errors.New("jpegxl: expected gray, gray-alpha, RGB or RGBA")
	}

	count := samples.Width * samples.Height * samples.Channels

	if samples.ExponentBits != 0 {
		validHalf := samples.BitsPerSample == 16 && samples.ExponentBits == 5
		validFloat := samples.BitsPerSample == 32 && samples.ExponentBits == 8

		if !validHalf && !validFloat || len(samples.Float) != count || len(samples.Unsigned) != 0 {
			return errors.New("jpegxl: invalid floating-point samples")
		}

		for _, value := range samples.Float {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return errors.New("jpegxl: non-finite sample")
			}
		}

		return nil
	}

	if samples.BitsPerSample < 2 || samples.BitsPerSample > 16 || len(samples.Unsigned) != count || len(samples.Float) != 0 {
		return errors.New("jpegxl: invalid unsigned samples")
	}

	maximum := uint16((1 << samples.BitsPerSample) - 1)

	for _, value := range samples.Unsigned {
		if value > maximum {
			return errors.New("jpegxl: sample exceeds declared precision")
		}
	}

	return nil
}

func Decode(reader io.Reader) (image.Image, error) {
	return DecodeWithOptions(reader, nil)
}

func DecodeWithOptions(reader io.Reader, options *DecodeOptions) (image.Image, error) {
	animation, _, err := decode(reader, options, 2)
	if err != nil {
		return nil, err
	}

	maxPixels := uint64(0)

	if options != nil {
		maxPixels = options.MaxPixels
	}

	return animation.Frames[0].Samples.image(maxPixels)
}

func DecodeConfig(reader io.Reader) (image.Config, error) {
	return DecodeConfigWithOptions(reader, nil)
}

func DecodeConfigWithOptions(reader io.Reader, options *DecodeOptions) (image.Config, error) {
	_, config, err := decode(reader, options, 1)
	return config, err
}

func DecodeAnimation(reader io.Reader, options *DecodeOptions) (*Animation, error) {
	animation, _, err := decode(reader, options, 0)
	return animation, err
}

func Encode(writer io.Writer, img image.Image, options *Options) error {
	frame := Frame{Image: img}
	animation := Animation{Frames: []Frame{frame}}

	return EncodeAnimation(writer, &animation, options)
}

func EncodeSamples(writer io.Writer, samples *Samples, options *Options) error {
	frame := Frame{Samples: samples}
	animation := Animation{Frames: []Frame{frame}}

	return EncodeAnimation(writer, &animation, options)
}

// EncodeAnimation writes full-canvas replacement frames. It does not implement
// arbitrary layers, cropped frame blending or extra channels beyond alpha.
// Set Options.ICC explicitly when re-encoding profiled decoded samples.
func EncodeAnimation(writer io.Writer, animation *Animation, options *Options) error {
	if options == nil {
		options = &Options{}
	}

	if animation == nil || len(animation.Frames) == 0 || len(animation.Frames) > 256 {
		return errors.New("jpegxl: expected 1..256 frames")
	}

	if !animation.Animated && len(animation.Frames) != 1 {
		return errors.New("jpegxl: multiple frames require Animated")
	}

	if animation.Animated && (animation.TicksNumerator == 0 || animation.TicksDenominator == 0) {
		return errors.New("jpegxl: animation time base must be positive")
	}

	native, err := encoderOptions(options)
	if err != nil {
		return err
	}

	first, err := frameSamples(&animation.Frames[0], options)
	if err != nil {
		return err
	}

	native.bits = C.int(first.BitsPerSample)
	native.exponent_bits = C.int(first.ExponentBits)
	native.channels = C.int(first.Channels)
	native.premultiplied = boolean(first.PremultipliedAlpha)
	native.have_animation = boolean(animation.Animated)
	native.tps_numerator = C.uint32_t(animation.TicksNumerator)
	native.tps_denominator = C.uint32_t(animation.TicksDenominator)
	native.loops = C.uint32_t(animation.LoopCount)

	var result C.cj_result

	defer C.cj_free(&result)

	encoder := C.cj_xl_encoder_create(C.uint32_t(first.Width), C.uint32_t(first.Height), &native, bytePointer(options.ICC), C.size_t(len(options.ICC)), &result)
	if encoder == nil {
		return nativeError(&result)
	}

	defer C.cj_xl_encoder_destroy(encoder)

	for index := range options.Boxes {
		box := &options.Boxes[index]
		typeName := C.CString(box.Type)
		status := C.cj_xl_encoder_box(encoder, typeName, bytePointer(box.Data), C.size_t(len(box.Data)), boolean(box.Compress), &result)
		C.free(unsafe.Pointer(typeName))

		if status == 0 {
			return nativeError(&result)
		}
	}

	for index := range animation.Frames {
		frame := &animation.Frames[index]
		samples := first

		if index != 0 {
			samples, err = frameSamples(frame, options)
			if err != nil {
				return err
			}
		}

		if samples.Width != first.Width || samples.Height != first.Height || samples.Channels != first.Channels || samples.BitsPerSample != first.BitsPerSample || samples.ExponentBits != first.ExponentBits || samples.PremultipliedAlpha != first.PremultipliedAlpha {
			return errors.New("jpegxl: animation frames must share dimensions, precision and channels")
		}

		if len(frame.Name) > 65535 || !utf8.ValidString(frame.Name) || strings.ContainsRune(frame.Name, 0) {
			return errors.New("jpegxl: invalid frame name")
		}

		address := unsafe.Pointer(unsafe.SliceData(samples.Unsigned))
		size := len(samples.Unsigned) * 2

		if samples.ExponentBits != 0 {
			address = unsafe.Pointer(unsafe.SliceData(samples.Float))
			size = len(samples.Float) * 4
		}

		name := C.CString(frame.Name)

		status := C.cj_xl_encoder_frame(encoder, address, C.size_t(size), C.uint32_t(frame.Duration), C.uint32_t(frame.Timecode), name, &result)

		C.free(unsafe.Pointer(name))

		if status == 0 {
			return nativeError(&result)
		}
	}

	status := C.cj_xl_encoder_finish(encoder, &result)
	if status == 0 {
		return nativeError(&result)
	}

	return pixels.Write(writer, unsafe.Slice((*byte)(unsafe.Pointer(result.data)), int(result.size)))
}

// RecompressJPEG stores an eligible classic JPEG in a JPEG XL container with
// reconstruction data. It preserves the original JPEG byte-for-byte, including
// metadata, rather than decoding and re-encoding its pixels. Unsupported JPEG
// coding modes are rejected. Input is limited to 64 MiB and 16M pixels. Only
// Effort, Threads, DecodingSpeed, CodestreamLevel, Settings and MaxMemoryBytes affect recompression;
// pixel, profile and box options are ignored. Container output is always enabled.
func RecompressJPEG(writer io.Writer, reader io.Reader, options *Options) error {
	settings := Options{Container: true}

	if options != nil {
		settings = *options
		settings.Container = true
	}

	native, err := encoderOptions(&settings)
	if err != nil {
		return err
	}

	data, err := pixels.Read(reader, 0)
	if err != nil {
		return err
	}

	config, err := stdjpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return err
	}

	err = pixels.Check(config.Width, config.Height, 3, 0)
	if err != nil {
		return err
	}

	native.container = 1

	var result C.cj_result

	defer C.cj_free(&result)

	status := C.cj_xl_recompress(bytePointer(data), C.size_t(len(data)), &native, &result)
	if status == 0 {
		return nativeError(&result)
	}

	return pixels.Write(writer, unsafe.Slice((*byte)(unsafe.Pointer(result.data)), int(result.size)))
}

// RestoreJPEG restores the exact original JPEG bytes or reports that the JPEG XL
// stream has no reconstruction data. No intermediate pixel image is produced.
func RestoreJPEG(writer io.Writer, reader io.Reader, options *DecodeOptions) error {
	if options == nil {
		options = &DecodeOptions{}
	}

	native, err := decoderOptions(options)
	if err != nil {
		return err
	}

	data, err := pixels.Read(reader, options.MaxInputBytes)
	if err != nil {
		return err
	}

	var result C.cj_result

	defer C.cj_free(&result)

	status := C.cj_xl_restore(bytePointer(data), C.size_t(len(data)), &native, &result)
	if status == 0 {
		return nativeError(&result)
	}

	return pixels.Write(writer, unsafe.Slice((*byte)(unsafe.Pointer(result.data)), int(result.size)))
}

func decode(reader io.Reader, options *DecodeOptions, mode int) (*Animation, image.Config, error) {
	if options == nil {
		options = &DecodeOptions{}
	}

	native, err := decoderOptions(options)
	if err != nil {
		return nil, image.Config{}, err
	}

	data, err := pixels.Read(reader, options.MaxInputBytes)
	if err != nil {
		return nil, image.Config{}, err
	}

	var result C.cj_xl_result

	defer C.cj_xl_free(&result)

	status := C.cj_xl_decode(bytePointer(data), C.size_t(len(data)), C.int(mode), &native, &result)
	if status == 0 {
		return nil, image.Config{}, nativeError(&result.base)
	}

	width := int(result.base.width)
	height := int(result.base.height)
	channels := int(result.base.components)
	bits := int(result.base.bits)

	config := image.Config{ColorModel: pixels.Model(channels, bits), Width: width, Height: height}

	if mode == 1 {
		return nil, config, nil
	}

	animation := &Animation{
		Frames:           make([]Frame, int(result.frame_count)),
		Animated:         result.info.have_animation != 0,
		TicksNumerator:   uint32(result.info.tps_numerator),
		TicksDenominator: uint32(result.info.tps_denominator),
		LoopCount:        uint32(result.info.loops),
		Orientation:      int(result.info.orientation),
		IntensityTarget:  float32(result.info.intensity_target),
		OutputICC:        C.GoBytes(unsafe.Pointer(result.icc), C.int(result.icc_size)),
		OriginalICC:      C.GoBytes(unsafe.Pointer(result.original_icc), C.int(result.original_icc_size)),
	}

	frames := unsafe.Slice(result.frames, int(result.frame_count))

	for index := range frames {
		frame := &frames[index]

		samples := &Samples{
			Width:              width,
			Height:             height,
			Channels:           channels,
			BitsPerSample:      bits,
			ExponentBits:       int(result.info.exponent_bits),
			PremultipliedAlpha: result.info.premultiplied != 0,
		}

		if samples.ExponentBits != 0 {
			samples.Float = append([]float32(nil), unsafe.Slice((*float32)(frame.pixels), int(frame.size)/4)...)
		} else {
			samples.Unsigned = append([]uint16(nil), unsafe.Slice((*uint16)(frame.pixels), int(frame.size)/2)...)
		}

		animation.Frames[index] = Frame{Samples: samples, Duration: uint32(frame.duration), Timecode: uint32(frame.timecode), Name: C.GoString(frame.name)}
	}

	metadata := unsafe.Slice((*byte)(unsafe.Pointer(result.base.data)), int(result.base.size))

	for len(metadata) >= 8 {
		length := int(binary.BigEndian.Uint32(metadata[4:8]))
		box := Box{Type: string(metadata[:4]), Data: append([]byte(nil), metadata[8:8+length]...)}

		animation.Boxes = append(animation.Boxes, box)

		metadata = metadata[8+length:]
	}

	return animation, config, nil
}

func frameSamples(frame *Frame, options *Options) (*Samples, error) {
	if frame.Samples != nil {
		if frame.Image != nil {
			return nil, errors.New("jpegxl: specify samples or image, not both")
		}

		return frame.Samples, frame.Samples.validate(0)
	}

	bits := options.BitsPerSample
	if bits == 0 {
		bits = pixels.Precision(frame.Image)
	}

	values, channels, err := pixels.Extract(frame.Image, bits, true)
	if err != nil {
		return nil, err
	}

	bounds := frame.Image.Bounds()
	samples := &Samples{Width: bounds.Dx(), Height: bounds.Dy(), Channels: channels, BitsPerSample: bits, Unsigned: values}

	return samples, nil
}

func encoderOptions(options *Options) (C.cj_xl_options, error) {
	var native C.cj_xl_options

	native.lossless = boolean(options.Lossless)
	native.container = boolean(options.Container)
	native.distance = 1
	native.effort = 7
	native.orientation = 1
	native.linear = boolean(options.Linear)
	native.intensity_target = C.float(options.IntensityTarget)
	native.level = C.int(options.CodestreamLevel)

	if options.Distance != nil {
		native.distance = C.float(*options.Distance)
	}

	if options.AlphaDistance != nil {
		native.alpha_distance = C.float(*options.AlphaDistance)
	}

	if options.Effort != 0 {
		native.effort = C.int(options.Effort)
	}

	if options.Orientation != 0 {
		native.orientation = C.int(options.Orientation)
	}

	native.decoding_speed = C.int(options.DecodingSpeed)

	err := resourceOptions(&native, options.Threads, options.MaxMemoryBytes)
	if err != nil {
		return native, err
	}

	validLevel := options.CodestreamLevel == 0 || options.CodestreamLevel == 5 || options.CodestreamLevel == 10 && options.Container
	validIntensity := !math.IsNaN(float64(options.IntensityTarget)) && options.IntensityTarget >= 0 && options.IntensityTarget <= 100000

	if !validLevel || !validIntensity || native.effort < 1 || native.effort > 10 || options.DecodingSpeed < 0 || options.DecodingSpeed > 4 || native.orientation < 1 || native.orientation > 8 {
		return native, errors.New("jpegxl: invalid encoder setting")
	}

	if !validDistance(float32(native.distance)) || !validDistance(float32(native.alpha_distance)) {
		return native, errors.New("jpegxl: distance must be finite and in 0..25")
	}

	if options.Lossless && native.alpha_distance != 0 {
		return native, errors.New("jpegxl: lossless coding requires exact alpha")
	}

	if len(options.ICC) > 4<<20 || len(options.Settings) > 40 {
		return native, pixels.ErrLimit
	}

	seen := uint64(0)

	for index, setting := range options.Settings {
		valid := setting.ID >= Resampling && setting.ID <= DisablePerceptualHeuristics && setting.ID != 4 && setting.ID != 5 && setting.ID != 31 && setting.ID != 35 && setting.ID != 36 && setting.ID != 37
		if !valid || seen&(1<<setting.ID) != 0 || int64(setting.Value) < math.MinInt32 || int64(setting.Value) > math.MaxInt32 {
			return native, errors.New("jpegxl: invalid or duplicate advanced setting")
		}

		if options.Lossless && setting.ID == KeepInvisible && setting.Value != 1 {
			return native, errors.New("jpegxl: lossless coding must preserve invisible RGB")
		}

		seen |= 1 << setting.ID
		native.setting_ids[index] = C.int(setting.ID)
		native.setting_values[index] = C.int(setting.Value)
	}

	native.setting_count = C.int(len(options.Settings))
	metadataSize := 0

	for index := range options.Boxes {
		box := &options.Boxes[index]
		if !options.Container || box.Type != "Exif" && box.Type != "xml " && box.Type != "jumb" {
			return native, errors.New("jpegxl: metadata needs a container and an Exif, xml or jumb type")
		}

		metadataSize += 8 + len(box.Data)
		if metadataSize > 4<<20 {
			return native, pixels.ErrLimit
		}
	}

	return native, nil
}

func decoderOptions(options *DecodeOptions) (C.cj_xl_options, error) {
	var native C.cj_xl_options

	err := resourceOptions(&native, options.Threads, options.MaxMemoryBytes)
	if err != nil {
		return native, err
	}

	native.max_pixels = C.uint64_t(options.MaxPixels)
	if native.max_pixels == 0 {
		native.max_pixels = pixels.MaxPixels
	}

	native.max_decoded_bytes = C.uint64_t(options.MaxDecodedBytes)
	if native.max_decoded_bytes == 0 {
		native.max_decoded_bytes = pixels.MaxBytes
	}

	native.max_frames = C.int(options.MaxFrames)

	if options.MaxFrames == 0 {
		native.max_frames = 256
	}

	if options.MaxFrames < 0 || options.MaxFrames > 4096 || native.max_decoded_bytes > pixels.MaxBytes {
		return native, pixels.ErrLimit
	}

	if math.IsNaN(float64(options.IntensityTarget)) || options.IntensityTarget < 0 || options.IntensityTarget > 100000 {
		return native, errors.New("jpegxl: invalid intensity target")
	}

	native.keep_orientation = boolean(options.KeepOrientation)
	native.unpremultiply = boolean(options.UnpremultiplyAlpha)
	native.render_spotcolors = boolean(options.RenderSpotColors)
	native.read_boxes = boolean(options.ReadBoxes)
	native.intensity_target = C.float(options.IntensityTarget)

	return native, nil
}

func resourceOptions(native *C.cj_xl_options, threads int, memory uint64) error {
	if threads == 0 {
		threads = 1
	}

	if threads < 1 || threads > 16 {
		return errors.New("jpegxl: threads must be in 1..16")
	}

	if memory == 0 {
		memory = pixels.MaxBytes
	}

	if memory < 1<<20 || memory > 2<<30 {
		return errors.New("jpegxl: native memory budget must be in 1 MiB..2 GiB")
	}

	native.threads = C.int(threads)
	native.max_memory_bytes = C.uint64_t(memory)
	native.max_pixels = pixels.MaxPixels

	return nil
}

func validDistance(value float32) bool {
	return !math.IsNaN(float64(value)) && value >= 0 && value <= 25
}

func nativeError(result *C.cj_result) error {
	message := C.GoString(&result.error[0])
	if message == "" {
		message = "native allocation failed"
	}

	return errors.New("jpegxl: " + message)
}

func boolean(value bool) C.int {
	if value {
		return 1
	}

	return 0
}

func bytePointer(data []byte) *C.uchar {
	return (*C.uchar)(unsafe.Pointer(unsafe.SliceData(data)))
}
