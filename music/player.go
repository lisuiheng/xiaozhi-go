package music

import (
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Player 音乐播放器
type Player struct {
	musicPath        string
	supportedFormats []string
	songs            []SongInfo
	currentIndex     int
	playing          bool
	paused           bool
	stopChan         chan struct{}
	cmd              *exec.Cmd
	mu               sync.Mutex
	logger           *slog.Logger

	// 音频可视化
	visualizeChan chan float64 // 音量级别通道 (0.0-1.0)
	visualizeStop chan struct{}
	audioData     []int16 // 当前音频数据
	audioDataMu   sync.Mutex
}

// SongInfo 歌曲信息
type SongInfo struct {
	Path string
	Name string
}

// NewPlayer 创建新的音乐播放器
func NewPlayer(musicPath string, supportedFormats []string, logger *slog.Logger) *Player {
	return &Player{
		musicPath:        musicPath,
		supportedFormats: supportedFormats,
		currentIndex:     -1,
		stopChan:         make(chan struct{}),
		visualizeChan:    make(chan float64, 10),
		visualizeStop:    make(chan struct{}),
		logger:           logger,
	}
}

// LoadSongs 加载音乐列表
func (p *Player) LoadSongs() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	files, err := os.ReadDir(p.musicPath)
	if err != nil {
		return fmt.Errorf("failed to read music directory: %w", err)
	}

	p.songs = []SongInfo{}
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		ext := strings.ToLower(filepath.Ext(file.Name()))
		for _, format := range p.supportedFormats {
			if ext == format {
				p.songs = append(p.songs, SongInfo{
					Path: filepath.Join(p.musicPath, file.Name()),
					Name: strings.TrimSuffix(file.Name(), ext),
				})
				break
			}
		}
	}

	p.logger.Info("Music loaded", "count", len(p.songs), "path", p.musicPath)
	return nil
}

// GetSongs 获取歌曲列表
func (p *Player) GetSongs() []SongInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.songs
}

// GetCurrentSong 获取当前播放歌曲
func (p *Player) GetCurrentSong() *SongInfo {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.currentIndex < 0 || p.currentIndex >= len(p.songs) {
		return nil
	}
	return &p.songs[p.currentIndex]
}

// IsPlaying 是否正在播放
func (p *Player) IsPlaying() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.playing
}

// IsPaused 是否暂停
func (p *Player) IsPaused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

// GetVisualizeChannel 获取可视化数据通道
func (p *Player) GetVisualizeChannel() <-chan float64 {
	return p.visualizeChan
}

// Play 播放
func (p *Player) Play() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.songs) == 0 {
		return fmt.Errorf("no songs available")
	}

	if p.playing && !p.paused {
		return nil
	}

	if p.paused {
		p.paused = false
		return nil
	}

	if p.currentIndex < 0 {
		p.currentIndex = 0
	}

	p.playing = true
	p.stopChan = make(chan struct{})

	go p.playLoop()
	p.logger.Info("Music started", "song", p.songs[p.currentIndex].Name)

	return nil
}

// PlaySong 播放指定歌曲
func (p *Player) PlaySong(index int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if index < 0 || index >= len(p.songs) {
		return fmt.Errorf("invalid song index: %d", index)
	}

	if p.playing {
		close(p.stopChan)
		if p.cmd != nil {
			p.cmd.Process.Kill()
		}
	}

	p.currentIndex = index
	p.playing = true
	p.paused = false
	p.stopChan = make(chan struct{})

	go p.playLoop()
	p.logger.Info("Playing song", "song", p.songs[p.currentIndex].Name)

	return nil
}

// Pause 暂停
func (p *Player) Pause() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.playing && !p.paused {
		p.paused = true
		p.logger.Info("Music paused")
	}
}

// Resume 恢复
func (p *Player) Resume() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.playing && p.paused {
		p.paused = false
		p.logger.Info("Music resumed")
	}
}

// Stop 停止
func (p *Player) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.playing {
		close(p.stopChan)
		if p.cmd != nil {
			p.cmd.Process.Kill()
			p.cmd = nil
		}
		p.playing = false
		p.paused = false
		p.logger.Info("Music stopped")
	}
}

// Next 下一首
func (p *Player) Next() error {
	p.mu.Lock()
	songsLen := len(p.songs)
	currentIdx := p.currentIndex
	p.mu.Unlock()

	if songsLen == 0 {
		return fmt.Errorf("no songs available")
	}

	nextIdx := (currentIdx + 1) % songsLen
	return p.PlaySong(nextIdx)
}

// Previous 上一首
func (p *Player) Previous() error {
	p.mu.Lock()
	songsLen := len(p.songs)
	currentIdx := p.currentIndex
	p.mu.Unlock()

	if songsLen == 0 {
		return fmt.Errorf("no songs available")
	}

	prevIdx := currentIdx - 1
	if prevIdx < 0 {
		prevIdx = songsLen - 1
	}
	return p.PlaySong(prevIdx)
}

