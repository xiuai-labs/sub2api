package service

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

/*
 * 🔴 **「历史总消耗」= 已删日志的归档 + 现存日志。** usage_logs 只留 90 天
 * （dashboard_aggregation.retention.usage_logs_days），直接 SUM 的话老号的总额过了保留期就悄悄变少。
 *
 * 归档由 usage_logs 上的 AFTER DELETE 触发器在删除的同一事务里累加
 * （migrations/xiu_001_account_cost_totals.sql），不挂上游清理代码的钩子：
 * 保留期清理、管理员手动清、删号级联一律接住，只增不减。
 */
type xiuAccountCostStore interface {
	GetXiuAccountTotalCostBatch(ctx context.Context, accountIDs []int64) (map[int64]*usagestats.AccountStats, error)
}

// GetXiuTotalCostBatch 批量取这些号的历史总消耗。accountIDs 须已去重（handler 做过）。
//
// 🔴 **不照抄上游的逐号退回。** 那条路把单号错误吞成零值，而 handler 会把零缓存五分钟，
// 调用方看到的就是「这号没花钱」。错就往上报。
func (s *AccountUsageService) GetXiuTotalCostBatch(ctx context.Context, accountIDs []int64) (map[int64]*WindowStats, error) {
	store, ok := s.usageLogRepo.(xiuAccountCostStore)
	if !ok {
		return nil, errors.New("usage log repository does not support xiu account cost")
	}
	statsByAccount, err := store.GetXiuAccountTotalCostBatch(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	// windowStatsFromAccountStats(nil) 补零值：调用方要的是每个号各是多少，缺键会被读成「没查到」
	result := make(map[int64]*WindowStats, len(accountIDs))
	for _, accountID := range accountIDs {
		result[accountID] = windowStatsFromAccountStats(statsByAccount[accountID])
	}
	return result, nil
}
