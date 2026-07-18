#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# 可通过同名环境变量覆盖这些参数，例如：
# INSTALL_DIR=/srv/tgbot SERVICE_NAME=my-tgbot sudo -E bash install.sh
REPO_OWNER="${REPO_OWNER:-seagullz4}"
REPO_NAME="${REPO_NAME:-tgbot-chatgo}"
INSTALL_DIR="${INSTALL_DIR:-/opt/tgbot-chatgo}"
SERVICE_NAME="${SERVICE_NAME:-tgbot-chatgo}"
SERVICE_USER="${SERVICE_USER:-tgbot-chatgo}"
SERVICE_GROUP="${SERVICE_GROUP:-${SERVICE_USER}}"
RELEASE_REQUEST="${VERSION:-latest}"

GITHUB_REPO_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}"
GITHUB_API_URL="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}"
SYSTEMD_UNIT="/etc/systemd/system/${SERVICE_NAME}.service"
VERSION_FILE="${INSTALL_DIR}/.version"
TEMP_DIR=""
INPUT_DEVICE="${INPUT_DEVICE:-/dev/tty}"

COLOR_RED='\033[0;31m'
COLOR_GREEN='\033[0;32m'
COLOR_YELLOW='\033[0;33m'
COLOR_BLUE='\033[0;34m'
COLOR_RESET='\033[0m'

info() { printf "%b[信息]%b %s\n" "$COLOR_BLUE" "$COLOR_RESET" "$*"; }
success() { printf "%b[完成]%b %s\n" "$COLOR_GREEN" "$COLOR_RESET" "$*"; }
warn() { printf "%b[注意]%b %s\n" "$COLOR_YELLOW" "$COLOR_RESET" "$*"; }
fatal() { printf "%b[错误]%b %s\n" "$COLOR_RED" "$COLOR_RESET" "$*" >&2; exit 1; }

cleanup_temp() {
  if [[ -n "$TEMP_DIR" && -d "$TEMP_DIR" ]]; then
    rm -rf -- "$TEMP_DIR"
  fi
  TEMP_DIR=""
}
trap cleanup_temp EXIT

require_root() {
  if [[ "${EUID}" -ne 0 ]]; then
    fatal "请使用 root 权限运行，例如：sudo bash install.sh"
  fi
}

command_exists() {
  command -v "$1" >/dev/null 2>&1
}

open_input_stream() {
  exec 9<"$INPUT_DEVICE"
}

