-- xiu fork：账号历史总消耗的归档（见仓库根 PATCHES.md「账号历史总消耗的批量接口」）。
--
-- usage_logs 只留 90 天，老行被删之后「这号一共烧了多少」会悄悄变少。
-- 这里在删除那一刻把被删的行按号累加进归档，读数 = 归档 + 现存行。
--
-- 文件名不带数字编号：runner 按文件名排序、跳过已应用的，xiu_ 排在所有数字之后，
-- 上游以后新增的迁移照样会被应用；xiu_ 基名也让 check-touchpoints 不把它算作上游改动。

-- 🔴 挂在 DELETE 上，**只对 DELETE 生效**：分区表的保留期清理走 DROP 分区
-- （dashboard_aggregation_repo.go 的 dropUsageLogsPartitions），不触发任何触发器，
-- 老行会绕过归档直接消失。上游只在表「已经是分区表」时才走那条路，这里当场拦下。
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_partitioned_table pt
        JOIN pg_class c ON c.oid = pt.partrelid
        WHERE c.relname = 'usage_logs'
    ) THEN
        RAISE EXCEPTION 'xiu_account_cost_totals: usage_logs is partitioned, DROP PARTITION would bypass the archive trigger';
    END IF;
END $$;

-- 🔴 不挂 accounts 外键：删号会级联删 usage_logs，归档得比日志活得久。
CREATE TABLE IF NOT EXISTS xiu_account_cost_totals (
    account_id    BIGINT PRIMARY KEY,
    requests      BIGINT NOT NULL DEFAULT 0,
    tokens        BIGINT NOT NULL DEFAULT 0,
    cost          DECIMAL(30, 10) NOT NULL DEFAULT 0,
    standard_cost DECIMAL(30, 10) NOT NULL DEFAULT 0,
    user_cost     DECIMAL(30, 10) NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE xiu_account_cost_totals IS 'xiu: 每个账号已被删除的 usage_logs 的累计用量，只增不减。';

-- 聚合列逐行照抄上游 GetAccountWindowStatsBatch，与读数 SQL 同口径（由 xiu_account_cost_repo_test.go 核对）。
--
-- 🔴 语句级 + 转换表：保留期清理一批删几千行，逐行触发就是几千次 upsert。
-- 🔴 ORDER BY account_id：两个并发的删除事务按同一顺序给归档行上锁，不会互相死锁。
-- 累加与删除同在一个事务，删除回滚归档跟着回滚 —— 日志与归档之间不存在重复或遗漏的瞬间。
CREATE OR REPLACE FUNCTION xiu_archive_deleted_usage_logs()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO xiu_account_cost_totals AS t (account_id, requests, tokens, cost, standard_cost, user_cost)
    SELECT
        account_id,
        COUNT(*) as requests,
        COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as tokens,
        COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) as cost,
        COALESCE(SUM(total_cost), 0) as standard_cost,
        COALESCE(SUM(actual_cost), 0) as user_cost
    FROM deleted_usage_logs
    GROUP BY account_id
    ORDER BY account_id
    ON CONFLICT (account_id) DO UPDATE SET
        requests      = t.requests + EXCLUDED.requests,
        tokens        = t.tokens + EXCLUDED.tokens,
        cost          = t.cost + EXCLUDED.cost,
        standard_cost = t.standard_cost + EXCLUDED.standard_cost,
        user_cost     = t.user_cost + EXCLUDED.user_cost,
        updated_at    = NOW();
    RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS xiu_usage_logs_archive_cost ON usage_logs;
CREATE TRIGGER xiu_usage_logs_archive_cost
AFTER DELETE ON usage_logs
REFERENCING OLD TABLE AS deleted_usage_logs
FOR EACH STATEMENT
EXECUTE FUNCTION xiu_archive_deleted_usage_logs();
