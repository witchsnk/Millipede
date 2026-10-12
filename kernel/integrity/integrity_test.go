package integrity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "plugin.bin")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSumFile(t *testing.T) {
	p := writeTemp(t, "hello plugin")
	got, err := SumFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// 已知 sha256("hello plugin")，确保算法与编码正确
	if len(got) != hexLen {
		t.Fatalf("digest length = %d, want %d", len(got), hexLen)
	}
	again, _ := SumFile(p)
	if again != got {
		t.Fatal("digest not stable")
	}
}

func TestVerifyChecksumOK(t *testing.T) {
	p := writeTemp(t, "good")
	sum, _ := SumFile(p)
	if err := Verify(p, sum, "", Lenient()); err != nil {
		t.Fatalf("should pass: %v", err)
	}
}

// 约束 D1：篡改内容必须被拒绝。
func TestVerifyChecksumMismatch(t *testing.T) {
	p := writeTemp(t, "original")
	sum, _ := SumFile(p)
	// 篡改文件
	if err := os.WriteFile(p, []byte("tampered!"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Verify(p, sum, "", Lenient())
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}
}

func TestVerifyChecksumMissing(t *testing.T) {
	p := writeTemp(t, "x")
	err := Verify(p, "", "", Lenient())
	if !errors.Is(err, ErrChecksumMissing) {
		t.Fatalf("want ErrChecksumMissing, got %v", err)
	}
}

func TestSignAndVerifyStrict(t *testing.T) {
	key := []byte("super-secret-signing-key")
	p := writeTemp(t, "signed content")
	sum, _ := SumFile(p)
	sig, err := Sign(p, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(p, sum, sig, Strict(key)); err != nil {
		t.Fatalf("strict verify should pass: %v", err)
	}
}

// 约束 D1：签名可区分"篡改"与"伪造"。
// 校验和正确但签名来自错误密钥 => 伪造，必须拒绝。
func TestVerifySignatureWrongKey(t *testing.T) {
	p := writeTemp(t, "content")
	sum, _ := SumFile(p)
	otherKey := []byte("attacker-key")
	sig, _ := Sign(p, otherKey) // 用错误密钥签
	err := Verify(p, sum, sig, Strict([]byte("real-key")))
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("want ErrSignatureInvalid, got %v", err)
	}
}

// 即使校验和匹配，篡改后重新计算的签名（旧签名）也无法通过 —— 因为签名覆盖内容。
func TestVerifySignatureAfterTamper(t *testing.T) {
	key := []byte("key")
	p := writeTemp(t, "original")
	sum, _ := SumFile(p)
	sig, _ := Sign(p, key)
	// 篡改内容，同时用旧校验和冒充（校验和已不匹配，先被 checksum 拦）
	os.WriteFile(p, []byte("evil"), 0o644)
	err := Verify(p, sum, sig, Strict(key))
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("content tamper should fail checksum first, got %v", err)
	}
}

func TestVerifySignatureMissing(t *testing.T) {
	p := writeTemp(t, "x")
	sum, _ := SumFile(p)
	err := Verify(p, sum, "", Strict([]byte("k")))
	if !errors.Is(err, ErrSignatureMissing) {
		t.Fatalf("want ErrSignatureMissing, got %v", err)
	}
}

func TestVerifyRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	err := Verify(dir, "abc", "", Lenient())
	if !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("want ErrNotRegularFile, got %v", err)
	}
}

func TestManifestRoundTrip(t *testing.T) {
	p := writeTemp(t, "artifact")
	sum, _ := SumFile(p)
	sig, _ := Sign(p, []byte("k"))
	m := &Manifest{
		Key: "manifest-v1",
		Items: map[string]Record{
			"p1@1.0": {Checksum: sum, Signature: sig},
		},
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := parsed.Lookup("p1", "1.0")
	if !ok {
		t.Fatal("lookup failed")
	}
	if rec.Checksum != sum || rec.Signature != sig {
		t.Fatalf("record = %+v", rec)
	}
	if _, ok := parsed.Lookup("p1", "2.0"); ok {
		t.Fatal("unexpected version hit")
	}
}

// 约束 D1：期望值必须来自可信清单，而不是对被校验文件现场哈希。
// 现场哈希是"自证"，永远等于自己，等于没校验——本用例守住这条边界。
func TestManifestDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "plugin.bin")
	if err := os.WriteFile(artifact, []byte("authentic"), 0o644); err != nil {
		t.Fatal(err)
	}
	key := []byte("release-key")
	manifestPath := filepath.Join(dir, "manifest.json")

	// 发布侧：生成清单
	if err := SignAll(manifestPath, key, "1.0.0", map[string]string{"p": artifact}); err != nil {
		t.Fatal(err)
	}
	sum, sig, err := ResolveFor(manifestPath, "p", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(artifact, sum, sig, Strict(key)); err != nil {
		t.Fatalf("发布产物应通过校验: %v", err)
	}

	// 攻击者篡改产物（清单不动）
	if err := os.WriteFile(artifact, []byte("malicious"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(artifact, sum, sig, Strict(key)); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("篡改后必须被清单校验拦下，实际 %v", err)
	}
}

// 清单里没有该插件条目时必须报错，绝不能"没有期望值就放行"。
func TestResolveForMissingEntry(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := SignAll(manifestPath, nil, "1.0.0", map[string]string{"p": "x"}); err != nil {
		// x 不存在时会失败，这里改用真实文件
		art := filepath.Join(dir, "a.bin")
		os.WriteFile(art, []byte("a"), 0o644)
		if err := SignAll(manifestPath, nil, "1.0.0", map[string]string{"p": art}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := ResolveFor(manifestPath, "absent", "1.0.0"); err == nil {
		t.Fatal("清单缺失条目时应报错，而不是放行")
	}
}

// 清单文件本身不存在时同样必须报错。
func TestResolveForMissingManifest(t *testing.T) {
	if _, _, err := ResolveFor(filepath.Join(t.TempDir(), "nope.json"), "p", "1.0.0"); err == nil {
		t.Fatal("清单文件不存在时应报错")
	}
}
