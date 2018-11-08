package ffmpeg

/*
#include "ffmpeg.h"
int wrap_avcodec_decode_video2(AVCodecContext *ctx, AVFrame *frame, void *data, int size, int *got) {
	struct AVPacket pkt = {.data = data, .size = size};
	return avcodec_decode_video2(ctx, frame, got, &pkt);
}
int wrap_av_opt_set_int_list(void* obj, const char* name, void* val, int64_t term, int64_t flags) {
	if (av_int_list_length(val, term) > INT_MAX / sizeof(*(val))) {
		return AVERROR(EINVAL);
	} 
	return av_opt_set_bin(obj, name, (const uint8_t *)(val), av_int_list_length(val, term) * sizeof(*(val)), flags);
}

	#cgo pkg-config: libavfilter
*/
import "C"
import (
	"unsafe"
	"runtime"
	"fmt"
	"image"
	"reflect"
	"github.com/nareix/joy4/av"
	"github.com/nareix/joy4/codec/h264parser"
)

// VideoFramerate represents a FPS value with a fraction (numerator + denominator)
type VideoFramerate struct {
	Num int
	Den int
}

type VideoFrame struct {
	Image image.YCbCr
	frame *C.AVFrame
	Framerate VideoFramerate
}

func (self *VideoFrame) Free() {
	self.Image = image.YCbCr{}
	C.av_frame_free(&self.frame)
}

func freeVideoFrame(self *VideoFrame) {
	self.Free()
}

func (v VideoFrame) Width() int {
	return v.Image.Rect.Dx()
}

func (v VideoFrame) Height() int {
	return v.Image.Rect.Dy()
}

func (v VideoFrame) GetPixelFormat() av.PixelFormat {
	return PixelFormatFF2AV(int32(v.frame.format))
}

func (v VideoFrame) GetStride() (yStride, cStride int) {
	return v.Image.YStride, v.Image.CStride
}

func (v VideoFrame) GetResolution() (w, h int) {
	return v.Width(), v.Height()
}

func (v VideoFrame) GetDataPtr() (y, cb, cr *[]uint8) {
	return &v.Image.Y, &v.Image.Cb, &v.Image.Cr
}

// GetFramerate returns the framerate as a fraction (numerator and denominator)
func (v VideoFrame) GetFramerate() (num, den int) {
	return v.Framerate.Num, v.Framerate.Den
}

func (v VideoFrame) GetScanningMode() (mode av.ScanningMode) {
	if int(v.frame.interlaced_frame) != 0 {
		if int(v.frame.top_field_first) != 0 {
			return av.InterlacedTFF
		} else {
			return av.InterlacedBFF
		}
	}
	return av.Progressive
}

func (v *VideoFrame) SetPixelFormat(format av.PixelFormat) {
	v.frame.format = C.int32_t(PixelFormatAV2FF(format))
}

func (v *VideoFrame) SetStride(yStride, cStride int) {
	v.Image.YStride = yStride
	v.Image.CStride = cStride
}

func (v *VideoFrame) SetResolution(w, h int) {
	v.Image.Rect = image.Rectangle{ image.Point{0,0}, image.Point{w, h}}
}

// SetFramerate sets the frame's FPS numerator and denominator
func (v *VideoFrame) SetFramerate(num, den int) {
	v.Framerate.Num = num
	v.Framerate.Den = den
}

type VideoScaler struct {
	inHeight int
	OutPixelFormat av.PixelFormat
	OutWidth int
	OutHeight int
	OutYStride int
	OutCStride int
	swsCtx *C.struct_SwsContext
	outputImgPtrs [3]*C.uint8_t
}

func (self *VideoScaler) Close() {
	if self != nil {
		C.sws_freeContext(self.swsCtx)
	}
}

func (self *VideoScaler) FreeOutputImage() {
	if self != nil {
		C.free(unsafe.Pointer(self.outputImgPtrs[0]))
		C.free(unsafe.Pointer(self.outputImgPtrs[1]))
		C.free(unsafe.Pointer(self.outputImgPtrs[2]))
		self.outputImgPtrs[0]= nil
		self.outputImgPtrs[1]= nil
		self.outputImgPtrs[2]= nil
	}
}