validate_settings() {
  [[ "$INSTALL_DIR" == /* ]] || fatal "INSTALL_DIR 必须是绝对路径：${INSTALL_DIR}"
  [[ "$SERVICE_NAME" =~ ^[A-Za-z0-9_.@-]+$ ]] || fatal "SERVICE_NAME 包含不安全字符：${SERVICE_NAME}"
  [[ "$SERVICE_USER" =~ ^[A-Za-z_][A-Za-z0-9_-]*[$]?$ ]] || fatal "SERVICE_USER 格式不正确：${SERVICE_USER}"
  [[ "$SERVICE_GROUP" =~ ^[A-Za-z_][A-Za-z0-9_-]*[$]?$ ]] || fatal "SERVICE_GROUP 格式不正确：${SERVICE_GROUP}"
}

install_dependencies() {
  local missing=()
  command_exists curl || missing+=(curl)
  command_exists unzip || missing+=(unzip)

  if (( ${#missing[@]} == 0 )); then
    return
  fi

  info "正在安装依赖：${missing[*]}"
  if command_exists apt-get; then
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y curl unzip ca-certificates
  elif command_exists dnf; then
    dnf install -y curl unzip ca-certificates
  elif command_exists yum; then
    yum install -y curl unzip ca-certificates
  elif command_exists zypper; then
    zypper --non-interactive install curl unzip ca-certificates
  elif command_exists pacman; then
    pacman -Sy --noconfirm curl unzip ca-certificates
  else
    fatal "未识别到支持的软件包管理器，请先手动安装 curl、unzip 和 ca-certificates。"
  fi
}

detect_architecture() {
  local machine
  machine="$(uname -m)"
  case "$machine" in
    x86_64|amd64)
      RELEASE_ARCH="amd64"
      ;;
    aarch64|arm64)
      RELEASE_ARCH="arm64"
      ;;
    *)
      fatal "暂不支持系统架构：${machine}。当前发布仅提供 amd64 和 arm64。"
      ;;
  esac
  info "检测到系统架构：${machine}，将使用 ${RELEASE_ARCH} 发布包。"
}

resolve_release() {
  local api_endpoint api_response latest_url asset_name requested_version
  asset_name="go-bot-linux-${RELEASE_ARCH}.zip"
  requested_version="$RELEASE_REQUEST"

  if [[ "$requested_version" == "latest" ]]; then
    api_endpoint="${GITHUB_API_URL}/releases/latest"
  else
    [[ "$requested_version" == v* ]] || requested_version="v${requested_version}"
    api_endpoint="${GITHUB_API_URL}/releases/tags/${requested_version}"
  fi

  api_response="$(curl -fsSL --retry 3 --connect-timeout 10 \
    -H 'Accept: application/vnd.github+json' \
    -H 'User-Agent: tgbot-chatgo-installer' \
    "$api_endpoint" 2>/dev/null || true)"

  if [[ -n "$api_response" ]]; then
    RESOLVED_VERSION="$(printf '%s' "$api_response" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
    DOWNLOAD_URL="$(printf '%s' "$api_response" | sed -n "s#.*\"browser_download_url\"[[:space:]]*:[[:space:]]*\"\([^\"]*/${asset_name}\)\".*#\1#p" | head -n 1)"

    [[ -n "$RESOLVED_VERSION" ]] || fatal "GitHub Release 数据中没有版本号。"
    [[ -n "$DOWNLOAD_URL" ]] || fatal "${RESOLVED_VERSION} 没有适用于 ${RELEASE_ARCH} 的发布资产：${asset_name}"
    info "已匹配发布版本：${RESOLVED_VERSION}"
    info "已匹配下载资产：${asset_name}"
    return
  fi

  warn "GitHub API 获取失败，将通过发布页确定版本并使用标准资产命名规则。"
  if [[ "$requested_version" == "latest" ]]; then
    latest_url="$(curl -fsSL --retry 3 --connect-timeout 10 -o /dev/null -w '%{url_effective}' \
      "${GITHUB_REPO_URL}/releases/latest")"
    RESOLVED_VERSION="${latest_url##*/}"
  else
    RESOLVED_VERSION="$requested_version"
  fi

  [[ -n "$RESOLVED_VERSION" && "$RESOLVED_VERSION" != "latest" ]] \
    || fatal "无法获取最新版本，请检查网络或通过 VERSION=vX.Y.Z 指定版本。"
  DOWNLOAD_URL="${GITHUB_REPO_URL}/releases/download/${RESOLVED_VERSION}/${asset_name}"
}

download_release() {
  local asset_name archive_file extracted_binary extracted_env_example
  asset_name="go-bot-linux-${RELEASE_ARCH}.zip"
  BINARY_ASSET="go-bot-linux-${RELEASE_ARCH}"
  archive_file="${TEMP_DIR}/${asset_name}"

  info "正在下载 ${RESOLVED_VERSION}：${asset_name}"
  curl -fL --retry 3 --retry-delay 2 --connect-timeout 15 \
    --progress-bar "$DOWNLOAD_URL" -o "$archive_file" \
    || fatal "下载失败：${DOWNLOAD_URL}"

  unzip -q "$archive_file" -d "${TEMP_DIR}/release" || fatal "安装包解压失败。"
  extracted_binary="$(find "${TEMP_DIR}/release" -type f -name "$BINARY_ASSET" -print -quit)"
  extracted_env_example="$(find "${TEMP_DIR}/release" -type f -name '.env.example' -print -quit)"
  [[ -n "$extracted_binary" ]] || fatal "安装包中未找到可执行文件：${BINARY_ASSET}"
  [[ -n "$extracted_env_example" ]] || fatal "安装包中未找到 .env.example，无法生成带注释的配置文件。"
  DOWNLOADED_BINARY="$extracted_binary"
  DOWNLOADED_ENV_EXAMPLE="$extracted_env_example"
}

