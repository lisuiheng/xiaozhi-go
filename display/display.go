package display

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"golang.org/x/image/bmp"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Linux 帧缓冲结构体
type fb_var_screeninfo struct {
	xres, yres               uint32
	xres_virtual             uint32
	yres_virtual             uint32
	xoffset, yoffset         uint32
	bits_per_pixel           uint32
	grayscale                uint32
	red, green, blue, transp fb_bitfield
}

type fb_bitfield struct {
	offset, length uint32
	msb_right      uint32
}

type fb_fix_screeninfo struct {
	id          [16]byte
	smem_start  uint32
	smem_len    uint32
	_type       uint32
	type_aux    uint32
	visual      uint32
	xpanstep    uint16
	ypanstep    uint16
	ywrapstep   uint16
	line_length uint32
	mmio_start  uint32
	mmio_len    uint32
	accel       uint32
}

// 对齐方式常量
const (
	ALIGN_LEFT   = 0
	ALIGN_CENTER = 1
	ALIGN_RIGHT  = 2
	ALIGN_TOP    = 0
	ALIGN_MIDDLE = 1
	ALIGN_BOTTOM = 2
)

// DisplayController 管理显示任务
type DisplayController struct {
	currentTask   *taskContext
	fontPath      string
	taskMutex     sync.Mutex
	fbInitialized bool
	currentAnim   string // 记录当前动画名称
	animMutex     sync.RWMutex
	fontFace      font.Face
	fontMutex     sync.Mutex
	fontSize      float64
}

type taskContext struct {
	ctx      context.Context
	cancel   context.CancelFunc
	taskType string        // "animation", "datetime", "text", "image"
	done     chan struct{} // 任务完成通知通道
}

// NewDisplayController 创建新的显示控制器
func NewDisplayController() *DisplayController {
	return &DisplayController{
		fontSize: 24,
		fontPath: "/usr/share/fonts/TTF/HarmonyOS_Sans_SC_Regular.ttf",
	}
}

// 帧缓冲相关变量
var (
	fb         *os.File
	fbData     []byte
	vinfo      fb_var_screeninfo
	finfo      fb_fix_screeninfo
	fbWidth    int
	fbHeight   int
	bpp        int
	lineLength int
	dbuffer    DoubleBuffer
)

// DoubleBuffer 双缓冲结构
type DoubleBuffer struct {
	frontBuffer []byte
	backBuffer  []byte
}

// Rotation 旋转角度类型
type Rotation int

const (
	ROTATE_0   Rotation = 0
	ROTATE_90  Rotation = 90
	ROTATE_180 Rotation = 180
	ROTATE_270 Rotation = 270
)

const (
	FBIOGET_VSCREENINFO = 0x4600
	FBIOGET_FSCREENINFO = 0x4602
	FBIO_WAITFORVSYNC   = 0x40044620
)

func (r *Rotation) String() string {
	return fmt.Sprintf("%d", *r)
}

func (r *Rotation) Set(value string) error {
	var rot int
	_, err := fmt.Sscanf(value, "%d", &rot)
	if err != nil {
		return err
	}
	switch Rotation(rot) {
	case ROTATE_0, ROTATE_90, ROTATE_180, ROTATE_270:
		*r = Rotation(rot)
		return nil
	default:
		return errors.New("rotation must be 0, 90, 180, or 270")
	}
}

// StartAnimation 开始播放动画
func (dc *DisplayController) StartAnimation(folderPath string, rotation Rotation, fps int, preload bool) error {
	slog.Info("StartAnimation called", "folderPath", folderPath, "rotation", rotation, "fps", fps, "preload", preload)

	dc.animMutex.Lock()
	// 如果已经是相同的动画，则不中断
	if dc.currentAnim == folderPath {
		dc.animMutex.Unlock()
		slog.Info("Animation already running, skipping", "folderPath", folderPath)
		return nil
	}
	dc.currentAnim = folderPath
	dc.animMutex.Unlock()

	dc.taskMutex.Lock()
	defer dc.taskMutex.Unlock()

	// 中断当前任务
	if dc.currentTask != nil {
		slog.Info("Interrupting current display task", "type", dc.currentTask.taskType)
		dc.currentTask.cancel()
		dc.waitTaskDone()
	}

	ctx, cancel := context.WithCancel(context.Background())
	dc.currentTask = &taskContext{
		ctx:      ctx,
		cancel:   cancel,
		taskType: "animation",
		done:     make(chan struct{}),
	}

	go func() {
		defer close(dc.currentTask.done)
		dc.runAnimation(ctx, folderPath, rotation, fps, preload)
	}()

	return nil
}

