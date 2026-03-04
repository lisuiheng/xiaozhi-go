package utils

import "time"

type ReconnectStrategy interface {
	NextDelay() time.Duration
	Reset()
}

type ExponentialBackoff struct {
	currentDelay time.Duration
	maxDelay     time.Duration
}

func NewExponentialBackoff() *ExponentialBackoff {
	return &ExponentialBackoff{
		currentDelay: 1 * time.Second,
		maxDelay:     30 * time.Second,
	}
}

func (e *ExponentialBackoff) NextDelay() time.Duration {
	delay := e.currentDelay
	e.currentDelay *= 2
	if e.currentDelay > e.maxDelay {
		e.currentDelay = e.maxDelay
	}
	return delay
}
