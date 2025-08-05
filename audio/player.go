package audio

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gordonklaus/portaudio"
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
	// 使用局部变量保存剩余数据，避免并发问题
	var (
		remaining []int16
		pos       int
	)

	// 先处理之前剩余的未播放数据
	filled := 0
	for filled < len(out[0]) && pos < len(remaining) {
		channel := pos % len(out)
		sample := pos / len(out)

		if sample < len(out[0]) {
			out[channel][sample] = float32(remaining[pos]) / 32768.0
			filled++
			pos++
		}
	}

	// 如果还有空间，从channel获取新数据
	for filled < len(out[0]) {
		select {
		case data := <-p.buffer:
			// 重置剩余数据处理位置
			remaining = data
			pos = 0

			// 填充新数据
			for filled < len(out[0]) && pos < len(remaining) {
				channel := pos % len(out)
				sample := pos / len(out)

				if sample < len(out[0]) {
					out[channel][sample] = float32(remaining[pos]) / 32768.0
					filled++
					pos++
				}
			}

		case <-p.done:
			// 收到停止信号，填充静音
			for i := range out {
				for j := filled; j < len(out[i]); j++ {
					out[i][j] = 0
				}
			}
			return

		default:
			// 没有新数据时跳出循环
			break
		}
	}

	// 保存未处理完的数据
	if pos < len(remaining) {
		remaining = remaining[pos:]
	} else {
		remaining = nil
	}

	// 填充剩余空间为静音
	for i := range out {
		for j := filled; j < len(out[i]); j++ {
			out[i][j] = 0
		}
	}
}

func (p *PCMPlayer) Play(data []int16) error {
	select {
	case p.buffer <- data:
		return nil
	case <-time.After(100 * time.Millisecond):
		return errors.New("audio buffer full")
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