prepare_release() {
  cleanup_temp
  install_dependencies
  detect_architecture
  TEMP_DIR="$(mktemp -d)"
  resolve_release
  download_release
}

prompt_text() {
  local variable_name="$1" prompt_message="$2" default_value="${3-}" input_value
  while true; do
    if [[ -n "$default_value" ]]; then
      printf "%s [%s]: " "$prompt_message" "$default_value"
    else
      printf "%s: " "$prompt_message"
    fi
    IFS= read -r input_value <&9
    input_value="${input_value:-$default_value}"
    if [[ -n "$input_value" ]]; then
      printf -v "$variable_name" '%s' "$input_value"
      return
    fi
    warn "此项不能为空，请重新输入。"
  done
}

prompt_secret() {
  local variable_name="$1" prompt_message="$2" input_value
  while true; do
    printf "%s: " "$prompt_message"
    IFS= read -r -s input_value <&9
    printf '\n'
    if [[ -n "$input_value" ]]; then
      printf -v "$variable_name" '%s' "$input_value"
      return
    fi
    warn "此项不能为空，请重新输入。"
  done
}

prompt_yes_no() {
  local variable_name="$1" prompt_message="$2" default_answer="$3" answer hint
  if [[ "$default_answer" == "TRUE" ]]; then
    hint="Y/n"
  else
    hint="y/N"
  fi

  while true; do
    printf "%s [%s]: " "$prompt_message" "$hint"
    IFS= read -r answer <&9
    case "${answer:-$default_answer}" in
      y|Y|yes|YES|Yes|TRUE|true)
        printf -v "$variable_name" '%s' "TRUE"
        return
        ;;
      n|N|no|NO|No|FALSE|false)
        printf -v "$variable_name" '%s' "FALSE"
        return
        ;;
      *)
        warn "请输入 y 或 n。"
        ;;
    esac
  done
}

prompt_integer() {
  local variable_name="$1" prompt_message="$2" default_value="$3" minimum="$4" maximum="$5" value
  while true; do
    prompt_text value "$prompt_message" "$default_value"
    if [[ "$value" =~ ^[0-9]+$ ]] && (( value >= minimum && value <= maximum )); then
      printf -v "$variable_name" '%s' "$value"
      return
    fi
    warn "请输入 ${minimum} 到 ${maximum} 之间的整数。"
  done
}

validate_single_line() {
  local label="$1" value="$2"
  [[ "$value" != *$'\n'* && "$value" != *$'\r'* ]] || fatal "${label} 不能包含换行符。"
}

env_value() {
  local value="$1"
  validate_single_line "配置值" "$value"
  if [[ "$value" != *'"'* ]]; then
    printf '"%s"' "$value"
  elif [[ "$value" != *"'"* ]]; then
    printf "'%s'" "$value"
  else
    fatal "配置值不能同时包含英文单引号和双引号，请修改后重试。"
  fi
}

read_template_value() {
  local key="$1" fallback="$2" line value
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == "${key}="* ]]; then
      value="${line#*=}"
      if (( ${#value} >= 2 )); then
        if [[ "${value:0:1}" == '"' && "${value: -1}" == '"' ]] \
          || [[ "${value:0:1}" == "'" && "${value: -1}" == "'" ]]; then
          value="${value:1:${#value}-2}"
        fi
      fi
      printf '%s' "$value"
      return
    fi
  done <"$DOWNLOADED_ENV_EXAMPLE"
  printf '%s' "$fallback"
}

read_template_bool() {
  local key="$1" fallback="$2" value
  value="$(read_template_value "$key" "$fallback")"
  case "${value^^}" in
    TRUE|FALSE) printf '%s' "${value^^}" ;;
    *) printf '%s' "$fallback" ;;
  esac
}

read_template_integer() {
  local key="$1" fallback="$2" value
  value="$(read_template_value "$key" "$fallback")"
  if [[ "$value" =~ ^[0-9]+$ ]]; then
    printf '%s' "$value"
  else
    printf '%s' "$fallback"
  fi
}

