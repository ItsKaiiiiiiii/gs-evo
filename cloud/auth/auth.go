// Package auth 实现凭证校验：前缀分发 + SHA-256 落库比对 + 进程内缓存（§6.7）。
// 鉴权 fail-closed，缓存 fail-open；缓存 TTL 钳制到 token 剩余寿命。
package auth

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/ItsKaiiiiiiii/gs-evo/cloud/store"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

const cacheTTL = 5 * time.Minute
const cacheMax = 4096

type entry struct {
	device    *store.Device
	expiresAt time.Time // = min(now+cacheTTL, token 剩余寿命)
}

// Cache sha256(token) → 设备 的进程内 LRU（v1 无 Redis，见 §6.7）。
type Cache struct {
	mu sync.Mutex
	m  map[string]entry
}

func NewCache() *Cache {
	return &Cache{m: make(map[string]entry)}
}

func (c *Cache) get(hash string) (*store.Device, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[hash]
	if !ok || time.Now().After(e.expiresAt) {
		if ok {
			delete(c.m, hash)
		}
		return nil, false
	}
	return e.device, true
}

func (c *Cache) put(hash string, d *store.Device) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := cacheTTL
	if remaining := time.Until(d.TokenExpiresAt); remaining < ttl {
		ttl = remaining // 钳制：缓存不能活过 token
	}
	if ttl <= 0 {
		return
	}
	if len(c.m) >= cacheMax { // 简易淘汰：清最旧的约一半
		for k := range c.m {
			delete(c.m, k)
			if len(c.m) < cacheMax/2 {
				break
			}
		}
	}
	c.m[hash] = entry{device: d, expiresAt: time.Now().Add(ttl)}
}

func (c *Cache) invalidate(hash string) {
	c.mu.Lock()
	delete(c.m, hash)
	c.mu.Unlock()
}

// Verifier device/admin token 校验器。
type Verifier struct {
	store *store.Store
	cache *Cache
}

func NewVerifier(st *store.Store) *Verifier {
	return &Verifier{store: st, cache: NewCache()}
}

// VerifyDevice 校验 Bearer device token；失败返回 nil（fail-closed）。
func (v *Verifier) VerifyDevice(ctx context.Context, bearer string) *store.Device {
	token := strings.TrimPrefix(bearer, "Bearer ")
	if !strings.HasPrefix(token, proto.TokenPrefixDevice) {
		return nil
	}
	hash := proto.HashToken(token)
	if d, ok := v.cache.get(hash); ok {
		return d
	}
	d, err := v.store.GetDeviceByTokenHash(hash)
	if err != nil || d == nil {
		return nil
	}
	v.cache.put(hash, d)
	return d
}

// VerifyAdminRole 校验 Bearer admin token，返回角色（""=无效；admin=全权；viewer=只读）。
func (v *Verifier) VerifyAdminRole(ctx context.Context, bearer string) string {
	token := strings.TrimPrefix(bearer, "Bearer ")
	if !strings.HasPrefix(token, proto.TokenPrefixAdmin) {
		return ""
	}
	role, err := v.store.GetAdminTokenRole(proto.HashToken(token))
	if err != nil {
		return ""
	}
	return role
}

// VerifyAdmin 校验 Bearer admin token（全权）。
func (v *Verifier) VerifyAdmin(ctx context.Context, bearer string) bool {
	return v.VerifyAdminRole(ctx, bearer) == "admin"
}

// RevokeDevice 吊销：库内失效 + 缓存失效（调用方负责踢线）。
func (v *Verifier) RevokeDevice(deviceID string) error {
	d, err := v.store.GetDevice(deviceID)
	if err != nil || d == nil {
		return err
	}
	if err := v.store.RevokeDevice(deviceID); err != nil {
		return err
	}
	v.cache.invalidate(d.TokenHash)
	return nil
}
