package input

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/lisuiheng/xiaozhi-go/logger"
)

const (
	EV_KEY      = 0x01 // 按键事件类型
	KEY_PRESS   = 1    // 按键按下
	KEY_RELEASE = 0    // 按键释放
)

type KeyEvent struct {
	_     [16]byte // 时间戳占位，保持24字节对齐
	Type  uint16
	Code  uint16
	Value int32
}

type StateGetter interface {
	GetCurrentState() string
	GetDisplayMode() string
}

type KeyboardListener struct {
	devicePath    string
	eventChan     chan KeyEvent
	stopChan      chan struct{}
	wg            sync.WaitGroup
	mu            sync.Mutex
	running       bool
	stateGetter   StateGetter
	actionFunc    func(string)
	lastKeyTime   time.Time     // 上次按键时间
	lastKeyCode   uint16        // 上次按键代码
	doubleTapTime time.Duration // 双击时间间隔
}

func NewKeyboardListener(devicePath string, stateGetter StateGetter, actionFunc func(string)) *KeyboardListener {
	// 列出可用的输入设备
	if entries, err := filepath.Glob("/dev/input/event*"); err == nil {
		logger.Info("Available input devices", "devices", entries)
	}

	return &KeyboardListener{
		devicePath:    devicePath,
		eventChan:     make(chan KeyEvent, 10),
		stopChan:      make(chan struct{}),
		doubleTapTime: 600 * time.Millisecond, // 双击间隔 600ms
		stateGetter:   stateGetter,
		actionFunc:    actionFunc,
	}
}

func (k *KeyboardListener) Start() error {
	logger.Info("Starting keyboard listener", "devicePath", k.devicePath)

	k.mu.Lock()
	defer k.mu.Unlock()

	if k.running {
		return errors.New("keyboard listener already running")
	}

	file, err := os.Open(k.devicePath)
	if err != nil {
		return fmt.Errorf("failed to open input device: %w", err)
	}

	logger.Info("Keyboard device opened successfully")

	k.running = true
	k.wg.Add(1)

	go func() {
		defer k.wg.Done()
		defer file.Close()

		logger.Info("Keyboard listener goroutine started")

		buffer := make([]byte, 24) // KeyEvent 大小
		for {
			select {
			case <-k.stopChan:
				logger.Info("Keyboard listener stopping")
				return
			case <-time.After(100 * time.Millisecond):
			}

			// 设置读取超时，使读取可被中断
			file.SetDeadline(time.Now().Add(100 * time.Millisecond))

			n, err := file.Read(buffer)
			if err != nil {
				// 超时或其他错误，忽略并继续循环
				if os.IsTimeout(err) || err == syscall.EINTR {
					continue
				}
				// 其他错误（如设备断开），退出
				logger.Error("Keyboard read error", "error", err)
				return
			}

			if n < 24 {
				continue
			}

			// 手动解析二进制数据
			var event KeyEvent
			event.Type = binary.LittleEndian.Uint16(buffer[16:18])
			event.Code = binary.LittleEndian.Uint16(buffer[18:20])
			event.Value = int32(binary.LittleEndian.Uint32(buffer[20:24]))

			logger.Debug("Key event received",
				"type", event.Type,
				"code", event.Code,
				"value", event.Value,
				"ev_key", EV_KEY,
				"key_release", KEY_RELEASE)

			if event.Type == EV_KEY && event.Value == KEY_RELEASE {
				now := time.Now()
				logger.Debug("Key release detected",
					"code", event.Code,
					"lastCode", k.lastKeyCode,
					"lastTime", k.lastKeyTime,
					"now", now,
					"doubleTapTime", k.doubleTapTime,
					"timeDiff", now.Sub(k.lastKeyTime))

				// 检查是否是双击
				if event.Code == k.lastKeyCode && now.Sub(k.lastKeyTime) < k.doubleTapTime {
					k.actionFunc("reset")       // 双击重置为初始状态
					k.lastKeyTime = time.Time{} // 重置时间，避免连续触发
					continue
				}

				// 处理单次按键 - 根据当前状态和显示模式决定动作
				currentState := k.stateGetter.GetCurrentState()
				displayMode := k.stateGetter.GetDisplayMode()

				logger.Debug("Processing key action",
					"currentState", currentState,
					"displayMode", displayMode)

				// 特殊处理：时钟模式下单击切换回表情模式并唤醒
				if displayMode == "clock" && (currentState == "idle" || currentState == "disconnected") {
					logger.Info("Triggering wakeup_from_clock")
					k.actionFunc("wakeup_from_clock")
				} else if currentState == "idle" || currentState == "disconnected" {
					logger.Info("Triggering wakeup")
					k.actionFunc("wakeup")
				} else if currentState != "listening" {
					// 非 idle/listening 状态下，单击触发 interrupt（中断当前操作）
					logger.Info("Triggering interrupt")
					k.actionFunc("interrupt")
				}

				k.lastKeyCode = event.Code
				k.lastKeyTime = now
			}
		}
	}()

	return nil
}

func (k *KeyboardListener) Stop() {
	k.mu.Lock()
	defer k.mu.Unlock()

	if !k.running {
		return
	}

	close(k.stopChan)
	k.wg.Wait()
	k.running = false
}

func (k *KeyboardListener) IsRunning() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.running
}
