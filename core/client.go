package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/lisuiheng/xiaozhi-go/audio"
	"github.com/lisuiheng/xiaozhi-go/display"
	"github.com/lisuiheng/xiaozhi-go/music"
	"github.com/lisuiheng/xiaozhi-go/pkg/interfaces"
	"github.com/lisuiheng/xiaozhi-go/protocols/websocket"
	"log/slog"
	"sync"
)

// DisplayMode 显示模式
type DisplayMode string

const (
	DisplayModeEmotion DisplayMode = "emotion" // 默认模式，显示表情
	DisplayModeClock   DisplayMode = "clock"   // 时钟模式，显示时间
	DisplayModeDialog  DisplayMode = "dialog"  // 对话模式，显示对话
	DisplayModeMusic   DisplayMode = "music"   // 音乐模式，显示音乐信息
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

	// 显示模式管理
	displayMode   DisplayMode
	displayModeMu sync.RWMutex

	// 对话模式缓冲区
	dialogBuffer   []string
	dialogBufferMu sync.Mutex

	// 音乐播放器
	musicPlayer *music.Player
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

	Music struct {
		Enabled          bool     `mapstructure:"enabled"`
		MusicPath        string   `mapstructure:"music_path"`
		SupportedFormats []string `mapstructure:"supported_formats"`
		AnimationPath    string   `mapstructure:"animation_path"`
		ShowSongName     bool     `mapstructure:"show_song_name"`
	} `mapstructure:"music"`

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

	// 初始化音乐播放器
	var musicPlayer *music.Player
	if cfg.Music.Enabled {
		musicPlayer = music.NewPlayer(cfg.Music.MusicPath, cfg.Music.SupportedFormats, log)
		if err := musicPlayer.LoadSongs(); err != nil {
			log.Warn("Failed to load music", "error", err)
		}
	}

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
		displayMode:   DisplayModeEmotion,
		musicPlayer:   musicPlayer,
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
		"type":    "hello",
		"version": 1,
		"features": map[string]interface{}{
			"mcp": true,
		},
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

// GetCurrentState 获取当前状态字符串（用于 keyboard 接口）
func (c *Client) GetCurrentState() string {
	return string(c.GetState())
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
			// 进入 Speaking 状态时停止音频发送
			c.audioCtrl.StopSending()
			c.logger.Debug("Auto-stopped audio sending for speaking state")
		case DeviceStateListening:
			// 进入 Listening 状态时确保可以发送
			if !c.audioCtrl.StartSending() {
				c.logger.Warn("Failed to start audio sending when entering listening state")
			}
		}

		c.state = newState
		c.logger.Info("State changed",
			"from", oldState,
			"to", newState)

		// 只在表情模式下才根据状态显示表情
		if c.GetDisplayMode() == DisplayModeEmotion {
			switch newState {
			case DeviceStateSpeaking:
				c.logger.Info("Attempting to show speaking emotion")
				if err := c.ShowEmotion("speaking"); err != nil {
					c.logger.Warn("Failed to show speaking emotion", "error", err)
				}
			case DeviceStateListening:
				c.logger.Info("Attempting to show listening emotion")
				if err := c.ShowEmotion("listening"); err != nil {
					c.logger.Warn("Failed to show listening emotion", "error", err)
				}
			case DeviceStateIdle:
				c.logger.Info("Attempting to show neutral emotion")
				if err := c.ShowEmotion("neutral"); err != nil {
					c.logger.Debug("Failed to show neutral emotion", "error", err)
				}
			}
		} else {
			c.logger.Debug("Not in emotion mode, skipping auto emotion update", "mode", c.GetDisplayMode())
		}
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
	fmt.Printf("%s Sending JSON message:\n%s\n", time.Now().Format("2006-01-02 15:04:05"), string(formattedJSON))

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
			// 检查 transport 是否存在
			c.stateMutex.RLock()
			transport := c.transport
			c.stateMutex.RUnlock()

			if transport == nil {
				// transport 已关闭，等待重新连接或退出
				select {
				case <-c.closeChan:
					return
				case <-time.After(100 * time.Millisecond):
					continue
				}
			}

			msgChan := transport.Receive()
			select {
			case msg, ok := <-msgChan:
				if !ok {
					// channel 已关闭
					c.logger.Info("Message channel closed")
					continue
				}
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
	fmt.Printf("%s Handling message:\n%s\n", time.Now().Format("2006-01-02 15:04:05"), string(formattedJSON))
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
	case "mcp": // 新增MCP消息处理
		return c.handleMCPMessage(message)
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

	// 注释掉自动监听，改为通过 keyboard 触发
	// if err := c.SendStartListening(ListenModeAuto); err != nil {
	// 	c.logger.Error("Failed to start auto listening", "error", err)
	// }

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

	// 在对话模式下，显示用户的语音
	if c.GetDisplayMode() == DisplayModeDialog {
		c.appendDialog("我", text)
	}

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
	emotion := "thinking"
	if e, ok := msg["emotion"].(string); ok {
		emotion = e
	}

	c.logger.Info("LLM response received",
		"text", text,
		"emotion", emotion,
		"session", sessionID)

	// 根据显示模式处理
	switch c.GetDisplayMode() {
	case DisplayModeEmotion:
		// 表情模式：只更新表情
		if err := c.ShowEmotion(emotion); err != nil {
			c.logger.Debug("Failed to show emotion", "emotion", emotion, "error", err)
		}
	case DisplayModeDialog:
		// 对话模式：显示AI回复
		c.appendDialog("AI", text)
	default:
		c.logger.Debug("Not in emotion mode, skipping emotion update", "currentMode", c.GetDisplayMode())
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
	c.logger.Info("ShowEmotion called", "emotion", emotionName)

	if c.config.Display.SkipExecution {
		c.logger.Warn("Display is disabled, skipping emotion", "emotion", emotionName)
		return nil
	}

	// 从配置中获取表情目录路径
	emotionPath, exists := c.config.Display.EmotionDirs[emotionName]
	c.logger.Info("Debug - Emotion lookup",
		"emotion", emotionName,
		"exists", exists,
		"path", emotionPath,
		"all_emotions", c.config.Display.EmotionDirs)

	if !exists {
		c.logger.Warn("Emotion not found in config", "emotion", emotionName, "available_emotions", c.config.Display.EmotionDirs)
		return fmt.Errorf("emotion not found: %s", emotionName)
	}

	c.logger.Info("Starting animation", "emotion", emotionName, "path", emotionPath, "fps", c.config.Display.FPS, "preload", c.config.Display.PreloadImages)
	rotation := display.Rotation(c.config.Display.Rotation)
	err := c.displayCtrl.StartAnimation(emotionPath, rotation, c.config.Display.FPS, c.config.Display.PreloadImages)
	if err != nil {
		c.logger.Error("Failed to start animation", "emotion", emotionName, "error", err)
	}
	return err
}

// ShowImage 显示单张图片
func (c *Client) ShowImage(imagePath string) error {
	if c.config.Display.SkipExecution {
		return nil
	}

	rotation := display.Rotation(c.config.Display.Rotation)
	return c.displayCtrl.ShowImage(imagePath, rotation)
}

// GetDisplayMode 获取当前显示模式
func (c *Client) GetDisplayMode() DisplayMode {
	c.displayModeMu.RLock()
	defer c.displayModeMu.RUnlock()
	return c.displayMode
}

// GetDisplayModeString 获取当前显示模式字符串（用于接口）
func (c *Client) GetDisplayModeString() string {
	c.displayModeMu.RLock()
	defer c.displayModeMu.RUnlock()
	return string(c.displayMode)
}

// StopMusic 停止音乐播放并恢复连接
func (c *Client) StopMusic() {
	c.logger.Info("StopMusic called")

	if c.musicPlayer != nil {
		c.musicPlayer.Stop()
	}

	// 恢复连接
	go c.reconnectAfterMusic()
}

// SetDisplayMode 设置显示模式
func (c *Client) SetDisplayMode(mode DisplayMode) {
	c.displayModeMu.Lock()
	c.displayMode = mode
	c.displayModeMu.Unlock()
	c.logger.Info("Display mode changed", "mode", mode)

	// 切换到对话模式时清空对话缓冲区
	if mode == DisplayModeDialog {
		c.clearDialog()
	}
}

// appendDialog 添加对话内容并更新显示
func (c *Client) appendDialog(speaker, text string) {
	c.dialogBufferMu.Lock()
	defer c.dialogBufferMu.Unlock()

	// 添加对话，最多保留 4 条
	c.dialogBuffer = append(c.dialogBuffer, fmt.Sprintf("[%s] %s", speaker, text))
	if len(c.dialogBuffer) > 4 {
		c.dialogBuffer = c.dialogBuffer[len(c.dialogBuffer)-4:]
	}

	// 更新显示
	dialogText := strings.Join(c.dialogBuffer, "\n")
	c.logger.Info("Dialog display updated", "text", dialogText)

	// 在后台线程更新显示，避免阻塞
	go func() {
		color := ColorRGB(255, 255, 255)
		if err := c.displayCtrl.ShowText(dialogText, c.config.Display.FontSize, color,
			c.config.Display.TextAlign.Horizontal, c.config.Display.TextAlign.Vertical); err != nil {
			c.logger.Warn("Failed to update dialog display", "error", err)
		}
	}()
}

// clearDialog 清空对话缓冲区
func (c *Client) clearDialog() {
	c.dialogBufferMu.Lock()
	defer c.dialogBufferMu.Unlock()
	c.dialogBuffer = nil
}

// ShowText 显示文本（不切换模式，仅显示）
func (c *Client) ShowText(text string, fontSize float64, hAlign, vAlign int) error {
	if c.config.Display.SkipExecution {
		return nil
	}

	color := ColorRGB(255, 255, 255)
	return c.displayCtrl.ShowText(text, fontSize, color, hAlign, vAlign)
}

// ShowDateTime 显示日期时间（切换到时钟模式）
func (c *Client) ShowDateTime() error {
	if c.config.Display.SkipExecution {
		return nil
	}

	// 切换到时钟模式
	c.SetDisplayMode(DisplayModeClock)

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

// SwitchToEmotionMode 切换到表情模式
func (c *Client) SwitchToEmotionMode() {
	c.SetDisplayMode(DisplayModeEmotion)
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

// ============================================================================
// MCP (Model Context Protocol) 实现
// ============================================================================

// MCP 相关常量
const (
	MCPProtocolVersion = "2024-11-05"
	MCPJSONRPCVersion  = "2.0"
)

// MCPRequest 表示 MCP 请求
type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      interface{}     `json:"id,omitempty"`
}

// MCPResponse 表示 MCP 响应
type MCPResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *MCPError   `json:"error,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

// MCPError 表示 MCP 错误
type MCPError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// MCPNotification 表示 MCP 通知（无 id 字段）
type MCPNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSON-RPC 错误码
const (
	MCPErrorParseError     = -32700
	MCPErrorInvalidRequest = -32600
	MCPErrorMethodNotFound = -32601
	MCPErrorInvalidParams  = -32602
	MCPErrorInternalError  = -32603
)

// InitializeParams 表示 initialize 请求参数
type InitializeParams struct {
	Capabilities struct {
		Vision *struct {
			URL   string `json:"url"`
			Token string `json:"token"`
		} `json:"vision,omitempty"`
	} `json:"capabilities,omitempty"`
}

// InitializeResult 表示 initialize 响应结果
type InitializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    MCPCapabilities `json:"capabilities"`
	ServerInfo      MCPServerInfo   `json:"serverInfo"`
}

// MCPCapabilities 表示设备能力
type MCPCapabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

// ToolsCapability 表示工具能力
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// MCPServerInfo 表示服务器信息
type MCPServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolsListResult 表示 tools/list 响应结果
type ToolsListResult struct {
	Tools      []MCPTool `json:"tools"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

// MCPTool 表示工具定义
type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// ToolsCallParams 表示 tools/call 请求参数
type ToolsCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// ToolsCallResult 表示 tools/call 响应结果
type ToolsCallResult struct {
	Content []MCPContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// MCPContent 表示工具执行结果内容
type MCPContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// MCPToolHandler 工具处理函数类型
type MCPToolHandler func(args map[string]interface{}) (interface{}, error)

// MCPToolRegistry 工具注册表
type MCPToolRegistry struct {
	tools    map[string]MCPTool
	handlers map[string]MCPToolHandler
}

// mcpRegistry 全局 MCP 工具注册表
var mcpRegistry = &MCPToolRegistry{
	tools:    make(map[string]MCPTool),
	handlers: make(map[string]MCPToolHandler),
}

// RegisterMCPTool 注册 MCP 工具
func RegisterMCPTool(name, description string, inputSchema map[string]interface{}, handler MCPToolHandler) {
	mcpRegistry.tools[name] = MCPTool{
		Name:        name,
		Description: description,
		InputSchema: inputSchema,
	}
	mcpRegistry.handlers[name] = handler
}

// GetMCPTools 获取所有注册的工具
func GetMCPTools() []MCPTool {
	tools := make([]MCPTool, 0, len(mcpRegistry.tools))
	for _, tool := range mcpRegistry.tools {
		tools = append(tools, tool)
	}
	return tools
}

// CallMCPTool 调用工具
func CallMCPTool(name string, args map[string]interface{}) (interface{}, error) {
	handler, ok := mcpRegistry.handlers[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	return handler(args)
}

// init 初始化默认 MCP 工具
func init() {
	// 注册获取设备状态工具
	RegisterMCPTool(
		"self.get_device_status",
		"获取当前设备状态，包括设备状态和会话信息",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		func(args map[string]interface{}) (interface{}, error) {
			// 这个处理器会在 Client 中被替换
			return map[string]interface{}{
				"state": "unknown",
			}, nil
		},
	)

	// 注册音量设置工具
	RegisterMCPTool(
		"self.audio_speaker.set_volume",
		"设置扬声器音量（0-100）",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"volume": map[string]interface{}{
					"type":        "integer",
					"description": "音量值（0-100）",
					"minimum":     0,
					"maximum":     100,
				},
			},
			"required": []string{"volume"},
		},
		func(args map[string]interface{}) (interface{}, error) {
			// 默认实现，实际会被替换
			return true, nil
		},
	)

	// 注册显示表情工具
	RegisterMCPTool(
		"self.display.show_emotion",
		"在显示屏上显示表情动画",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"emotion": map[string]interface{}{
					"type":        "string",
					"description": "表情名称（如：neutral中立、happy开心、sad悲伤、thinking思考、angry生气、surprised惊讶、dizzy眩晕、blink眨眼）",
				},
			},
			"required": []string{"emotion"},
		},
		func(args map[string]interface{}) (interface{}, error) {
			// 默认实现
			return true, nil
		},
	)

	// 注册显示文本工具
	RegisterMCPTool(
		"self.display.show_text",
		"在显示屏上显示文本",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"text": map[string]interface{}{
					"type":        "string",
					"description": "要显示的文本内容",
				},
				"font_size": map[string]interface{}{
					"type":        "number",
					"description": "字体大小",
					"default":     24,
				},
			},
			"required": []string{"text"},
		},
		func(args map[string]interface{}) (interface{}, error) {
			return true, nil
		},
	)

	// 注册显示时间工具
	RegisterMCPTool(
		"self.display.show_time",
		"在显示屏上显示当前时间（切换到时钟模式）",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		func(args map[string]interface{}) (interface{}, error) {
			return true, nil
		},
	)

	// 注册切换显示模式工具
	RegisterMCPTool(
		"self.display.set_mode",
		"切换显示模式：emotion(默认表情模式)、clock(时钟模式)、dialog(对话模式)",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"mode": map[string]interface{}{
					"type":        "string",
					"description": "显示模式：emotion(表情模式)、clock(时钟模式)、dialog(对话模式)、music(音乐模式)",
					"enum":        []string{"emotion", "clock", "dialog", "music"},
				},
			},
			"required": []string{"mode"},
		},
		func(args map[string]interface{}) (interface{}, error) {
			return true, nil
		},
	)

	// 注册获取显示状态工具
	RegisterMCPTool(
		"self.display.get_status",
		"获取当前显示状态，包括显示模式和当前显示内容",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		func(args map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{
				"mode": "emotion",
			}, nil
		},
	)

	// 注册音乐播放工具
	RegisterMCPTool(
		"self.music.play",
		"播放本地音乐",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		func(args map[string]interface{}) (interface{}, error) {
			return true, nil
		},
	)

	// 注册音乐暂停工具
	RegisterMCPTool(
		"self.music.pause",
		"暂停当前播放的音乐",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		func(args map[string]interface{}) (interface{}, error) {
			return true, nil
		},
	)

	// 注册音乐停止工具
	RegisterMCPTool(
		"self.music.stop",
		"停止播放音乐",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		func(args map[string]interface{}) (interface{}, error) {
			return true, nil
		},
	)

	// 注册下一首工具
	RegisterMCPTool(
		"self.music.next",
		"播放下一首音乐",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		func(args map[string]interface{}) (interface{}, error) {
			return true, nil
		},
	)

	// 注册上一首工具
	RegisterMCPTool(
		"self.music.previous",
		"播放上一首音乐",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		func(args map[string]interface{}) (interface{}, error) {
			return true, nil
		},
	)

	// 注册获取音乐列表工具
	RegisterMCPTool(
		"self.music.list",
		"获取本地音乐列表",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		func(args map[string]interface{}) (interface{}, error) {
			return []interface{}{}, nil
		},
	)

	// 注册播放指定歌曲工具
	RegisterMCPTool(
		"self.music.play_song",
		"播放指定索引的歌曲",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"index": map[string]interface{}{
					"type":        "integer",
					"description": "歌曲索引（从0开始）",
				},
			},
			"required": []string{"index"},
		},
		func(args map[string]interface{}) (interface{}, error) {
			return true, nil
		},
	)
}

