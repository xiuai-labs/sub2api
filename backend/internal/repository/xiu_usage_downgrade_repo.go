package repository

// xiu fork：用量记录的降智标记存在 Redis（见 service/xiu_usage_downgrade.go 与仓库根 PATCHES.md）。
// 把已有缓存里的 Redis 客户端借给 service 侧类型断言取用，不动上游的接口与装配。

import "github.com/redis/go-redis/v9"

func (c *gatewayCache) XiuRedis() *redis.Client { return c.rdb }

func (c *apiKeyCache) XiuRedis() *redis.Client { return c.rdb }
