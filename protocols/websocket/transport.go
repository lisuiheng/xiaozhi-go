// protocols/websocket/transport.go
package websocket

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/lisuiheng/xiaozhi-go/pkg/interfaces"
)

var _ interfaces.TransportProtocol = (*WSProtocol)(nil)

type WSProtocol struct {
	conn      *websocket.Conn
	config    Config
	msgChan   chan interfaces.Message
	closeChan chan struct{}
	mu        sync.Mutex
}

// Config 定义websocket特有的配置
type Config struct {
	Server struct {
		URL             string
		ProtocolVersion int
	}
	Auth struct {
		AccessToken string
	}
	Device struct {
		MAC  string
		UUID string
	}
	Audio struct {
		Format        string
		SampleRate    int
		Channels      int
		FrameDuration int
	}
}

func NewWebSocketProtocol(config Config) (*WSProtocol, error) {
	return &WSProtocol{
		config:    config,
		msgChan:   make(chan interfaces.Message, 100),
		closeChan: make(chan struct{}),
	}, nil
}

func (p *WSProtocol) Connect(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	headers := http.Header{}
	headers.Set("Authorization", fmt.Sprintf("Bearer %s", p.config.Auth.AccessToken))
	headers.Set("Protocol-Version", fmt.Sprintf("%d", p.config.Server.ProtocolVersion))
	headers.Set("Device-Id", p.config.Device.MAC)
	headers.Set("Client-Id", p.config.Device.UUID)

	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, p.config.Server.URL, headers)
	if err != nil {
		return fmt.Errorf("%w: %v", interfaces.ErrConnectionFailed, err)
	}
	p.conn = conn

	go p.readPump()
	return nil
}

func (p *WSProtocol) readPump() {
	defer close(p.msgChan)
	for {
		select {
		case <-p.closeChan:
			return
		default:
			msgType, data, err := p.conn.ReadMessage()
			if err != nil {
				return
			}
			p.msgChan <- interfaces.Message{
				Payload: data,
				Type:    convertMsgType(msgType),
			}
		}
	}
}

func convertMsgType(wsType int) interfaces.MessageType {
	switch wsType {
	case websocket.TextMessage:
		return interfaces.MsgText
	case websocket.BinaryMessage:
		return interfaces.MsgBinary
	default:
		return interfaces.MsgControl
	}
}

func (p *WSProtocol) Send(data []byte, msgType interfaces.MessageType) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn == nil {
		return interfaces.ErrConnectionFailed
	}

	wsType := websocket.TextMessage
	if msgType == interfaces.MsgBinary {
		wsType = websocket.BinaryMessage
	}
	return p.conn.WriteMessage(wsType, data)
}

func (p *WSProtocol) Receive() <-chan interfaces.Message {
	return p.msgChan
}

func (p *WSProtocol) ProtocolType() string { return "websocket" }

func (p *WSProtocol) Close() error {
	close(p.closeChan)
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}