// handleMCPMessage 处理 MCP 消息
func (c *Client) handleMCPMessage(msg map[string]interface{}) error {
	payload, ok := msg["payload"].(map[string]interface{})
	if !ok {
		return errors.New("MCP message missing payload field")
	}

	// 解析 MCP 请求
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	var mcpReq MCPRequest
	if err := json.Unmarshal(payloadBytes, &mcpReq); err != nil {
		return c.sendMCPError(mcpReq.ID, MCPErrorParseError, "Parse error", nil)
	}

	// 验证 JSONRPC 版本
	if mcpReq.JSONRPC != MCPJSONRPCVersion {
		return c.sendMCPError(mcpReq.ID, MCPErrorInvalidRequest, "Invalid JSON-RPC version", nil)
	}

	c.logger.Info("Received MCP request", "method", mcpReq.Method, "id", mcpReq.ID)

	// 根据方法分发处理
	switch mcpReq.Method {
	case "initialize":
		return c.handleMCPInitialize(mcpReq)
	case "tools/list":
		return c.handleMCPToolsList(mcpReq)
	case "tools/call":
		return c.handleMCPToolsCall(mcpReq)
	case "notifications/initialized":
		// 这是一个通知，不需要响应
		c.logger.Info("MCP session initialized")
		return nil
	default:
		return c.sendMCPError(mcpReq.ID, MCPErrorMethodNotFound, fmt.Sprintf("Method not found: %s", mcpReq.Method), nil)
	}
}