collect_configuration() {
  local default_app_name default_disable_verification default_message_interval
  local default_forward_ack default_topic_ban default_clear_messages
  default_app_name="$(read_template_value APP_NAME 'baibai-bot')"
  default_disable_verification="$(read_template_bool DISABLE_VERIFICATION FALSE)"
  default_message_interval="$(read_template_integer MESSAGE_INTERVAL 0)"
  default_forward_ack="$(read_template_bool USER_FORWARD_ACK TRUE)"
  default_topic_ban="$(read_template_bool DELETE_TOPIC_AS_FOREVER_BAN FALSE)"
  default_clear_messages="$(read_template_bool DELETE_USER_MESSAGE_ON_CLEAR_CMD TRUE)"
  printf '\n'
  info "开始配置 Telegram 机器人。ID 获取说明：${GITHUB_REPO_URL}/blob/main/README2.md"
  printf '  1. 使用 @BotFather 创建机器人并取得 Bot Token。\n'
  printf '  2. 创建启用了“话题”的 Telegram 超级群。\n'
  printf '  3. 将机器人设为群管理员，并授予管理话题等必要权限。\n'
  printf '  4. 准备超级群 ID（通常以 -100 开头）和管理员个人数字 ID。\n\n'

  while true; do
    prompt_secret BOT_TOKEN "请输入 Bot Token（输入内容不会回显，例如 123456789:AA...）"
    if [[ "$BOT_TOKEN" =~ ^[0-9]+:[A-Za-z0-9_-]+$ ]]; then
      break
    fi
    warn "Bot Token 格式看起来不正确，应类似：123456789:AAAbbb_ccc"
  done

  while true; do
    prompt_text ADMIN_GROUP_ID "请输入已开启话题的管理超级群 ID（通常以 -100 开头）"
    if [[ "$ADMIN_GROUP_ID" =~ ^-[0-9]+$ ]]; then
      [[ "$ADMIN_GROUP_ID" == -100* ]] || warn "该 ID 未以 -100 开头，请确认它确实是超级群 ID。"
      break
    fi
    warn "群组 ID 必须是负整数，例如：-1001234567890"
  done

  while true; do
    prompt_text ADMIN_USER_IDS "请输入管理员用户 ID；多人请用英文逗号分隔（例如 123456789,987654321）"
    ADMIN_USER_IDS="${ADMIN_USER_IDS//[[:space:]]/}"
    if [[ "$ADMIN_USER_IDS" =~ ^[0-9]+(,[0-9]+)*$ ]]; then
      break
    fi
    warn "管理员 ID 必须是正整数，多人之间使用英文逗号。"
  done

  prompt_text BOT_APP_NAME "请输入应用名称" "$default_app_name"
  prompt_yes_no DISABLE_VERIFICATION "是否关闭加减乘除安全验证？" "$default_disable_verification"
  prompt_integer MESSAGE_INTERVAL "用户连续发送消息的最小间隔（秒，0 表示不限制）" "$default_message_interval" "0" "86400"
  prompt_yes_no USER_FORWARD_ACK "成功转发后，是否向用户发送「已转达客服」的回执？" "$default_forward_ack"
  prompt_yes_no DELETE_TOPIC_AS_FOREVER_BAN "管理员删除用户话题后，是否永久禁止该用户自动新建话题？" "$default_topic_ban"
  prompt_yes_no DELETE_USER_MESSAGE_ON_CLEAR_CMD "执行 /clear 后，是否同时删除用户私聊中的已映射消息？" "$default_clear_messages"
  validate_single_line "应用名称" "$BOT_APP_NAME"
}

create_service_account() {
  if ! getent group "$SERVICE_GROUP" >/dev/null 2>&1; then
    groupadd --system "$SERVICE_GROUP"
  fi
  if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    useradd --system --gid "$SERVICE_GROUP" --home-dir "$INSTALL_DIR" \
      --shell "$(command -v nologin || printf '/usr/sbin/nologin')" "$SERVICE_USER"
  fi
}

