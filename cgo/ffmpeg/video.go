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

void free_filters_io(AVFilterContext* f) {
	for(int i=0; i<f->nb_inputs;i++) {
		if(f->inputs[i]) {
			free(f->inputs[i]);
			f->inputs[i] = NULL;
		}
	}
	for(int i=0; i<f->nb_outputs;i++) {
		if(f->outputs[i]) {
			free(f->outputs[i]);
			f->outputs[i] = NULL;
		}
	}
}

	#cgo pkg-config: libavfilter
	#include <libavfilter/avfilter.h>
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

type VideoFrame struct {
	Image image.YCbCr
	frame *C.AVFrame
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

type VideoScaler struct {
	inPixelFormat, OutPixelFormat av.PixelFormat
	inWidth, OutWidth int
	inHeight, OutHeight int
	inYStride, OutYStride int
	inCStride, OutCStride int
	inFpsNum, OutFpsNum int
	inFpsDen, OutFpsDen int
	swsCtx *C.struct_SwsContext

	pts int
	framerateConverterReady bool
	inVideoFilter  *C.AVFilterContext // the first filter in the video chain
	outVideoFilter *C.AVFilterContext // the last filter in the video chain
	outputImgPtrs [3]*C.uint8_t
}

func (self *VideoScaler) Close() {
	C.sws_freeContext(self.swsCtx);
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
		self.inPixelFormat	= PixelFormatFF2AV(int32(src.frame.format))
		self.inWidth		= src.Image.Rect.Dx()
		self.inHeight		= src.Image.Rect.Dy()
		self.inYStride		= src.Image.YStride
		self.inCStride		= src.Image.CStride

		fmt.Printf("Create scale context: %s, %dx%d -> %s, %dx%d\n",
				/*C.av_get_pix_fmt_name*/(self.inPixelFormat.String()), self.inWidth, self.inHeight,
				/*C.av_get_pix_fmt_name*/(self.OutPixelFormat.String()), self.OutWidth, self.OutHeight);

		self.swsCtx = C.sws_getContext(C.int(self.inWidth), C.int(self.inHeight), PixelFormatAV2FF(self.inPixelFormat),
			C.int(self.OutWidth), C.int(self.OutHeight), PixelFormatAV2FF(self.OutPixelFormat),
			C.SWS_BILINEAR, (*C.SwsFilter)(C.NULL), (*C.SwsFilter)(C.NULL), (*C.double)(C.NULL))

		if self.swsCtx == nil {
			err = fmt.Errorf("Impossible to create scale context for the conversion fmt:%d s:%dx%d -> fmt:%d s:%dx%d\n",
				self.inPixelFormat, self.inWidth, self.inHeight,
				self.OutPixelFormat, self.OutWidth, self.OutHeight);
			return
		}
	}

	if !self.framerateConverterReady {
		err = self.ConfigureVideoFilters()
		if err == nil {

			fmt.Println("ConfigureVideoFilters ok")
			self.framerateConverterReady = true
		} else {
			fmt.Println("ConfigureVideoFilters failed:", err)
		}
	}


	dst, err = self.videoScaleOne(src)
	var frame C.AVFrame

	if /* TODO fps conv needed && */ self.framerateConverterReady {
		var cret C.int

		// VideoFrameAssignToFF(frame, ff.frame)
		frame.format = C.int32_t(PixelFormatAV2FF(dst.GetPixelFormat()))

		ys, cs := dst.GetStride()
		frame.linesize[0] = C.int(ys)
		frame.linesize[1] = C.int(cs)
		frame.linesize[2] = C.int(cs)

		w, h := dst.GetResolution()
		frame.width = C.int(w)
		frame.height = C.int(h)
		frame.sample_aspect_ratio.num = 1 // TODO
		frame.sample_aspect_ratio.den = 1

		data0, data1, data2 := dst.GetDataPtr()
		frame.data[0] = (*C.uchar)(data0)
		frame.data[1] = (*C.uchar)(data1)
		frame.data[2] = (*C.uchar)(data2)

		frame.pts = C.int64_t(self.pts)
		self.pts++

		// fmt.Printf("\033[44m%+v\n\033[0m", frame)
		// fmt.Printf("\033[44m%+v\n\033[0m", self.inVideoFilter)
		// fmt.Printf("\033[44m%+v\n\033[0m", self.outVideoFilter)

		cret = C.av_buffersrc_add_frame(self.inVideoFilter, &frame)

		if int(cret) < 0 {
			err = fmt.Errorf("av_buffersrc_add_frame failed")
			fmt.Println(err)
			return
		}


		cret = C.av_buffersink_get_frame_flags(self.outVideoFilter, &frame, C.int(0))
		if int(cret) < 0 {
			if cret == C.AVERROR_EOF {
				// is->viddec.finished = is->viddec.pkt_serial;
				fmt.Println("finished !!!!!!")
			}
			// ret = 0;
			// break;
		}""
	}
	return
}

