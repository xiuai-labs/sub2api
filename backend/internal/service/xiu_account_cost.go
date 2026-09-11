package service

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// 全历史起点。usage_logs 不可能有 1970 年之前的行，所以它等价于「不设下界」，
// 又比 time.Time{}（公元 1 年）对驱动与索引都友好。
var xiuAllTimeStart = time.Unix(0, 0).UTC()

// GetXiuTotalCostBatch 批量取「这些号从头到现在一共烧了多少」。
//
// 上游两条现成的路都答不了这句话：GetTodayStatsBatch 把起点钉死在 timezone.Today()，
// GetStats 那条 handler 把 days 夹在 1..90（超了还静默退回 30）。而 xiu-pool 的
// sub2api 页每一行要答的第一个问题就是「这号值不值钱」，那是个不带窗口的问题。
//
// 🔴 这里不新写 SQL —— 复用同一条 GetAccountWindowStatsBatch，只把窗口起点放到 1970。
// 金额口径因此与上游的「今日消耗」逐字相同（AccountStats.Cost = 账号口径，
// SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1))），
// 两个数才比得起来。
//
// 三步（去重 → 批量 SQL → 失败退回逐号）与 GetTodayStatsBatch 是同一套。上游没把
// 「起点可给」这一层抽出来，而 fork 的纪律是不改上游文件（见仓库根 PATCHES.md），
// 于是这里重走一遍。上游哪天抽出来了，这个文件整份删掉。
func (s *AccountUsageService) GetXiuTotalCostBatch(ctx context.Context, accountIDs []int64) (map[int64]*WindowStats, error) {
	uniqueIDs := make([]int64, 0, len(accountIDs))
	seen := make(map[int64]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			continue
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		uniqueIDs = append(uniqueIDs, accountID)
	}

	result := make(map[int64]*WindowStats, len(uniqueIDs))
	if len(uniqueIDs) == 0 {
		return result, nil
	}

	if batchReader, ok := s.usageLogRepo.(accountWindowStatsBatchReader); ok {
		statsByAccount, err := batchReader.GetAccountWindowStatsBatch(ctx, uniqueIDs, xiuAllTimeStart)
		if err == nil {
			for _, accountID := range uniqueIDs {
				result[accountID] = windowStatsFromAccountStats(statsByAccount[accountID])
			}
			return result, nil
		}
	}

	// 退回逐号。并发上限照 GetTodayStatsBatch 取 8：这条路比那条贵得多（全历史扫），
	// 放宽只会把对方的连接池顶满
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for _, accountID := range uniqueIDs {
		id := accountID
		g.Go(func() error {
			stats, err := s.usageLogRepo.GetAccountWindowStats(gctx, id, xiuAllTimeStart)
			if err != nil {
				return nil
			}
			mu.Lock()
			result[id] = windowStatsFromAccountStats(stats)
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()

	// 一个号都没花过钱时 SQL 不回它那一行。补成零值而不是留空：
	// 调用方要的是「这一批每个号各是多少」，缺键会被读成「没查到」
	for _, accountID := range uniqueIDs {
		if _, ok := result[accountID]; !ok {
			result[accountID] = &WindowStats{}
		}
	}
	return result, nil
}
