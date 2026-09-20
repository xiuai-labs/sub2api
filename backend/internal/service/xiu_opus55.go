package service

import "strings"

// isClaudeOpus55Model 识别 Opus 5.5 的各种拼写。
//
// 它的名字包含 "opus-5"，而它比 Opus 5 便宜（$4/$20 vs $5/$25）：
// 按 "opus-5" 子串归系列的**计价**查找必须先过这一关，否则静默多收 25%。
// 能力判断（fast mode、effort 档位）与 Opus 5 相同，照常继承即可。
//
// 边界判断同 isClaudeFable51Model：marker 后不能紧跟数字，免得误吞 opus-5-50 之类。
func isClaudeOpus55Model(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, marker := range []string{"opus-5-5", "opus-5.5", "opus5.5", "opus55"} {
		if at := strings.Index(model, marker); at >= 0 {
			after := at + len(marker)
			if after == len(model) || model[after] < '0' || model[after] > '9' {
				return true
			}
		}
	}
	return false
}