// ShowDateTime 显示时间日期
func (dc *DisplayController) ShowDateTime(
	fontSize float64,
	color interface{},
	hAlign, vAlign int,
	timeFormat, dateFormat string,
) error {
	dc.taskMutex.Lock()
	defer dc.taskMutex.Unlock()

	// 中断当前任务
	if dc.currentTask != nil {
		slog.Info("Interrupting current display task", "type", dc.currentTask.taskType)
		dc.currentTask.cancel()
		dc.waitTaskDone()
	}

	// 清屏
	dc.clearScreen()

	// 清除动画记录
	dc.animMutex.Lock()
	dc.currentAnim = ""
	dc.animMutex.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	dc.currentTask = &taskContext{
		ctx:      ctx,
		cancel:   cancel,
		taskType: "datetime",
		done:     make(chan struct{}),
	}

	go func() {
		defer close(dc.currentTask.done)
		dc.runDateTime(ctx, fontSize, color, hAlign, vAlign, timeFormat, dateFormat)
	}()

	return nil
}

// ShowText 显示文本
func (dc *DisplayController) ShowText(text string, fontSize float64, color interface{}, hAlign, vAlign int) error {
	dc.taskMutex.Lock()
	defer dc.taskMutex.Unlock()

	// 中断当前任务
	if dc.currentTask != nil {
		slog.Info("Interrupting current display task", "type", dc.currentTask.taskType)
		dc.currentTask.cancel()
		dc.waitTaskDone()
	}

	// 清屏
	dc.clearScreen()

	// 清除动画记录
	dc.animMutex.Lock()
	dc.currentAnim = ""
	dc.animMutex.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	dc.currentTask = &taskContext{
		ctx:      ctx,
		cancel:   cancel,
		taskType: "text",
		done:     make(chan struct{}),
	}

	go func() {
		defer close(dc.currentTask.done)
		dc.runText(ctx, text, fontSize, color, hAlign, vAlign)
	}()

	return nil
}

// ShowImage 显示单张图片
func (dc *DisplayController) ShowImage(imagePath string, rotation Rotation) error {
	dc.taskMutex.Lock()
	defer dc.taskMutex.Unlock()

	// 中断当前任务
	if dc.currentTask != nil {
		slog.Info("Interrupting current display task", "type", dc.currentTask.taskType)
		dc.currentTask.cancel()
		dc.waitTaskDone()
	}

	// 清屏
	dc.clearScreen()

	// 清除动画记录
	dc.animMutex.Lock()
	dc.currentAnim = ""
	dc.animMutex.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	dc.currentTask = &taskContext{
		ctx:      ctx,
		cancel:   cancel,
		taskType: "image",
		done:     make(chan struct{}),
	}

	go func() {
		defer close(dc.currentTask.done)
		dc.runImage(ctx, imagePath, rotation)
	}()

	return nil
}

// Stop 停止当前显示任务
func (dc *DisplayController) Stop() {
	dc.taskMutex.Lock()
	defer dc.taskMutex.Unlock()

	if dc.currentTask != nil {
		slog.Info("Stopping current display task", "type", dc.currentTask.taskType)
		dc.currentTask.cancel()
		dc.waitTaskDone()
	}

	dc.animMutex.Lock()
	dc.currentAnim = ""
	dc.animMutex.Unlock()
}

