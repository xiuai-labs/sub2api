package service

import "github.com/tidwall/gjson"

// xiuDropTopPAfterTemperatureFill 在 OAuth 伪装补 temperature=1 之后调用：body 里有 top_p 就删掉。
// 4.5 代起 temperature 与 top_p 二选一，补了 temperature 再留着 top_p 就是上游 400
// "`temperature` and `top_p` cannot both be specified for this model"（2026-10-12 客户反馈，见 PATCHES.md）。
func xiuDropTopPAfterTemperatureFill(body []byte, modified bool) ([]byte, bool) {
	if !gjson.GetBytes(body, "top_p").Exists() {
		return body, modified
	}
	if next, ok := deleteJSONPathBytes(body, "top_p"); ok {
		return next, true
	}
	return body, modified
}
