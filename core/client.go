package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lisuiheng/xiaozhi-go/audio"
	"github.com/lisuiheng/xiaozhi-go/display"
	"github.com/lisuiheng/xiaozhi-go/pkg/interfaces"
	"github.com/lisuiheng/xiaozhi-go/protocols/websocket"
	"log/slog"
	"sync"
)

type Client struct {
	config        Config
	transport     interfaces.TransportProtocol
	state         DeviceState
	stateMutex    sync.RWMutex
	sessionID     string
	closeChan     chan struct{}
	messageChan   chan []byte
	audioSendChan chan []byte
	wg            sync.WaitGroup
	logger        *slog.Logger
	audioCtrl     audio.Controller
	audioRecorder audio.Recorder
	audioStopChan chan struct{}
	audioDecoder  *audio.OpusDecoder
	audioPlayer   audio.AudioPlayer
	displayCtrl   *display.DisplayController
}

// Config 是客户端配置结构（已调整为匹配YAML文件的结构）
type Config struct {
	System struct {
		ExclusiveMode bool   `mapstructure:"exclusive_mode"`
		BasePath      string `mapstructure:"base_path"`
		DeviceID      string `mapstructure:"device_id"`
		ClientID      string `mapstructure:"client_id"`

		Network struct {
			Transport string           `mapstructure:"transport"`
			Port      int              `mapstructure:"port"`
			Websocket *WebsocketConfig `mapstructure:"websocket"`
			MQTTUDP   *MQTTUDPConfig   `mapstructure:"mqtt_udp"`
		} `mapstructure:"network"`
	} `mapstructure:"system"`

	Audio struct {
		SampleRate     int    `mapstructure:"sample_rate"`
		Channels       int    `mapstructure:"channels"`
		FrameDuration  int    `mapstructure:"frame_duration"`
		SilenceTimeout string `mapstructure:"silence_timeout"`
	} `mapstructure:"audio"`

	Display struct {
		FPS           int               `mapstructure:"fps"`
		SkipExecution bool              `mapstructure:"skip_execution"`
		Brightness    int               `mapstructure:"brightness"`
		Rotation      int               `mapstructure:"rotation"`
		PreloadImages bool              `mapstructure:"preload_images"`
		FontPath      string            `mapstructure:"font_path"`
		FontSize      float64           `mapstructure:"font_size"`
		TextAlign     TextAlignConfig   `mapstructure:"text_align"`
		TimeFormat    string            `mapstructure:"time_format"`
		DateFormat    string            `mapstructure:"date_format"`
		EmotionDirs   map[string]string `mapstructure:"emotion_dirs"`
	} `mapstructure:"display"`

	Logging struct {
		Level   string   `mapstructure:"level"`
		Outputs []string `mapstructure:"outputs"`
	} `mapstructure:"logging"`
}

type WebsocketConfig struct {
	URL         string `mapstructure:"url"`
	AccessToken string `mapstructure:"access_token"`
}

type MQTTUDPConfig struct {
	BrokerAddress string `mapstructure:"broker_address"`
	Topic         string `mapstructure:"topic"`
	QOS           int    `mapstructure:"qos"`
}

// TextAlignConfig 文本对齐配置
type TextAlignConfig struct {
	Horizontal int `mapstructure:"horizontal"`
	Vertical   int `mapstructure:"vertical"`
}

// DeviceState 表示设备状态
type DeviceState string

const (
	DeviceStateUnknown      DeviceState = "unknown"
	DeviceStateConnecting   DeviceState = "connecting"
	DeviceStateIdle         DeviceState = "idle"
	DeviceStateListening    DeviceState = "listening"
	DeviceStateSpeaking     DeviceState = "speaking"
	DeviceStateDisconnected DeviceState = "disconnected"
)

// ListenMode 定义监听模式
type ListenMode string

const (
	ListenModeAuto     ListenMode = "auto"
	ListenModeManual   ListenMode = "manual"
	ListenModeRealtime ListenMode = "realtime"
)

