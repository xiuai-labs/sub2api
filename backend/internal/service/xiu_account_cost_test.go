package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type xiuCostBatchRepoStub struct {
	UsageLogRepository
	rows     map[int64]*usagestats.AccountStats
	err      error
	gotIDs   []int64
	gotStart time.Time
}

func (r *xiuCostBatchRepoStub) GetAccountWindowStatsBatch(_ context.Context, accountIDs []int64, startTime time.Time) (map[int64]*usagestats.AccountStats, error) {
	r.gotIDs = accountIDs
	r.gotStart = startTime
	return r.rows, r.err
}

func TestGetXiuTotalCostBatch_FillsZeroForAccountsWithoutRows(t *testing.T) {
	repo := &xiuCostBatchRepoStub{rows: map[int64]*usagestats.AccountStats{
		1: {Requests: 3, Cost: 1.5},
	}}
	svc := &AccountUsageService{usageLogRepo: repo}

	got, err := svc.GetXiuTotalCostBatch(context.Background(), []int64{1, 2})

	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, repo.gotIDs)
	require.True(t, repo.gotStart.Equal(time.Unix(0, 0)))
	require.Equal(t, &WindowStats{Requests: 3, Cost: 1.5}, got[1])
	require.Equal(t, &WindowStats{}, got[2])
}

// 出错必须往上报，不能补成零：零会被 handler 缓存五分钟，调用方看到的是「这号没花钱」
func TestGetXiuTotalCostBatch_PropagatesQueryError(t *testing.T) {
	repo := &xiuCostBatchRepoStub{err: errors.New("statement timeout")}
	svc := &AccountUsageService{usageLogRepo: repo}

	got, err := svc.GetXiuTotalCostBatch(context.Background(), []int64{1})

	require.ErrorContains(t, err, "statement timeout")
	require.Nil(t, got)
}

func TestGetXiuTotalCostBatch_RejectsRepoWithoutBatchReader(t *testing.T) {
	svc := &AccountUsageService{usageLogRepo: &usageBatchLogRepoStub{}}

	got, err := svc.GetXiuTotalCostBatch(context.Background(), []int64{1})

	require.Error(t, err)
	require.Nil(t, got)
}
