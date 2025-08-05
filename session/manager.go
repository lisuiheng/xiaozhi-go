package session

//import "encoding/json"
//
//type Manager struct {
//	protocol    core.TransportProtocol
//	currentMode ListenMode
//	active      bool
//}
//
//type ListenMode string
//
//const (
//	ModeAuto     ListenMode = "auto"
//	ModeManual   ListenMode = "manual"
//	ModeRealtime ListenMode = "realtime"
//)
//
//func NewManager(proto core.TransportProtocol) *Manager {
//	return &Manager{
//		protocol: proto,
//	}
//}
//
//func (m *Manager) StartListening(mode ListenMode) error {
//	msg := map[string]interface{}{
//		"type":  "listen",
//		"state": "start",
//		"mode":  string(mode),
//	}
//
//	data, _ := json.Marshal(msg)
//	return m.protocol.Send(data, core.MsgControl)
//}