// Status 包含客户端状态信息
type Status struct {
	State            DeviceState
	SessionID        string
	ConnectionStatus string
}

// NewClient 创建一个新的 xiaozhi 客户端
func NewClient(cfg Config, log *slog.Logger) (*Client, error) {
	if log == nil {
		return nil, errors.New("logger cannot be nil")
	}

	recorder, err := audio.NewRecorder(
		audio.Config{
			SampleRate:    cfg.Audio.SampleRate,
			Channels:      cfg.Audio.Channels,
			FrameDuration: cfg.Audio.FrameDuration,
		},
		log,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create audio recorder: %w", err)
	}

	// 初始化OPUS解码器
	decoder, err := audio.NewOpusDecoder(
		cfg.Audio.SampleRate,
		cfg.Audio.Channels,
		log,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create opus decoder: %w", err)
	}

	// 初始化音频播放器
	player, err := audio.NewPCMPlayer(
		cfg.Audio.SampleRate,
		cfg.Audio.FrameDuration,
		cfg.Audio.Channels,
		log,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create audio player: %w", err)
	}

	// 初始化显示控制器
	displayCtrl := display.NewDisplayController()

	return &Client{
		config:        cfg,
		state:         DeviceStateUnknown,
		closeChan:     make(chan struct{}),
		messageChan:   make(chan []byte, 100),
		audioSendChan: make(chan []byte, 100),
		logger:        log,
		audioCtrl:     audio.NewController(),
		audioRecorder: recorder,
		audioStopChan: make(chan struct{}),
		audioDecoder:  decoder,
		audioPlayer:   player,
		displayCtrl:   displayCtrl,
	}, nil
}

func (c *Client) Connect(ctx context.Context) error {
	c.setState(DeviceStateConnecting)
	c.logger.Info("Connecting to server",
		"url", c.config.System.Network.Websocket.URL,
		"transport", c.config.System.Network.Transport)

	transport, err := NewProtocol(c.config)
	if err != nil {
		c.setState(DeviceStateUnknown)
		c.logger.Error("Failed to create transport", "error", err)
		return err
	}

	if err := transport.Connect(ctx); err != nil {
		c.setState(DeviceStateUnknown)
		c.logger.Error("Failed to connect to server", "error", err)
		return fmt.Errorf("failed to connect to server: %v", err)
	}

	c.transport = transport

	helloMsg := map[string]interface{}{
		"type":      "hello",
		"version":   1,
		"transport": c.config.System.Network.Transport,
		"audio_params": map[string]interface{}{
			"format":         "opus",
			"sample_rate":    c.config.Audio.SampleRate,
			"channels":       c.config.Audio.Channels,
			"frame_duration": c.config.Audio.FrameDuration,
		},
	}

	if err := c.sendJSON(helloMsg); err != nil {
		c.transport.Close()
		c.setState(DeviceStateUnknown)
		c.logger.Error("Failed to send hello message", "error", err)
		return fmt.Errorf("failed to send hello message: %v", err)
	}

	c.wg.Add(1)
	go c.messageHandler()

	c.wg.Add(1)
	go c.audioSender()

	c.logger.Info("Connected to server successfully")
	c.setState(DeviceStateIdle)
	return nil
}

// Run 启动客户端主循环
func (c *Client) Run(ctx context.Context) error {
	c.logger.Info("Starting client main loop")
	defer c.logger.Info("Client main loop stopped")

	if err := c.Connect(ctx); err != nil {
		return err
	}

	// 主循环
	for {
		select {
		case <-ctx.Done():
			c.logger.Info("Context cancelled, stopping client")
			return nil
		case <-c.closeChan:
			c.logger.Info("Close signal received, stopping client")
			return nil
		case msg := <-c.messageChan:
			c.logger.Debug("Received message", "message", string(msg))
			if err := c.handleMessage(msg); err != nil {
				c.logger.Error("Failed to handle message", "error", err)
			}
		}
	}
}