func (self *VideoScaler) videoScaleOne(src *VideoFrame) (dst *VideoFrame, err error) {
	var srcPtr ([3]*C.uint8_t)
	srcPtr[0] = (*C.uint8_t)(unsafe.Pointer(&src.Image.Y[0]))
	srcPtr[1] = (*C.uint8_t)(unsafe.Pointer(&src.Image.Cb[0]))
	srcPtr[2] = (*C.uint8_t)(unsafe.Pointer(&src.Image.Cr[0]))

	var inStrides ([3]C.int)
	inStrides[0] = C.int(src.Image.YStride)
	inStrides[1] = C.int(src.Image.CStride)
	inStrides[2] = C.int(src.Image.CStride)

	var outStrides ([3]C.int)
	outStrides[0] = C.int(self.OutYStride)
	outStrides[1] = C.int(self.OutCStride)
	outStrides[2] = C.int(self.OutCStride)

	// TODO 420 only
	lsize := self.OutYStride * self.OutHeight
	csize := self.OutCStride * self.OutHeight

	var dataPtr ([4]*C.uint8_t)
	dataPtr[0]= (*C.uint8_t)(C.malloc(C.size_t(lsize)))
	dataPtr[1]= (*C.uint8_t)(C.malloc(C.size_t(csize)))
	dataPtr[2]= (*C.uint8_t)(C.malloc(C.size_t(csize)))

	self.outputImgPtrs[0] = dataPtr[0]
	self.outputImgPtrs[1] = dataPtr[1]
	self.outputImgPtrs[2] = dataPtr[2]

	// convert to destination format and resolution
	C.sws_scale(self.swsCtx, &srcPtr[0], &inStrides[0], 0, C.int(self.inHeight), &dataPtr[0], &outStrides[0])

	dst					= &VideoFrame{}
	dst.frame			= &C.AVFrame{} // TODO deep copy input to keep frame properties
	dst.frame.format	= C.int32_t(PixelFormatAV2FF(self.OutPixelFormat))
	dst.Image.Y			= fromCPtr(unsafe.Pointer(dataPtr[0]), lsize)
	dst.Image.Cb		= fromCPtr(unsafe.Pointer(dataPtr[1]), csize)
	dst.Image.Cr		= fromCPtr(unsafe.Pointer(dataPtr[2]), csize)
	dst.Image.YStride	= int(outStrides[0])
	dst.Image.CStride	= int(outStrides[1])
	dst.Image.Rect		= image.Rect(0, 0, self.OutWidth, self.OutHeight)
	return
}


func (self *VideoScaler) VideoScale(src *VideoFrame) (dst *VideoFrame, err error) {
	if self.swsCtx == nil {
		self.inHeight = src.Image.Rect.Dy()

		self.swsCtx = C.sws_getContext(C.int(src.Image.Rect.Dx()), C.int(self.inHeight), int32(src.frame.format),
			C.int(self.OutWidth), C.int(self.OutHeight), PixelFormatAV2FF(self.OutPixelFormat),
			C.SWS_BILINEAR, (*C.SwsFilter)(C.NULL), (*C.SwsFilter)(C.NULL), (*C.double)(C.NULL))

		if self.swsCtx == nil {
			err = fmt.Errorf("Impossible to create scale context for the conversion fmt:%d s:%dx%d -> fmt:%d s:%dx%d\n",
				PixelFormatFF2AV(int32(src.frame.format)), src.Image.Rect.Dx(), self.inHeight,
				self.OutPixelFormat, self.OutWidth, self.OutHeight);
			return
		}
	}

	dst, err = self.videoScaleOne(src)
	return
}

// FramerateConverter allows increasing or decreasing the video framerate using libavfilter's processing filters
type FramerateConverter struct {
	inPixelFormat, OutPixelFormat av.PixelFormat
	inWidth, OutWidth int
	inHeight, OutHeight int
	inFpsNum, OutFpsNum int
	inFpsDen, OutFpsDen int
	pts int64
	graph *C.struct_AVFilterGraph
	graphSource *C.AVFilterContext
	graphSink *C.AVFilterContext
	outputFrames []*C.AVFrame
}

