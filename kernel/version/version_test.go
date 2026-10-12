package version

import (
	"testing"

	"github.com/witchsnk/millipede/sdk/contract"
)

func v(maj, min int) contract.APIVersion { return contract.APIVersion{Major: maj, Minor: min} }

func TestCheckOK(t *testing.T) {
	r := Range{Min: v(1, 0), Max: v(1, 9)}
	if err := Check("notifier", r, v(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := Check("notifier", r, v(1, 5)); err != nil {
		t.Fatal(err)
	}
}

// 约束 A4：不匹配必须拒绝并给出明确原因，而不是运行到一半 panic。
func TestCheckTooOld(t *testing.T) {
	r := Range{Min: v(1, 2), Max: v(1, 9)}
	err := Check("notifier", r, v(1, 0))
	if err == nil {
		t.Fatal("want error for too-old plugin")
	}
}

func TestCheckTooNew(t *testing.T) {
	r := Range{Min: v(1, 0), Max: v(1, 9)}
	err := Check("notifier", r, v(2, 0))
	if err == nil {
		t.Fatal("want error for major mismatch")
	}
}

func TestVersionString(t *testing.T) {
	if got := v(1, 20).String(); got != "1.20" {
		t.Fatalf("String = %s", got)
	}
	if got := v(0, 0).String(); got != "0.0" {
		t.Fatalf("String = %s", got)
	}
}
