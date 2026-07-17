# Telegram 客服中继机器人(telegram双向聊天机器人)

一款基于go语言开发的轻量级多功能双向聊天机器人，普通用户私聊机器人后，消息会转发到管理员超级群的独立话题；管理员在话题中回复，消息会再转回用户。

使用教程：
下载最新的release文件并且解压到Linux服务器上面，新建一个文件夹
```bash
mkdir tgbot-chatgo
cd tgbot-chatgo
#下载最新压缩包
unzip xxxxx.zip
```
必须正确填写./env里面的文件内容

示例：
```bash
# 基础配置
APP_NAME=interactive-bot
BOT_TOKEN=123456789:replace-with-your-token
WELCOME_MESSAGE="你好，我是客服机器人。请直接发送消息联系我们。"

# 管理群必须是已开启 Topics 的超级群
ADMIN_GROUP_ID=-1001234567890
ADMIN_USER_IDS=123456789,987654321
```
之后对软件包进行授予权限，并进行执行二进制文件
```bash
chmod +x bot-linux-amd64
#或者 chmod +x bot-linux-arm64
./bot-linux-amd64
#或者./bot-linux-arm64 
```

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
| `APP_NAME` | 应用名称 |
| `WELCOME_MESSAGE` | 用户 `/start` 欢迎语 |
| `DISABLE_VERIFICATION` | 是否关闭人机验证，`TRUE` / `FALSE` |
| `MESSAGE_INTERVAL` | 用户发消息最小间隔（秒），`0` 表示不限制 |
| `USER_FORWARD_ACK` | 转发成功后是否提示“已转达客服” |
| `DATABASE_PATH` | SQLite 路径，默认 `data/db.sqlite3` |
| `BOT_WORKERS` | 并发处理数，默认 `4` |

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