// Close frees the allocated filter graph
func (self *FramerateConverter) Close() {
	if self != nil {
		C.avfilter_graph_free(&self.graph)
	}
}

// FreeFirstOutputImage frees the allocated image at the start of the queue "outputFrames"
func (self *FramerateConverter) FreeFirstOutputImage() {
	if self != nil {
		if len(self.outputFrames) <= 0 {
			return
		}

		if self.outputFrames[0] != nil {
			C.av_frame_free(&self.outputFrames[0])
		}

		// Remove [0] from the queue
		self.outputFrames = self.outputFrames[1:]
	}
}

// FreeOutputImages frees all allocated images stored in outputFrames
func (self *FramerateConverter) FreeOutputImages() {
	if self != nil {
		for len(self.outputFrames) > 0 {
			self.FreeFirstOutputImage()
		}
	}
}

// ConvertFramerate pushes a frame in the filtergraph and receives 0, 1 or more frames with converted framerate
func (self *FramerateConverter) ConvertFramerate(in *VideoFrame) (out []*VideoFrame, err error){
	if self.graph == nil {
		err = self.ConfigureVideoFilters()
		if err != nil {
			return
		}
	}

	if self.graph == nil {
		return
	}

	in.frame.pts = C.int64_t(self.pts)
	self.pts++
	cret := C.av_buffersrc_add_frame(self.graphSource, in.frame)
	if int(cret) < 0 {
		err = fmt.Errorf("av_buffersrc_add_frame failed")
		return
	}

	for int(cret) == 0 {
		frame := C.av_frame_alloc()
		cret = C.av_buffersink_get_frame_flags(self.graphSink, frame, C.int(0))
		if cret < 0 {
			C.av_frame_free(&frame)
			break
		}

		lsize := frame.linesize[0] * frame.height
		csize := frame.linesize[1] * frame.height

		f := &VideoFrame{}
		f.frame = frame
		self.outputFrames = append(self.outputFrames, frame)

		f.Image.Y = fromCPtr(unsafe.Pointer(frame.data[0]), int(lsize))
		f.Image.Cb = fromCPtr(unsafe.Pointer(frame.data[1]), int(csize))
		f.Image.Cr = fromCPtr(unsafe.Pointer(frame.data[2]), int(csize))
		f.Image.YStride = int(frame.linesize[0])
		f.Image.CStride = int(frame.linesize[1])
		f.Image.Rect = in.Image.Rect
		f.Framerate.Num = self.OutFpsNum
		f.Framerate.Den = self.OutFpsDen
		out = append(out, f)
	}
	return
}

// ConfigureVideoFilters creates the filtergraph: BufferSrc => FpsConv => BufferSink
func (self *FramerateConverter) ConfigureVideoFilters() (err error) {
	var ret int
	var graphSource, graphSink *C.AVFilterContext
	self.graph = C.avfilter_graph_alloc()

	// Input filter config
	bufferSrcArgs := fmt.Sprintf("video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=%d/%d:frame_rate=%d/%d",
		self.inWidth, self.inHeight, C.int32_t(PixelFormatAV2FF(self.inPixelFormat)),
		self.inFpsDen, self.inFpsNum, 1, 1, // TODO sar num,  max(sar denom, 1)
		self.inFpsNum, self.inFpsDen)

	ret = self.CreateFilter(&graphSource, "buffer", "joy4_buffersrc", bufferSrcArgs)
	if ret < 0 {
		err = fmt.Errorf("avfilter_graph_create_filter failed")
		return
	}

	// Output filter config
	ret = self.CreateFilter(&graphSink, "buffersink", "joy4_buffersink", "")
	if ret < 0 {
		err = fmt.Errorf("avfilter_graph_create_filter failed")
		return
	}

	// Set the only possible output format as I420
	var pixFmts [2]C.enum_AVPixelFormat
	pixFmts[0] = C.AV_PIX_FMT_YUV420P
	pixFmts[1] = C.AV_PIX_FMT_NONE;

	strPixFmts := C.CString("pix_fmts")
	defer C.free(unsafe.Pointer(strPixFmts))

	ret = int(C.wrap_av_opt_set_int_list(unsafe.Pointer(graphSink), strPixFmts, unsafe.Pointer(&pixFmts),  C.AV_PIX_FMT_NONE, C.AV_OPT_SEARCH_CHILDREN))
	if ret < 0 {
		err = fmt.Errorf("wrap_av_opt_set_int_list failed")
		return
	}

	// Add the 'fps' filter between the source and sink filters
	filterarg := fmt.Sprintf("fps=%d/%d", self.OutFpsNum, self.OutFpsDen)
	self.AddFilter(graphSource, graphSink, "framerate", filterarg)
	ret = int(C.avfilter_graph_config(self.graph, C.NULL))
	if ret < 0 {
		err = fmt.Errorf("avfilter_graph_config failed")
	}

	self.graphSource = graphSource;
	self.graphSink = graphSink;
	return
}

