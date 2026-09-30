package service

// 上游 HTTP 200 后在流里发 event:error 时，按 error.type 还原 Anthropic 对应的状态码。
//
// 上游对流内 error 一律记 403（只有「未输出 + overloaded」改 529），handler 再翻成
// "Upstream access forbidden"：过载、内容过滤都被说成号被封，客户端也认不出 overloaded 去退避。
// 线上数据见 PATCHES.md「流内 error 不再一律 403」。
//
// 只改状态码：换不换号 handler 按「写没写过字节」判，与状态码无关；
// 529 冷却仍只在上游原有的「未输出」分支触发 —— 流中途的错误不算账号的错。
//
// 挂载点：gateway_forward.go 里 semanticStatus 的初值一行。

import (
	"net/http"

	"github.com/tidwall/gjson"
)

// 映射照 Anthropic 文档的 HTTP 错误表：https://docs.anthropic.com/en/api/errors
var xiuSSEErrorTypeStatus = map[string]int{
	"invalid_request_error": http.StatusBadRequest,
	"authentication_error":  http.StatusUnauthorized,
	"billing_error":         http.StatusPaymentRequired,
	"permission_error":      http.StatusForbidden,
	"not_found_error":       http.StatusNotFound,
	"request_too_large":     http.StatusRequestEntityTooLarge,
	"rate_limit_error":      http.StatusTooManyRequests,
	"api_error":             http.StatusInternalServerError,
	"overloaded_error":      529,
}

// 认不出的类型给 502：它确实是上游的错，但不该冒充权限问题。
func xiuSSEErrorStatus(body []byte) int {
	if status, ok := xiuSSEErrorTypeStatus[gjson.GetBytes(body, "error.type").String()]; ok {
		return status
	}
	return http.StatusBadGateway
}
