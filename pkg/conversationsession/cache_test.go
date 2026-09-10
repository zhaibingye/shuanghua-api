package conversationsession

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrefixConversationLifecycle(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			config := CacheConfig{Now: func() time.Time { return now }}
			var server *miniredis.Miniredis
			if backend == "redis" {
				server = miniredis.RunT(t)
				config.Redis = redis.NewClient(&redis.Options{Addr: server.Addr()})
				t.Cleanup(func() { require.NoError(t, config.Redis.Close()) })
			}
			cache := NewCache(config)
			first, err := cache.Match(context.Background(), "alice/upstream", []string{"user-a"})
			require.NoError(t, err)
			require.NotEmpty(t, first)
			next, err := cache.Match(context.Background(), "alice/upstream", []string{"user-a", "assistant-b", "user-c"})
			require.NoError(t, err)
			assert.Equal(t, first, next, "appending messages must keep the conversation")
			branch, err := cache.Match(context.Background(), "alice/upstream", []string{"user-a", "assistant-b", "user-x"})
			require.NoError(t, err)
			assert.NotEqual(t, first, branch, "a divergent history is a new branch")
			if backend == "redis" {
				cache = NewCache(config)
			}
			continued, err := cache.Match(context.Background(), "alice/upstream", []string{"user-a", "assistant-b", "user-x", "assistant-y"})
			require.NoError(t, err)
			assert.Equal(t, branch, continued, "a restart or extension must retain a remembered branch")
			retry, err := cache.Match(context.Background(), "alice/upstream", []string{"user-a", "assistant-b", "user-c"})
			require.NoError(t, err)
			assert.Equal(t, first, retry, "revisiting the original branch must not select the newer fork")
			other, err := cache.Match(context.Background(), "bob/upstream", []string{"user-a"})
			require.NoError(t, err)
			assert.NotEqual(t, first, other)
			other, err = cache.Match(context.Background(), "alice/other-upstream", []string{"user-a"})
			require.NoError(t, err)
			assert.NotEqual(t, first, other)

			now = now.Add(2 * time.Hour)
			if server != nil {
				server.FastForward(2 * time.Hour)
			}
			expired, err := cache.Match(context.Background(), "alice/upstream", []string{"user-a", "assistant-b", "user-x"})
			require.NoError(t, err)
			assert.NotEqual(t, branch, expired, "expired branch associations must not remain live")
		})
	}
}

func TestPrefixCapacityEvictsInactiveBranches(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			config := CacheConfig{Capacity: 3, Now: func() time.Time { return now }}
			if backend == "redis" {
				server := miniredis.RunT(t)
				config.Redis = redis.NewClient(&redis.Options{Addr: server.Addr()})
				t.Cleanup(func() { require.NoError(t, config.Redis.Close()) })
			}
			cache := NewCache(config)
			original, err := cache.Match(context.Background(), "scope", []string{"root", "a"})
			require.NoError(t, err)
			now = now.Add(time.Second)
			_, err = cache.Match(context.Background(), "scope", []string{"root", "b"})
			require.NoError(t, err)
			now = now.Add(time.Second)
			_, err = cache.Match(context.Background(), "scope", []string{"other"})
			require.NoError(t, err)
			now = now.Add(time.Second)
			revisited, err := cache.Match(context.Background(), "scope", []string{"root", "a"})
			require.NoError(t, err)
			assert.NotEqual(t, original, revisited, "evicted history is inferred again, not retained indefinitely")
		})
	}
}

func TestConcurrentRedisBranchesAndResponseAliases(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	left, right := NewCache(CacheConfig{Redis: client}), NewCache(CacheConfig{Redis: client})
	original, err := left.Match(context.Background(), "scope", []string{"root", "original"})
	require.NoError(t, err)
	start := make(chan struct{})
	var wait sync.WaitGroup
	var ids [2]string
	var errs [2]error
	for i, cache := range []*Cache{left, right} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			ids[i], errs[i] = cache.Match(context.Background(), "scope", []string{"root", "fork"})
		}()
	}
	close(start)
	wait.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.NotEqual(t, original, ids[0])
	assert.Equal(t, ids[0], ids[1], "concurrent creation must agree across cache instances")

	require.NoError(t, left.BindResponse("scope", "resp-one", ids[0]))
	previous, err := right.ResponseSession("scope", "resp-one")
	require.NoError(t, err)
	assert.Equal(t, ids[0], previous)
	isolated, err := right.ResponseSession("other-scope", "resp-one")
	require.NoError(t, err)
	assert.Empty(t, isolated)
	require.NoError(t, right.BindResponse("scope", "resp-two", previous))
	continued, err := left.ResponseSession("scope", "resp-two")
	require.NoError(t, err)
	assert.Equal(t, ids[0], continued, "each response in an incremental chain maps to the original conversation")
	server.FastForward(2 * time.Hour)
	expired, err := right.ResponseSession("scope", "resp-one")
	require.NoError(t, err)
	assert.Empty(t, expired)
}

func TestRedisFailureKeepsWarmLocalBranchAndAliases(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	cache := NewCache(CacheConfig{Redis: client})
	_, err := cache.Match(context.Background(), "scope", []string{"root", "original"})
	require.NoError(t, err)
	branch, err := cache.Match(context.Background(), "scope", []string{"root", "branch"})
	require.NoError(t, err)
	require.NoError(t, cache.BindResponse("scope", "resp-one", branch))
	require.NoError(t, client.Close()) // deterministic transport failure, no timeout/sleep
	continued, err := cache.Match(context.Background(), "scope", []string{"root", "branch", "next"})
	require.Error(t, err)
	assert.Equal(t, branch, continued)
	previous, err := cache.ResponseSession("scope", "resp-one")
	require.Error(t, err)
	assert.Equal(t, branch, previous)
}