// playLoop 播放循环
func (p *Player) playLoop() {
	for {
		select {
		case <-p.stopChan:
			return
		default:
			p.mu.Lock()
			if !p.playing || p.paused {
				p.mu.Unlock()
				return
			}

			if p.currentIndex < 0 || p.currentIndex >= len(p.songs) {
				p.playing = false
				p.mu.Unlock()
				return
			}

			song := p.songs[p.currentIndex]
			p.mu.Unlock()

			if err := p.playFileWithVisualize(song.Path); err != nil {
				p.logger.Warn("Failed to play song, stopping playback", "song", song.Name, "error", err)
				// 播放失败时停止，不继续重试
				p.mu.Lock()
				p.playing = false
				p.mu.Unlock()
				return
			}

			p.mu.Lock()
			if !p.playing {
				p.mu.Unlock()
				return
			}
			p.currentIndex = (p.currentIndex + 1) % len(p.songs)
			p.mu.Unlock()
		}
	}
}

// playFileWithVisualize 播放文件并发送可视化数据
func (p *Player) playFileWithVisualize(filePath string) error {
	ext := strings.ToLower(filepath.Ext(filePath))

	// WAV 文件使用内部播放器以获取音频数据
	if ext == ".wav" {
		return p.playWavWithVisualize(filePath)
	}

	// 其他格式使用外部播放器，发送模拟可视化数据
	go p.sendSimulatedVisualize()
	defer func() {
		select {
		case <-p.visualizeStop:
		default:
			close(p.visualizeStop)
			p.visualizeStop = make(chan struct{})
		}
	}()

	return p.playWithExternalPlayer(filePath)
}

// playWavWithVisualize 播放 WAV 文件并发送实时可视化数据
func (p *Player) playWavWithVisualize(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	// 跳过 WAV 头
	if _, err = file.Seek(44, io.SeekStart); err != nil {
		return err
	}

	// 打开音频设备
	audioDev, err := os.OpenFile("/dev/snd/pcmC0D0p", os.O_WRONLY, 0)
	if err != nil {
		// 如果无法打开音频设备，回退到 aplay
		return p.playWav(filePath)
	}
	defer audioDev.Close()

	buf := make([]byte, 4096)
	ticker := time.NewTicker(50 * time.Millisecond) // 20 FPS 可视化更新
	defer ticker.Stop()

	for {
		select {
		case <-p.stopChan:
			return nil
		case <-ticker.C:
			// 计算音量级别并发送
			p.audioDataMu.Lock()
			level := p.calculateVolumeLevel(p.audioData)
			p.audioDataMu.Unlock()

			select {
			case p.visualizeChan <- level:
			default:
			}
		default:
			n, err := file.Read(buf)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}

			// 保存音频数据用于分析
			sampleCount := n / 2
			samples := make([]int16, sampleCount)
			for i := 0; i < sampleCount; i++ {
				samples[i] = int16(binary.LittleEndian.Uint16(buf[i*2:]))
			}

			p.audioDataMu.Lock()
			p.audioData = samples
			p.audioDataMu.Unlock()

			if _, err := audioDev.Write(buf[:n]); err != nil {
				return err
			}
		}
	}
}

// calculateVolumeLevel 计算音量级别
func (p *Player) calculateVolumeLevel(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}

	// 计算 RMS (均方根) 音量
	var sum float64
	for _, s := range samples {
		val := float64(s) / 32768.0
		sum += val * val
	}
	rms := math.Sqrt(sum / float64(len(samples)))

	// 归一化到 0.0-1.0 范围
	level := rms * 3.0 // 放大系数
	if level > 1.0 {
		level = 1.0
	}

	return level
}

// sendSimulatedVisualize 发送模拟的可视化数据
func (p *Player) sendSimulatedVisualize() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	// 简单的模拟波形
	phase := 0.0
	for {
		select {
		case <-p.visualizeStop:
			return
		case <-ticker.C:
			// 生成模拟音量级别
			phase += 0.3
			level := 0.3 + 0.4*math.Abs(math.Sin(phase)) + 0.3*math.Abs(math.Sin(phase*2.5))

			p.mu.Lock()
			paused := p.paused
			p.mu.Unlock()

			if paused {
				level = 0
			}

			select {
			case p.visualizeChan <- level:
			default:
			}
		}
	}
}

// playWithExternalPlayer 使用外部播放器
func (p *Player) playWithExternalPlayer(filePath string) error {
	ext := strings.ToLower(filepath.Ext(filePath))

	p.mu.Lock()
	switch ext {
	case ".mp3":
		// 优先使用 ffplay（ffmpeg 的一部分），更稳定
		if _, err := exec.LookPath("ffplay"); err == nil {
			p.cmd = exec.Command("ffplay", "-nodisp", "-autoexit", "-loglevel", "quiet", filePath)
		} else if _, err := exec.LookPath("mpg123"); err == nil {
			p.cmd = exec.Command("mpg123", "-q", filePath)
		} else if _, err := exec.LookPath("madplay"); err == nil {
			p.cmd = exec.Command("madplay", "-q", filePath)
		} else {
			p.mu.Unlock()
			return fmt.Errorf("no MP3 player found (need ffplay, mpg123, or madplay)")
		}
	default:
		p.cmd = exec.Command("aplay", filePath)
	}
	p.mu.Unlock()

	output, err := p.cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("player failed: %w, output: %s", err, string(output))
	}
	return nil
}

// playWav 使用 aplay 播放 WAV 文件
func (p *Player) playWav(filePath string) error {
	p.mu.Lock()
	p.cmd = exec.Command("aplay", filePath)
	p.mu.Unlock()

	output, err := p.cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("aplay failed: %w, output: %s", err, string(output))
	}
	return nil
}