// StartListening 开始监听模式
func (c *Client) StartListening(mode ListenMode) error {
	currentState := c.GetState()
	if currentState != DeviceStateIdle {
		c.logger.Warn("Cannot start listening from current state", "currentState", currentState)
		return fmt.Errorf("device is not in idle state (current: %s)", currentState)
	}

	msg := map[string]interface{}{
		"session_id": c.sessionID,
		"type":       "listen",
		"state":      "start",
		"mode":       mode,
	}

	c.logger.Info("Starting listening", "mode", mode)
	if err := c.sendJSON(msg); err != nil {
		c.logger.Error("Failed to send start listening command", "error", err)
		return err
	}

	c.setState(DeviceStateListening)
	return nil
}

// StopListening 停止监听模式
func (c *Client) StopListening() error {
	currentState := c.GetState()
	if currentState != DeviceStateListening {
		c.logger.Warn("Cannot stop listening from current state", "currentState", currentState)
		return fmt.Errorf("device is not in listening state (current: %s)", currentState)
	}

	msg := map[string]interface{}{
		"session_id": c.sessionID,
		"type":       "listen",
		"state":      "stop",
	}

	c.logger.Info("Stopping listening")
	if err := c.sendJSON(msg); err != nil {
		c.logger.Error("Failed to send stop listening command", "error", err)
		return err
	}

	c.setState(DeviceStateIdle)
	return nil
}

// 修改后的 SendAudio（不再管理状态）
func (c *Client) SendAudio(data []byte) error {
	if !c.audioCtrl.IsSending() {
		return errors.New("audio stream not started")
	}

	select {
	case c.audioSendChan <- data:
		c.logger.Debug("Audio data sent", "size", len(data))
		return nil
	default:
		return errors.New("audio send buffer full")
	}
}

// Client 添加流控制方法
func (c *Client) BeginAudioStream() error {
	if !c.audioCtrl.StartSending() {
		return errors.New("cannot begin stream: currently receiving")
	}
	return nil
}

func (c *Client) EndAudioStream() {
	c.audioCtrl.StopSending()
}

// GetStatus 获取当前状态
func (c *Client) GetStatus() Status {
	c.stateMutex.RLock()
	defer c.stateMutex.RUnlock()

	connStatus := "disconnected"
	if c.transport != nil {
		connStatus = "connected"
	}

	return Status{
		State:            c.state,
		SessionID:        c.sessionID,
		ConnectionStatus: connStatus,
	}
}

// Close 关闭客户端连接
func (c *Client) Close() error {
	c.logger.Info("Closing client connection")
	close(c.closeChan)

	// 停止音频采集
	c.StopAudioCapture()

	if c.transport != nil {
		if err := c.transport.Close(); err != nil {
			c.logger.Error("Failed to close WebSocket connection", "error", err)
			return err
		}
	}

	c.wg.Wait()
	c.setState(DeviceStateDisconnected)
	c.logger.Info("Client closed successfully")
	return nil
}

// GetState 获取当前设备状态
func (c *Client) GetState() DeviceState {
	c.stateMutex.RLock()
	defer c.stateMutex.RUnlock()
	return c.state
}

// 设置设备状态
func (c *Client) setState(newState DeviceState) {
	c.stateMutex.Lock()
	defer c.stateMutex.Unlock()

	oldState := c.state
	if oldState != newState {
		// 状态转换时的特殊处理
		switch newState {
		case DeviceStateSpeaking:
			// 进入Speaking状态时停止音频发送
			c.audioCtrl.StopSending()
			c.logger.Debug("Auto-stopped audio sending for speaking state")
		case DeviceStateListening:
			// 进入Listening状态时确保可以发送
			if !c.audioCtrl.StartSending() {
				c.logger.Warn("Failed to start audio sending when entering listening state")
			}
		}

		c.state = newState
		c.logger.Info("State changed",
			"from", oldState,
			"to", newState)
	}
}

