package logger

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

var (
	globalLogger *slog.Logger
	once         sync.Once
)

type Config struct {
	Level   string   `json:"level" yaml:"level"`     // debug/info/warn/error
	Outputs []string `json:"outputs" yaml:"outputs"` // stdout/file path
}

func Init(cfg Config) error {
	var err error
	once.Do(func() {
		// 设置日志级别
		level := slog.LevelInfo
		switch cfg.Level {
		case "debug":
			level = slog.LevelDebug
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}

		// 创建多个输出writer
		var writers []io.Writer
		for _, output := range cfg.Outputs {
			switch output {
			case "", "stdout":
				writers = append(writers, os.Stdout)
			default:
				// 确保目录存在
				if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
					panic(err)
				}

				// 打开或创建日志文件
				file, err := os.OpenFile(output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
				if err != nil {
					panic(err)
				}
				writers = append(writers, file)
			}
		}

		// 如果没有指定输出，默认使用stdout
		if len(writers) == 0 {
			writers = append(writers, os.Stdout)
		}
		// 创建多writer
		multiWriter := io.MultiWriter(writers...)

		// 创建 logger
		globalLogger = slog.New(slog.NewTextHandler(multiWriter, &slog.HandlerOptions{
			Level: level,
		}))
	})
	return err
}

func Debug(msg string, args ...interface{}) {
	globalLogger.Debug(msg, args...)
}

func Info(msg string, args ...interface{}) {
	globalLogger.Info(msg, args...)
}

func Warn(msg string, args ...interface{}) {
	globalLogger.Warn(msg, args...)
}

func Error(msg string, args ...interface{}) {
	globalLogger.Error(msg, args...)
}

func Logger() *slog.Logger {
	return globalLogger
}
