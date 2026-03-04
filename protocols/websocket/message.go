package websocket

//func convertMsgType(wsType int) core.MessageType {
//	switch wsType {
//	case websocket.TextMessage:
//		return core.MsgText
//	case websocket.BinaryMessage:
//		return core.MsgBinary
//	default:
//		return core.MsgControl
//	}
//}
//
//func (p *WSProtocol) Send(data []byte, msgType core.MessageType) error {
//	wsType := websocket.TextMessage
//	if msgType == core.MsgBinary {
//		wsType = websocket.BinaryMessage
//	}
//	return p.conn.WriteMessage(wsType, data)
//}
