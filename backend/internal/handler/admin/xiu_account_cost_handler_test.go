package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"
)

type xiuCostRepoProbe struct {
	service.UsageLogRepository
	calls   atomic.Int32
	mu      sync.Mutex
	gotIDs  [][]int64
	ctxErrs []error
	release chan struct{} // 非 nil 时查询挂住，直到被关闭
	err     error
}

func (r *xiuCostRepoProbe) GetXiuAccountTotalCostBatch(ctx context.Context, accountIDs []int64) (map[int64]*usagestats.AccountStats, error) {
	r.calls.Add(1)
	r.mu.Lock()
	r.gotIDs = append(r.gotIDs, accountIDs)
	r.ctxErrs = append(r.ctxErrs, ctx.Err())
	r.mu.Unlock()
	if r.release != nil {
		<-r.release
	}
	if r.err != nil {
		return nil, r.err
	}
	rows := make(map[int64]*usagestats.AccountStats, len(accountIDs))
	for _, id := range accountIDs {
		rows[id] = &usagestats.AccountStats{Cost: float64(id) * 10}
	}
	return rows, nil
}

type xiuCostResponse struct {
	Code int `json:"code"`
	Data struct {
		Stats      map[string]service.WindowStats `json:"stats"`
		ComputedAt time.Time                      `json:"computed_at"`
	} `json:"data"`
}

func resetXiuTotalCostCacheForTest() {
	accountXiuTotalCostCache = newSnapshotCache(xiuTotalCostTTL)
	accountXiuTotalCostFlight = singleflight.Group{}
}

func newXiuCostRouter(t *testing.T, repo *xiuCostRepoProbe) *gin.Engine {
	t.Helper()
	resetXiuTotalCostCacheForTest()
	t.Cleanup(resetXiuTotalCostCacheForTest)
	gin.SetMode(gin.TestMode)
	svc := service.NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler := &AccountHandler{accountUsageService: svc}
	router := gin.New()
	router.POST("/cost", handler.GetXiuBatchTotalCost)
	return router
}

func postXiuCost(router *gin.Engine, ctx context.Context, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/cost", bytes.NewBufferString(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeXiuCost(t *testing.T, rec *httptest.ResponseRecorder) xiuCostResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out xiuCostResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestXiuBatchTotalCost_MissThenHit(t *testing.T) {
	repo := &xiuCostRepoProbe{}
	router := newXiuCostRouter(t, repo)

	first := postXiuCost(router, context.Background(), `{"account_ids":[2,1,2]}`)
	got := decodeXiuCost(t, first)
	require.Equal(t, "0/2", first.Header().Get("X-Xiu-Cost-Cache"))
	require.Equal(t, 10.0, got.Data.Stats["1"].Cost)
	require.Equal(t, 20.0, got.Data.Stats["2"].Cost)

	second := postXiuCost(router, context.Background(), `{"account_ids":[1,2]}`)
	decodeXiuCost(t, second)
	require.Equal(t, "2/2", second.Header().Get("X-Xiu-Cost-Cache"))
	require.EqualValues(t, 1, repo.calls.Load())
}

// 按号做键：池子里新加一个号，只让它自己那一条现算
func TestXiuBatchTotalCost_PartialHitQueriesOnlyMissing(t *testing.T) {
	repo := &xiuCostRepoProbe{}
	router := newXiuCostRouter(t, repo)

	decodeXiuCost(t, postXiuCost(router, context.Background(), `{"account_ids":[1]}`))
	rec := postXiuCost(router, context.Background(), `{"account_ids":[1,2]}`)
	decodeXiuCost(t, rec)

	require.Equal(t, "1/2", rec.Header().Get("X-Xiu-Cost-Cache"))
	require.Equal(t, [][]int64{{1}, {2}}, repo.gotIDs)
}

func TestXiuBatchTotalCost_ComputedAtIsOldestEntry(t *testing.T) {
	repo := &xiuCostRepoProbe{}
	router := newXiuCostRouter(t, repo)

	old := time.Now().Add(-4 * time.Minute).UTC().Truncate(time.Second)
	accountXiuTotalCostCache.items[xiuTotalCostCacheKey(1)] = snapshotCacheEntry{
		Payload:   &service.WindowStats{Cost: 9},
		ExpiresAt: old.Add(xiuTotalCostTTL),
	}

	got := decodeXiuCost(t, postXiuCost(router, context.Background(), `{"account_ids":[1,2]}`))

	require.True(t, got.Data.ComputedAt.Equal(old), "computed_at=%s want %s", got.Data.ComputedAt, old)
	require.Equal(t, 9.0, got.Data.Stats["1"].Cost)
}

// 失败不进缓存：否则一次超时会让这批号「没花钱」五分钟
func TestXiuBatchTotalCost_ErrorIsNotCached(t *testing.T) {
	repo := &xiuCostRepoProbe{err: errors.New("statement timeout")}
	router := newXiuCostRouter(t, repo)

	rec := postXiuCost(router, context.Background(), `{"account_ids":[1]}`)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	repo.err = nil
	decodeXiuCost(t, postXiuCost(router, context.Background(), `{"account_ids":[1]}`))
	require.EqualValues(t, 2, repo.calls.Load())
}

func TestXiuBatchTotalCost_ConcurrentMissesShareOneQuery(t *testing.T) {
	repo := &xiuCostRepoProbe{release: make(chan struct{})}
	router := newXiuCostRouter(t, repo)

	const callers = 5
	var wg sync.WaitGroup
	codes := make([]int, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = postXiuCost(router, context.Background(), `{"account_ids":[1,2]}`).Code
		}()
	}
	// 给其余请求留出挂进同一个 flight 的时间；首个查询在 release 之前一直挂着
	time.Sleep(200 * time.Millisecond)
	close(repo.release)
	wg.Wait()

	require.EqualValues(t, 1, repo.calls.Load())
	for _, code := range codes {
		require.Equal(t, http.StatusOK, code)
	}
}

// 调用方断开不许把全历史 SUM 一起取消：算完照样进缓存，下一轮直接命中
func TestXiuBatchTotalCost_LoadSurvivesClientCancel(t *testing.T) {
	repo := &xiuCostRepoProbe{}
	router := newXiuCostRouter(t, repo)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	postXiuCost(router, ctx, `{"account_ids":[1]}`)

	require.Equal(t, []error{nil}, repo.ctxErrs)
	rec := postXiuCost(router, context.Background(), `{"account_ids":[1]}`)
	decodeXiuCost(t, rec)
	require.Equal(t, "1/1", rec.Header().Get("X-Xiu-Cost-Cache"))
}

func TestXiuBatchTotalCost_EmptyListSkipsQuery(t *testing.T) {
	repo := &xiuCostRepoProbe{}
	router := newXiuCostRouter(t, repo)

	got := decodeXiuCost(t, postXiuCost(router, context.Background(), `{"account_ids":[0,-1]}`))

	require.Empty(t, got.Data.Stats)
	require.EqualValues(t, 0, repo.calls.Load())
}

func TestXiuBatchTotalCost_RejectsBadBody(t *testing.T) {
	router := newXiuCostRouter(t, &xiuCostRepoProbe{})

	rec := postXiuCost(router, context.Background(), `{`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}
