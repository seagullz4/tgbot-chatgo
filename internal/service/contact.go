package service

import "strings"

const directContactUnavailableNotice = "⚠️ 该用户无法直接联系（受 Telegram 隐私设置限制）。"

func IsDirectContactPrivacyRestricted(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "BUTTON_USER_PRIVACY_RESTRICTED")
}

func WithDirectContactUnavailableNotice(text string) string {
	if strings.Contains(text, directContactUnavailableNotice) {
		return text
	}
	return strings.TrimRight(text, "\n") + "\n\n" + directContactUnavailableNotice
}