// waitTaskDone 等待任务完成
func (dc *DisplayController) waitTaskDone() {
	if dc.currentTask == nil {
		return
	}

	select {
	case <-dc.currentTask.done:
		slog.Debug("任务已完全停止")
	case <-time.After(500 * time.Millisecond):
		slog.Warn("任务停止超时，强制终止")
	}
	dc.currentTask = nil
}

// runAnimation 动画播放实现
func (dc *DisplayController) runAnimation(ctx context.Context, folderPath string, rotation Rotation, fps int, preload bool) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Animation panic recovered", "error", r)
		}
	}()

	slog.Info("开始动画", "文件夹", folderPath, "旋转", rotation, "帧率", fps, "预加载", preload)

	if ctx.Err() != nil {
		slog.Info("动画在开始前已被取消")
		return
	}

	if err := dc.initFramebuffer(); err != nil {
		slog.Error("初始化帧缓冲失败", "错误", err)
		return
	}
	defer dc.closeFramebuffer()

	// 清空后台缓冲
	for i := range dbuffer.backBuffer {
		dbuffer.backBuffer[i] = 0
	}

	var images []image.Image
	var imageFiles []string

	if preload {
		loadedImages, err := dc.preloadImages(folderPath)
		if err != nil {
			slog.Error("预加载失败", "错误", err)
			return
		}
		images = loadedImages
		slog.Info("图片预加载完成", "数量", len(images))
	} else {
		files, err := os.ReadDir(folderPath)
		if err != nil {
			slog.Error("读取目录失败", "文件夹", folderPath, "错误", err)
			return
		}

		for _, file := range files {
			if file.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(file.Name()))
			switch ext {
			case ".jpg", ".jpeg", ".png", ".bmp":
				imageFiles = append(imageFiles, filepath.Join(folderPath, file.Name()))
			}
		}

		if len(imageFiles) == 0 {
			slog.Error("未找到支持的图片文件", "文件夹", folderPath)
			return
		}
	}

	frameDuration := time.Duration(float64(time.Second) / float64(fps))

	if preload {
	animationLoop:
		for {
			for _, img := range images {
				select {
				case <-ctx.Done():
					slog.Info("动画被中断")
					break animationLoop
				default:
					startTime := time.Now()

					if err := dc.drawImageWithDoubleBuffer(img, rotation); err != nil {
						slog.Error("帧渲染错误", "错误", err)
						continue
					}

					elapsed := time.Since(startTime)
					if remaining := frameDuration - elapsed; remaining > 0 {
						select {
						case <-time.After(remaining):
						case <-ctx.Done():
							break animationLoop
						}
					}
				}
			}
			if ctx.Err() != nil {
				break animationLoop
			}
		}
	} else {
	frameLoop:
		for {
			for _, filePath := range imageFiles {
				select {
				case <-ctx.Done():
					slog.Info("动画被中断")
					break frameLoop
				default:
					startTime := time.Now()

					img, err := dc.loadImage(filePath)
					if err != nil {
						slog.Warn("跳过图片", "文件", filePath, "错误", err)
						continue
					}

					if err := dc.drawImageWithDoubleBuffer(img, rotation); err != nil {
						slog.Error("帧渲染错误", "文件", filePath, "错误", err)
						continue
					}

					elapsed := time.Since(startTime)
					if remaining := frameDuration - elapsed; remaining > 0 {
						select {
						case <-time.After(remaining):
						case <-ctx.Done():
							break frameLoop
						}
					}
				}
			}
			if ctx.Err() != nil {
				break frameLoop
			}
		}
	}

	slog.Info("动画停止")
}