// handleMCPInitialize 处理 initialize 请求
func (c *Client) handleMCPInitialize(req MCPRequest) error {
	// 解析参数（可选）
	var params InitializeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return c.sendMCPError(req.ID, MCPErrorInvalidParams, "Invalid params", err.Error())
		}
	}

	// 如果有视觉能力配置，可以保存
	if params.Capabilities.Vision != nil {
		c.logger.Info("Received vision capability",
			"url", params.Capabilities.Vision.URL)
	}

	// 构建响应
	result := InitializeResult{
		ProtocolVersion: MCPProtocolVersion,
		Capabilities: MCPCapabilities{
			Tools: &ToolsCapability{
				ListChanged: false,
			},
		},
		ServerInfo: MCPServerInfo{
			Name:    "xiaozhi-go",
			Version: "1.0.0",
		},
	}

	return c.sendMCPResponse(req.ID, result)
}

// handleMCPToolsList 处理 tools/list 请求
func (c *Client) handleMCPToolsList(req MCPRequest) error {
	// 解析参数获取 cursor（用于分页，暂不支持）
	cursor := ""
	if len(req.Params) > 0 {
		var params struct {
			Cursor string `json:"cursor"`
		}
		if err := json.Unmarshal(req.Params, &params); err == nil {
			cursor = params.Cursor
		}
	}

	// 如果有 cursor，说明需要分页（暂不支持）
	if cursor != "" {
		return c.sendMCPResponse(req.ID, ToolsListResult{
			Tools:      []MCPTool{},
			NextCursor: "",
		})
	}

	// 返回所有工具
	tools := GetMCPTools()

	c.logger.Info("Returning tools list", "count", len(tools))

	return c.sendMCPResponse(req.ID, ToolsListResult{
		Tools:      tools,
		NextCursor: "",
	})
}

