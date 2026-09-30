package claude

import "strings"

// xiuStrictSamplingFamilies：对非默认 temperature / top_p / top_k 一律 400 的型号
// （Opus 4.7 起的新一代；2026-10-08 线上实测 opus-5 / opus-5-5 / sonnet-5-5 / haiku-5-5）。
// 与 new-api relaykit 的 strictSampling 是同一批。
var xiuStrictSamplingFamilies = []string{
	"claude-fable-5", "claude-mythos-5", "claude-mythos-preview",
	"claude-opus-5", "claude-sonnet-5", "claude-haiku-5",
	"claude-opus-4-8", "claude-opus-4-7",
}

// XiuIsStrictSamplingModel 判断型号是否不收采样参数。
// 走与 effort 目录同一个归一化（Bedrock / Vertex 前缀、-thinking、日期后缀、点号写法），
// 免得同一个型号换个写法就漏判。
func XiuIsStrictSamplingModel(model string) bool {
	id := normalizeEffortModelID(model)
	for _, family := range xiuStrictSamplingFamilies {
		if strings.HasPrefix(id, family) {
			return true
		}
	}
	return false
}