remove_legacy_env_entry() {
  local legacy_env="${INSTALL_DIR}/env"
  if [[ -L "$legacy_env" ]]; then
    if [[ "$(readlink "$legacy_env")" == ".env" ]]; then
      rm -f -- "$legacy_env"
      info "已清理旧版脚本遗留的 env -> .env 链接。"
    else
      warn "发现非本脚本创建的符号链接 ${legacy_env}，为安全起见未删除。"
    fi
  elif [[ -e "$legacy_env" ]]; then
    warn "发现 ${legacy_env}，程序实际只读取 .env，因此未修改该文件。"
  fi
}

ensure_install_directories() {
  [[ ! -L "$INSTALL_DIR" ]] || fatal "安装目录不能是符号链接：${INSTALL_DIR}"
  [[ ! -L "${INSTALL_DIR}/data" ]] || fatal "数据库目录不能是符号链接：${INSTALL_DIR}/data"
  create_service_account
  install -d -m 0755 -o root -g root "$INSTALL_DIR"
  remove_legacy_env_entry
  install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_GROUP" "${INSTALL_DIR}/data"
  chown -R "$SERVICE_USER:$SERVICE_GROUP" "${INSTALL_DIR}/data"
}

replace_env_value() {
  local env_file="$1" key="$2" replacement="$3" temp_file line found=0
  temp_file="$(mktemp "${TEMP_DIR}/env-update.XXXXXX")"
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == "${key}="* ]]; then
      printf '%s=%s\n' "$key" "$replacement"
      found=1
    else
      printf '%s\n' "$line"
    fi
  done <"$env_file" >"$temp_file"

  if (( found == 0 )); then
    printf '\n%s=%s\n' "$key" "$replacement" >>"$temp_file"
  fi
  cat "$temp_file" >"$env_file"
  rm -f -- "$temp_file"
}

install_template_reference() {
  local template_file="${INSTALL_DIR}/.env.example"
  [[ ! -L "$template_file" ]] || fatal "配置模板不能是符号链接：${template_file}"
  install -m 0644 -o root -g root "$DOWNLOADED_ENV_EXAMPLE" "$template_file"
}

write_environment_file() {
  local env_file="${INSTALL_DIR}/.env"
  [[ ! -L "$env_file" ]] || fatal "配置文件不能是符号链接：${env_file}"

  install_template_reference
  install -m 0640 -o root -g "$SERVICE_GROUP" "$DOWNLOADED_ENV_EXAMPLE" "$env_file"
  replace_env_value "$env_file" APP_NAME "$(env_value "$BOT_APP_NAME")"
  replace_env_value "$env_file" BOT_TOKEN "$(env_value "$BOT_TOKEN")"
  replace_env_value "$env_file" ADMIN_GROUP_ID "$ADMIN_GROUP_ID"
  replace_env_value "$env_file" ADMIN_USER_IDS "$ADMIN_USER_IDS"
  replace_env_value "$env_file" DELETE_TOPIC_AS_FOREVER_BAN "$DELETE_TOPIC_AS_FOREVER_BAN"
  replace_env_value "$env_file" DELETE_USER_MESSAGE_ON_CLEAR_CMD "$DELETE_USER_MESSAGE_ON_CLEAR_CMD"
  replace_env_value "$env_file" DISABLE_VERIFICATION "$DISABLE_VERIFICATION"
  replace_env_value "$env_file" MESSAGE_INTERVAL "$MESSAGE_INTERVAL"
  replace_env_value "$env_file" USER_FORWARD_ACK "$USER_FORWARD_ACK"
  chmod 640 "$env_file"
  chown root:"$SERVICE_GROUP" "$env_file"
}

preserve_environment_permissions() {
  [[ ! -L "${INSTALL_DIR}/.env" ]] || fatal "配置文件不能是符号链接：${INSTALL_DIR}/.env"
  [[ -f "${INSTALL_DIR}/.env" ]] || fatal "缺少配置文件：${INSTALL_DIR}/.env"
  chmod 640 "${INSTALL_DIR}/.env"
  chown root:"$SERVICE_GROUP" "${INSTALL_DIR}/.env"
}