// handleMCPToolsCall 处理 tools/call 请求
func (c *Client) handleMCPToolsCall(req MCPRequest) error {
	// 解析参数
	var params ToolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return c.sendMCPError(req.ID, MCPErrorInvalidParams, "Invalid params", err.Error())
	}

	c.logger.Info("Calling tool", "name", params.Name, "arguments", params.Arguments)

	// 执行工具
	var result interface{}
	var err error

	switch params.Name {
	case "self.get_device_status":
		result = c.getDeviceStatus()
	case "self.audio_speaker.set_volume":
		result, err = c.setVolume(params.Arguments)
	case "self.display.show_emotion":
		result, err = c.showEmotionTool(params.Arguments)
	case "self.display.show_text":
		result, err = c.showTextTool(params.Arguments)
	case "self.display.show_time":
		result, err = c.showTimeTool(params.Arguments)
	case "self.display.set_mode":
		result, err = c.setDisplayModeTool(params.Arguments)
	case "self.display.get_status":
		result = c.getDisplayStatus()
	// 音乐相关工具
	case "self.music.play":
		result, err = c.musicPlayTool(params.Arguments)
	case "self.music.pause":
		result, err = c.musicPauseTool(params.Arguments)
	case "self.music.stop":
		result, err = c.musicStopTool(params.Arguments)
	case "self.music.next":
		result, err = c.musicNextTool(params.Arguments)
	case "self.music.previous":
		result, err = c.musicPreviousTool(params.Arguments)
	case "self.music.list":
		result = c.musicListTool()
	case "self.music.play_song":
		result, err = c.musicPlaySongTool(params.Arguments)
	default:
		// 尝试从注册表调用
		result, err = CallMCPTool(params.Name, params.Arguments)
	}

	// 构建响应
	callResult := ToolsCallResult{
		Content: []MCPContent{},
	}

	if err != nil {
		callResult.IsError = true
		callResult.Content = append(callResult.Content, MCPContent{
			Type: "text",
			Text: err.Error(),
		})
	} else {
		// 将结果转换为 JSON 文本
		resultBytes, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			callResult.Content = append(callResult.Content, MCPContent{
				Type: "text",
				Text: fmt.Sprintf("%v", result),
			})
		} else {
			callResult.Content = append(callResult.Content, MCPContent{
				Type: "text",
				Text: string(resultBytes),
			})
		}
	}

	return c.sendMCPResponse(req.ID, callResult)
}

