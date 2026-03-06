# xiaozhi-go

运行在嵌入式设备上的智能语音助手客户端。

## MCP (Model Context Protocol) 支持

xiaozhi-go 深度集成 MCP 协议，自动发现并暴露丰富的工具能力。

### 可用工具

#### 音乐控制

| 工具 | 功能 |
|------|------|
| `self.music.play` | 播放本地音乐 |
| `self.music.pause` | 暂停播放 |
| `self.music.stop` | 停止播放 |
| `self.music.next` | 下一首 |
| `self.music.previous` | 上一首 |
| `self.music.list` | 获取音乐列表 |
| `self.music.play_song` | 播放指定歌曲 |

#### 显示屏控制

| 工具 | 功能 |
|------|------|
| `self.display.show_text` | 显示文本 |
| `self.display.show_time` | 显示时间 |
| `self.display.show_emotion` | 显示表情动画 |
| `self.display.set_mode` | 切换显示模式 |
| `self.display.get_status` | 获取显示状态 |

#### 设备控制

| 工具 | 功能 |
|------|------|
| `self.get_device_status` | 获取设备状态 |
| `self.audio_speaker.set_volume` | 设置音量 |

### MCP 工作流程

```
服务器                    设备 (xiaozhi-go)
   │                          │
   │◄────── tools/list ──────│
   │                          │
   │───── 工具列表 + Schema ──►
   │                          │
   │◄─── tools/call (播放音乐)─┤
   │                          │
   │─── 发送响应 ─────────────►
   │                          │
   │─── abort (断开连接) ────►
   │                          │
   │      [播放音乐...]        │
```

## 功能特性

### 核心功能

- **语音交互**：实时语音对话
- **多模式监听**：自动/手动/实时模式
- **WebSocket 通信**：低延迟长连接
- **双向语音流**：录制与播放

### 音频处理

- **Opus 编解码**：24kHz 高效编码
- **ALSA 音频**：可配置采样率、声道、帧时长
- **静音检测**：自动结束语音输入

### 显示功能

- **表情动画**：10+ 种表情（happy, sad, angry, surprised, dizzy, neutral, listening, speaking, thinking, blink）
- **帧动画**：BMP 序列播放，可配置帧率
- **文本/时间显示**：自定义字体、大小、颜色
- **屏幕控制**：亮度、旋转、双缓冲

### 设备交互

- **键盘控制**：唤醒、中断、空闲操作
- **状态管理**：unknown → connecting → idle → listening → speaking → disconnected
- **自动重连**：网络异常自动恢复

## 项目结构

```
xiaozhi-go/
├── cmd/xiaozhi/           # 主程序入口
├── core/                  # 核心客户端
│   └── client.go
├── audio/                 # 音频模块
│   ├── recorder.go
│   ├── player.go
│   └── opus_codec.go
├── display/               # 显示模块
│   └── emotions/         # 表情资源
├── input/                # 输入模块
│   └── keyboard.go
├── music/                # 音乐播放器
├── protocols/websocket/  # WebSocket 协议
├── logger/               # 日志
└── config/config.yaml    # 配置文件
```

## 设备状态

| 状态 | 说明 |
|------|------|
| unknown | 初始状态 |
| connecting | 连接中 |
| idle | 空闲 |
| listening | 监听中 |
| speaking | 说话中 |
| disconnected | 已断开 |

## 运行要求

- Linux 操作系统
- ALSA 音频库
- Framebuffer 设备
- WebSocket 服务器

## 许可证

MIT License