install_binary() {
  [[ ! -L "${INSTALL_DIR}/go-bot" ]] || fatal "程序文件不能是符号链接：${INSTALL_DIR}/go-bot"
  install -m 0755 -o root -g root "$DOWNLOADED_BINARY" "${INSTALL_DIR}/go-bot"
}

write_version_file() {
  [[ ! -L "$VERSION_FILE" ]] || fatal "版本文件不能是符号链接：${VERSION_FILE}"
  printf '%s\n' "$RESOLVED_VERSION" >"$VERSION_FILE"
  chmod 644 "$VERSION_FILE"
  chown root:root "$VERSION_FILE"
}

write_systemd_unit() {
  [[ ! -L "$SYSTEMD_UNIT" ]] || fatal "systemd 服务文件不能是符号链接：${SYSTEMD_UNIT}"
  cat >"$SYSTEMD_UNIT" <<EOF
[Unit]
Description=Telegram Customer Service Relay Bot
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_GROUP}
WorkingDirectory=${INSTALL_DIR}
ExecStart=${INSTALL_DIR}/go-bot
Restart=on-failure
RestartSec=5s
TimeoutStopSec=30s
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=full
ReadWritePaths=${INSTALL_DIR}/data

[Install]
WantedBy=multi-user.target
EOF
  chmod 644 "$SYSTEMD_UNIT"
  systemctl daemon-reload
  systemctl enable "$SERVICE_NAME" >/dev/null
}

stop_service_if_running() {
  if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
    info "正在停止 ${SERVICE_NAME} 服务。"
    systemctl stop "$SERVICE_NAME"
  fi
}

start_and_check_service() {
  info "正在启动 ${SERVICE_NAME} 服务。"
  if ! systemctl restart "$SERVICE_NAME"; then
    warn "systemd 无法启动服务。"
    systemctl status "$SERVICE_NAME" --no-pager -l || true
    journalctl -u "$SERVICE_NAME" -n 30 --no-pager || true
    return 1
  fi

  sleep 5
  if systemctl is-active --quiet "$SERVICE_NAME"; then
    success "${SERVICE_NAME} 已成功启动。"
    return 0
  fi

  warn "服务未能保持运行，通常是 Token、群 ID、话题功能或机器人权限配置有误。"
  systemctl status "$SERVICE_NAME" --no-pager -l || true
  journalctl -u "$SERVICE_NAME" -n 30 --no-pager || true
  return 1
}

installation_exists() {
  [[ -d "$INSTALL_DIR" || -x "${INSTALL_DIR}/go-bot" || -f "${INSTALL_DIR}/.env" || -f "$SYSTEMD_UNIT" ]]
}

installation_is_usable() {
  [[ -x "${INSTALL_DIR}/go-bot" && -f "${INSTALL_DIR}/.env" ]]
}

read_installed_version() {
  if [[ -s "$VERSION_FILE" ]]; then
    head -n 1 "$VERSION_FILE"
  else
    printf '未知（旧安装未记录版本）'
  fi
}

show_installation_status() {
  local installed_version service_status
  if installation_is_usable; then
    installed_version="$(read_installed_version)"
    if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
      service_status="运行中"
    else
      service_status="未运行"
    fi
    printf '当前状态：已安装，版本 %s，服务%s\n' "$installed_version" "$service_status"
  elif installation_exists; then
    printf '当前状态：检测到不完整安装，建议选择 3 重新安装\n'
  else
    printf '当前状态：尚未安装\n'
  fi
}

print_management_help() {
  cat <<EOF

常用管理命令：
  查看状态：systemctl status ${SERVICE_NAME}
  重启服务：systemctl restart ${SERVICE_NAME}
  暂停服务：systemctl stop ${SERVICE_NAME}
  启动服务：systemctl start ${SERVICE_NAME}
  查看日志：journalctl -u ${SERVICE_NAME} -f

配置文件：${INSTALL_DIR}/.env
修改配置后执行：systemctl restart ${SERVICE_NAME}
EOF
}

