# Telegram 客服中继机器人(telegram双向聊天机器人)

一款基于go语言开发的轻量级多功能双向聊天机器人，普通用户私聊机器人后，消息会转发到管理员超级群的独立话题；管理员在话题中回复，消息会再转回用户。

使用教程：

方法一：
一键安装脚本（适用于Linux）
```bash
bash <(curl -sSL https://raw.githubusercontent.com/seagullz4/tgbot-chatgo/main/install.sh)
```

方法二：
1. 下载最新 [release](https://github.com/seagullz4/tgbot-chatgo/releases) 压缩包，并上传到 Linux 服务器  
2. 新建目录并解压：
```bash
mkdir tgbot-chatgo
cd tgbot-chatgo
# 将 xxxxx.zip 换成你实际下载的压缩包名称
unzip xxxxx.zip
```
3. 正确填写 `.env` 文件内容（可参考压缩包内的 `.env.example`）  
关于如何获取机器人 ID、群组 ID 等信息，请看 [id获取文档](https://github.com/seagullz4/tgbot-chatgo/blob/main/README2.md)

示例：
```bash
# 基础配置
APP_NAME=interactive-bot
BOT_TOKEN=123456789:replace-with-your-token
WELCOME_MESSAGE="你好，我是客服机器人。请直接发送消息联系我们。"

# 管理群必须是已开启 Topics 的超级群
ADMIN_GROUP_ID=-1001234567890
ADMIN_USER_IDS=123456789,987654321

# 唯一超级管理员（只能配置一个 ID，仅通过 .env 手动修改）
OWNER_USER_IDS=123456789

# 话题被外部删除后，是否禁止用户自动创建新话题
DELETE_TOPIC_AS_FOREVER_BAN=FALSE

# /clear 后是否同时删除用户私聊中的已映射消息
DELETE_USER_MESSAGE_ON_CLEAR_CMD=TRUE

# 是否关闭加减乘除安全验证
DISABLE_VERIFICATION=FALSE

# 用户发送消息的最小间隔（秒）；0 表示不限制
MESSAGE_INTERVAL=5

# 成功转发后是否向用户发送“已转达客服”回执
USER_FORWARD_ACK=TRUE

# 数据路径
DATABASE_PATH=data/db.sqlite3

# 日志文件与滚动参数
LOG_PATH=logs/bot.log
LOG_MAX_SIZE_MB=10
LOG_MAX_BACKUPS=5

# 并发与网络参数
BOT_WORKERS=4
POLL_TIMEOUT_SECONDS=50
HTTP_MAX_IDLE_PER_HOST=16
```
4. 给程序执行权限并启动：
```bash
chmod +x go-bot-linux-amd64
# 或者：chmod +x go-bot-linux-arm64
./go-bot-linux-amd64
# 或者：./go-bot-linux-arm64
```

低内存占用
<img width="774" height="73" alt="image" src="https://github.com/user-attachments/assets/53dbb89e-ebab-40ae-a25a-9bedda346602" />

## 开发环境

- 安装 [Go 1.22+](https://go.dev/dl/)
- 准备一个 Telegram Bot Token（BotFather）
- 准备一个已开启 Topics 的超级群，并把机器人加进去（需要管理话题权限）

检查是否安装成功：

```bash
go version
```

进入项目目录后拉取依赖：

```bash
go mod download
```

## 配置

复制示例配置到项目根目录：

```bash
cp .env.example .env
```

PowerShell：

```powershell
Copy-Item .env.example .env
```

按需修改 `.env`，主要项如下：

| 配置项 | 说明 |
|---|---|
| `BOT_TOKEN` | 机器人 Token（必填） |
| `ADMIN_GROUP_ID` | 管理超级群 ID，一般是 `-100...`（必填） |
| `ADMIN_USER_IDS` | 管理员用户 ID，多个用英文逗号分隔（必填） |
| `OWNER_USER_IDS` | 唯一超级管理员 ID，只能配置一个，仅通过 `.env` 手动修改（必填） |
| `APP_NAME` | 应用名称 |
| `WELCOME_MESSAGE` | 用户 `/start` 欢迎语 |
| `DELETE_TOPIC_AS_FOREVER_BAN` | 话题被外部删除后，是否禁止用户自动创建新话题，`TRUE` / `FALSE` |
| `DELETE_USER_MESSAGE_ON_CLEAR_CMD` | `/clear` 后是否同时删除用户私聊中的已映射消息，`TRUE` / `FALSE` |
| `DISABLE_VERIFICATION` | 是否关闭加减乘除安全验证，`TRUE` / `FALSE` |
| `MESSAGE_INTERVAL` | 用户发消息最小间隔（秒），`0` 表示不限制 |
| `USER_FORWARD_ACK` | 转发成功后是否提示“已转达客服” |
| `DATABASE_PATH` | SQLite 路径，默认 `data/db.sqlite3` |
| `LOG_PATH` | 日志文件路径，默认 `logs/bot.log` |
| `LOG_MAX_SIZE_MB` | 单个日志文件最大体积（MB） |
| `LOG_MAX_BACKUPS` | 日志滚动保留份数 |
| `BOT_WORKERS` | 并发处理数，默认 `4` |
| `POLL_TIMEOUT_SECONDS` | 长轮询超时时间（秒） |
| `HTTP_MAX_IDLE_PER_HOST` | HTTP 连接池空闲连接数 |

其他可选配置可直接看 `.env.example`。布尔值统一写 `TRUE` 或 `FALSE`。

## 启动开发

配置好 `.env` 后，在项目根目录运行：

```bash
go run ./cmd/bot
```

运行测试：

```bash
go test ./...
```

## 项目结构

```text
cmd/bot/            程序入口
internal/app/       启动装配
internal/config/    配置读取
internal/handler/   命令与消息入口
internal/service/   业务逻辑
internal/store/     数据库
internal/model/     数据模型
internal/job/       延迟任务
.env.example        配置示例
```


鸣谢  [Telegram-interactive-bot](https://github.com/MiHaKun/Telegram-interactive-bot)  本项目部分想法包括结构来源于这个优秀的项目
