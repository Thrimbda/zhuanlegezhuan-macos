# 砖了个砖 macOS 求解器

在 Mac 微信或 iPhone 镜像里自动操作《砖了个砖》。程序截取游戏窗口、识别 14×10 棋盘，然后用 Go 求解器规划消除顺序。Swift 助手通过 macOS 鼠标事件执行，Python 在每步之后对照实际棋盘。Mac 微信支持自动选择高亮配对目标和连续过关。

## 构建

需要 macOS 14 或更新版本、Xcode 命令行工具、Go 1.22+、Python 3.10+。iPhone 镜像需要 macOS 15 或更新版本。给运行命令的应用开启「屏幕与系统音频录制」权限。自动操作还需要「辅助功能」权限。

```sh
make
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r requirements.txt
```

## 首次校准

当前坐标适配原生截图为 429×805 的 Mac 微信窗口，以及 300×663 的 iPhone 镜像窗口。打开棋盘，手动消掉一两对砖，再保存截图。

```sh
mkdir -p .local
./bin/mirror --wechat frame .local/board.png
```

按截图从左到右、从上到下，在 `.local/labels.txt` 写 14 行，每行 10 个空格分隔的标记。同一图案用同一个标记，空格用 `.`。例如，`苹果 铃铛 . 帽子` 表示四个格子。生成模板后，先试跑求解确认识别结果。

```sh
python calibrate.py tiles --wechat .local/board.png .local/labels.txt
python play_mirror.py --wechat --horizontal
```

使用 iPhone 镜像时，在截图、校准和运行命令中省去 `--wechat`。截图、标签和模板存放在本地的 `.local/`。

## 自动游玩

```sh
# Mac 微信打一局
python play_mirror.py --wechat --horizontal --execute

# iPhone 镜像打一局
python play_mirror.py --horizontal --execute
```

`--horizontal` 把推砖限制为横向，仍允许纵向直接配对。可加 `--limit 3` 先验证前三步。

要连续打关，先在 Mac 微信的「获取奖励」画面截图并校准。连玩会点「确定」，等新棋盘识别成功后继续。

```sh
./bin/mirror --wechat frame .local/reward.png
python calibrate.py reward .local/reward.png
python play_mirror.py --wechat --horizontal --execute --loop
```

知道当前关数时，可加 `--start-level 9 --stop-level 15`，在打完第 15 关后停在奖励页。运行时按 `Ctrl+C` 停止。遇到棋盘不符或无法求解时，脚本会退出。棋盘校验失败时，已抓到的末帧保存在 `.local/last-frame.png`。

## 求解器来源

`native/` 来自 [Kevin-HYX/zhuanlegezhuan-solver](https://github.com/Kevin-HYX/zhuanlegezhuan-solver/tree/87bee62214a034ef23f84ddbec4f3c20d227b30c)，其 MIT 许可证保留在 `native/LICENSE`。此项目在上游代码上增加了可选的横向推砖限制。运行 `make test` 执行 Go 测试。
