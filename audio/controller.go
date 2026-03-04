// audio/controller.go
package audio

import "sync"

// controller 实现音频控制逻辑
type controller struct {
	mu          sync.Mutex
	isSending   bool
	isReceiving bool
}

// NewController 创建新的音频控制器实例
func NewController() Controller {
	return &controller{}
}

func (c *controller) StartSending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isReceiving {
		return false
	}

	c.isSending = true
	return true
}

func (c *controller) StopSending() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.isSending = false
}

func (c *controller) StartReceiving() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isSending {
		return false
	}

	c.isReceiving = true
	return true
}

func (c *controller) StopReceiving() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.isReceiving = false
}

func (c *controller) IsSending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isSending
}

func (c *controller) IsReceiving() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isReceiving
}
