//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 每个用例一个回滚事务，归档表先清空：它是全表共享的，别的用例删过日志就会留下累计值。
func newXiuCostTestRepo(t *testing.T) (*usageLogRepository, *sql.Tx) {
	t.Helper()
	tx := testTx(t)
	_, err := tx.ExecContext(context.Background(), `DELETE FROM xiu_account_cost_totals`)
	require.NoError(t, err)
	return newUsageLogRepositoryWithSQL(nil, tx), tx
}

// 插入时临时切到 replica 跳过外键 —— 为几条日志造齐 users/api_keys/accounts 不值当。
// 插完必须切回 origin：replica 同样会关掉被测的归档触发器。
func insertXiuCostLog(t *testing.T, tx *sql.Tx, userID, accountID int64, totalCost, actualCost float64, statsCost, multiplier *float64, createdAt time.Time) {
	t.Helper()
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `SET LOCAL session_replication_role = replica`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO usage_logs (user_id, api_key_id, account_id, model, input_tokens, output_tokens,
			cache_creation_tokens, cache_read_tokens, total_cost, actual_cost,
			account_stats_cost, account_rate_multiplier, created_at)
		VALUES ($1, 1, $2, 'm', 1, 2, 3, 4, $3, $4, $5, $6, $7)
	`, userID, accountID, totalCost, actualCost, statsCost, multiplier, createdAt)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SET LOCAL session_replication_role = origin`)
	require.NoError(t, err)
}

func ptrFloat(v float64) *float64 { return &v }

var (
	xiuDay1 = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	xiuDay2 = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	xiuDay3 = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
)

// 这条补丁存在的理由：日志被删了，总额不能跟着少。金额口径与上游 GetAccountWindowStatsBatch 逐字相同
func TestXiuAccountCost_TotalSurvivesUsageLogDeletion(t *testing.T) {
	ctx := context.Background()
	repo, tx := newXiuCostTestRepo(t)
	insertXiuCostLog(t, tx, 1, 1, 1.0, 0.5, nil, nil, xiuDay1)
	insertXiuCostLog(t, tx, 1, 1, 2.0, 1.0, ptrFloat(3.0), ptrFloat(2.0), xiuDay2)
	insertXiuCostLog(t, tx, 1, 2, 4.0, 2.0, nil, ptrFloat(0.5), xiuDay3)

	want, err := repo.GetAccountWindowStatsBatch(ctx, []int64{1, 2, 3}, time.Unix(0, 0))
	require.NoError(t, err)
	before, err := repo.GetXiuAccountTotalCostBatch(ctx, []int64{1, 2, 3})
	require.NoError(t, err)
	require.Equal(t, want, before)

	// 两条语句删：保留期清理是分批删的，第二批得累加在第一批之上，而不是覆盖
	_, err = tx.ExecContext(ctx, `DELETE FROM usage_logs WHERE created_at < $1`, xiuDay2)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `DELETE FROM usage_logs WHERE created_at < $1`, xiuDay3.Add(time.Hour))
	require.NoError(t, err)

	after, err := repo.GetXiuAccountTotalCostBatch(ctx, []int64{1, 2, 3})
	require.NoError(t, err)
	require.Equal(t, want, after)
	require.Equal(t, &usagestats.AccountStats{Requests: 2, Tokens: 20, Cost: 7, StandardCost: 3, UserCost: 1.5}, after[1])
	require.Equal(t, &usagestats.AccountStats{}, after[3], "没花过钱的号补零值，不缺键")
}

// 删用户 / 删号走外键级联删日志，语句级触发器照样得接住
func TestXiuAccountCost_CascadedDeleteIsArchived(t *testing.T) {
	ctx := context.Background()
	user := mustCreateUser(t, testEntClient(t), &service.User{})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID)
	})
	repo, tx := newXiuCostTestRepo(t)
	insertXiuCostLog(t, tx, user.ID, 7, 5.0, 5.0, nil, nil, xiuDay1)

	_, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, user.ID)
	require.NoError(t, err)

	var remaining int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs WHERE account_id = 7`).Scan(&remaining))
	require.Zero(t, remaining, "级联没删掉日志，这条用例就测不到触发器")
	got, err := repo.GetXiuAccountTotalCostBatch(ctx, []int64{7})
	require.NoError(t, err)
	require.Equal(t, 5.0, got[7].Cost)
}

// 删除回滚，归档跟着回滚：累加与删除在同一个事务里，不会出现「日志还在、归档已加」的重复计数
func TestXiuAccountCost_RolledBackDeleteIsNotArchived(t *testing.T) {
	ctx := context.Background()
	repo, tx := newXiuCostTestRepo(t)
	insertXiuCostLog(t, tx, 1, 1, 1.0, 1.0, nil, nil, xiuDay1)

	_, err := tx.ExecContext(ctx, `SAVEPOINT before_delete`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `DELETE FROM usage_logs WHERE account_id = 1`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT before_delete`)
	require.NoError(t, err)

	got, err := repo.GetXiuAccountTotalCostBatch(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, 1.0, got[1].Cost)
}

func TestXiuAccountCost_EmptyIDsSkipQuery(t *testing.T) {
	repo, _ := newXiuCostTestRepo(t)

	got, err := repo.GetXiuAccountTotalCostBatch(context.Background(), nil)

	require.NoError(t, err)
	require.Empty(t, got)
}
