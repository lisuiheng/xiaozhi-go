package websocket

//import (
//	"context"
//	"github.com/gorilla/websocket"
//	"time"
//)
//
//type WSProtocol struct {
//	conn      *websocket.Conn
//	endpoint  string
//	clientID  string
//	msgChan   chan core.Message
//	closeChan chan struct{}
//}
//
//func NewWSProtocol(endpoint, clientID string) (*WSProtocol, error) {
//	return &WSProtocol{
//		endpoint:  endpoint,
//		clientID:  clientID,
//		msgChan:   make(chan core.Message, 100),
//		closeChan: make(chan struct{}),
//	}, nil
//}
//
//func (p *WSProtocol) Connect() error {
//	headers := map[string]string{
//		"Authorization":    "Bearer " + p.clientID, // 简化示例
//		"Protocol-Version": "1",
//		"Client-Id":        p.clientID,
//	}
//
//	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
//	defer cancel()
//
//	conn, _, err := websocket.DefaultDialer.DialContext(ctx, p.endpoint, headers)
//	if err != nil {
//		return err
//	}
//	p.conn = conn
//
//	go p.readPump()
//	return nil
//}
//
//func (p *WSProtocol) readPump() {
//	for {
//		select {
//		case <-p.closeChan:
//			return
//		default:
//			msgType, data, err := p.conn.ReadMessage()
//			if err != nil {
//				close(p.msgChan)
//				return
//			}
//
//			p.msgChan <- core.Message{
//				Payload: data,
//				Type:    convertMsgType(msgType),
//			}
//		}
//	}
//}