// CreateFilter fills filterPtr according to the given filterType 
func (self *FramerateConverter) CreateFilter(filterPtr **C.AVFilterContext, filterType string, filterName string, filterArgs string) int {
	cFilterType := C.CString(filterType)
	defer C.free(unsafe.Pointer(cFilterType))

	cFilterName := C.CString(filterName)
	defer C.free(unsafe.Pointer(cFilterName))

	cFilterArgs := C.CString(filterArgs)
	defer C.free(unsafe.Pointer(cFilterArgs))

	filterDef := C.avfilter_get_by_name(cFilterType)
	cret := C.avfilter_graph_create_filter(filterPtr, filterDef, cFilterName, cFilterArgs, C.NULL, self.graph)
	return int(cret)
}

// AddFilter creates a filter and adds it in the filtergraph, between prevFilter and nextFilter
func (self *FramerateConverter) AddFilter(prevFilter *C.AVFilterContext, nextFilter *C.AVFilterContext, name string, arg string) (err error){
	var ret int
	var newFilter *C.AVFilterContext

	ret = self.CreateFilter(&newFilter, name, "joy4_fpsconv", arg)
	if ret < 0 {
		err = fmt.Errorf("avfilter_graph_create_filter failed")
		return
	}

	ret = int(C.avfilter_link(newFilter, 0, nextFilter, 0))
	if ret < 0 {
		err = fmt.Errorf("first avfilter_link failed: %d", ret)
		return
	}

	ret = int(C.avfilter_link(prevFilter, 0, newFilter, 0))
	if ret < 0 {
		err = fmt.Errorf("second avfilter_link failed: %d", ret)
		return
	}

	return
}

// VideoEncoder contains all params that must be set by user to initialize the video encoder
type VideoEncoder struct {
	ff *ffctx
	Bitrate int
	width int
	height int
	gopSize int
	fpsNum, fpsDen int
	pixelFormat av.PixelFormat
	codecData h264parser.CodecData
	codecDataInitialised bool
	pts int64
	scaler *VideoScaler
	framerateConverter *FramerateConverter
	bm av.BitrateMeasure
}

