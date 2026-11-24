package mail_gmail

import (
	"time"

	"github.com/jellydator/ttlcache/v3"
)

func newCache[V any](ttl time.Duration) *ttlcache.Cache[string, V] {
	cache := ttlcache.New(
		ttlcache.WithTTL[string, V](ttl),
		ttlcache.WithDisableTouchOnHit[string, V](),
		ttlcache.WithCapacity[string, V](10000),
	)
	go cache.Start()
	return cache
}
