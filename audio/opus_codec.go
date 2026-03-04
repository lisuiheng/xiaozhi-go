package audio

import (
	"errors"
	"fmt"
	"github.com/hraban/opus"
	"log/slog"
)

// OpusDecoder OPUS音频解码器
type OpusDecoder struct {
	decoder    *opus.Decoder
	sampleRate int
	channels   int
	logger     *slog.Logger
}

// NewOpusDecoder 创建新的OPUS解码器
func NewOpusDecoder(sampleRate, channels int, logger *slog.Logger) (*OpusDecoder, error) {
	dec, err := opus.NewDecoder(sampleRate, channels)
	if err != nil {
		return nil, fmt.Errorf("failed to create opus decoder: %w", err)
	}

	return &OpusDecoder{
		decoder:    dec,
		sampleRate: sampleRate,
		channels:   channels,
		logger:     logger,
	}, nil
}

// Decode 解码OPUS音频数据
func (d *OpusDecoder) Decode(opusData []byte) ([]int16, error) {
	if d.decoder == nil {
		return nil, errors.New("decoder not initialized")
	}

	// 计算最大可能的PCM输出大小
	maxFrameSize := 5760 * d.channels // OPUS最大帧大小
	pcm := make([]int16, maxFrameSize)

	n, err := d.decoder.Decode(opusData, pcm)
	if err != nil {
		return nil, fmt.Errorf("opus decode failed: %w", err)
	}

	return pcm[:n*d.channels], nil
}

// Close 释放解码器资源
func (d *OpusDecoder) Close() {
	if d.decoder != nil {
		d.decoder = nil
	}
}

// OpusEncoder OPUS音频编码器
type OpusEncoder struct {
	encoder    *opus.Encoder
	sampleRate int
	channels   int
	logger     *slog.Logger
}

// NewOpusEncoder 创建新的OPUS编码器
func NewOpusEncoder(sampleRate, channels, bitrate int, logger *slog.Logger) (*OpusEncoder, error) {
	enc, err := opus.NewEncoder(sampleRate, channels, opus.AppVoIP)
	if err != nil {
		return nil, fmt.Errorf("failed to create opus encoder: %w", err)
	}

	if err := enc.SetBitrate(bitrate); err != nil {
		return nil, fmt.Errorf("failed to set bitrate: %w", err)
	}

	return &OpusEncoder{
		encoder:    enc,
		sampleRate: sampleRate,
		channels:   channels,
		logger:     logger,
	}, nil
}

// Encode 编码PCM音频数据
func (e *OpusEncoder) Encode(pcm []int16) ([]byte, error) {
	if e.encoder == nil {
		return nil, errors.New("encoder not initialized")
	}

	data := make([]byte, 4000) // OPUS最大包大小
	n, err := e.encoder.Encode(pcm, data)
	if err != nil {
		return nil, fmt.Errorf("opus encode failed: %w", err)
	}

	return data[:n], nil
}

// Close 释放编码器资源
func (e *OpusEncoder) Close() {
	if e.encoder != nil {
		e.encoder = nil
	}
}
