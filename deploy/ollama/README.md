# Ollama 本地服务（macOS + Homebrew）

用 `brew install ollama` 安装后，Ollama 作为 LaunchAgent 在后台运行，监听 `http://127.0.0.1:11434`。日常操作用 `ollama.sh`：

```bash
./deploy/ollama/ollama.sh start     # 启动并等待 API 就绪
./deploy/ollama/ollama.sh status    # API、launchd 状态、端口、已加载模型
./deploy/ollama/ollama.sh restart   # 重启进程
./deploy/ollama/ollama.sh stop      # 停止服务，释放内存
./deploy/ollama/ollama.sh logs      # 跟踪 /opt/homebrew/var/log/ollama.log
```

`stop` 只把服务从当前登录会话卸掉，plist 保留，下次登录还会自动启动。只想释放某个模型占的内存、保留服务时，用 `ollama stop <模型名>`。

## 首次安装

```bash
brew install ollama
./deploy/ollama/ollama.sh start
ollama pull qwen3:14b
ollama run qwen3:14b
```

模型存在 `~/.ollama/models`，换机器后要重新 `pull`。项目里用本地模型时设置 `AGENT_BASE_URL=http://localhost:11434/v1`。

## 为什么不用 `brew services restart`

在一台 M1 Pro 上遇到过：`brew services restart ollama` 之后，`brew services list` 一直显示 `other`，`curl` 连不上 11434。`launchctl print` 的状态是：

```text
state = not running
runs = 0
last exit code = (never exited)
pended nondemand spawn = speculative
```

服务已经加载进 launchd，但 launchd 只把启动挂起，从没真正执行过，所以既不算 `started` 也不算 `stopped`，Homebrew 显示为 `other`。`ollama` 程序本身没问题，`ollama serve` 前台运行是正常的。

`brew services restart` 会先卸载再重新加载服务，重新加载后可能又停在这个挂起状态。`launchctl kickstart -k` 不看启动条件、直接运行，能把进程拉起来。`ollama.sh` 在加载服务后总会补一次 `kickstart`。

## 手动操作

```bash
DOMAIN="gui/$(id -u)"
PLIST="$HOME/Library/LaunchAgents/sh.brew.ollama.plist"
LABEL="$(plutil -extract Label raw -o - "$PLIST")"   # 旧版 Homebrew 是 homebrew.mxcl.ollama
TARGET="$DOMAIN/$LABEL"

launchctl print "$TARGET" | grep -E 'state|pid|runs|last exit'   # 查看状态
launchctl enable "$TARGET"                                       # 未加载时：启用
launchctl bootstrap "$DOMAIN" "$PLIST"                           # 未加载时：加载
launchctl kickstart -k "$TARGET"                                 # 启动 / 重启
launchctl bootout "$TARGET"                                      # 停止并卸载
curl -s http://127.0.0.1:11434/api/version                       # 有版本号即正常
```

## 常见报错

| 现象 | 原因 | 处理 |
|---|---|---|
| `kickstart`：`Could not find service ... in domain for user gui: 501` | 服务没加载到当前会话 | 先 `enable` + `bootstrap`，再 `kickstart` |
| `bootstrap` / `load`：`5: Input/output error` | launchd 没给出具体原因，可能是已加载、plist 有问题、被禁用 | 先 `launchctl print "$TARGET"` 看是否已加载；再按下面的检查项排查 |
| `bootout`：`5: Input/output error` | 服务本来就没加载 | 直接 `bootstrap` |
| `brew services list` 显示 `other`，`runs = 0` | 已加载但从未启动 | `launchctl kickstart -k "$TARGET"` |
| `curl` 连不上，但 `ollama serve` 前台正常 | launchd 那份没起来，或被前台进程占了端口 | `pkill -f 'ollama serve'` 后 `ollama.sh start` |

错误 5 的检查项：

```bash
plutil -lint "$PLIST"                                   # plist 语法
ls -l "$PLIST"                                          # 属主是自己、权限 644
launchctl print-disabled "$DOMAIN" | grep -i ollama     # 应为 enabled
sudo sfltool dumpbtm | grep -i -B4 -A12 ollama          # 后台项目是否被禁用
log show --last 2m --style compact --predicate 'eventMessage CONTAINS[c] "ollama"'
```

如果后台项目被禁用，到「系统设置 → 通用 → 登录项与扩展 → 允许在后台」打开 `ollama`。

不要用 `sudo launchctl bootstrap gui/...` 来启动，那样服务会以 root 身份运行，`~/.ollama` 下的模型和密钥也会对不上。

## 临时方案

launchd 怎么都起不来时，先手动在后台跑，重启电脑后需要再执行一次：

```bash
nohup /opt/homebrew/bin/ollama serve >> "$HOME/ollama-serve.log" 2>&1 &
```

之后改回 launchd 管理前，先 `pkill -f 'ollama serve'`，否则 launchd 那份会因为端口被占而退出。
