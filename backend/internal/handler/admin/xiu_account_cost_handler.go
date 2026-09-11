package admin

import (
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// 🔴 TTL 比上游那几张快照缓存（30 秒）长一个量级，判据是这条查询的代价与这个数的变速：
// 全历史 SUM 每个号都要顺着 idx(account_id, created_at) 扫到底，比「今日」贵得多；
// 而「这号一共烧了多少」半小时不变也改变不了任何决定。
// 调用方（xiu-pool 的 sub2api 页）每 30 秒对账一次，没有这一层的话等于每 30 秒全表扫一遍。
var accountXiuTotalCostCache = newSnapshotCache(5 * time.Minute)

// 一个号的那一条。**连它算于什么时候一起存** —— 缓存命中时要回的是当初算出来那一刻，
// 而 snapshotCache 自己不往外说条目的年纪。
type xiuTotalCostEntry struct {
	Stats      *service.WindowStats
	ComputedAt time.Time
}

// 🔴 **键是单个账号，不是这一批。** 上游那张今日统计缓存把整批 id 拼成一个键
// （`buildAccountTodayStatsBatchCacheKey`），那条查询便宜、TTL 只有 30 秒，代价小；
// 这一条不行：**池子里加一个号、删一个号，整批的键就全变了**，而加号删号正是调用方
// 那个系统每天在做的事。那样这层缓存会在最该起作用的时候（刚导入一批号、页面正盯着看）
// 整批落空，而落空一次就是一整轮全历史 SUM。按号存之后，一个号的变动只让它自己那一条过期。
//
// 顺带也不必操心 id 的顺序 —— 按批做键时还要先排序才不会被分页顺序抖出无谓的 miss。
func xiuTotalCostCacheKey(accountID int64) string {
	return "accounts_xiu_total_cost:" + strconv.FormatInt(accountID, 10)
}

// XiuBatchTotalCostRequest 与上游 BatchTodayStatsRequest 同形，刻意不复用它：
// 那是上游文件里的类型，跟着它改签名等于给自己加一条 rebase 冲突。
type XiuBatchTotalCostRequest struct {
	AccountIDs []int64 `json:"account_ids" binding:"required"`
}

// GetXiuBatchTotalCost 批量取这些号的历史总消耗。
// POST /api/v1/admin/accounts/xiu-total-cost/batch
//
// 路径带 xiu- 前缀与文件名同一个理由：这是 fork 自己加的面，撞不上上游将来的任何路由
// （见仓库根 PATCHES.md）。
func (h *AccountHandler) GetXiuBatchTotalCost(c *gin.Context) {
	var req XiuBatchTotalCostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	accountIDs := normalizeInt64IDList(req.AccountIDs)
	if len(accountIDs) == 0 {
		response.Success(c, gin.H{"stats": map[string]any{}, "computed_at": time.Now().UTC()})
		return
	}

	/*
	 * 🔴 **`computed_at` 取这一批里最旧的那个。** 混合命中时每个号的数年纪不一样，
	 * 而协议只给整批一个时刻 —— 报最新的那个等于拿刚算出来的号替五分钟前的号背书。
	 * 读它的人（xiu-pool 的「看到于」那一格）宁可保守，也不能被骗。
	 */
	stats := make(map[int64]*service.WindowStats, len(accountIDs))
	missing := make([]int64, 0, len(accountIDs))
	oldest := time.Now().UTC()
	for _, accountID := range accountIDs {
		entry, ok := accountXiuTotalCostCache.Get(xiuTotalCostCacheKey(accountID))
		cached, typed := entry.Payload.(xiuTotalCostEntry)
		if !ok || !typed {
			missing = append(missing, accountID)
			continue
		}
		stats[accountID] = cached.Stats
		if cached.ComputedAt.Before(oldest) {
			oldest = cached.ComputedAt
		}
	}

	if len(missing) > 0 {
		fresh, err := h.accountUsageService.GetXiuTotalCostBatch(c.Request.Context(), missing)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		computedAt := time.Now().UTC()
		for accountID, one := range fresh {
			stats[accountID] = one
			accountXiuTotalCostCache.Set(
				xiuTotalCostCacheKey(accountID),
				xiuTotalCostEntry{Stats: one, ComputedAt: computedAt},
			)
		}
	}

	// 现算了几个 / 直接拿缓存的几个，排查「这一口为什么慢」时只看这一行就够
	c.Header("X-Snapshot-Cache", strconv.Itoa(len(accountIDs)-len(missing))+"/"+strconv.Itoa(len(accountIDs)))
	response.Success(c, gin.H{"stats": stats, "computed_at": oldest})
}
