package audio

import (
	"errors"
	"fmt"
	"github.com/gordonklaus/portaudio"
	"log/slog"
)

// PCMPlayer PortAudio实现的PCM播放器
type PCMPlayer struct {
	sampleRate int
	channels   int
	buffer     chan []int16
	done       chan struct{}
	logger     *slog.Logger
	stream     *portaudio.Stream
}

// NewPCMPlayer 创建新的PortAudio PCM播放器
func NewPCMPlayer(sampleRate, frameDuration, channels int, logger *slog.Logger) (*PCMPlayer, error) {
	// 初始化PortAudio
	if err := portaudio.Initialize(); err != nil {
		return nil, fmt.Errorf("failed to initialize PortAudio: %w", err)
	}

	// 创建播放器实例
	player := &PCMPlayer{
		sampleRate: sampleRate,
		channels:   channels,
		buffer:     make(chan []int16, 100),
		done:       make(chan struct{}),
		logger:     logger,
	}

	frameSize := sampleRate * frameDuration / 1000
	// 打开音频流
	stream, err := portaudio.OpenDefaultStream(
		0,                    // 输入通道数(0表示不录音)
		channels,             // 输出通道数
		float64(sampleRate),  // 采样率
		frameSize*3,          // 让PortAudio选择最佳缓冲区大小
		player.audioCallback, // 回调函数
	)
	if err != nil {
		portaudio.Terminate()
		return nil, fmt.Errorf("failed to open audio stream: %w", err)
	}

	player.stream = stream

	// 启动音频流
	if err := stream.Start(); err != nil {
		stream.Close()
		portaudio.Terminate()
		return nil, fmt.Errorf("failed to start audio stream: %w", err)
	}

	go player.playbackLoop()
	return player, nil
}

func (p *PCMPlayer) audioCallback(out [][]float32) {
	// 计算总共需要处理的样本数（所有通道）
	totalSamples := len(out) * len(out[0])
	processed := 0

	// 处理缓冲区中的数据
	for processed < totalSamples {
		select {
		case chunk := <-p.buffer:
			// 将int16样本转换为float32并填充到输出缓冲区
			for i := 0; i < len(chunk) && processed < totalSamples; i++ {
				channel := processed % len(out)
				sample := processed / len(out)
				out[channel][sample] = float32(chunk[i]) / 32768.0
				processed++
			}
		default:
			// 没有数据可用时填充静音
			for processed < totalSamples {
				channel := processed % len(out)
				sample := processed / len(out)
				out[channel][sample] = 0
				processed++
			}
			return
		}
	}
}

func (p *PCMPlayer) Play(data []int16) error {
	select {
	case p.buffer <- data:
		return nil
	case <-p.done:
		return errors.New("audio player closed")
	}
}

func (p *PCMPlayer) playbackLoop() {
	// 这里可以添加额外的播放控制逻辑
	<-p.done
}

func (p *PCMPlayer) Close() error {
	close(p.done)

	if p.stream != nil {
		// 停止并关闭音频流
		if err := p.stream.Stop(); err != nil {
			p.logger.Error("failed to stop audio stream", "error", err)
		}
		if err := p.stream.Close(); err != nil {
			p.logger.Error("failed to close audio stream", "error", err)
		}
	}

	// 终止PortAudio
	portaudio.Terminate()
	return nil
}