// getDeviceStatus 获取设备状态
func (c *Client) getDeviceStatus() map[string]interface{} {
	status := c.GetStatus()
	return map[string]interface{}{
		"state":             string(status.State),
		"session_id":        status.SessionID,
		"connection_status": status.ConnectionStatus,
	}
}

// setVolume 设置音量
func (c *Client) setVolume(args map[string]interface{}) (interface{}, error) {
	volume, ok := args["volume"].(float64)
	if !ok {
		return nil, errors.New("volume must be a number")
	}

	// 转换为整数
	volumeInt := int(volume)
	if volumeInt < 0 || volumeInt > 100 {
		return nil, errors.New("volume must be between 0 and 100")
	}

	// 使用 amixer 设置音量
	cmd := exec.Command("amixer", "set", "Power Amplifier", fmt.Sprintf("%d%%", volumeInt))
	output, err := cmd.CombinedOutput()
	if err != nil {
		c.logger.Error("Failed to set volume", "error", err, "output", string(output))
		return nil, fmt.Errorf("failed to set volume: %w", err)
	}

	c.logger.Info("Volume set successfully", "volume", volumeInt)

	return true, nil
}

// showEmotionTool 显示表情工具
func (c *Client) showEmotionTool(args map[string]interface{}) (interface{}, error) {
	emotion, ok := args["emotion"].(string)
	if !ok {
		return nil, errors.New("emotion must be a string")
	}

	// 切换到表情模式
	c.SetDisplayMode(DisplayModeEmotion)

	if err := c.ShowEmotion(emotion); err != nil {
		return nil, err
	}

	return true, nil
}