install_application() {
  if installation_exists; then
    warn "已检测到安装内容。首次安装不会覆盖现有文件，请选择 2 更新或 3 重新安装。"
    return
  fi

  prepare_release
  collect_configuration

  printf '\n即将首次安装：\n'
  printf '  版本：%s (%s)\n' "$RESOLVED_VERSION" "$RELEASE_ARCH"
  printf '  目录：%s\n' "$INSTALL_DIR"
  printf '  服务：%s\n' "$SERVICE_NAME"
  prompt_yes_no CONFIRM_INSTALL "确认开始安装？" "TRUE"
  if [[ "$CONFIRM_INSTALL" != "TRUE" ]]; then
    warn "已取消安装。"
    cleanup_temp
    return
  fi

  ensure_install_directories
  install_binary
  write_environment_file
  write_version_file
  write_systemd_unit

  if ! start_and_check_service; then
    warn "程序文件已经安装，但服务启动失败。可选择 3 重新安装并重新填写配置。"
  else
    success "首次安装完成：${RESOLVED_VERSION}"
    print_management_help
  fi
  cleanup_temp
}

update_application() {
  local installed_version
  if ! installation_is_usable; then
    warn "未检测到可更新的完整安装。请先选择 1 安装，或选择 3 修复不完整安装。"
    return
  fi

  installed_version="$(read_installed_version)"
  prepare_release

  printf '\n更新检查：\n'
  printf '  当前版本：%s\n' "$installed_version"
  printf '  目标版本：%s\n' "$RESOLVED_VERSION"
  if [[ "$installed_version" == "$RESOLVED_VERSION" ]]; then
    info "当前已经是目标版本，无需更新。如需强制覆盖程序，请选择 3 重新安装。"
    cleanup_temp
    return
  fi

  prompt_yes_no CONFIRM_UPDATE "确认更新程序？配置和数据库将保持不变" "TRUE"
  if [[ "$CONFIRM_UPDATE" != "TRUE" ]]; then
    warn "已取消更新。"
    cleanup_temp
    return
  fi

  stop_service_if_running
  ensure_install_directories
  install_template_reference
  preserve_environment_permissions
  install_binary
  write_version_file
  write_systemd_unit

  if ! start_and_check_service; then
    warn "程序已更新到 ${RESOLVED_VERSION}，但服务启动失败，请查看上方日志。"
  else
    success "更新完成：${installed_version} -> ${RESOLVED_VERSION}"
    print_management_help
  fi
  cleanup_temp
}

reinstall_application() {
  local had_existing_install="FALSE"
  if installation_exists; then
    had_existing_install="TRUE"
  fi

  prepare_release
  collect_configuration
  if [[ "$had_existing_install" == "TRUE" && -d "${INSTALL_DIR}/data" ]]; then
    prompt_yes_no KEEP_DATABASE "是否保留现有 SQLite 数据库？" "TRUE"
  else
    KEEP_DATABASE="FALSE"
  fi

  printf '\n即将重新安装：\n'
  printf '  目标版本：%s (%s)\n' "$RESOLVED_VERSION" "$RELEASE_ARCH"
  printf '  重写配置：是\n'
  if [[ "$KEEP_DATABASE" == "TRUE" ]]; then
    printf '  保留数据库：是\n'
  else
    printf '  保留数据库：否，现有数据将被删除\n'
  fi
  prompt_yes_no CONFIRM_REINSTALL "确认开始重新安装？" "FALSE"
  if [[ "$CONFIRM_REINSTALL" != "TRUE" ]]; then
    warn "已取消重新安装。"
    cleanup_temp
    return
  fi

  stop_service_if_running
  if [[ "$KEEP_DATABASE" != "TRUE" && -d "${INSTALL_DIR}/data" ]]; then
    validate_uninstall_path
    rm -rf -- "${INSTALL_DIR}/data"
  fi

  ensure_install_directories
  install_binary
  write_environment_file
  write_version_file
  write_systemd_unit

  if ! start_and_check_service; then
    warn "重新安装已完成，但服务启动失败。请核对新填写的 Token、群 ID 和机器人权限。"
  else
    success "重新安装完成：${RESOLVED_VERSION}"
    print_management_help
  fi
  cleanup_temp
}

