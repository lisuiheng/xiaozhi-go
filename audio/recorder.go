package audio

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/gen2brain/malgo"
)

type recorder struct {
	config      Config
	logger      *slog.Logger
	opusEncoder *OpusEncoder // 使用opus_codec.go中的编码器
}

type Config struct {
	SampleRate    int
	Channels      int
	FrameDuration int // 毫秒
}

func NewRecorder(cfg Config, logger *slog.Logger) (Recorder, error) {
	// 使用现有OpusEncoder实现
	encoder, err := NewOpusEncoder(
		cfg.SampleRate,
		cfg.Channels,
		32000, // 32kbps比特率
		logger,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create opus encoder: %w", err)
	}

	return &recorder{
		config:      cfg,
		logger:      logger,
		opusEncoder: encoder,
	}, nil
}

func (r *recorder) Record(ctx context.Context, dataChan chan<- []byte) error {
	//// 创建或打开PCM文件
	//pcmFile, err := os.Create("/home/lee/Downloads/test.pcm")
	//if err != nil {
	//	return fmt.Errorf("failed to create PCM file: %w", err)
	//}
	//defer pcmFile.Close()
	//
	//// 创建WAV文件头并写入
	//if err := writeWavHeader(pcmFile, r.config.SampleRate, r.config.Channels); err != nil {
	//	return fmt.Errorf("failed to write WAV header: %w", err)
	//}

	defer func() {
		if r.opusEncoder != nil {
			r.opusEncoder.Close() // 使用opus_codec.go中的Close方法
		}
	}()

	// 计算帧大小 (样本数)
	frameSize := (r.config.SampleRate * r.config.Channels * r.config.FrameDuration) / 1000
	if frameSize <= 0 {
		return fmt.Errorf("invalid frame size: %d", frameSize)
	}

	// 初始化malgo上下文
	ctxMalgo, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		r.logger.Debug("malgo", "message", message)
	})
	if err != nil {
		return fmt.Errorf("failed to initialize audio context: %w", err)
	}
	defer func() {
		_ = ctxMalgo.Uninit()
		ctxMalgo.Free()
	}()

	// 创建设备配置
	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = uint32(r.config.Channels)
	deviceConfig.SampleRate = uint32(r.config.SampleRate)
	deviceConfig.PeriodSizeInFrames = uint32(frameSize)

	// 创建捕获回调
	captureCallback := func(_, pcmData []byte, _ uint32) {
		select {
		case <-ctx.Done():
			return
		default:
			//// 1. 先将原始PCM数据写入文件
			//if _, err := pcmFile.Write(pcmData); err != nil {
			//	r.logger.Error("Failed to write PCM data", "error", err)
			//}

			// PCM数据转换
			pcm := bytesToInt16(pcmData) // 需要实现这个辅助函数

			// 使用opus_codec.go的Encode方法
			opusData, err := r.opusEncoder.Encode(pcm)
			if err != nil {
				r.logger.Error("OPUS encode failed", "error", err)
				return
			}

			select {
			case dataChan <- opusData:
			case <-time.After(100 * time.Millisecond):
				r.logger.Warn("Audio channel blocked, dropping frame")
			case <-ctx.Done():
			}
		}
	}

	// 创建设备
	device, err := malgo.InitDevice(ctxMalgo.Context, deviceConfig, malgo.DeviceCallbacks{
		Data: captureCallback,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize audio device: %w", err)
	}
	defer device.Uninit()

	// 启动设备
	if err := device.Start(); err != nil {
		return fmt.Errorf("failed to start audio device: %w", err)
	}
	defer device.Stop()

	r.logger.Info("Audio recording started",
		"sample_rate", r.config.SampleRate,
		"channels", r.config.Channels,
		"frame_size", frameSize)

	// 等待上下文取消
	<-ctx.Done()
	r.logger.Info("Audio recording stopped")
	return nil
}

// bytesToInt16 将byte切片转换为int16切片
func bytesToInt16(b []byte) []int16 {
	if len(b)%2 != 0 {
		b = b[:len(b)-1] // 确保长度是偶数
	}

	pcm := make([]int16, len(b)/2)
	for i := 0; i < len(pcm); i++ {
		pcm[i] = int16(b[i*2]) | int16(b[i*2+1])<<8
	}
	return pcm
}

func writeWavHeader(file *os.File, sampleRate, channels int) error {
	// WAV文件头结构
	type WavHeader struct {
		RiffMark      [4]byte // "RIFF"
		FileSize      uint32  // 文件总大小-8
		WaveMark      [4]byte // "WAVE"
		FmtMark       [4]byte // "fmt "
		FmtSize       uint32  // fmt chunk大小(16)
		AudioFormat   uint16  // 1=PCM
		NumChannels   uint16
		SampleRate    uint32
		ByteRate      uint32  // SampleRate * NumChannels * BitsPerSample/8
		BlockAlign    uint16  // NumChannels * BitsPerSample/8
		BitsPerSample uint16  // 16
		DataMark      [4]byte // "data"
		DataSize      uint32  // 原始数据大小
	}

	header := WavHeader{
		RiffMark:      [4]byte{'R', 'I', 'F', 'F'},
		FileSize:      0, // 稍后填充
		WaveMark:      [4]byte{'W', 'A', 'V', 'E'},
		FmtMark:       [4]byte{'f', 'm', 't', ' '},
		FmtSize:       16,
		AudioFormat:   1, // PCM
		NumChannels:   uint16(channels),
		SampleRate:    uint32(sampleRate),
		BitsPerSample: 16,
	}
	header.ByteRate = header.SampleRate * uint32(header.NumChannels) * uint32(header.BitsPerSample) / 8
	header.BlockAlign = header.NumChannels * header.BitsPerSample / 8
	header.DataMark = [4]byte{'d', 'a', 't', 'a'}
	header.DataSize = 0 // 稍后填充

	// 写入初始头文件
	return binary.Write(file, binary.LittleEndian, &header)
}
