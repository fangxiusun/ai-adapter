package util

import "unicode/utf8"

const (
	// maskPlaceholder 是固定宽度的遮罩标记。
	// 固定宽度可避免通过遮罩长度反推真实密钥长度。
	maskPlaceholder = "***"

	// minLenToReveal 是开始露出字符的最小长度，低于此值整体遮蔽。
	minLenToReveal = 12

	// maxVisiblePerSide 是首尾每侧最多露出的 rune 数（硬上限）。
	// 即使密钥极长，也不会露出更多，限制重构风险。
	maxVisiblePerSide = 3

	// maxRevealRatio 限制露出字符占总长的比例上限。
	maxRevealRatio = 0.33
)

// MaskKey 将 API 密钥转换为可安全写入日志的形式。
// 规则：
//   - 空串返回空串。
//   - 长度不足 minLenToReveal 时整体遮蔽为 "***"。
//   - 否则露出首尾各若干字符，露出量同时受占比和硬上限约束，
//     中间以固定宽度 "***" 替换。
func MaskKey(key string) string {
	n := utf8.RuneCountInString(key)
	if n == 0 {
		return ""
	}

	// 按占比算出可露出的总量，再折半到每侧，并夹在硬上限内。
	perSide := int(float64(n)*maxRevealRatio) / 2
	if perSide > maxVisiblePerSide {
		perSide = maxVisiblePerSide
	}

	// 太短或不足以安全露出时，整体遮蔽。
	if n < minLenToReveal || perSide < 1 {
		return maskPlaceholder
	}

	runes := []rune(key)
	return string(runes[:perSide]) + maskPlaceholder + string(runes[n-perSide:])
}