// static int configure_video_filters()
func (self *VideoScaler) ConfigureVideoFilters() (err error) {
	var ret int
	var filt_src, filt_out, last_filter *C.AVFilterContext
	var graph *C.struct_AVFilterGraph = C.avfilter_graph_alloc() // TODO free

	// sws_flags_str := fmt.Sprintf("flags=%s", ) // sws flags go here
	// csws_flags_str := C.CString(sws_flags_str)
	// defer C.free(unsafe.Pointer(csws_flags_str))
	// if C.strlen(csws_flags_str) {
	// 	csws_flags_str[C.strlen(csws_flags_str)-1] = 0 // '\0'
	// }
	// graph.scale_sws_opts = av_strdup(csws_flags_str)

	buffersrc_args := fmt.Sprintf("video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=%d/%d:frame_rate=%d/%d",
		self.inWidth, self.inHeight, C.int32_t(PixelFormatAV2FF(self.inPixelFormat)),
		self.OutFpsDen, self.OutFpsNum, 1, 1, // sar num,  max(sar denom, 1)
		self.OutFpsNum, self.OutFpsDen)

	fmt.Printf("\033[44m%+v\n\033[0m", buffersrc_args)

	// fmt.Println("buffersrc_args", buffersrc_args)
	cbuffersrc_args := C.CString(buffersrc_args)
	defer C.free(unsafe.Pointer(cbuffersrc_args))

	strbuffer := C.CString("buffer")
	defer C.free(unsafe.Pointer(strbuffer))

	strffplay_buffer := C.CString("ffplay_buffer")
	defer C.free(unsafe.Pointer(strffplay_buffer))

	ret = int(C.avfilter_graph_create_filter(&filt_src, C.avfilter_get_by_name(strbuffer), strffplay_buffer, cbuffersrc_args, C.NULL, graph))
	if ret < 0 {
		err = fmt.Errorf("avfilter_graph_create_filter failed")
		return
	}

	strbuffersink := C.CString("buffersink")
	defer C.free(unsafe.Pointer(strbuffersink))

	strffplay_buffersink := C.CString("ffplay_buffersink")
	defer C.free(unsafe.Pointer(strffplay_buffersink))

	ret = int(C.avfilter_graph_create_filter(&filt_out, C.avfilter_get_by_name(strbuffersink), strffplay_buffersink, (*C.char)(C.NULL), C.NULL, graph))
	if ret < 0 {
		err = fmt.Errorf("avfilter_graph_create_filter failed")
		return
	}



	var pix_fmts [2]C.enum_AVPixelFormat
	pix_fmts[0] = C.AV_PIX_FMT_YUV420P
	pix_fmts[1] = C.AV_PIX_FMT_NONE;


	strpix_fmts := C.CString("pix_fmts")
    defer C.free(unsafe.Pointer(strpix_fmts))

	// TODO address of pix_fmts ?
	ret = int(C.wrap_av_opt_set_int_list(unsafe.Pointer(filt_out), strpix_fmts, unsafe.Pointer(&pix_fmts),  C.AV_PIX_FMT_NONE, C.AV_OPT_SEARCH_CHILDREN))
	if ret < 0 {
		err = fmt.Errorf("wrap_av_opt_set_int_list failed")
		return
	}

	last_filter = filt_out;

	// FIXME version without configureFiltergraph
	filterarg := fmt.Sprintf("fps=%d/%d", 1000, 1000)
	self.AddFilter(graph, filt_src, last_filter, "framerate", filterarg)
	ret = int(C.avfilter_graph_config(graph, C.NULL))
	if ret < 0 {
		err = fmt.Errorf("avfilter_graph_config failed")
	}

/*
	// TODO fill vfilters with filters pipeline
	vfilters := fmt.Sprintf("fps=fps=%d/%d", 1000, 1000)//self.OutFpsNum, self.OutFpsDen)
	cvfilters := C.CString(vfilters)
	defer C.free(unsafe.Pointer(cvfilters))

	err = self.configureFiltergraph(graph, cvfilters, filt_src, last_filter)
	if err != nil {
		return
	}
*/

	self.inVideoFilter  = filt_src;
	self.outVideoFilter = filt_out;
	return
}

