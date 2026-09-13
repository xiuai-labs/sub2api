package service

import (
	"context"
	"errors"
	"time"
)

// 起点放到 1970 = 不设下界。但「全历史」实际只到 usage_logs 的保留期为止
// （dashboard_aggregation.retention.usage_logs_days，默认 90 天）—— 这条接口
// 回的是「保留期内一共烧了多少」，要更长就调保留期，不是改这里。
var xiuAllTimeStart = time.Unix(0, 0).UTC()

// GetXiuTotalCostBatch 批量取这些号的历史总消耗。accountIDs 须已去重（handler 做过）。
//
// 🔴 不新写 SQL，复用上游「今日消耗」那条 GetAccountWindowStatsBatch，只换起点 ——
// 金额口径因此逐字相同，两个数才比得起来。
//
// 🔴 **不照抄上游的逐号退回。** 那条路把单号错误吞成零值，而 handler 会把零缓存五分钟，
// 调用方看到的就是「这号没花钱」。况且批量 SQL 失败最可能是全历史扫太贵，
// 这时再并发扫 N 遍只会更糟。错就往上报。
func (s *AccountUsageService) GetXiuTotalCostBatch(ctx context.Context, accountIDs []int64) (map[int64]*WindowStats, error) {
	batchReader, ok := s.usageLogRepo.(accountWindowStatsBatchReader)
	if !ok {
		return nil, errors.New("usage log repository does not support batch window stats")
	}
	statsByAccount, err := batchReader.GetAccountWindowStatsBatch(ctx, accountIDs, xiuAllTimeStart)
	if err != nil {
		return nil, err
	}
	// 没花过钱的号 SQL 不回它那一行，windowStatsFromAccountStats(nil) 补成零值：
	// 调用方要的是每个号各是多少，缺键会被读成「没查到」
	result := make(map[int64]*WindowStats, len(accountIDs))
	for _, accountID := range accountIDs {
		result[accountID] = windowStatsFromAccountStats(statsByAccount[accountID])
	}
	return result, nil
}
