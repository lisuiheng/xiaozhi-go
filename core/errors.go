package core

import "errors"

var (
	ErrUnsupportedProtocol = errors.New("unsupported protocol")
	ErrConnectionLost      = errors.New("connection lost")
	ErrAuthFailed          = errors.New("authentication failed")
	ErrConnectionFailed    = errors.New("connection failed")
	// ...其他错误定义
)