// showTextTool 显示文本工具
func (c *Client) showTextTool(args map[string]interface{}) (interface{}, error) {
	text, ok := args["text"].(string)
	if !ok {
		return nil, errors.New("text must be a string")
	}

	fontSize := c.config.Display.FontSize
	if fs, ok := args["font_size"].(float64); ok {
		fontSize = fs
	}

	if err := c.ShowText(text, fontSize, c.config.Display.TextAlign.Horizontal, c.config.Display.TextAlign.Vertical); err != nil {
		return nil, err
	}

	return true, nil
}

// showTimeTool 显示时间工具
func (c *Client) showTimeTool(args map[string]interface{}) (interface{}, error) {
	if err := c.ShowDateTime(); err != nil {
		return nil, err
	}
	return true, nil
}

// setDisplayModeTool 设置显示模式工具
func (c *Client) setDisplayModeTool(args map[string]interface{}) (interface{}, error) {
	mode, ok := args["mode"].(string)
	if !ok {
		return nil, errors.New("mode must be a string")
	}

	var displayMode DisplayMode
	switch mode {
	case "emotion":
		displayMode = DisplayModeEmotion
	case "clock":
		displayMode = DisplayModeClock
	case "dialog":
		displayMode = DisplayModeDialog
	case "music":
		displayMode = DisplayModeMusic
	default:
		return nil, fmt.Errorf("invalid display mode: %s, valid modes are: emotion, clock, dialog, music", mode)
	}

	c.SetDisplayMode(displayMode)

	// 根据模式执行相应操作
	switch displayMode {
	case DisplayModeClock:
		if err := c.ShowDateTime(); err != nil {
			return nil, err
		}
	case DisplayModeDialog:
		// 显示对话模式提示
		c.clearDialog()
		if err := c.ShowText("对话模式已开启", c.config.Display.FontSize,
			c.config.Display.TextAlign.Horizontal, c.config.Display.TextAlign.Vertical); err != nil {
			c.logger.Warn("Failed to show dialog mode hint", "error", err)
		}
	case DisplayModeMusic:
		// 显示音乐模式提示
		if err := c.ShowText("音乐模式", c.config.Display.FontSize,
			c.config.Display.TextAlign.Horizontal, c.config.Display.TextAlign.Vertical); err != nil {
			c.logger.Warn("Failed to show music mode hint", "error", err)
		}
	case DisplayModeEmotion:
		// 显示默认表情
		if err := c.ShowEmotion("neutral"); err != nil {
			c.logger.Warn("Failed to show neutral emotion", "error", err)
		}
	}

	return map[string]interface{}{
		"mode":    string(displayMode),
		"success": true,
	}, nil
}

