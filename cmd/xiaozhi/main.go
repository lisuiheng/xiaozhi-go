package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lisuiheng/xiaozhi-go/core"
	"github.com/lisuiheng/xiaozhi-go/logger"
	"github.com/spf13/viper"
)

func main() {
	// 加载配置
	//cfg, err := loadConfig("D:\\GolandProjects\\xiaozhi-go\\config\\config.yaml")
	cfg, err := loadConfig("/media/lee/48624A91624A8422/GolandProjects/xiaozhi-go/config/config.yaml")
	if err != nil {
		logger.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	// 初始化日志
	if err := initLogger(cfg); err != nil {
		logger.Error("Failed to initialize logger", "error", err)
		os.Exit(1)
	}
	defer logger.Logger().Info("Shutting down xiaozhi service")

	// 创建应用客户端
	client, err := core.NewClient(cfg, logger.Logger())
	if err != nil {
		logger.Error("Failed to create client", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Error("Failed to close client", "error", err)
		}
	}()

	// 设置信号处理
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// 启动主服务
	go func() {
		logger.Info("Starting xiaozhi service")
		if err := client.Run(ctx); err != nil {
			logger.Error("Service runtime error", "error", err)
			cancel()
		}
	}()

	// 等待终止信号
	select {
	case sig := <-sigChan:
		logger.Info("Received signal, shutting down", "signal", sig)
	case <-ctx.Done():
		logger.Info("Context cancelled, shutting down")
	}

	logger.Info("Service shutdown completed")
}

// loadConfig 加载配置文件
func loadConfig(configPath string) (core.Config, error) {
	viper.SetConfigType("yaml")

	if configPath != "" {
		viper.SetConfigFile(configPath)
	} else {
		viper.SetConfigName("config")
		viper.AddConfigPath(".")
		viper.AddConfigPath("configs")
		viper.AddConfigPath("/etc/xiaozhi")
	}

	// 设置默认值
	viper.SetDefault("server.protocol_version", 1)
	viper.SetDefault("audio.sample_rate", 16000)
	viper.SetDefault("audio.channels", 1)
	viper.SetDefault("audio.frame_duration", 60)
	viper.SetDefault("logging.level", "info")

	// 读取配置文件
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return core.Config{}, fmt.Errorf("failed to read config: %v", err)
		}
	}

	// 绑定环境变量
	viper.AutomaticEnv()
	viper.SetEnvPrefix("XIAOZHI")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	var cfg core.Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return core.Config{}, fmt.Errorf("failed to unmarshal config: %v", err)
	}

	return cfg, nil
}

// initLogger 初始化日志系统
func initLogger(cfg core.Config) error {
	logCfg := logger.Config{
		Level:   cfg.Logging.Level,
		Outputs: cfg.Logging.Outputs,
	}

	// 调试模式覆盖配置
	if viper.GetBool("debug") {
		logCfg.Level = "debug"
		logCfg.Outputs = []string{"stdout"}
		logger.Debug("Debug mode enabled")
	}

	return logger.Init(logCfg)
}
