package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/lib/pq"
)

// 五个聚合列，逐行照抄上游 GetAccountWindowStatsBatch（usage_log_repo_stats.go）；
// 归档触发器（migrations/xiu_001_account_cost_totals.sql）里是同一份。
// 🔴 口径只能跟着上游走：「今日消耗」与「历史总消耗」要比得起来。三处对不上时
// xiu_account_cost_repo_test.go 会在 rebase 后第一次跑单测时拦下。
const xiuAccountCostAggregates = `
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as tokens,
			COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) as cost,
			COALESCE(SUM(total_cost), 0) as standard_cost,
			COALESCE(SUM(actual_cost), 0) as user_cost`

// 🔴 **归档与现存行必须出自同一个快照**，所以是一条语句：分开读的话，
// 中间提交的一批删除会让那几行在两边都算到（或两边都漏掉）。
const xiuAccountCostReadSQL = `
	WITH live AS (
		SELECT account_id,` + xiuAccountCostAggregates + `
		FROM usage_logs
		WHERE account_id = ANY($1)
		GROUP BY account_id
	)
	SELECT
		ids.account_id,
		COALESCE(t.requests, 0) + COALESCE(l.requests, 0),
		COALESCE(t.tokens, 0) + COALESCE(l.tokens, 0),
		COALESCE(t.cost, 0) + COALESCE(l.cost, 0),
		COALESCE(t.standard_cost, 0) + COALESCE(l.standard_cost, 0),
		COALESCE(t.user_cost, 0) + COALESCE(l.user_cost, 0)
	FROM unnest($1::bigint[]) AS ids(account_id)
	LEFT JOIN xiu_account_cost_totals t ON t.account_id = ids.account_id
	LEFT JOIN live l ON l.account_id = ids.account_id
`

// GetXiuAccountTotalCostBatch 批量取这些号的历史总消耗（已删日志的归档 + 现存日志）。
// 每个传入的号都有一条，没花过钱的是零值。
func (r *usageLogRepository) GetXiuAccountTotalCostBatch(ctx context.Context, accountIDs []int64) (map[int64]*usagestats.AccountStats, error) {
	result := make(map[int64]*usagestats.AccountStats, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}

	rows, err := r.sql.QueryContext(ctx, xiuAccountCostReadSQL, pq.Array(accountIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var accountID int64
		stats := &usagestats.AccountStats{}
		if err := rows.Scan(&accountID, &stats.Requests, &stats.Tokens, &stats.Cost, &stats.StandardCost, &stats.UserCost); err != nil {
			return nil, err
		}
		result[accountID] = stats
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