// Setup initializes the encoder context and checks user params
func (enc *VideoEncoder) Setup() (err error) {
	ff := &enc.ff.ff
	ff.frame = C.av_frame_alloc()

	// Check user parameters
	if enc.width <= 0 || enc.height <= 0 {
		err = fmt.Errorf("Error: Invalid resolution: %d x %d", enc.width, enc.height)
		return
	}

	if enc.pixelFormat == av.PixelFormat(0) {
		enc.pixelFormat = PixelFormatFF2AV(*ff.codec.sample_fmts)
		fmt.Println("Warning: Applying default pixel format:", enc.pixelFormat)
	}

	if enc.fpsDen <= 0 || enc.fpsNum <= 0 {
		err = fmt.Errorf("Error: Invalid framerate: %d / %d", enc.fpsNum, enc.fpsDen)
		return
	}

	if enc.gopSize <= 0 {
		fmt.Println("Warning: applying minimum gop size: 2 frames")
		enc.gopSize = 2
	} else if enc.gopSize > 240 {
		fmt.Println("Warning: applying maximum gop size: 240 frames")
		enc.gopSize = 240
	}

	if enc.Bitrate == 0 {
		fmt.Println("Warning: applying minimum bitrate: 100 kbps")
		enc.Bitrate = 100000
	} else if enc.Bitrate > 10000000 {
		fmt.Println("Warning: applying maximum bitrate: 10 Mbps")
		enc.Bitrate = 10000000
	}


	if err = enc.SetOption("preset", "ultrafast"); err != nil {
		return
	}
	if err = enc.SetOption("crf", "23"); err != nil {
		return
	}

	// All the following params are described in ffmpeg: avcodec.h, in struct AVCodecContext
	ff.codecCtx.width			= C.int(enc.width)
	ff.codecCtx.height			= C.int(enc.height)
	ff.codecCtx.pix_fmt			= PixelFormatAV2FF(enc.pixelFormat)

	ff.codecCtx.time_base.num	= C.int(enc.fpsDen)
	ff.codecCtx.time_base.den	= C.int(enc.fpsNum)
	ff.codecCtx.gop_size		= C.int(enc.gopSize)

	// Use VBV for rate control.
	// rc_max_rate is the target bitrate, and rc_buffer_size is the time window
	// over which the bitrate is controlled. By setting size = max * 2, we give
	// a window of 2 seconds to mitigate the effects of bitrate peaks on the 
	// overall quality
	ff.codecCtx.rc_max_rate		= C.int64_t(enc.Bitrate)
	ff.codecCtx.rc_buffer_size	= C.int(ff.codecCtx.rc_max_rate * 2)

	if C.avcodec_open2(ff.codecCtx, ff.codec, &ff.options) != 0 {
		err = fmt.Errorf("ffmpeg: encoder: avcodec_open2 failed")
		return
	}

	// Leave codecData uninitialized until SPS and PPS are received (see in encodeOne())
	enc.codecData = h264parser.CodecData{}

	return
}

func (enc *VideoEncoder) prepare() (err error) {
	ff := &enc.ff.ff
	if ff.frame == nil {
		if err = enc.Setup(); err != nil {
			return
		}
	}
	return
}

// CodecData returns the video codec data of the encoder
func (enc *VideoEncoder) CodecData() (codec av.VideoCodecData, err error) {
	if err = enc.prepare(); err != nil {
		return
	}
	codec = enc.codecData
	return
}

func (enc *VideoEncoder) encodeOne(img *VideoFrame) (gotpkt bool, pkt av.Packet, err error) {
	if err = enc.prepare(); err != nil {
		return
	}

	ff := &enc.ff.ff
	cpkt := C.AVPacket{}
	cgotpkt := C.int(0)

	ff.frame.data[0] = (*C.uchar)(unsafe.Pointer(&img.Image.Y[0]))
	ff.frame.data[1] = (*C.uchar)(unsafe.Pointer(&img.Image.Cb[0]))
	ff.frame.data[2] = (*C.uchar)(unsafe.Pointer(&img.Image.Cr[0]))

	ff.frame.linesize[0] = C.int(img.Image.YStride)
	ff.frame.linesize[1] = C.int(img.Image.CStride)
	ff.frame.linesize[2] = C.int(img.Image.CStride)

	ff.frame.width  = C.int(img.Image.Rect.Dx())
	ff.frame.height = C.int(img.Image.Rect.Dy())
	ff.frame.format = img.frame.format
	ff.frame.sample_aspect_ratio.num = 0 // TODO
	ff.frame.sample_aspect_ratio.den = 1

	ff.frame.pts = C.int64_t(enc.pts)
	enc.pts++

	cerr := C.avcodec_encode_video2(ff.codecCtx, &cpkt, ff.frame, &cgotpkt)
	if cerr < C.int(0) {
		err = fmt.Errorf("ffmpeg: avcodec_encode_video2 failed: %d", cerr)
		return
	}

	var avpkt av.Packet
	if cgotpkt != 0 {
		gotpkt = true

		if debug {
			fmt.Println("encoded frame with pts:", cpkt.pts," dts:", cpkt.dts, "duration:", cpkt.duration, "flags:", cpkt.flags)
		}

		avpkt.Data = C.GoBytes(unsafe.Pointer(cpkt.data), cpkt.size)
		avpkt.IsKeyFrame = (cpkt.flags & C.AV_PKT_FLAG_KEY) == C.AV_PKT_FLAG_KEY

		// Initialize codecData from SPS and PPS
		// This is done only once, when the first key frame is encoded
		if !enc.codecDataInitialised {
			var codecData av.CodecData
			codecData, err = h264parser.PktToCodecData(avpkt)
			if err == nil {
				enc.codecData = codecData.(h264parser.CodecData)
				enc.codecDataInitialised = true
			}
		}

		C.av_packet_unref(&cpkt)
	} else if enc.codecDataInitialised {
		fmt.Println("ffmpeg: no pkt !")
	}

	if debug {
		if ok, kbps := enc.bm.Measure(len(avpkt.Data)); ok {
			fmt.Println("Encoded video bitrate (kbps):", kbps)
		}
	}
	return gotpkt, avpkt, err
}