// Note: this func adds a filter before the lastly added filter, so the
// processing order of the filters is in reverse
func (self *VideoScaler) AddFilter(graph *C.AVFilterGraph, first_filter *C.AVFilterContext, last_filter *C.AVFilterContext, name string, arg string) (err error){
	var ret int
	var filt_ctx *C.AVFilterContext

	strname := C.CString(name)
	defer C.free(unsafe.Pointer(strname))

	strprefix := C.CString("ffplay_")
	defer C.free(unsafe.Pointer(strprefix))

	strarg := C.CString(arg)
	defer C.free(unsafe.Pointer(strarg))

	ret = int(C.avfilter_graph_create_filter(&filt_ctx, C.avfilter_get_by_name(strname), strprefix, strarg, C.NULL, graph))
	if ret < 0 {
		err = fmt.Errorf("avfilter_graph_create_filter failed")
		return
	}

	ret = int(C.avfilter_link(filt_ctx, 0, last_filter, 0))
	if ret < 0 {
		err = fmt.Errorf("first avfilter_link failed: %d", ret)
		return
	}

	ret = int(C.avfilter_link(first_filter, 0, filt_ctx, 0))
	if ret < 0 {
		err = fmt.Errorf("second avfilter_link failed: %d", ret)
		return
	}

	return
}


func (self *VideoScaler) configureFiltergraph(graph *C.AVFilterGraph, filtergraph *C.char, source_ctx *C.AVFilterContext, sink_ctx *C.AVFilterContext) (err error){
	var inputs, outputs *C.AVFilterInOut

	nb_filters_init := graph.nb_filters 
	if filtergraph != (*C.char)(C.NULL) {
		outputs = C.avfilter_inout_alloc()
		inputs  = C.avfilter_inout_alloc()
		defer C.avfilter_inout_free(&outputs)
		defer C.avfilter_inout_free(&inputs)
		if (unsafe.Pointer(outputs) == C.NULL || unsafe.Pointer(inputs) == C.NULL) {
			err = fmt.Errorf("ENOMEM")
			return
		}
	}

	strin := C.CString("in")
	defer C.free(unsafe.Pointer(strin))
	outputs.name       = C.av_strdup(strin)
	outputs.filter_ctx = source_ctx
	outputs.pad_idx    = 0
	outputs.next       = (*C.struct_AVFilterInOut)(C.NULL)

	strout := C.CString("out")
	defer C.free(unsafe.Pointer(strout))
	inputs.name        = C.av_strdup(strout)
	inputs.filter_ctx  = sink_ctx
	inputs.pad_idx     = 0
	inputs.next        = (*C.struct_AVFilterInOut)(C.NULL)

	ret := int(C.avfilter_graph_parse_ptr(graph, filtergraph, &inputs, &outputs, C.NULL))
	if ret < 0 {
		err = fmt.Errorf("avfilter_graph_parse_ptr failed")
		return
	} else {
		C.free_filters_io(source_ctx)
		C.free_filters_io(sink_ctx)

		ret = int(C.avfilter_link(source_ctx, 0, sink_ctx, 0))

		if ret < 0 {
			err = fmt.Errorf("avfilter_link failed: %d", ret) // FIXME
			return
		}
	}

	// Reorder the filters to ensure that inputs of the custom filters are merged first
	nb_filters := graph.nb_filters
	filters := (*[1 << 30]C.AVFilterContext)(unsafe.Pointer(graph.filters))[:nb_filters:nb_filters]

	// fmt.Printf("filters: %+v\n", filters)
	// fmt.Println("nb_filters", nb_filters)
	// fmt.Println("nb_filters_init", nb_filters_init)

	for i := 0; i < int(nb_filters - nb_filters_init); i++ {
		// swap
		target := i + int(nb_filters) - 1
		fmt.Println("swap", i, "with", target)

		filters[i], filters[target] = filters[target], filters[i]
	}

	// fmt.Printf("filters: %+v\n", filters)
	// fmt.Printf("graph: %+v\n", graph)

	ret = int(C.avfilter_graph_config(graph, C.NULL))
	if ret < 0 {
		err = fmt.Errorf("avfilter_graph_config failed")
	} else {
		err = fmt.Errorf("avfilter_graph_config ok")
	}
	return
}


