package conversationsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/samber/hot"
)

const (
	DefaultTTL      = time.Hour
	defaultCapacity = 65536
	prefixNamespace = "new-api:conversation_session:v1:{prefixes}:"
)

type CacheConfig struct {
	Redis        *redis.Client
	RedisEnabled func() bool
	TTL          time.Duration
	Capacity     int
	Now          func() time.Time
}

type prefixNode struct {
	ID        string
	HasChild  bool
	ExpiresAt time.Time
}

// Cache maintains a bounded prefix trie (represented by rolling hashes) and
// response-ID aliases. Redis resolves and extends a path atomically; a local LRU
// mirrors visited paths so a Redis outage degrades without rejecting relay traffic.
// Errors are returned alongside usable fallback identities for the caller to log.
// No message text, credentials, or user IDs are stored in keys or values.
type Cache struct {
	config       CacheConfig
	mu           sync.Mutex
	prefixes     *hot.HotCache[string, prefixNode]
	aliases      *cachex.HybridCache[string]
	localAliases *hot.HotCache[string, string]
}

func NewCache(config CacheConfig) *Cache {
	if config.TTL <= 0 {
		config.TTL = DefaultTTL
	}
	if config.Capacity <= 0 {
		config.Capacity = defaultCapacity
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Cache{
		config:   config,
		prefixes: hot.NewHotCache[string, prefixNode](hot.LRU, config.Capacity).Build(),
		aliases: cachex.NewHybridCache[string](cachex.HybridCacheConfig[string]{
			Namespace:    "new-api:conversation_session:v1:responses",
			Redis:        config.Redis,
			RedisEnabled: config.RedisEnabled,
			RedisCodec:   cachex.StringCodec{},
			Memory: func() *hot.HotCache[string, string] {
				return hot.NewHotCache[string, string](hot.LRU, config.Capacity).WithTTL(config.TTL).Build()
			},
		}),
		localAliases: hot.NewHotCache[string, string](hot.LRU, config.Capacity).WithTTL(config.TTL).Build(),
	}
}

// Each prefix has its original session and a bit recording whether a continuation
// has ever been seen. Extending a leaf continues the session; diverging from an
// internal node starts a branch. Exact/shortened histories keep their own identity.
// All keys share a hash tag. The expiry-aware LRU ledger bounds Redis prefix keys
// globally, not only in each conversation or each process.
var matchPrefixes = redis.NewScript(`
local count = #KEYS - 1
local ttl = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local score = tonumber(ARGV[3])
local sid = ARGV[4]
for i = count, 1, -1 do
    local node = redis.call('GET', KEYS[i + 1])
    if node then
        sid = string.sub(node, 1, -3)
        if i < count and string.sub(node, -1) == '1' then
            sid = ARGV[i + 4]
        end
        break
    end
end
local result = {sid}
for i = 1, count do
    local node = redis.call('GET', KEYS[i + 1])
    if not node then node = sid .. '|0' end
    if i < count then node = string.sub(node, 1, -2) .. '1' end
    redis.call('SET', KEYS[i + 1], node, 'PX', ttl)
    redis.call('ZADD', KEYS[1], score, KEYS[i + 1])
    result[#result + 1] = node
end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', score - ttl)
local excess = redis.call('ZCARD', KEYS[1]) - capacity
if excess > 0 then
    local victims = redis.call('ZRANGE', KEYS[1], 0, excess - 1)
    for _, key in ipairs(victims) do
        redis.call('DEL', key)
        redis.call('ZREM', KEYS[1], key)
    end
end
redis.call('PEXPIRE', KEYS[1], ttl)
return result
`)

func Digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (c *Cache) Match(ctx context.Context, scope string, prefixes []string) (string, error) {
	if scope == "" || len(prefixes) == 0 || len(prefixes) > MaxTurns {
		return uuid.NewString(), nil
	}
	keys := make([]string, len(prefixes)+1)
	keys[0] = prefixNamespace + "lru"
	args := []any{c.config.TTL.Milliseconds(), c.config.Capacity, c.config.Now().UnixMilli()}
	for i, prefix := range prefixes {
		keys[i+1] = prefixNamespace + Digest(scope+":"+prefix)
		args = append(args, StableID(keys[i+1]))
	}
	var redisErr error
	if c.config.Redis != nil && (c.config.RedisEnabled == nil || c.config.RedisEnabled()) {
		operationCtx, cancel := context.WithTimeout(ctx, time.Second)
		result, err := matchPrefixes.Run(operationCtx, c.config.Redis, keys, args...).StringSlice()
		cancel()
		redisErr = err
		if err == nil && len(result) == len(keys) {
			c.mu.Lock()
			defer c.mu.Unlock()
			now := c.config.Now()
			for i, value := range result[1:] {
				id, child, _ := strings.Cut(value, "|")
				previous, found, _ := c.prefixes.Get(keys[i+1])
				hasChild := child == "1" || (found && now.Before(previous.ExpiresAt) && previous.ID == id && previous.HasChild)
				c.prefixes.Set(keys[i+1], prefixNode{ID: id, HasChild: hasChild, ExpiresAt: now.Add(c.config.TTL)})
			}
			return result[0], nil
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.config.Now()
	nodes := make([]prefixNode, len(prefixes))
	sessionID := args[3].(string)
	matched := false
	for i := len(prefixes) - 1; i >= 0; i-- {
		node, found, _ := c.prefixes.Get(keys[i+1])
		if !found || !now.Before(node.ExpiresAt) {
			continue
		}
		nodes[i] = node
		if !matched {
			sessionID = node.ID
			if i < len(prefixes)-1 && node.HasChild {
				sessionID = args[i+4].(string)
			}
			matched = true
		}
	}
	for i, node := range nodes {
		if node.ID == "" {
			node.ID = sessionID
		}
		node.HasChild = node.HasChild || i < len(prefixes)-1
		node.ExpiresAt = now.Add(c.config.TTL)
		c.prefixes.Set(keys[i+1], node)
	}
	return sessionID, redisErr
}

func (c *Cache) ResponseSession(scope, responseID string) (string, error) {
	key := Digest(scope + ":" + responseID)
	id, found, err := c.aliases.Get(key)
	if err != nil {
		id, _, _ = c.localAliases.Get(key)
		return id, err
	}
	if !found {
		return "", nil
	}
	c.localAliases.Set(key, id)
	return id, c.aliases.SetWithTTL(key, id, c.config.TTL)
}

func (c *Cache) BindResponse(scope, responseID, sessionID string) error {
	if scope == "" || NormalizeID(responseID) == "" || NormalizeID(sessionID) == "" {
		return nil
	}
	key := Digest(scope + ":" + responseID)
	c.localAliases.Set(key, sessionID)
	return c.aliases.SetWithTTL(key, sessionID, c.config.TTL)
}