// runDateTime 时间日期显示实现
func (dc *DisplayController) runDateTime(
	ctx context.Context,
	fontSize float64,
	color interface{},
	hAlign, vAlign int,
	timeFormat, dateFormat string,
) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("时间日期显示 panic 恢复", "错误", r)
		}
	}()

	if ctx.Err() != nil {
		slog.Info("时间日期显示在开始前已被取消")
		return
	}

	slog.Info("显示时间日期", "大小", fontSize, "时间格式", timeFormat, "日期格式", dateFormat)

	if err := dc.initFramebuffer(); err != nil {
		slog.Error("初始化帧缓冲失败", "错误", err)
		return
	}
	defer dc.closeFramebuffer()

	if err := dc.loadFont(dc.fontPath, fontSize); err != nil {
		slog.Error("加载字体错误", "路径", dc.fontPath, "错误", err)
		return
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

datetimeLoop:
	for {
		select {
		case <-ctx.Done():
			slog.Info("时间日期显示被中断")
			break datetimeLoop
		case now := <-ticker.C:
			// 清空后台缓冲
			for i := range dbuffer.backBuffer {
				dbuffer.backBuffer[i] = 0
			}

			timeStr := now.Format(timeFormat)
			dateStr := now.Format(dateFormat)
			text := dateStr + "\n" + timeStr

			dc.drawTextToBuffer(text, color, hAlign, vAlign)

			dc.waitForVSync()
			copy(dbuffer.frontBuffer, dbuffer.backBuffer)
			copy(fbData, dbuffer.frontBuffer)
		}
	}
}

// runText 文本显示实现
func (dc *DisplayController) runText(ctx context.Context, text string, fontSize float64, color interface{}, hAlign, vAlign int) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("文本显示 panic 恢复", "错误", r)
		}
	}()

	if ctx.Err() != nil {
		slog.Info("文本显示在开始前已被取消")
		return
	}

	slog.Info("显示文本", "文本", text, "大小", fontSize)

	if err := dc.initFramebuffer(); err != nil {
		slog.Error("初始化帧缓冲失败", "错误", err)
		return
	}
	defer dc.closeFramebuffer()

	// 清空后台缓冲
	for i := range dbuffer.backBuffer {
		dbuffer.backBuffer[i] = 0
	}

	if err := dc.loadFont(dc.fontPath, fontSize); err != nil {
		slog.Error("加载字体错误", "路径", dc.fontPath, "错误", err)
		return
	}

	dc.drawTextToBuffer(text, color, hAlign, vAlign)

	dc.waitForVSync()
	copy(dbuffer.frontBuffer, dbuffer.backBuffer)
	copy(fbData, dbuffer.frontBuffer)

	slog.Info("文本显示完成")

	// 保持显示直到被中断
	select {
	case <-ctx.Done():
		slog.Info("文本显示被中断")
	}
}

// runImage 单张图片显示实现
func (dc *DisplayController) runImage(ctx context.Context, imagePath string, rotation Rotation) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("图片显示 panic 恢复", "错误", r)
		}
	}()

	if ctx.Err() != nil {
		slog.Info("图片显示在开始前已被取消")
		return
	}

	slog.Info("显示图片", "路径", imagePath, "旋转", rotation)

	if err := dc.initFramebuffer(); err != nil {
		slog.Error("初始化帧缓冲失败", "错误", err)
		return
	}
	defer dc.closeFramebuffer()

	for i := range dbuffer.backBuffer {
		dbuffer.backBuffer[i] = 0
	}

	img, err := dc.loadImage(imagePath)
	if err != nil {
		slog.Error("加载图片错误", "路径", imagePath, "错误", err)
		return
	}

	if ctx.Err() != nil {
		slog.Info("图片显示在加载后被取消")
		return
	}

	if err := dc.drawImageWithDoubleBuffer(img, rotation); err != nil {
		slog.Error("绘制图片错误", "错误", err)
		return
	}

	slog.Info("图片显示完成", "旋转", rotation)

	select {
	case <-ctx.Done():
		slog.Info("图片显示被中断")
		dc.clearScreen()
	}
}

