// pkg/interfaces/transport.go
package interfaces

import (
	"context"
	"errors"
)

var (
	ErrConnectionFailed    = errors.New("connection failed")
	ErrUnsupportedProtocol = errors.New("unsupported protocol")
)

type TransportProtocol interface {
	Connect(ctx context.Context) error
	Send(data []byte, msgType MessageType) error
	Receive() <-chan Message
	Close() error
	ProtocolType() string
}

type Message struct {
	Payload []byte
	Type    MessageType
}

type MessageType int

const (
	MsgText    MessageType = iota // JSON文本
	MsgBinary                     // 二进制数据（如音频）
	MsgControl                    // 控制指令
)
