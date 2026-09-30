-- xiu fork：撤掉「账号历史总消耗」（见仓库根 PATCHES.md「已撤」）。
--
-- xiu_001 已经在线上应用过，文件不能改，所以另起一条把它建的东西拆掉。
-- 顺序是触发器 → 函数 → 表：触发器引用着函数，先删函数会被依赖挡住。
--
-- 🔴 归档表里「超过保留期的累计」随之消失，这是定过的：xiu-pool 改为自己按天存官方日账。
DROP TRIGGER IF EXISTS xiu_usage_logs_archive_cost ON usage_logs;
DROP FUNCTION IF EXISTS xiu_archive_deleted_usage_logs();
DROP TABLE IF EXISTS xiu_account_cost_totals;