// getDisplayStatus 获取显示状态
func (c *Client) getDisplayStatus() map[string]interface{} {
	return map[string]interface{}{
		"mode":         string(c.GetDisplayMode()),
		"device_state": string(c.GetState()),
	}
}

// ============================================================================
// 音乐相关工具
// ============================================================================

// disconnectForMusic 断开连接以释放音频设备用于播放音乐
func (c *Client) disconnectForMusic() {
	c.logger.Info("Disconnecting for music playback")

	// 先切换到音乐模式，防止状态变化触发表情显示
	c.SetDisplayMode(DisplayModeMusic)

	// 发送 abort 消息通知服务器中断会话
	if c.transport != nil && c.sessionID != "" {
		abortMsg := map[string]interface{}{
			"type":       "abort",
			"session_id": c.sessionID,
			"reason":     "music_playback",
		}
		if err := c.sendJSON(abortMsg); err != nil {
			c.logger.Warn("Failed to send abort message", "error", err)
		}
	}

	// 停止音频发送
	c.audioCtrl.StopSending()
	c.audioCtrl.StopReceiving()

	// 停止音频采集
	c.StopAudioCapture()

	// 不关闭 audioPlayer，只是停止接收
	// 注意：不要调用 c.audioPlayer.Close()，否则无法恢复

	// 关闭 WebSocket 连接
	if c.transport != nil {
		if err := c.transport.Close(); err != nil {
			c.logger.Warn("Failed to close transport for music", "error", err)
		}
		c.transport = nil
	}

	// 设置状态但不触发表情（已在音乐模式）
	c.stateMutex.Lock()
	c.state = DeviceStateIdle
	c.sessionID = ""
	c.stateMutex.Unlock()

	// 等待音频设备完全释放
	time.Sleep(500 * time.Millisecond)

	c.logger.Info("Disconnected for music playback, audio device released")
}

// reconnectAfterMusic 音乐结束后重新连接
func (c *Client) reconnectAfterMusic() {
	c.logger.Info("Reconnecting after music playback")

	// 切换回表情模式
	c.SetDisplayMode(DisplayModeEmotion)

	// 显示默认表情
	if err := c.ShowEmotion("neutral"); err != nil {
		c.logger.Warn("Failed to show neutral emotion after music", "error", err)
	}

	// 重新建立 WebSocket 连接
	ctx := context.Background()
	if err := c.Connect(ctx); err != nil {
		c.logger.Error("Failed to reconnect after music", "error", err)
		return
	}

	c.logger.Info("Reconnected after music playback")
}

// ShowMusicAnimation 显示音乐可视化效果
func (c *Client) ShowMusicAnimation(songName string) error {
	c.logger.Info("ShowMusicAnimation called", "songName", songName)

	if c.config.Display.SkipExecution {
		c.logger.Info("Display execution skipped")
		return nil
	}

	// 使用程序生成的可视化效果
	if c.musicPlayer != nil {
		levelChan := c.musicPlayer.GetVisualizeChannel()
		color := struct{ R, G, B uint8 }{R: 0, G: 180, B: 255} // 青色
		c.logger.Info("Starting music visualizer")
		return c.displayCtrl.ShowMusicVisualizer(levelChan, songName, color)
	}

	c.logger.Warn("Music player is nil")
	return nil
}

// musicPlayTool 播放音乐
func (c *Client) musicPlayTool(args map[string]interface{}) (interface{}, error) {
	if c.musicPlayer == nil {
		return nil, errors.New("music player is not initialized")
	}

	c.logger.Info("musicPlayTool: preparing to play music")

	// 断开 WebSocket 连接，释放音频设备
	c.disconnectForMusic()

	// 切换到音乐模式
	c.SetDisplayMode(DisplayModeMusic)

	if err := c.musicPlayer.Play(); err != nil {
		c.logger.Error("Failed to start music playback", "error", err)
		return nil, err
	}

	// 显示音乐动画
	if song := c.musicPlayer.GetCurrentSong(); song != nil {
		go c.ShowMusicAnimation(song.Name)
	}

	return map[string]interface{}{
		"playing": true,
		"success": true,
	}, nil
}

// musicPauseTool 暂停音乐
func (c *Client) musicPauseTool(args map[string]interface{}) (interface{}, error) {
	if c.musicPlayer == nil {
		return nil, errors.New("music player is not initialized")
	}

	c.musicPlayer.Pause()

	return map[string]interface{}{
		"paused":  true,
		"success": true,
	}, nil
}