func (self *VideoEncoder) scale(img *VideoFrame) (out *VideoFrame, err error) {
	if self.scaler == nil {
		self.scaler = &VideoScaler{
			OutPixelFormat:	self.pixelFormat,
			OutWidth:		self.width,
			OutHeight:		self.height,
			OutYStride:		self.width,
			OutCStride:		self.width/self.pixelFormat.HorizontalSubsampleRatio(),
		}
	}
	if out, err = self.scaler.VideoScale(img); err != nil {
		return
	}
	return
}

// convertFramerate instanciates a FramerateConverter to convert from img's framerate to the encoder's framerate
func (self *VideoEncoder) convertFramerate(img *VideoFrame) (out []*VideoFrame, err error) {
	if self.framerateConverter == nil {
		self.framerateConverter = &FramerateConverter{
			inPixelFormat:	PixelFormatFF2AV(int32(img.frame.format)),
			inWidth:		img.Image.Rect.Dx(),
			inHeight:		img.Image.Rect.Dy(),
			inFpsNum:		img.Framerate.Num,
			inFpsDen:		img.Framerate.Den,
			OutPixelFormat:	self.pixelFormat,
			OutWidth:		self.width,
			OutHeight:		self.height,
			OutFpsNum:		self.fpsNum,
			OutFpsDen:		self.fpsDen,
		}
	}
	if out, err = self.framerateConverter.ConvertFramerate(img); err != nil {
		return
	}
	return
}


func (enc *VideoEncoder) Encode(img *VideoFrame) (pkts []av.Packet, err error) {
	var gotpkt bool
	var pkt av.Packet
	var frames []*VideoFrame

	// If the input framerate and desired encoding framerate differ, convert using FramerateConverter
	imgFps := float64(img.Framerate.Num) / float64(img.Framerate.Den)
	encFps := float64(enc.fpsNum) / float64(enc.fpsDen)

	if imgFps != encFps {
		if frames, err = enc.convertFramerate(img); err != nil {
			return nil, err
		}
		if frames == nil || len(frames) <= 0 {
			return
		}
	} else {
		frames = append(frames, img)
	}

	// When converting to a framerate higher than that of the input,
	// convertFramerate can return multiple frames, so process them all here.
	for _, f := range frames {
		if PixelFormatFF2AV(int32(f.frame.format)) != enc.pixelFormat || f.Width() != enc.width || f.Height() != enc.height {
			if f, err = enc.scale(f); err != nil {
				enc.framerateConverter.FreeOutputImages()
				return nil, err
			}
		}

		if gotpkt, pkt, err = enc.encodeOne(f); err != nil {
			enc.scaler.FreeOutputImage()
			enc.framerateConverter.FreeOutputImages()
			return nil, err
		}
		if gotpkt {
			pkts = append(pkts, pkt)
		}

		enc.scaler.FreeOutputImage()
		enc.framerateConverter.FreeFirstOutputImage()
	}
	return
}

func (enc *VideoEncoder) Close() {
	freeFFCtx(enc.ff)
	enc.scaler.Close()
	enc.framerateConverter.Close()
}