// loadFont 加载字体
func (dc *DisplayController) loadFont(fontPath string, fontSize float64) error {
	dc.fontMutex.Lock()
	defer dc.fontMutex.Unlock()

	if dc.fontFace != nil && dc.fontSize == fontSize {
		return nil
	}

	if dc.fontFace != nil {
		dc.fontFace = nil
	}

	fontBytes, err := os.ReadFile(fontPath)
	if err != nil {
		return fmt.Errorf("读取字体文件失败：%w", err)
	}

	ft, err := opentype.Parse(fontBytes)
	if err != nil {
		return fmt.Errorf("解析字体失败：%w", err)
	}

	face, err := opentype.NewFace(ft, &opentype.FaceOptions{
		Size:    fontSize,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return fmt.Errorf("创建字体面失败：%w", err)
	}

	dc.fontFace = face
	dc.fontSize = fontSize
	return nil
}

// drawTextToBuffer 绘制文本到后台缓冲
func (dc *DisplayController) drawTextToBuffer(text string, color interface{}, hAlign, vAlign int) {
	if dc.fontFace == nil {
		slog.Error("字体未加载")
		return
	}

	metrics := dc.fontFace.Metrics()
	ascent := metrics.Ascent.Ceil()
	descent := metrics.Descent.Ceil()
	lineHeight := ascent + descent + 2

	lines := strings.Split(text, "\n")
	maxWidth := 0
	for _, line := range lines {
		width := font.MeasureString(dc.fontFace, line).Ceil()
		if width > maxWidth {
			maxWidth = width
		}
	}

	totalHeight := len(lines) * lineHeight

	var startX, startY int

	switch hAlign {
	case ALIGN_CENTER:
		startX = (fbWidth - maxWidth) / 2
	case ALIGN_RIGHT:
		startX = fbWidth - maxWidth
	default:
		startX = 0
	}

	switch vAlign {
	case ALIGN_MIDDLE:
		startY = (fbHeight-totalHeight)/2 + ascent
	case ALIGN_BOTTOM:
		startY = fbHeight - totalHeight + ascent
	default:
		startY = ascent
	}

	y := startY
	for _, line := range lines {
		if line == "" {
			y += lineHeight
			continue
		}

		width := font.MeasureString(dc.fontFace, line).Ceil()
		x := startX
		if hAlign == ALIGN_CENTER {
			x = (fbWidth - width) / 2
		} else if hAlign == ALIGN_RIGHT {
			x = fbWidth - width
		}

		dc.drawString(line, x, y, color)
		y += lineHeight
	}
}

// drawString 绘制字符串
func (dc *DisplayController) drawString(text string, x, y int, colorValue interface{}) {
	var col color.Color
	switch v := colorValue.(type) {
	case color.Color:
		col = v
	case struct{ R, G, B uint8 }:
		col = color.RGBA{R: v.R, G: v.G, B: v.B, A: 255}
	default:
		col = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	}

	drawer := font.Drawer{
		Dst:  &framebufferTarget{buffer: dbuffer.backBuffer, width: fbWidth, height: fbHeight, bpp: bpp, lineLength: lineLength},
		Src:  image.NewUniform(col),
		Face: dc.fontFace,
		Dot:  fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y)},
	}

	prevC := rune(-1)
	for _, c := range text {
		if prevC >= 0 {
			x += dc.fontFace.Kern(prevC, c).Ceil()
		}
		dr, mask, maskp, advance, ok := dc.fontFace.Glyph(drawer.Dot, c)
		if !ok {
			continue
		}

		drawGlyph(&drawer, mask, maskp, dr, col)
		prevC = c
		drawer.Dot.X += advance
	}
}

// drawGlyph 绘制字形
func drawGlyph(d *font.Drawer, mask image.Image, maskp image.Point, dr image.Rectangle, c interface{}) {
	if mask == nil {
		return
	}

	var col color.Color
	switch v := c.(type) {
	case color.Color:
		col = v
	case struct{ R, G, B uint8 }:
		col = color.RGBA{R: v.R, G: v.G, B: v.B, A: 255}
	default:
		col = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	}

	dx := dr.Min.X
	dy := dr.Min.Y

	for y := maskp.Y; y < maskp.Y+dr.Dy(); y++ {
		for x := maskp.X; x < maskp.X+dr.Dx(); x++ {
			_, _, _, a := mask.At(x, y).RGBA()
			if a > 0 {
				d.Dst.Set(dx+(x-maskp.X), dy+(y-maskp.Y), col)
			}
		}
	}
}

