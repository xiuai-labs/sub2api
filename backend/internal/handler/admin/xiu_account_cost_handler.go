package admin

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
)

// 🔴 比上游快照缓存（30 秒）长一个量级：现存日志的 SUM 要顺着 idx(account_id, created_at)
// 扫完整个保留期，而「这号一共烧了多少」半小时不变也改变不了任何决定。
// 调用方每 30 秒对账一次，没有这一层等于每 30 秒把保留期内的日志扫一遍。
const xiuTotalCostTTL = 5 * time.Minute

// 查询脱离了请求 context（见 loadXiuTotalCost），总得有个上限，免得挂死的查询永远占着连接
const xiuTotalCostLoadTimeout = time.Minute

var (
	accountXiuTotalCostCache = newSnapshotCache(xiuTotalCostTTL)
	// 上一轮没算完下一轮又到、多个标签页同时开着 —— 同一批缺口只查一次
	accountXiuTotalCostFlight singleflight.Group
)

// 🔴 键是单个号，不是整批（上游今日统计是整批一个键）。加号删号是调用方的日常，
// 按批做键会让整批在刚导入号、页面正盯着看的时候一起落空，落空一次就是一整轮全历史 SUM。
func xiuTotalCostCacheKey(accountID int64) string {
	return "accounts_xiu_total_cost:" + strconv.FormatInt(accountID, 10)
}

// GetXiuBatchTotalCost 批量取这些号的历史总消耗。
// POST /api/v1/admin/accounts/xiu-total-cost/batch
//
// 路径带 xiu- 前缀：fork 自己加的面，撞不上上游将来的路由（见仓库根 PATCHES.md）。
func (h *AccountHandler) GetXiuBatchTotalCost(c *gin.Context) {
	var req BatchTodayStatsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	accountIDs := normalizeInt64IDList(req.AccountIDs)

	/*
	 * 🔴 **`computed_at` 取这一批里最旧的那条的计算时刻**（= 过期时刻 - TTL）。
	 * 混合命中时每个号的年纪不一样，协议只给整批一个时刻；报最新的等于拿刚算出来的号
	 * 替五分钟前的号背书。读它的人（xiu-pool 的「看到于」）宁可保守。
	 */
	stats := make(map[int64]*service.WindowStats, len(accountIDs))
	missing := make([]int64, 0, len(accountIDs))
	computedAt := time.Now()
	for _, accountID := range accountIDs {
		entry, ok := accountXiuTotalCostCache.Get(xiuTotalCostCacheKey(accountID))
		cached, isStats := entry.Payload.(*service.WindowStats)
		if !ok || !isStats {
			missing = append(missing, accountID)
			continue
		}
		stats[accountID] = cached
		if at := entry.ExpiresAt.Add(-xiuTotalCostTTL); at.Before(computedAt) {
			computedAt = at
		}
	}

	if len(missing) > 0 {
		fresh, err := h.loadXiuTotalCost(c.Request.Context(), missing)
		if err != nil {
			response.ErrorFrom(c, fmt.Errorf("load xiu total cost for %d accounts: %w", len(missing), err))
			return
		}
		for accountID, one := range fresh {
			stats[accountID] = one
		}
	}

	// 命中数/总数，排查「这一口为什么慢」时只看这一行。不复用上游的 X-Snapshot-Cache：
	// 那个头的值是 hit/miss，同名不同格式会误导人
	c.Header("X-Xiu-Cost-Cache", strconv.Itoa(len(accountIDs)-len(missing))+"/"+strconv.Itoa(len(accountIDs)))
	response.Success(c, gin.H{"stats": stats, "computed_at": computedAt.UTC()})
}

// 🔴 查询**不跟着请求取消**：SUM 算到一半被调用方超时掐掉的话，算力白烧、也进不了缓存，
// 下一轮从零再来 —— 慢查询因此永远填不满缓存。脱离之后算完照样落缓存，下一轮直接命中。
//
// 返回的 map 被同一个 flight 的所有等待者共享，只读。
func (h *AccountHandler) loadXiuTotalCost(ctx context.Context, accountIDs []int64) (map[int64]*service.WindowStats, error) {
	// accountIDs 已由 normalizeInt64IDList 排过序，同一批缺口必然拼出同一个键
	value, err, _ := accountXiuTotalCostFlight.Do(fmt.Sprint(accountIDs), func() (any, error) {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), xiuTotalCostLoadTimeout)
		defer cancel()
		fresh, err := h.accountUsageService.GetXiuTotalCostBatch(loadCtx, accountIDs)
		if err != nil {
			return nil, err
		}
		for accountID, one := range fresh {
			accountXiuTotalCostCache.Set(xiuTotalCostCacheKey(accountID), one)
		}
		return fresh, nil
	})
	if err != nil {
		return nil, err
	}
	fresh, ok := value.(map[int64]*service.WindowStats)
	if !ok {
		return nil, fmt.Errorf("unexpected xiu total cost flight result %T", value)
	}
	return fresh, nil
}
