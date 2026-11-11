# DSH Pinky Mirror

把 [Pinky](https://github.com/DIYgod/pinky) 的手机实时画面接进 [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) 的右侧边栏。

**不打开 Pinky 客户端窗口，也能在 DSH 里看手机画面、按导航键。**

![界面示意](docs/screenshot.png)

---

## 为什么需要这个

Pinky 的网关只暴露三个 HTTP 路由：

| 路由 | 用途 |
|---|---|
| `/mcp` | Agent 的 `phone_*` 工具 |
| `/upload/` | 文件上传 |
| `/download/` | 文件下载 |

手机画面（scrcpy 的 H.264）在 Go GUI 进程内部解码后**直接画进窗口，从不经过网关**。所以官方版本没有任何办法从外部拿到画面。

本项目给 Pinky 加了一个**画面流出口**，默认关闭，不影响原版任何行为。

---

## 特点

- **不改动官方 Pinky** —— 作为独立实例运行，与系统安装的 Pinky **完全隔离**（私有 `PINKY_HOME`、独立 socket、独立端口）
- **无需客户端窗口** —— `daemon` 模式，纯后台运行
- **操作逻辑一致** —— 19 个 `phone_*` 工具、审批规则、屏幕操作全部沿用原实现
- **自带依赖** —— 二进制与 FFmpeg 库都在本地，不依赖系统包

---

## 效果

| 功能 | 说明 |
|---|---|
| 实时画面 | MJPEG，15fps，画面静止时自动停止出帧（scrcpy 的省电设计） |
| 手机外框 | 复刻 Pinky 的边框参数（`ui/bezel.go`）：边框 6.3px、圆角 = 短边 × 0.137 |
| 导航键 | 返回 / 主页 / 最近任务，走 `Gateway.Key()`，与客户端点按键同一条路径 |
| 状态提示 | 实时 / 静止 / 未连接，静止不误报为断流 |

---

## 安装

### 依赖

| 依赖 | 说明 |
|---|---|
| Go 1.27+ | 编译 Pinky（[下载](https://go.dev/dl/)） |
| FFmpeg 9 运行库 | `libavcodec.so.63`。安装官方 Pinky 即自带，或装 ffmpeg 9 |
| adb | 连接手机，`sudo apt install android-tools-adb` |
| DSH | 已安装并跑过 `dsh web` |

### 一键安装

```bash
git clone https://github.com/luckyruiguo/dsh-pinky-mirror.git
cd dsh-pinky-mirror
./install.sh
```

脚本会：

1. 检查依赖
2. 编译独立版 Pinky（约 2-3 分钟）
3. 复制 FFmpeg 库到本地
4. 安装 systemd 用户服务并启动
5. 配置 DSH 侧边栏插件

**只装服务、不动 DSH 配置：**

```bash
./install.sh --skip-dsh
```

### 自定义路径和端口

```bash
PINKY_MIRROR_DIR=~/my-pinky PINKY_MIRROR_PORT=7470 ./install.sh
```

---

## 使用

### 1. 重启 DSH

```bash
dsh web
```

### 2. 打开侧边栏

刷新浏览器，右侧边栏「开始」页会出现 **📱 手机**，点开即是实时画面。

### 3. 直接访问画面

画面服务自己也提供网页，可以单独打开：

```
http://127.0.0.1:7466/screen/view
```

---

## 服务管理

```bash
systemctl --user status  pinky-standalone   # 状态
systemctl --user restart pinky-standalone   # 重启
systemctl --user stop    pinky-standalone   # 停止
journalctl --user -u pinky-standalone -f    # 日志
```

服务配置了 `Restart=always`，崩溃自动恢复，开机自启。

---

## 与官方 Pinky 并存

本项目使用私有 `PINKY_HOME`，因此：

| | 官方 Pinky | 本项目 |
|---|---|---|
| 配置目录 | `~/.config/pinky/` | `<安装目录>/home/config/` |
| socket | `/run/user/<uid>/pinky/pinky.sock` | `<安装目录>/home/run/pinky.sock` |
| MCP HTTP | `127.0.0.1:7465` | `127.0.0.1:7467` |
| 画面流 | 无 | `127.0.0.1:7466` |

**两个可以同时运行，互不干扰。** 官方 Pinky 看不到本项目的网关，反之亦然。

> ⚠️ 配置不共享。如果你在官方 Pinky 里调过审批规则，需要在本项目里重新配。

---

## HTTP 接口

| 端点 | 方法 | 说明 |
|---|---|---|
| `/screen/stream` | GET | MJPEG 流 |
| `/screen/frame.jpg` | GET | 单帧快照 |
| `/screen/view` | GET | 独立查看页（可 iframe 嵌入） |
| `/screen/health` | GET | 状态 JSON |
| `/screen/key?k=back\|home\|recents` | POST | 发送导航键 |
| `/screen/takeover?on=1` | POST | 接管开关 |

`/screen/health` 示例：

```json
{
  "connected": true,
  "live": true,
  "seq": 132,
  "frameAge": 27,
  "encoder": true,
  "fps": 15,
  "takeover": false
}
```

- `connected` —— 链路是否健康（有画面可达）
- `live` —— 最近是否有新帧（**静止时为 false 属正常**，scrcpy 不发重复帧）

---

## 对上游的改动

改动只有**纯新增**，不修改任何既有逻辑：

| 文件 | 改动 | 行数 |
|---|---|---|
| `internal/screenstream/` | 新增包：解码 → JPEG → MJPEG | ~750 |
| `cmd/pinky/daemon.go` | 新增 `--screen`、`--screen-fps`、`--screen-width` 参数 | ~30 |

核心模块 `internal/mcp/`、`internal/approval/`、驱动层 **一行未改**。

画面流默认关闭（不传 `--screen` 时行为与原版完全一致）。

---

## 已知限制

| 限制 | 说明 |
|---|---|
| **手机休眠/锁屏时无画面** | scrcpy 的省电设计。唤醒手机即可恢复，无需重启服务 |
| **画面静止时帧率为 0** | 同上。用 `connected` 而非 `live` 判断链路是否健康 |
| **不跟随上游更新** | 本项目是 fork，Pinky 更新后需重新 `./install.sh` |
| **Linux + X11 优先** | 底层用 adb/scrcpy，理论上跨平台，但仅在 Linux 测试过 |

---

## 卸载

```bash
./uninstall.sh
```

会停止服务、移除 DSH 插件配置，并询问是否删除安装目录。

---

## 排障

**侧边栏空白？**

1. 确认服务在跑：`systemctl --user status pinky-standalone`
2. 确认画面流：`curl http://127.0.0.1:7466/screen/health`
3. 直接看画面：浏览器打开 `http://127.0.0.1:7466/screen/view`
4. 若以上都正常，说明是 DSH 插件问题，重启 DSH 并强制刷新浏览器（Ctrl+Shift+R）

**画面不显示 / 一直 connecting？**

```bash
adb shell dumpsys power | grep mWakefulness   # 应为 Awake
adb shell input keyevent KEYCODE_WAKEUP       # 唤醒
```

**编译失败？**

```bash
cd pinky-fork && go mod download && go build ./cmd/pinky
```

---

## 许可

本项目的 Go 部分基于 [Pinky](https://github.com/DIYgod/pinky)（MIT）。修改同样以 MIT 发布，详见 [LICENSE](LICENSE)。

`Pinky` 是 [DIYgod](https://github.com/DIYgod) 的项目，本项目与之无隶属关系。
