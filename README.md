# xiaozhi-go


## 项目简介

xiaozhi-go 是一个使用 Golang 实现的小智语音客户端，旨在为嵌入式设备（如 Raspberry Pi、NanoPi 等）提供语音交互能力。本仓库是基于 [xiaozhi-esp32](https://github.com/78/xiaozhi-esp32) 移植到 x86/ARM64 架构的版本。

## 功能特性

### 核心功能

- **语音交互**：支持与云端语音服务进行实时语音对话
- **多模式监听**：支持自动模式（auto）、手动模式（manual）和实时模式（realtime）三种监听模式
- **WebSocket 通信**：通过 WebSocket 协议与服务器保持长连接，实现低延迟通信
- **双向语音流**：支持音频的录制和播放，支持单向/双向语音流模式切换

### 音频处理

- **Opus 编解码**：内置高效 Opus 音频编解码器，支持 24kHz 采样率
- **音频录制**：支持 ALSA 音频采集，可配置采样率、声道数、帧时长
- **音频播放**：支持 PCM 音频播放，实时解码并播放服务器返回的音频数据
- **静音检测**：支持静音超时检测，自动结束语音输入

### 显示功能

- **表情动画**：支持多种表情动画，包括开心、伤心、生气、惊讶、晕眩、中性、聆听、说话、思考、眨眼等
- **帧动画播放**：支持 BMP 图片序列动画播放，可配置帧率（默认 8fps）
- **文本显示**：支持自定义字体、大小、颜色、位置显示文本
- **时间显示**：支持实时时间显示，可配置日期和时间格式
- **图片显示**：支持单张图片显示，可配置旋转角度
- **屏幕控制**：支持亮度调节、屏幕旋转、双缓冲等特性

### 设备交互

- **键盘控制**：支持键盘事件监听，可自定义唤醒、中断、空闲等操作
- **状态管理**：支持设备状态机管理（未知、连接中、空闲、监听中、说话中、断开连接）
- **自动重连**：支持网络异常时的自动重连机制

## 技术架构

```
xiaozhi-go/
├── cmd/                    # 命令行入口
│   ├── xiaozhi/           # 主程序
│   └── xiaozhi-cli/       # CLI 工具
├── core/                  # 核心客户端逻辑
│   ├── client.go          # 客户端主逻辑
│   └── errors.go          # 错误定义
├── audio/                 # 音频模块
│   ├── controller.go      # 音频控制器
│   ├── recorder.go        # 音频录制
│   ├── player.go          # 音频播放
│   ├── opus_codec.go      # Opus 编解码
│   └── interface.go       # 接口定义
├── display/               # 显示模块
│   ├── display.go         # 显示控制器
│   └── emotions/          # 表情动画资源
├── input/                  # 输入模块
│   └── keyboard.go        # 键盘监听
├── protocols/             # 协议实现
│   └── websocket/         # WebSocket 协议
├── logger/                 # 日志模块
├── session/                # 会话管理
├── utils/                  # 工具函数
└── config/                # 配置文件
    └── config.yaml        # 主配置文件
```

## 设备状态

| 状态 | 说明 |
|------|------|
| unknown | 初始未知状态 |
| connecting | 正在连接服务器 |
| idle | 空闲状态，等待用户交互 |
| listening | 正在监听用户语音 |
| speaking | 正在播放语音回复 |
| disconnected | 与服务器断开连接 |

## 支持的表情

| 表情 | 说明 |
|------|------|
| happy | 开心 |
| sad | 伤心 |
| angry | 生气 |
| surprised | 惊讶 |
| dizzy | 晕眩 |
| neutral | 中性 |
| listening | 聆听中 |
| speaking | 说话中 |
| thinking | 思考中 |
| blink | 眨眼 |

## 配置说明

配置文件位于 `config/config.yaml`，主要配置项包括：

- **system**: 系统配置（设备 ID、客户端 ID、网络设置）
- **display**: 显示配置（帧率、亮度、旋转角度、表情目录）
- **audio**: 音频配置（采样率、声道数、帧时长、静音超时）
- **logging**: 日志配置（日志级别、输出位置）

## 运行要求

- Linux 操作系统
- ALSA 音频库
- Framebuffer 设备（用于显示）
- WebSocket 服务器连接

## 许可证

[MIT License](LICENSE)