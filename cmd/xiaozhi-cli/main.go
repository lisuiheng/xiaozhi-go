package main

//import (
//	"bufio"
//	"context"
//	"flag"
//	"fmt"
//	"os"
//	"strings"
//	"time"
//
//	"github.com/fatih/color"
//	"github.com/spf13/viper"
//	"xiaozhi-go/internal/app"
//	"xiaozhi-go/logger"
//)
//
//func main() {
//	// 命令行参数
//	configPath := flag.String("config", "", "Path to config file")
//	command := flag.String("cmd", "", "Command to execute (listen, stop, status, etc.)")
//	debug := flag.Bool("debug", false, "Enable debug mode")
//	flag.Parse()
//
//	// 加载配置
//	cfg, err := app.LoadConfig(*configPath)
//	if err != nil {
//		fmt.Printf("Failed to load config: %v\n", err)
//		os.Exit(1)
//	}
//
//	// 初始化日志
//	logCfg := logger.Config{
//		Level:   "info",
//		Outputs: []string{"stdout"},
//	}
//	if *debug {
//		logCfg.Level = "debug"
//	}
//	if err := logger.Init(logCfg); err != nil {
//		fmt.Printf("Failed to initialize logger: %v\n", err)
//		os.Exit(1)
//	}
//
//	// 创建客户端
//	client, err := app.NewClient(cfg, logger.Logger())
//	if err != nil {
//		logger.Error("Failed to create client", "error", err)
//		os.Exit(1)
//	}
//	defer func() {
//		if err := client.Close(); err != nil {
//			logger.Error("Failed to close client", "error", err)
//		}
//	}()
//
//	// 如果指定了命令，直接执行
//	if *command != "" {
//		executeCommand(client, *command)
//		return
//	}
//
//	// 交互式模式
//	startInteractive(client)
//}
//
//func executeCommand(client *app.Client, cmd string) {
//	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
//	defer cancel()
//
//	switch strings.ToLower(cmd) {
//	case "listen":
//		if err := client.StartListening(app.ListenModeAuto); err != nil {
//			logger.Error("Failed to start listening", "error", err)
//			os.Exit(1)
//		}
//		logger.Info("Started listening")
//	case "stop":
//		if err := client.StopListening(); err != nil {
//			logger.Error("Failed to stop listening", "error", err)
//			os.Exit(1)
//		}
//		logger.Info("Stopped listening")
//	case "status":
//		status := client.GetStatus()
//		logger.Info("Current status",
//			"state", status.State,
//			"session_id", status.SessionID,
//			"connection", status.ConnectionStatus)
//	default:
//		logger.Error("Unknown command", "command", cmd)
//		os.Exit(1)
//	}
//
//	// 等待操作完成
//	select {
//	case <-ctx.Done():
//		logger.Warn("Operation timed out")
//	case <-time.After(5 * time.Second):
//	}
//}
//
//func startInteractive(client *app.Client) {
//	blue := color.New(color.FgBlue).SprintFunc()
//	green := color.New(color.FgGreen).SprintFunc()
//	red := color.New(color.FgRed).SprintFunc()
//
//	reader := bufio.NewReader(os.Stdin)
//
//	for {
//		fmt.Printf("\n%s ", blue("xiaozhi-cli>"))
//		input, _ := reader.ReadString('\n')
//		input = strings.TrimSpace(input)
//
//		if input == "" {
//			continue
//		}
//
//		parts := strings.Fields(input)
//		cmd := parts[0]
//		args := parts[1:]
//
//		switch cmd {
//		case "listen", "start":
//			mode := app.ListenModeAuto
//			if len(args) > 0 {
//				mode = app.ListenMode(args[0])
//			}
//			if err := client.StartListening(mode); err != nil {
//				fmt.Printf("%s Error: %v\n", red("✗"), err)
//			} else {
//				fmt.Printf("%s Started listening in %s mode\n", green("✓"), mode)
//			}
//		case "stop":
//			if err := client.StopListening(); err != nil {
//				fmt.Printf("%s Error: %v\n", red("✗"), err)
//			} else {
//				fmt.Printf("%s Stopped listening\n", green("✓"))
//			}
//		case "status":
//			status := client.GetStatus()
//			fmt.Println("\nCurrent Status:")
//			fmt.Printf("  State: %s\n", status.State)
//			fmt.Printf("  Session ID: %s\n", status.SessionID)
//			fmt.Printf("  Connection: %s\n", status.ConnectionStatus)
//		case "exit", "quit":
//			fmt.Println("Exiting...")
//			return
//		case "help":
//			printHelp()
//		default:
//			fmt.Printf("%s Unknown command: %s\n", red("✗"), cmd)
//			printHelp()
//		}
//	}
//}
//
//func printHelp() {
//	fmt.Println("\nAvailable commands:")
//	fmt.Println("  listen [mode] - Start listening (modes: auto, manual, realtime)")
//	fmt.Println("  stop          - Stop listening")
//	fmt.Println("  status        - Show current status")
//	fmt.Println("  exit/quit     - Exit the program")
//	fmt.Println("  help          - Show this help message")
//}
