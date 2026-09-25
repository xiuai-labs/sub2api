package admin

// xiu fork：用量列表的降智标记（见 service/xiu_usage_downgrade.go 与仓库根 PATCHES.md）。
// 前端拿到一页用量之后，带着这一页每行的 (id, api_key_id, request_id) 来查；只返回被标记的那几条。

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const xiuUsageDowngradeMaxRows = 500

// XiuListDowngrades handles POST /api/v1/admin/usage/xiu-downgrades
func (h *UsageHandler) XiuListDowngrades(c *gin.Context) {
	var req struct {
		Rows []service.XiuUsageDowngradeQuery `json:"rows"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	if len(req.Rows) > xiuUsageDowngradeMaxRows {
		response.BadRequest(c, "Too many rows")
		return
	}
	reports, err := h.apiKeyService.XiuListUsageDowngrades(c.Request.Context(), req.Rows)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, reports)
}