func (enc *VideoEncoder) SetPixelFormat(fmt av.PixelFormat) (err error) {
	enc.pixelFormat = fmt
	return
}

func (enc *VideoEncoder) SetFramerate(num, den int) (err error) {
	enc.fpsNum = num
	enc.fpsDen = den
	return
}

func (enc *VideoEncoder) SetGopSize(gopSize int) (err error) {
	enc.gopSize = gopSize
	return
}

func (enc *VideoEncoder) SetResolution(w, h int) (err error) {
	enc.width = w
	enc.height = h
	return
}

func (enc *VideoEncoder) SetBitrate(bitrate int) (err error) {
	enc.Bitrate = bitrate
	return
}

func (enc *VideoEncoder) SetOption(key string, val interface{}) (err error) {
	ff := &enc.ff.ff

	sval := fmt.Sprint(val)
	if key == "profile" {
		ff.profile = C.avcodec_profile_name_to_int(ff.codec, C.CString(sval))
		if ff.profile == C.FF_PROFILE_UNKNOWN {
			err = fmt.Errorf("ffmpeg: profile `%s` invalid", sval)
			return
		}
		return
	}

	C.av_dict_set(&ff.options, C.CString(key), C.CString(sval), 0)
	return
}

func (enc *VideoEncoder) GetOption(key string, val interface{}) (err error) {
	ff := &enc.ff.ff
	entry := C.av_dict_get(ff.options, C.CString(key), nil, 0)
	if entry == nil {
		err = fmt.Errorf("ffmpeg: GetOption failed: `%s` not exists", key)
		return
	}
	switch p := val.(type) {
	case *string:
		*p = C.GoString(entry.value)
	case *int:
		fmt.Sscanf(C.GoString(entry.value), "%d", p)
	default:
		err = fmt.Errorf("ffmpeg: GetOption failed: val must be *string or *int receiver")
		return
	}
	return
}



func NewVideoEncoderByCodecType(typ av.CodecType) (enc *VideoEncoder, err error) {
	var id uint32

	switch typ {
	case av.H264:
		id = C.AV_CODEC_ID_H264

	default:
		err = fmt.Errorf("ffmpeg: cannot find encoder codecType=%v", typ)
		return
	}

	codec := C.avcodec_find_encoder(id)
	if codec == nil || C.avcodec_get_type(id) != C.AVMEDIA_TYPE_VIDEO {
		err = fmt.Errorf("ffmpeg: cannot find video encoder codecId=%v", id)
		return
	}

	_enc := &VideoEncoder{}
	if _enc.ff, err = newFFCtxByCodec(codec); err != nil {
		err = fmt.Errorf("could not instantiate enc. err = %v", err)
		return
	}
	enc = _enc

	return
}

func NewVideoEncoderByName(name string) (enc *VideoEncoder, err error) {
	_enc := &VideoEncoder{}

	codec := C.avcodec_find_encoder_by_name(C.CString(name))
	if codec == nil || C.avcodec_get_type(codec.id) != C.AVMEDIA_TYPE_VIDEO {
		err = fmt.Errorf("ffmpeg: cannot find video encoder name=%s", name)
		return
	}

	if _enc.ff, err = newFFCtxByCodec(codec); err != nil {
		return
	}
	enc = _enc
	return
}

type VideoDecoder struct {
	ff *ffctx
	Extradata []byte
}

func (self *VideoDecoder) Setup() (err error) {
	ff := &self.ff.ff
	if len(self.Extradata) > 0 {
		ff.codecCtx.extradata = (*C.uint8_t)(unsafe.Pointer(&self.Extradata[0]))
		ff.codecCtx.extradata_size = C.int(len(self.Extradata))
	}
	if C.avcodec_open2(ff.codecCtx, ff.codec, nil) != 0 {
		err = fmt.Errorf("ffmpeg: decoder: avcodec_open2 failed")
		return
	}
	return
}

