package integrity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SignAll 为一批插件产物生成清单文件。
//
// 这是发布侧（CI）的动作：生成后应把清单随插件一起分发，
// 宿加载入时只信任清单里的期望值，而**不是**对插件文件现场哈希
// （后者是自证，永远等于自己，等于没校验）。
func SignAll(manifestPath string, key []byte, version string, artifacts map[string]string) error {
	m := &Manifest{Key: fmt.Sprintf("millipede-manifest v1, pluginVersion=%s", version), Items: map[string]Record{}}
	for id, path := range artifacts {
		sum, err := SumFile(path)
		if err != nil {
			return err
		}
		rec := Record{Checksum: sum}
		if len(key) > 0 {
			sig, err := Sign(path, key)
			if err != nil {
				return err
			}
			rec.Signature = sig
		}
		m.Items[id+"@"+version] = rec
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("integrity: marshal manifest: %w", err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		return fmt.Errorf("integrity: write manifest %s: %w", manifestPath, err)
	}
	return nil
}

// ResolveFor 按插件 ID 与版本从清单取出期望的校验和/签名。
// 清单缺失或无对应条目时返回错误——绝不允许"没有期望值就放行"。
func ResolveFor(manifestPath, id, version string) (checksum, signature string, err error) {
	m, err := LoadManifest(manifestPath)
	if err != nil {
		return "", "", err
	}
	rec, ok := m.Lookup(id, version)
	if !ok {
		return "", "", fmt.Errorf("%w: manifest %s has no entry for %s@%s",
			ErrChecksumMissing, filepath.Base(manifestPath), id, version)
	}
	return rec.Checksum, rec.Signature, nil
}
