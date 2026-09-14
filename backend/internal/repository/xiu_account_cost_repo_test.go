package repository

import (
	"os"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// 口径漂移的哨兵：同一套聚合列住在三处 —— 上游 GetAccountWindowStatsBatch、读数 SQL、归档触发器。
// 拦的是 rebase 之后上游改了「今日消耗」的算法，而归档还按旧口径在累加 —— 两个数从此比不起来，且不报错。
func TestXiuAccountCostAggregates_MatchUpstreamAndTrigger(t *testing.T) {
	upstream, err := os.ReadFile("usage_log_repo_stats.go")
	require.NoError(t, err)
	// 迁移应用后内容就冻结了；口径要变得另写一条迁移 CREATE OR REPLACE 触发器函数，这里改指新文件
	trigger, err := migrations.FS.ReadFile("xiu_001_account_cost_totals.sql")
	require.NoError(t, err)

	for _, line := range strings.Split(xiuAccountCostAggregates, "\n") {
		line = strings.TrimSuffix(strings.TrimSpace(line), ",")
		if line == "" {
			continue
		}
		require.Contains(t, string(upstream), line, "上游 GetAccountWindowStatsBatch 的口径变了，同步读数 SQL 与归档触发器，并想清楚已归档的数怎么办")
		require.Contains(t, string(trigger), line, "归档触发器与读数 SQL 的口径不一致")
	}
}
