package input

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

const (
	EV_KEY      = 0x01 // 按键事件类型
	KEY_PRESS   = 1    // 按键按下
	KEY_RELEASE = 0    // 按键释放
)

type KeyEvent struct {
	Time  syscall.Timeval
	Type  uint16
	Code  uint16
	Value int32
}

type StateGetter interface {
	GetCurrentState() string
}

// DisplayModeGetter 获取显示模式的接口
type DisplayModeGetter interface {
	GetDisplayModeString() string
	StopMusic()
}

type KeyboardListener struct {
	devicePath        string
	eventChan         chan KeyEvent
	stopChan          chan struct{}
	wg                sync.WaitGroup
	mu                sync.Mutex
	running           bool
	stateGetter       StateGetter
	displayModeGetter DisplayModeGetter
	actionFunc        func(string)
	lastKeyTime       time.Time     // 上次按键时间
	lastKeyCode       uint16        // 上次按键代码
	doubleTapTime     time.Duration // 双击时间间隔
}

func NewKeyboardListener(devicePath string, stateGetter StateGetter, displayModeGetter DisplayModeGetter, actionFunc func(string)) *KeyboardListener {
	return &KeyboardListener{
		devicePath:        devicePath,
		eventChan:         make(chan KeyEvent, 10),
		stopChan:          make(chan struct{}),
		doubleTapTime:     600 * time.Millisecond, // 双击间隔 600ms
		stateGetter:       stateGetter,
		displayModeGetter: displayModeGetter,
		actionFunc:        actionFunc,
	}
}

func (k *KeyboardListener) Start() error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.running {
		return errors.New("keyboard listener already running")
	}

	file, err := os.Open(k.devicePath)
	if err != nil {
		return fmt.Errorf("failed to open input device: %w", err)
	}

	k.running = true
	k.wg.Add(1)

	go func() {
		defer k.wg.Done()
		defer file.Close()

		for {
			select {
			case <-k.stopChan:
				return
			default:
				var event KeyEvent
				if err := binary.Read(file, binary.LittleEndian, &event); err != nil {
					time.Sleep(10 * time.Millisecond)
					continue
				}

				if event.Type == EV_KEY && event.Value == KEY_RELEASE {
					now := time.Now()
					// 检查是否是双击
					if event.Code == k.lastKeyCode && now.Sub(k.lastKeyTime) < k.doubleTapTime {
						k.actionFunc("idle")        // 双击进入空闲状态
						k.lastKeyTime = time.Time{} // 重置时间，避免连续触发
						continue
					}

					// 检查是否在音乐模式
					if k.displayModeGetter != nil {
						displayMode := k.displayModeGetter.GetDisplayModeString()
						if displayMode == "music" {
							// 在音乐模式下，单击停止音乐
							k.displayModeGetter.StopMusic()
							k.lastKeyCode = event.Code
							k.lastKeyTime = now
							continue
						}
					}

					// 处理单次按键 - 根据当前状态决定动作
					currentState := k.stateGetter.GetCurrentState()
					if currentState == "idle" || currentState == "disconnected" {
						k.actionFunc("wakeup")
					} else if currentState != "listening" {
						// 非 idle/listening 状态下，单击触发 interrupt（中断当前操作）
						k.actionFunc("interrupt")
					}

					k.lastKeyCode = event.Code
					k.lastKeyTime = now
				}
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