// 发送 JSON 消息
func (c *Client) sendJSON(data interface{}) error {
	if c.transport == nil {
		c.logger.Error("Cannot send message, not connected to server")
		return errors.New("not connected to server")
	}

	msg, err := json.Marshal(data)
	if err != nil {
		c.logger.Error("Failed to marshal message", "error", err)
		return fmt.Errorf("failed to marshal message: %v", err)
	}

	// 打印发送的 JSON（格式化输出）
	formattedJSON, _ := json.MarshalIndent(data, "", "  ")
	c.logger.Info("Sending JSON message", "json", string(formattedJSON))

	return c.transport.Send(msg, interfaces.MsgText)
}

// 修改 messageHandler 方法
func (c *Client) messageHandler() {
	defer c.wg.Done()
	for {
		select {
		case <-c.closeChan:
			return
		default:
			msgChan := c.transport.Receive()
			select {
			case msg := <-msgChan:
				switch msg.Type {
				case interfaces.MsgText: // 文本消息（JSON）
					if err := c.handleTextMessage(msg.Payload); err != nil {
						c.logger.Error("Failed to handle text message", "error", err)
					}
				case interfaces.MsgBinary: // 二进制消息
					if err := c.handleBinaryMessage(msg.Payload); err != nil {
						c.logger.Error("Failed to handle binary message", "error", err)
					}
				}
			case <-c.closeChan:
				return
			}
		}
	}
}

// 示例：处理接收到的OPUS音频流
func (c *Client) handleReceivedAudio(data []byte) error {
	if !c.audioCtrl.IsReceiving() {
		return errors.New("not in audio receiving state")
	}

	// 1. 解码音频（假设使用OPUS解码器）
	pcmData, err := c.audioDecoder.Decode(data)
	if err != nil {
		return fmt.Errorf("audio decode failed: %w", err)
	}

	// 2. 播放音频
	if err := c.audioPlayer.Play(pcmData); err != nil {
		return fmt.Errorf("audio play failed: %w", err)
	}

	c.logger.Debug("Played audio frame", "size", len(pcmData))
	return nil
}

// 新增二进制消息处理方法
func (c *Client) handleBinaryMessage(data []byte) error {
	// 根据业务逻辑处理二进制数据（如音频、文件等）
	// 示例：如果是TTS音频数据，传递给播放器
	if c.audioCtrl.IsReceiving() {
		return c.handleReceivedAudio(data)
	}

	c.logger.Debug("Received unexpected binary message", "size", len(data))
	return nil
}

// 实现handleTextMessage
func (c *Client) handleTextMessage(data []byte) error {
	// 1. 空数据检查
	if len(data) == 0 {
		c.logger.Debug("Empty message received")
		return nil
	}
	var msg map[string]interface{}
	if err := json.Unmarshal(data, &msg); err != nil {
		c.logger.Error("JSON unmarshal failed",
			"error", err,
			"raw_data", string(data), // 同时打印原始数据
		)
		return fmt.Errorf("failed to unmarshal message: %w", err)
	}
	return c.handleMessage(data) // 复用现有逻辑
}

// 修改 audioSender 方法
func (c *Client) audioSender() {
	defer c.wg.Done()
	c.logger.Debug("Starting audio sender")
	defer c.logger.Debug("Audio sender stopped")

	for {
		select {
		case <-c.closeChan:
			return
		case data := <-c.audioSendChan:
			if c.audioCtrl.IsSending() {
				if err := c.transport.Send(data, interfaces.MsgBinary); err != nil {
					c.logger.Error("Failed to send audio", "error", err)
					return
				}
			}
		}
	}
}