validate_uninstall_path() {
  [[ "$INSTALL_DIR" == /* ]] || fatal "卸载目录必须是绝对路径：${INSTALL_DIR}"
  case "$INSTALL_DIR" in
    ""|/|/bin|/boot|/dev|/etc|/home|/lib|/lib64|/opt|/root|/run|/sbin|/srv|/tmp|/usr|/var)
      fatal "拒绝删除不安全的目录：${INSTALL_DIR}"
      ;;
  esac
}

remove_service_account() {
  local passwd_entry account_home account_shell
  passwd_entry="$(getent passwd "$SERVICE_USER" 2>/dev/null || true)"
  [[ -n "$passwd_entry" ]] || return

  account_home="$(printf '%s' "$passwd_entry" | cut -d: -f6)"
  account_shell="$(printf '%s' "$passwd_entry" | cut -d: -f7)"
  if [[ "$account_home" == "$INSTALL_DIR" && "$account_shell" == *nologin ]]; then
    userdel "$SERVICE_USER" 2>/dev/null || warn "未能删除系统用户 ${SERVICE_USER}，可稍后手动处理。"
    if getent group "$SERVICE_GROUP" >/dev/null 2>&1; then
      groupdel "$SERVICE_GROUP" 2>/dev/null || true
    fi
  else
    warn "未删除系统用户 ${SERVICE_USER}：它不像本脚本创建的专用服务账户。"
  fi
}

uninstall_application() {
  if ! installation_exists && ! id "$SERVICE_USER" >/dev/null 2>&1; then
    warn "未发现 ${SERVICE_NAME} 的安装内容，无需卸载。"
    return
  fi

  validate_uninstall_path
  printf '\n即将完整卸载：\n'
  printf '  systemd 服务：%s\n' "$SERVICE_NAME"
  printf '  安装目录：%s\n' "$INSTALL_DIR"
  warn "该操作会永久删除 .env 配置和 SQLite 数据库，无法恢复。"
  prompt_yes_no CONFIRM_UNINSTALL "确认继续卸载？" "FALSE"
  if [[ "$CONFIRM_UNINSTALL" != "TRUE" ]]; then
    warn "已取消卸载。"
    return
  fi

  info "正在停止并禁用 ${SERVICE_NAME} 服务。"
  systemctl stop "$SERVICE_NAME" 2>/dev/null || true
  systemctl disable "$SERVICE_NAME" 2>/dev/null || true
  rm -f -- "$SYSTEMD_UNIT"
  systemctl daemon-reload
  systemctl reset-failed "$SERVICE_NAME" 2>/dev/null || true

  rm -rf -- "$INSTALL_DIR"
  remove_service_account
  success "${SERVICE_NAME} 已完整卸载。"
}

show_menu() {
  printf '\n'
  printf '========================================\n'
  printf ' tgbot-chatgo Linux 管理脚本\n'
  printf '========================================\n'
  show_installation_status
  printf '\n'
  printf '  1. 首次安装\n'
  printf '  2. 更新程序（保留配置和数据库）\n'
  printf '  3. 重新安装（重新生成配置）\n'
  printf '  4. 完整卸载\n'
  printf '  0. 退出脚本\n'
  printf '\n'
}

wait_for_enter() {
  printf '\n按 Enter 返回主菜单...'
  IFS= read -r _ <&9
}

main() {
  local menu_choice
  require_root
  validate_settings
  command_exists systemctl || fatal "当前系统没有 systemctl；此脚本仅适用于使用 systemd 的 Linux 发行版。"
  open_input_stream

  if (( $# > 0 )); then
    warn "此版本不需要命令参数，将直接进入交互菜单。"
  fi

  while true; do
    show_menu
    printf '请输入选项 [0-4]: '
    IFS= read -r menu_choice <&9
    case "$menu_choice" in
      1)
        install_application
        wait_for_enter
        ;;
      2)
        update_application
        wait_for_enter
        ;;
      3)
        reinstall_application
        wait_for_enter
        ;;
      4)
        uninstall_application
        wait_for_enter
        ;;
      0)
        success "已退出脚本。"
        break
        ;;
      *)
        warn "无效选项，请输入 0、1、2、3 或 4。"
        ;;
    esac
  done
}

main "$@"