func (self *VideoDecoder) Decode(pkt []byte) (img *VideoFrame, err error) {
	ff := &self.ff.ff

	cgotimg := C.int(0)
	frame := C.av_frame_alloc()
	cerr := C.wrap_avcodec_decode_video2(ff.codecCtx, frame, unsafe.Pointer(&pkt[0]), C.int(len(pkt)), &cgotimg)
	if cerr < C.int(0) {
		err = fmt.Errorf("ffmpeg: avcodec_decode_video2 failed: %d", cerr)
		return
	}

	if cgotimg != C.int(0) {
		w := int(frame.width)
		h := int(frame.height)
		ys := int(frame.linesize[0])
		cs := int(frame.linesize[1])

		num, den := self.GetFramerate()

		img = &VideoFrame{
			Image: image.YCbCr{
				Y: fromCPtr(unsafe.Pointer(frame.data[0]), ys*h),
				Cb: fromCPtr(unsafe.Pointer(frame.data[1]), cs*h/2),
				Cr: fromCPtr(unsafe.Pointer(frame.data[2]), cs*h/2),
				YStride: ys,
				CStride: cs,
				SubsampleRatio: image.YCbCrSubsampleRatio420,
				Rect: image.Rect(0, 0, w, h),
			},
			frame: frame,
			Framerate: VideoFramerate{
				Num: num,
				Den: den,
			},
		}
		runtime.SetFinalizer(img, freeVideoFrame)
	}

	return
}

func (dec *VideoDecoder) Close() {
	freeFFCtx(dec.ff)
}

func (dec VideoDecoder) GetFramerate() (num, den int) {
	ff := &dec.ff.ff
	num = int(ff.codecCtx.framerate.num)
	den = int(ff.codecCtx.framerate.den)
	return
}

func NewVideoDecoder(stream av.CodecData) (dec *VideoDecoder, err error) {
	_dec := &VideoDecoder{}
	var id uint32

	switch stream.Type() {
	case av.H264:
		h264 := stream.(h264parser.CodecData)
		_dec.Extradata = h264.AVCDecoderConfRecordBytes()
		id = C.AV_CODEC_ID_H264

	default:
		err = fmt.Errorf("ffmpeg: NewVideoDecoder codec=%v unsupported", stream.Type())
		return
	}

	c := C.avcodec_find_decoder(id)
	if c == nil || C.avcodec_get_type(id) != C.AVMEDIA_TYPE_VIDEO {
		err = fmt.Errorf("ffmpeg: cannot find video decoder codecId=%d", id)
		return
	}

	if _dec.ff, err = newFFCtxByCodec(c); err != nil {
		return
	}
	if err =  _dec.Setup(); err != nil {
		return
	}

	dec = _dec
	return
}

func fromCPtr(buf unsafe.Pointer, size int) (ret []uint8) {
	hdr := (*reflect.SliceHeader)((unsafe.Pointer(&ret)))
	hdr.Cap = size
	hdr.Len = size
	hdr.Data = uintptr(buf)
	return
}

func PixelFormatAV2FF(pixelFormat av.PixelFormat) (ffpixelfmt int32) {
	switch pixelFormat {
	case av.I420:
		ffpixelfmt = C.AV_PIX_FMT_YUV420P
	case av.NV12:
		ffpixelfmt = C.AV_PIX_FMT_NV12
	case av.NV21:
		ffpixelfmt = C.AV_PIX_FMT_NV21
	case av.UYVY:
		ffpixelfmt = C.AV_PIX_FMT_UYVY422
	case av.YUYV:
		ffpixelfmt = C.AV_PIX_FMT_YUYV422
	}
	return
}

func PixelFormatFF2AV(ffpixelfmt int32) (pixelFormat av.PixelFormat) {
	switch ffpixelfmt {
	case C.AV_PIX_FMT_YUV420P:
		pixelFormat = av.I420
	case C.AV_PIX_FMT_NV12:
		pixelFormat = av.NV12
	case C.AV_PIX_FMT_NV21:
		pixelFormat = av.NV21
	case C.AV_PIX_FMT_UYVY422:
		pixelFormat = av.UYVY
	case C.AV_PIX_FMT_YUYV422:
		pixelFormat = av.YUYV
	}
	return
}