// framebufferTarget 实现 draw.Image 接口
type framebufferTarget struct {
	buffer      []byte
	width       int
	height      int
	bpp         int
	lineLength  int
	lastSetX    int
	lastSetY    int
	lastSetData uint16
}

func (ft *framebufferTarget) ColorModel() color.Model {
	return color.RGBAModel
}

func (ft *framebufferTarget) Bounds() image.Rectangle {
	return image.Rect(0, 0, ft.width, ft.height)
}

func (ft *framebufferTarget) At(x, y int) color.Color {
	if x < 0 || y < 0 || x >= ft.width || y >= ft.height {
		return color.RGBA{}
	}
	offset := y*ft.lineLength + x*(ft.bpp/8)
	color565 := binary.LittleEndian.Uint16(ft.buffer[offset:])
	r := uint8((color565>>11)&0x1F) << 3
	g := uint8((color565>>5)&0x3F) << 2
	b := uint8(color565&0x1F) << 3
	return color.RGBA{R: r, G: g, B: b, A: 255}
}

func (ft *framebufferTarget) Set(x, y int, c color.Color) {
	if x < 0 || y < 0 || x >= ft.width || y >= ft.height {
		return
	}

	if x == ft.lastSetX && y == ft.lastSetY {
		r, g, b, _ := c.RGBA()
		color565 := uint16((r>>8)&0xF8)<<8 | uint16((g>>8)&0xFC)<<3 | uint16((b>>8)&0xF8)>>3
		if color565 == ft.lastSetData {
			return
		}
	}

	offset := y*ft.lineLength + x*(ft.bpp/8)
	r, g, b, _ := c.RGBA()
	color565 := uint16((r>>8)&0xF8)<<8 | uint16((g>>8)&0xFC)<<3 | uint16((b>>8)&0xF8)>>3
	binary.LittleEndian.PutUint16(ft.buffer[offset:], color565)

	ft.lastSetX = x
	ft.lastSetY = y
	ft.lastSetData = color565
}

func (dc *DisplayController) initFramebuffer() error {
	if dc.fbInitialized {
		return nil
	}

	var err error
	fb, err = os.OpenFile("/dev/fb0", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("打开帧缓冲错误：%v", err)
	}

	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fb.Fd()),
		FBIOGET_VSCREENINFO,
		uintptr(unsafe.Pointer(&vinfo)),
	); errno != 0 {
		fb.Close()
		return fmt.Errorf("获取可变屏幕信息错误：%v", errno)
	}

	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fb.Fd()),
		FBIOGET_FSCREENINFO,
		uintptr(unsafe.Pointer(&finfo)),
	); errno != 0 {
		fb.Close()
		return fmt.Errorf("获取固定屏幕信息错误：%v", errno)
	}

	fbWidth = int(vinfo.xres)
	fbHeight = int(vinfo.yres)
	bpp = int(vinfo.bits_per_pixel)
	lineLength = int(finfo.line_length)

	fbSize := finfo.smem_len
	fbData, err = syscall.Mmap(
		int(fb.Fd()),
		0,
		int(fbSize),
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_SHARED)
	if err != nil {
		fb.Close()
		return fmt.Errorf("映射帧缓冲错误：%v", err)
	}

	dbuffer.frontBuffer = make([]byte, len(fbData))
	dbuffer.backBuffer = make([]byte, len(fbData))
	copy(dbuffer.frontBuffer, fbData)

	dc.fbInitialized = true
	return nil
}

func (dc *DisplayController) closeFramebuffer() {
	if fbData != nil {
		syscall.Munmap(fbData)
		fbData = nil
	}
	if fb != nil {
		fb.Close()
		fb = nil
	}
	dc.fbInitialized = false
}

func (dc *DisplayController) waitForVSync() {
	if fb != nil {
		syscall.Syscall(syscall.SYS_IOCTL, fb.Fd(), FBIO_WAITFORVSYNC, 0)
	}
}