// 处理接收到的消息
func (c *Client) handleMessage(msg []byte) error {
	var message map[string]interface{}
	if err := json.Unmarshal(msg, &message); err != nil {
		c.logger.Error("Failed to unmarshal message",
			"error", err,
			"raw_message", string(msg), // 同时记录原始消息
		)
		return fmt.Errorf("failed to unmarshal message: %v", err)
	}

	msgType, ok := message["type"].(string)
	if !ok {
		c.logger.Error("Received message without type field")
		return errors.New("message type is missing")
	}

	formattedJSON, _ := json.MarshalIndent(message, "", "  ")
	c.logger.Info("Handling message",
		"json",
		formattedJSON,
	)
	switch msgType {
	case "hello":
		return c.handleHelloResponse(message)
	case "listen":
		return c.handleListenMessage(message)
	case "tts":
		return c.handleTTSMessage(message)
	case "stt": // 新增STT处理
		return c.handleSTTMessage(message)
	case "llm": // 新增LLM消息处理
		return c.handleLLMMessage(message)
	case "abort":
		return c.handleAbortMessage(message)
	case "error":
		return c.handleErrorMessage(message)
	default:
		c.logger.Warn("Unknown message type received", "type", msgType)
		return nil
	}
}

// 处理 hello 响应
func (c *Client) handleHelloResponse(msg map[string]interface{}) error {
	// 将消息转换为JSON字符串并打印
	jsonData, err := json.Marshal(msg)
	if err != nil {
		c.logger.Error("Failed to marshal hello response", "error", err)
		return fmt.Errorf("failed to marshal hello response: %v", err)
	}

	c.logger.Info("Received hello response from server", "response", string(jsonData))
	c.sessionID = msg["session_id"].(string)

	if err := c.SendStartListening(ListenModeAuto); err != nil {
		c.logger.Error("Failed to start auto listening", "error", err)
	}

	// 启动音频流
	if err := c.BeginAudioStream(); err != nil {
		c.logger.Error("Failed to start audio stream", "error", err)
		return err
	}

	// 启动语音采集
	go c.startAudioCapture()

	return nil
}

// 处理 listen 消息
func (c *Client) handleListenMessage(msg map[string]interface{}) error {
	state, ok := msg["state"].(string)
	if !ok {
		c.logger.Error("Listen message missing state field")
		return errors.New("listen state is missing")
	}

	switch state {
	case "detect":
		if text, ok := msg["text"].(string); ok {
			c.logger.Info("Wake word detected", "text", text)
		}
	default:
		c.logger.Debug("Received listen message", "state", state)
	}

	return nil
}

// 处理 TTS 消息
func (c *Client) handleTTSMessage(msg map[string]interface{}) error {
	state, ok := msg["state"].(string)
	if !ok {
		return errors.New("missing state field")
	}

	switch state {
	case "start":
		c.EndAudioStream()
		if c.GetState() == DeviceStateListening {
			c.logger.Debug("Forcing stop listening due to TTS start")
			c.setState(DeviceStateSpeaking)
		}

		if !c.audioCtrl.StartReceiving() {
			return errors.New("cannot receive while sending")
		}
		c.setState(DeviceStateSpeaking)
	case "stop":
		c.audioCtrl.StopReceiving()
		c.logger.Info("Stopped audio receiving")
		c.setState(DeviceStateIdle)
		if err := c.SendStartListening(ListenModeAuto); err != nil {
			c.logger.Error("Failed to start auto listening", "error", err)
		}

		// 启动音频流
		if err := c.BeginAudioStream(); err != nil {
			c.logger.Error("Failed to start audio stream", "error", err)
			return err
		}
	case "sentence_start":
		// 获取并打印句子文本
		if text, ok := msg["text"].(string); ok {
			c.logger.Info("TTS sentence started",
				"text", text,
				"session_id", msg["session_id"])
		} else {
			c.logger.Warn("TTS sentence_start missing text")
		}

	case "sentence_end":
		// 获取并打印句子文本
		if text, ok := msg["text"].(string); ok {
			c.logger.Info("TTS sentence ended",
				"text", text,
				"session_id", msg["session_id"])
		} else {
			c.logger.Warn("TTS sentence_end missing text")
		}
	}

	return nil
}

