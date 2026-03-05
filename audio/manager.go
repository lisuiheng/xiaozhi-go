// audio/manager.go
package audio

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// audioResourceManager 统一管理音频输入输出设备及编解码资源
type audioResourceManager struct {
	mu          sync.RWMutex
	config      Config
	logger      *slog.Logger
	recorder    Recorder
	player      AudioPlayer
	decoder     *OpusDecoder
	encoder     *OpusEncoder
	isRecording bool
	isPlaying   bool
	closeChan   chan struct{}
	closed      bool
}

// NewManager 创建新的音频管理器
func NewManager(cfg Config, logger *slog.Logger) (Manager, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}

	manager := &audioResourceManager{
		config:    cfg,
		logger:    logger,
		closeChan: make(chan struct{}),
	}

	// 初始化 OPUS 编码器
	encoder, err := NewOpusEncoder(
		cfg.SampleRate,
		cfg.Channels,
		32000, // 32kbps 比特率
		logger,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create opus encoder: %w", err)
	}
	manager.encoder = encoder

	// 初始化 OPUS 解码器
	decoder, err := NewOpusDecoder(
		cfg.SampleRate,
		cfg.Channels,
		logger,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create opus decoder: %w", err)
	}
	manager.decoder = decoder

	// 初始化音频播放器
	player, err := NewPCMPlayer(
		cfg.SampleRate,
		cfg.FrameDuration,
		cfg.Channels,
		logger,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create audio player: %w", err)
	}
	manager.player = player

	// 初始化录音机（延迟初始化，需要时再创建）
	// recorder 将在 StartRecording 时创建

	return manager, nil
}

// StartRecording 开始录音
func (m *audioResourceManager) StartRecording(dataChan chan<- []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.isRecording {
		return fmt.Errorf("already recording")
	}

	if m.closed {
		return fmt.Errorf("manager is closed")
	}

	// 创建录音机
	recorder, err := NewRecorder(m.config, m.logger)
	if err != nil {
		return fmt.Errorf("failed to create recorder: %w", err)
	}
	m.recorder = recorder

	m.isRecording = true

	// 在后台启动录音
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()
		if err := m.recorder.Record(ctx, dataChan); err != nil {
			m.logger.Error("Recording failed", "error", err)
		}
	}()

	m.logger.Info("Recording started")
	return nil
}

// StopRecording 停止录音
func (m *audioResourceManager) StopRecording() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.isRecording {
		return
	}

	// recorder 会在 Record 方法结束时自动释放资源
	m.recorder = nil
	m.isRecording = false

	m.logger.Info("Recording stopped")
}

// Play 播放音频数据
func (m *audioResourceManager) Play(data []int16) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return fmt.Errorf("manager is closed")
	}

	if m.player == nil {
		return fmt.Errorf("player not initialized")
	}

	return m.player.Play(data)
}

// Decode 解码 OPUS音频数据
func (m *audioResourceManager) Decode(opusData []byte) ([]int16, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, fmt.Errorf("manager is closed")
	}

	if m.decoder == nil {
		return nil, fmt.Errorf("decoder not initialized")
	}

	return m.decoder.Decode(opusData)
}

// Encode 编码 PCM 音频数据
func (m *audioResourceManager) Encode(pcm []int16) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, fmt.Errorf("manager is closed")
	}

	if m.encoder == nil {
		return nil, fmt.Errorf("encoder not initialized")
	}

	return m.encoder.Encode(pcm)
}

// IsRecording 是否正在录音
func (m *audioResourceManager) IsRecording() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isRecording
}

// IsPlaying 是否正在播放
func (m *audioResourceManager) IsPlaying() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isPlaying
}

// Close 关闭音频管理器，释放所有资源
func (m *audioResourceManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}

	m.closed = true
	close(m.closeChan)

	var errs []error

	// 停止录音
	if m.isRecording {
		m.StopRecording()
	}

	// 关闭播放器
	if m.player != nil {
		if err := m.player.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close player: %w", err))
		}
		m.player = nil
	}

	// 关闭解码器
	if m.decoder != nil {
		m.decoder.Close()
		m.decoder = nil
	}

	// 关闭编码器
	if m.encoder != nil {
		m.encoder.Close()
		m.encoder = nil
	}

	m.logger.Info("Audio manager closed")

	if len(errs) > 0 {
		return fmt.Errorf("errors while closing: %v", errs)
	}
	return nil
}

// Reinitialize 重新初始化音频设备（用于音乐播放后恢复）
func (m *audioResourceManager) Reinitialize() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return fmt.Errorf("manager is closed")
	}

	var errs []error

	// 重新创建播放器
	if m.player != nil {
		if err := m.player.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close old player: %w", err))
		}
	}

	player, err := NewPCMPlayer(
		m.config.SampleRate,
		m.config.FrameDuration,
		m.config.Channels,
		m.logger,
	)
	if err != nil {
		return fmt.Errorf("failed to recreate player: %w", err)
	}
	m.player = player

	// 重新创建解码器
	if m.decoder != nil {
		m.decoder.Close()
	}

	decoder, err := NewOpusDecoder(
		m.config.SampleRate,
		m.config.Channels,
		m.logger,
	)
	if err != nil {
		return fmt.Errorf("failed to recreate decoder: %w", err)
	}
	m.decoder = decoder

	m.logger.Info("Audio manager reinitialized")

	if len(errs) > 0 {
		return fmt.Errorf("errors while reinitializing: %v", errs)
	}
	return nil
}
