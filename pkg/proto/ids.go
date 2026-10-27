package proto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// NewToken 生成带前缀的不透明随机 token（20 字节 crypto/rand → 40 hex，§6.7）。
// 明文仅在签发响应出现一次；服务端只存 SHA-256 哈希。
func NewToken(prefix string) string {
	b := make([]byte, tokenRandomHexSize/2)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func NewDeviceToken() string { return NewToken(TokenPrefixDevice) }
func NewEnrollToken() string { return NewToken(TokenPrefixEnroll) }
func NewAdminToken() string  { return NewToken(TokenPrefixAdmin) }

// HashToken 服务端落库形态：SHA-256 hex。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewID 生成唯一 id（cmd_id 等）：毫秒时间戳 + 随机，字典序即时间序（UUIDv7 风格，无外部依赖）。
func NewID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	ts := uint64(time.Now().UnixMilli())
	return fmt.Sprintf("%s-%012x%016x", prefix, ts, be64(b))
}

func be64(b []byte) uint64 {
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v
}

// NewDeviceID 本机持久身份（UUIDv7 格式字符串）。
func NewDeviceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	ts := uint64(time.Now().UnixMilli())
	b[0] = byte(ts >> 40)
	b[1] = byte(ts >> 32)
	b[2] = byte(ts >> 24)
	b[3] = byte(ts >> 16)
	b[4] = byte(ts >> 8)
	b[5] = byte(ts)
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