// handleSTTMessage 处理语音识别结果
func (c *Client) handleSTTMessage(msg map[string]interface{}) error {
	// 基础字段校验
	sessionID, ok := msg["session_id"].(string)
	if !ok {
		return errors.New("STT message missing session_id")
	}

	text, ok := msg["text"].(string)
	if !ok {
		return errors.New("STT message missing text")
	}

	c.logger.Info("STT result received",
		"text", text,
		"session", sessionID)
	return nil
}

// handleLLMMessage 处理来自大语言模型的消息
func (c *Client) handleLLMMessage(msg map[string]interface{}) error {
	// 基础字段校验
	sessionID, ok := msg["session_id"].(string)
	if !ok {
		return errors.New("LLM message missing session_id")
	}

	text, ok := msg["text"].(string)
	if !ok {
		return errors.New("LLM message missing text")
	}

	// 获取表情（可选）
	emotion := "neutral"
	if e, ok := msg["emotion"].(string); ok {
		emotion = e
	}

	c.logger.Info("LLM response received",
		"text", text,
		"emotion", emotion,
		"session", sessionID)

	// 这里可以添加对LLM响应的进一步处理逻辑
	// 例如：
	// - 显示在UI上
	// - 触发特定动作
	// - 转换为语音（TTS）

	// 示例：如果消息包含emoji或特定内容，可以触发特定处理
	if text == "😎" {
		c.logger.Debug("Received cool emoji response")
		// 可以在这里添加特殊处理逻辑
	}

	return nil
}

// 处理中止消息
func (c *Client) handleAbortMessage(msg map[string]interface{}) error {
	reason, _ := msg["reason"].(string)
	c.logger.Info("Session aborted", "reason", reason)
	c.setState(DeviceStateIdle)
	return nil
}

// handleErrorMessage 处理错误类型的消息
func (c *Client) handleErrorMessage(message map[string]interface{}) error {
	errorMsg, ok := message["message"].(string)
	if !ok {
		c.logger.Error("Received error message without 'message' field")
		return errors.New("error message is missing 'message' field")
	}

	sessionID, ok := message["session_id"].(string)
	if !ok {
		c.logger.Error("Received error message without 'session_id' field")
		return errors.New("error message is missing 'session_id' field")
	}

	c.logger.Error("Received error message",
		"session_id", sessionID,
		"error", errorMsg,
	)

	// 可以根据错误类型进行不同的处理，例如重试、通知用户等
	return fmt.Errorf("session %s error: %s", sessionID, errorMsg)
}

// 修改 NewProtocol 函数
// NewProtocol 根据配置创建对应的协议实例
func NewProtocol(config Config) (interfaces.TransportProtocol, error) {
	switch config.System.Network.Transport {
	case "websocket":
		if config.System.Network.Websocket == nil {
			return nil, errors.New("websocket config missing")
		}

		wsConfig := websocket.Config{
			Server: struct {
				URL             string
				ProtocolVersion int
			}{
				URL:             config.System.Network.Websocket.URL,
				ProtocolVersion: 1, // 使用默认值或从配置中获取
			},
			Auth: struct {
				AccessToken string
			}{
				AccessToken: config.System.Network.Websocket.AccessToken,
			},
			Device: struct {
				MAC  string
				UUID string
			}{
				MAC:  config.System.DeviceID,
				UUID: config.System.ClientID,
			},
			Audio: struct {
				Format        string
				SampleRate    int
				Channels      int
				FrameDuration int
			}{
				Format:        "pcm", // 默认值
				SampleRate:    config.Audio.SampleRate,
				Channels:      config.Audio.Channels,
				FrameDuration: config.Audio.FrameDuration,
			},
		}
		return websocket.NewWebSocketProtocol(wsConfig)
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", config.System.Network.Transport)
	}
}