func (dc *DisplayController) drawImageWithDoubleBuffer(img image.Image, rotation Rotation) error {
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	imgWidth := rgba.Bounds().Dx()
	imgHeight := rgba.Bounds().Dy()

	targetWidth := imgWidth
	targetHeight := imgHeight
	if rotation == ROTATE_90 || rotation == ROTATE_270 {
		targetWidth = imgHeight
		targetHeight = imgWidth
	}

	scaleX := float64(fbWidth) / float64(targetWidth)
	scaleY := float64(fbHeight) / float64(targetHeight)
	scale := scaleX
	if scaleY < scaleX {
		scale = scaleY
	}

	displayWidth := int(float64(targetWidth) * scale)
	displayHeight := int(float64(targetHeight) * scale)
	offsetX := (fbWidth - displayWidth) / 2
	offsetY := (fbHeight - displayHeight) / 2

	for y := 0; y < displayHeight; y++ {
		for x := 0; x < displayWidth; x++ {
			srcX := float64(x) / scale
			srcY := float64(y) / scale

			rotX, rotY := getRotatedPixel(
				int(srcX),
				int(srcY),
				imgWidth,
				imgHeight,
				rotation)

			if rotX < 0 || rotY < 0 || rotX >= imgWidth || rotY >= imgHeight {
				continue
			}

			idx := (rotY*rgba.Stride + rotX*4)
			r := rgba.Pix[idx]
			g := rgba.Pix[idx+1]
			b := rgba.Pix[idx+2]

			fbX := x + offsetX
			fbY := y + offsetY
			if fbX < 0 || fbY < 0 || fbX >= fbWidth || fbY >= fbHeight {
				continue
			}

			offset := fbY*lineLength + fbX*(bpp/8)
			switch bpp {
			case 16:
				color := uint16(r>>3)<<11 | uint16(g>>2)<<5 | uint16(b>>3)
				binary.LittleEndian.PutUint16(dbuffer.backBuffer[offset:], color)
			case 32:
				dbuffer.backBuffer[offset] = b
				dbuffer.backBuffer[offset+1] = g
				dbuffer.backBuffer[offset+2] = r
				dbuffer.backBuffer[offset+3] = 0xFF
			}
		}
	}

	dc.waitForVSync()
	copy(dbuffer.frontBuffer, dbuffer.backBuffer)
	copy(fbData, dbuffer.frontBuffer)

	return nil
}

func (dc *DisplayController) clearScreen() {
	if !dc.fbInitialized || fbData == nil {
		return
	}
	for i := range fbData {
		fbData[i] = 0
	}
}

func (dc *DisplayController) preloadImages(folderPath string) ([]image.Image, error) {
	files, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, fmt.Errorf("读取文件夹错误：%v", err)
	}

	var images []image.Image
	for _, file := range files {
		if !file.IsDir() {
			ext := filepath.Ext(file.Name())
			switch ext {
			case ".jpg", ".jpeg", ".png", ".bmp":
				img, err := dc.loadImage(filepath.Join(folderPath, file.Name()))
				if err != nil {
					slog.Warn("因错误跳过图片", "文件", file.Name(), "错误", err)
					continue
				}
				images = append(images, img)
			}
		}
	}

	if len(images) == 0 {
		return nil, fmt.Errorf("文件夹中未找到有效图片")
	}

	return images, nil
}

func (dc *DisplayController) loadImage(path string) (image.Image, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return dc.decodeImage(file)
}

func (dc *DisplayController) decodeImage(file *os.File) (image.Image, error) {
	ext := filepath.Ext(file.Name())
	switch ext {
	case ".bmp":
		return bmp.Decode(file)
	default:
		img, _, err := image.Decode(file)
		return img, err
	}
}

func getRotatedPixel(x, y, width, height int, rotation Rotation) (outX, outY int) {
	switch rotation {
	case ROTATE_90:
		return height - 1 - y, x
	case ROTATE_180:
		return width - 1 - x, height - 1 - y
	case ROTATE_270:
		return y, width - 1 - x
	default:
		return x, y
	}
}
