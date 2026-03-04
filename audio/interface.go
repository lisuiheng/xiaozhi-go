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
