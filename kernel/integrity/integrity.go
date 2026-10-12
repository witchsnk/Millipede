// Package integrity 实现插件产物的完整性校验与签名验证（约束 D1）。
//
// 背景坑点（见 docs/04 风险清单「构建/分发/安全」）：
// 插件目录一旦可写，恶意二进制就能被塞进去直接加载，等于宿主被完全接管。
// 因此加载前必须强制校验；本包提供两级校验：
//
//  1. Checksum（sha256）：防篡改、防止传输/拷贝过程中损坏；
//  2. Signature（HMAC-SHA256）：防伪造，只有持有密钥的一方能签发可信产物。
//
// 设计要点：
//   - Verify 在校验失败时返回明确原因，绝不"打日志然后继续加载"。
//   - 校验始终针对文件真实字节流，而非调用方传入的声明值。
//   - 校验失败不泄漏密钥信息，只说明差异来源。
package integrity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrChecksumMismatch 表示内容摘要与声明值不符。
var ErrChecksumMismatch = errors.New("integrity: checksum mismatch")

// ErrChecksumMissing 表示未提供期望的校验和。
var ErrChecksumMissing = errors.New("integrity: checksum required but not provided")

// ErrSignatureMissing 表示声明需要签名但未提供。
var ErrSignatureMissing = errors.New("integrity: signature required but not provided")

// ErrSignatureInvalid 表示签名不匹配（产物来源不可信）。
var ErrSignatureInvalid = errors.New("integrity: signature invalid")

// ErrNotRegularFile 表示目标不是普通文件（拒绝目录、设备文件、符号链接等）。
var ErrNotRegularFile = errors.New("integrity: not a regular file")

// 十六进制摘要长度（sha256）。
const hexLen = sha256.Size * 2

// SumFile 计算文件的 sha256 十六进制摘要。
func SumFile(path string) (string, error) {
	sum, err := sumFile(path)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum), nil
}

func sumFile(path string) ([]byte, error) {
	if err := checkRegular(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("integrity: open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, fmt.Errorf("integrity: hash %s: %w", path, err)
	}
	return h.Sum(nil), nil
}

// checkRegular 拒绝非普通文件，避免把目录/设备当作插件加载。
func checkRegular(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("integrity: empty path")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("integrity: stat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s (mode %s)", ErrNotRegularFile, path, fi.Mode())
	}
	return nil
}

// Sign 用共享密钥对文件内容签名，返回十六进制 HMAC-SHA256。
// 私钥/密钥只存在于发布侧，宿主仅持有公钥等价物（对称场景即同一密钥）。
func Sign(path string, key []byte) (string, error) {
	if len(key) == 0 {
		return "", errors.New("integrity: empty signing key")
	}
	sum, err := sumFile(path)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(sum)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Policy 描述一个插件产物的校验要求。
type Policy struct {
	// Disabled 显式关闭全部校验。
	// 必须用独立布尔位表达"关闭"，不能用零值 Policy 表达——
	// 否则调用方无法区分"没配置"（应取安全默认）与"明确要关闭"。
	// Off() 是唯一应该设置它的入口。
	Disabled bool
	// RequireChecksum 是否强制校验 sha256。
	RequireChecksum bool
	// RequireSignature 是否强制校验签名。
	RequireSignature bool
	// Key 签名校验使用的密钥；RequireSignature 为 true 时不得为空。
	Key []byte
}

// Strict 是生产环境的推荐策略：校验和 + 签名都必须通过。
func Strict(key []byte) Policy {
	return Policy{RequireChecksum: true, RequireSignature: true, Key: key}
}

// Lenient 适用于本地开发：只校验校验和，签名可选。
func Lenient() Policy {
	return Policy{RequireChecksum: true}
}

// Off 关闭全部校验。仅用于明确知晓风险的场景（如一次性本地冒烟测试）。
func Off() Policy {
	return Policy{Disabled: true}
}

// active 返回策略是否生效。
func (p Policy) active() bool { return !p.Disabled }

// Enabled 报告策略是否启用校验（供不直接依赖本包的上层判断，例如产物监听器）。
//
// 注意：Enabled 为 true 只代表"要求提供校验材料"，
// 不代表具体某次校验通过；具体校验请调用 Verify。
func (p Policy) Enabled() bool { return p.active() }

// Verify 按策略校验插件产物。
//
// 校验顺序：文件类型 -> checksum -> signature。
// 任一环节失败立即返回，绝不继续加载。
func Verify(path string, wantChecksum, wantSignature string, p Policy) error {
	if !p.active() {
		return nil
	}
	sum, err := sumFile(path)
	if err != nil {
		return err
	}

	if p.RequireChecksum {
		if strings.TrimSpace(wantChecksum) == "" {
			return fmt.Errorf("%w: %s", ErrChecksumMissing, filepath.Base(path))
		}
		if !equalHex(wantChecksum, sum) {
			return fmt.Errorf("%w: %s (want %s, got %s)",
				ErrChecksumMismatch, filepath.Base(path),
				short(wantChecksum), hex.EncodeToString(sum))
		}
	}

	if p.RequireSignature {
		if strings.TrimSpace(wantSignature) == "" {
			return fmt.Errorf("%w: %s", ErrSignatureMissing, filepath.Base(path))
		}
		if len(p.Key) == 0 {
			return errors.New("integrity: signature required but verification key is empty")
		}
		mac := hmac.New(sha256.New, p.Key)
		mac.Write(sum)
		wantSig, err := hex.DecodeString(normalizeHex(wantSignature))
		if err != nil {
			return fmt.Errorf("%w: %s (malformed signature)", ErrSignatureInvalid, filepath.Base(path))
		}
		if !hmac.Equal(wantSig, mac.Sum(nil)) {
			// 不回显期望签名，避免给暴力破解提供反馈。
			return fmt.Errorf("%w: %s", ErrSignatureInvalid, filepath.Base(path))
		}
	}
	return nil
}

// equalHex 以常量时间语义比较十六进制摘要（大小写不敏感、可带 sha256: 前缀）。
func equalHex(want string, sum []byte) bool {
	raw, err := hex.DecodeString(normalizeHex(want))
	if err != nil {
		return false
	}
	return hmac.Equal(raw, sum)
}

func normalizeHex(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "sha256:")
	return s
}

func short(s string) string {
	s = normalizeHex(s)
	if len(s) <= 12 {
		return s
	}
	return s[:12] + "..."
}

// Manifest 是插件发布方给出的清单文件，记录各产物的校验和与签名。
// CI 生成它，宿主据此校验，避免把期望值硬编码进二进制。
type Manifest struct {
	// Key 为 "pluginID@version"，Value 为该产物的校验和与签名。
	Key   string            `json:"key"`
	Items map[string]Record `json:"items"`
}

// Record 是清单中的一条产物记录。
type Record struct {
	Checksum  string `json:"checksum"`
	Signature string `json:"signature,omitempty"`
}

// Lookup 按 pluginID@version 取记录。
func (m *Manifest) Lookup(id, version string) (Record, bool) {
	if m == nil || m.Items == nil {
		return Record{}, false
	}
	r, ok := m.Items[id+"@"+version]
	return r, ok
}

// ParseManifest 解析清单 JSON。
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("integrity: parse manifest: %w", err)
	}
	return &m, nil
}

// LoadManifest 从磁盘读取清单。
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("integrity: read manifest %s: %w", path, err)
	}
	return ParseManifest(b)
}