// 添加startAudioCapture方法
func (c *Client) startAudioCapture() {
	c.logger.Info("Starting audio capture")

	// 创建音频数据通道
	audioDataChan := make(chan []byte, 100)
	defer close(audioDataChan)

	// 启动音频采集
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := c.audioRecorder.Record(ctx, audioDataChan); err != nil {
			c.logger.Error("Audio recording failed", "error", err)
		}
	}()

	// 处理采集到的音频数据
	for {
		select {
		case <-c.closeChan:
			c.logger.Info("Stopping audio capture due to client shutdown")
			return
		case <-c.audioStopChan:
			c.logger.Info("Stopping audio capture")
			return
		case data, ok := <-audioDataChan:
			if !ok {
				c.logger.Info("Audio data channel closed")
				return
			}

			// 关键修改：只有在Listening状态且可以发送时才发送音频
			if c.GetState() == DeviceStateListening && c.audioCtrl.IsSending() {
				if err := c.SendAudio(data); err != nil {
					c.logger.Warn("Failed to send audio data",
						"error", err,
						"state", c.GetState())
				}
			} else {
				c.logger.Debug("Skipping audio send",
					"reason", "wrong state or not sending",
					"state", c.GetState(),
					"isSending", c.audioCtrl.IsSending())
			}
		}
	}
}

// 添加StopAudioCapture方法
func (c *Client) StopAudioCapture() {
	select {
	case c.audioStopChan <- struct{}{}:
		c.logger.Info("Sent stop signal to audio capture")
	default:
		c.logger.Warn("Audio stop channel is full")
	}
}

// SendStartListening 发送开始监听指令
func (c *Client) SendStartListening(mode ListenMode) error {
	// 检查当前状态
	if c.GetState() != DeviceStateIdle && c.GetState() != DeviceStateConnecting {
		return fmt.Errorf("cannot start listening from state: %s", c.GetState())
	}

	// 构造监听消息
	msg := map[string]interface{}{
		"session_id": c.sessionID,
		"type":       "listen",
		"state":      "start",
		"mode":       mode,
	}

	// 发送 JSON 消息
	if err := c.sendJSON(msg); err != nil {
		return fmt.Errorf("failed to send listen command: %w", err)
	}

	// 更新设备状态
	c.setState(DeviceStateListening)
	c.logger.Info("Listening started", "mode", mode)
	return nil
}

// Display 相关方法

// ShowEmotion 显示表情动画
func (c *Client) ShowEmotion(emotionName string) error {
	if c.config.Display.SkipExecution {
		return nil
	}

	// 从配置中获取表情目录路径
	emotionPath, exists := c.config.Display.EmotionDirs[emotionName]
	if !exists {
		return fmt.Errorf("emotion not found: %s", emotionName)
	}

	rotation := display.Rotation(c.config.Display.Rotation)
	return c.displayCtrl.StartAnimation(emotionPath, rotation, c.config.Display.FPS, c.config.Display.PreloadImages)
}

// ShowImage 显示单张图片
func (c *Client) ShowImage(imagePath string) error {
	if c.config.Display.SkipExecution {
		return nil
	}

	rotation := display.Rotation(c.config.Display.Rotation)
	return c.displayCtrl.ShowImage(imagePath, rotation)
}

// ShowText 显示文本
func (c *Client) ShowText(text string, fontSize float64, hAlign, vAlign int) error {
	if c.config.Display.SkipExecution {
		return nil
	}

	color := ColorRGB(255, 255, 255) // 默认白色
	return c.displayCtrl.ShowText(text, fontSize, color, hAlign, vAlign)
}

// ShowDateTime 显示日期时间
func (c *Client) ShowDateTime() error {
	if c.config.Display.SkipExecution {
		return nil
	}

	color := ColorRGB(255, 255, 255)
	return c.displayCtrl.ShowDateTime(
		c.config.Display.FontSize,
		color,
		c.config.Display.TextAlign.Horizontal,
		c.config.Display.TextAlign.Vertical,
		c.config.Display.TimeFormat,
		c.config.Display.DateFormat,
	)
}

// StopDisplay 停止显示
func (c *Client) StopDisplay() {
	c.displayCtrl.Stop()
}

// ColorRGB 创建 RGB 颜色
func ColorRGB(r, g, b uint8) interface{} {
	return struct {
		R uint8
		G uint8
		B uint8
	}{R: r, G: g, B: b}
}
