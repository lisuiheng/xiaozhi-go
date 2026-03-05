// audio/interface.go
package audio

import "context"

// Controller 定义音频控制接口
type Controller interface {
	StartSending() bool
	StopSending()
	StartReceiving() bool
	StopReceiving()
	IsSending() bool
	IsReceiving() bool
}

// Recorder 定义音频采集接口
type Recorder interface {
	Record(ctx context.Context, dataChan chan<- []byte) error
}

// AudioPlayer 音频播放器接口
type AudioPlayer interface {
	Play(data []int16) error
	Close() error
}

// Manager 音频管理器接口，统一管理所有音频资源
type Manager interface {
	// 录音控制
	StartRecording(dataChan chan<- []byte) error
	StopRecording()
	IsRecording() bool

	// 播放控制
	Play(data []int16) error
	IsPlaying() bool

	// 编解码
	Decode(opusData []byte) ([]int16, error)
	Encode(pcm []int16) ([]byte, error)

	// 生命周期管理
	Close() error
	Reinitialize() error
}