// musicStopTool 停止音乐
func (c *Client) musicStopTool(args map[string]interface{}) (interface{}, error) {
	if c.musicPlayer == nil {
		return nil, errors.New("music player is not initialized")
	}

	c.musicPlayer.Stop()

	// 停止音乐后恢复 WebSocket 连接
	go c.reconnectAfterMusic()

	return map[string]interface{}{
		"stopped": true,
		"success": true,
	}, nil
}

// musicNextTool 下一首
func (c *Client) musicNextTool(args map[string]interface{}) (interface{}, error) {
	if c.musicPlayer == nil {
		return nil, errors.New("music player is not initialized")
	}

	if err := c.musicPlayer.Next(); err != nil {
		return nil, err
	}

	// 显示音乐动画
	if song := c.musicPlayer.GetCurrentSong(); song != nil {
		go c.ShowMusicAnimation(song.Name)
	}

	return map[string]interface{}{
		"success": true,
	}, nil
}

// musicPreviousTool 上一首
func (c *Client) musicPreviousTool(args map[string]interface{}) (interface{}, error) {
	if c.musicPlayer == nil {
		return nil, errors.New("music player is not initialized")
	}

	if err := c.musicPlayer.Previous(); err != nil {
		return nil, err
	}

	// 显示音乐动画
	if song := c.musicPlayer.GetCurrentSong(); song != nil {
		go c.ShowMusicAnimation(song.Name)
	}

	return map[string]interface{}{
		"success": true,
	}, nil
}

// musicListTool 获取音乐列表
func (c *Client) musicListTool() interface{} {
	if c.musicPlayer == nil {
		return []interface{}{}
	}

	songs := c.musicPlayer.GetSongs()
	result := make([]map[string]interface{}, len(songs))
	for i, song := range songs {
		result[i] = map[string]interface{}{
			"index": i,
			"name":  song.Name,
			"path":  song.Path,
		}
	}

	return map[string]interface{}{
		"songs": result,
		"count": len(result),
	}
}

// musicPlaySongTool 播放指定歌曲
func (c *Client) musicPlaySongTool(args map[string]interface{}) (interface{}, error) {
	if c.musicPlayer == nil {
		return nil, errors.New("music player is not initialized")
	}

	c.logger.Info("musicPlaySongTool: preparing to play music")

	// 断开 WebSocket 连接，释放音频设备
	c.disconnectForMusic()

	index, ok := args["index"].(float64)
	if !ok {
		return nil, errors.New("index must be a number")
	}

	indexInt := int(index)
	if err := c.musicPlayer.PlaySong(indexInt); err != nil {
		c.logger.Error("Failed to start music playback", "error", err)
		return nil, err
	}

	// 切换到音乐模式
	c.SetDisplayMode(DisplayModeMusic)

	// 显示音乐动画
	if song := c.musicPlayer.GetCurrentSong(); song != nil {
		go c.ShowMusicAnimation(song.Name)
	}

	return map[string]interface{}{
		"success": true,
		"index":   indexInt,
	}, nil
}

// sendMCPResponse 发送 MCP 响应
func (c *Client) sendMCPResponse(id interface{}, result interface{}) error {
	response := MCPResponse{
		JSONRPC: MCPJSONRPCVersion,
		Result:  result,
		ID:      id,
	}

	return c.sendMCPMessage(response)
}

// sendMCPError 发送 MCP 错误响应
func (c *Client) sendMCPError(id interface{}, code int, message string, data interface{}) error {
	response := MCPResponse{
		JSONRPC: MCPJSONRPCVersion,
		Error: &MCPError{
			Code:    code,
			Message: message,
			Data:    data,
		},
		ID: id,
	}

	return c.sendMCPMessage(response)
}

// sendMCPMessage 发送 MCP 消息
func (c *Client) sendMCPMessage(response interface{}) error {
	msg := map[string]interface{}{
		"session_id": c.sessionID,
		"type":       "mcp",
		"payload":    response,
	}

	return c.sendJSON(msg)
}

// SendMCPNotification 发送 MCP 通知
func (c *Client) SendMCPNotification(method string, params interface{}) error {
	notification := MCPNotification{
		JSONRPC: MCPJSONRPCVersion,
		Method:  method,
	}

	if params != nil {
		paramsBytes, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("failed to marshal notification params: %w", err)
		}
		notification.Params = paramsBytes
	}

	msg := map[string]interface{}{
		"session_id": c.sessionID,
		"type":       "mcp",
		"payload":    notification,
	}

	return c.sendJSON(msg)
}