// TODO VideoConverter type


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


	// All the following params are described in ffmpeg: avcodec.h, in struct AVCodecContext
	ff.codecCtx.width			= C.int(enc.width)
	ff.codecCtx.height			= C.int(enc.height)
	ff.codecCtx.pix_fmt			= PixelFormatAV2FF(enc.pixelFormat)

	ff.codecCtx.time_base.num	= C.int(enc.fpsDen)
	ff.codecCtx.time_base.den	= C.int(enc.fpsNum)
	ff.codecCtx.gop_size		= C.int(enc.gopSize)
	ff.codecCtx.bit_rate		= C.int64_t(enc.Bitrate)

	if C.avcodec_open2(ff.codecCtx, ff.codec, nil) != 0 {
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

func (enc *VideoEncoder) encodeOne(img *VideoFrame) (gotpkt bool, pkt []byte, err error) {
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

	// Increase pts and convert in 90k: pts * 90000 / fps
	ff.frame.pts = C.int64_t( int(enc.pts) * enc.fpsDen * 90000 / enc.fpsNum)
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

	if ok, kbps := enc.bm.Measure(len(avpkt.Data)); ok {
		fmt.Println("Encoded video bitrate (kbps):", kbps)
	}

	return gotpkt, avpkt.Data, err
}


func (self *VideoEncoder) scale(img *VideoFrame) (out *VideoFrame, err error) {
	if self.scaler == nil {
		self.scaler = &VideoScaler{
			inPixelFormat:	PixelFormatFF2AV(int32(img.frame.format)),
			inWidth:		img.Image.Rect.Dx(),
			inHeight:		img.Image.Rect.Dy(),
			inYStride:		img.Image.YStride,
			inCStride:		img.Image.CStride,
			inFpsNum:			in.FpsNum,
			inFpsDen:			in.FpsDen,
			OutPixelFormat:	self.pixelFormat,
			OutWidth:		self.width,
			OutHeight:		self.height,
			OutYStride:		self.width,
			OutCStride:		self.width/self.pixelFormat.HorizontalSubsampleRatio(),
			OutFpsNum:			self.fpsNum,
			OutFpsDen:			self.fpsDen,
		}
	}
	if out, err = self.scaler.VideoScale(img); err != nil {
		return
	}
	return
}


func (enc *VideoEncoder) Encode(img *VideoFrame) (pkts [][]byte, err error) {
	var gotpkt bool
	var pkt []byte

	if PixelFormatFF2AV(int32(img.frame.format)) != enc.pixelFormat || img.Width() != enc.width || img.Height() != enc.height {
		if img, err = enc.scale(img); err != nil {
			return nil, err
		}
	}

	if gotpkt, pkt, err = enc.encodeOne(img); err != nil {
		return nil, err
	}
	if gotpkt {
		pkts = append(pkts, pkt)
	}

	enc.scaler.FreeOutputImage()
	return
}

func (enc *VideoEncoder) Close() {
	freeFFCtx(enc.ff)
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

		img = &VideoFrame{Image: image.YCbCr{
			Y: fromCPtr(unsafe.Pointer(frame.data[0]), ys*h),
			Cb: fromCPtr(unsafe.Pointer(frame.data[1]), cs*h/2),
			Cr: fromCPtr(unsafe.Pointer(frame.data[2]), cs*h/2),
			YStride: ys,
			CStride: cs,
			SubsampleRatio: image.YCbCrSubsampleRatio420,
			Rect: image.Rect(0, 0, w, h),
		}, frame: frame}
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
