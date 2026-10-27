package skillpkg

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// SignatureDomain stable 版本签名的被签消息 = domain + sha256hex（§6.6）。
const SignatureDomain = "gs-evo-bundle-v1:"

// MessageToSign 生成待签名消息。
func MessageToSign(sha256Hex string) []byte {
	return []byte(SignatureDomain + sha256Hex)
}

// GenerateKeyPair 生成 ed25519 密钥对（cloud 首次启动时落盘 data_dir/keys/）。
func GenerateKeyPair() (pubHex, privHex string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return hex.EncodeToString(pub), hex.EncodeToString(priv), nil
}

// Sign 对 sha256hex 签名，返回 hex。
func Sign(privHex, sha256Hex string) (string, error) {
	priv, err := decodePriv(privHex)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, MessageToSign(sha256Hex))
	return hex.EncodeToString(sig), nil
}

// Verify client 侧验签：失败即拒装（D8）。
func Verify(pubHex, sha256Hex, sigHex string) error {
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("bad public key")
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("bad signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), MessageToSign(sha256Hex), sig) {
		return fmt.Errorf("signature verify failed")
	}
	return nil
}

func decodePriv(privHex string) (ed25519.PrivateKey, error) {
	b, err := hex.DecodeString(privHex)
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("bad private key")
	}
	return ed25519.PrivateKey(b), nil
}

// UpdateSignatureDomain 客户端自更新包的签名域（与 skill 包区分）。
const UpdateSignatureDomain = "gs-evo-update-v1:"

// SignUpdate 对自更新包 sha256 签名。
func SignUpdate(privHex, sha256Hex string) (string, error) {
	priv, err := decodePriv(privHex)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(ed25519.Sign(priv, []byte(UpdateSignatureDomain+sha256Hex))), nil
}

// VerifyUpdate client 侧验自更新包签名。
func VerifyUpdate(pubHex, sha256Hex, sigHex string) error {
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("bad public key")
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("bad signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(UpdateSignatureDomain+sha256Hex), sig) {
		return fmt.Errorf("update signature verify failed")
	}
	return nil
}
