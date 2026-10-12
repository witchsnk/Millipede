package wasm

import (
	"testing"
	"time"

	"github.com/witchsnk/millipede/kernel/loader"
)

// TestWASMLoaderBuild verifies the WASM loader builds and basic functions work
func TestWASMLoaderBuild(t *testing.T) {
	// Just verify the loader can be instantiated
	l := NewWASMLoader()
	if l == nil {
		t.Fatal("Failed to create WASM loader")
	}

	// Verify kind
	if l.Kind() != loader.BackendWASM {
		t.Fatalf("Expected BackendWASM, got %s", l.Kind())
	}

	// Verify config validation
	if err := l.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig failed: %v", err)
	}

	// Verify options can be applied
	l.ApplyOptions(
		WithHandshakeTimeout(3*time.Second),
		WithMaxSteps(5000),
	)
	t.Log("WASMLoader options applied successfully")
}

// TestWASMLoaderVerifyRuntime verifies runtime is available
func TestWASMLoaderVerifyRuntime(t *testing.T) {
	l := NewWASMLoader()
	if err := l.VerifyRuntime(); err != nil {
		t.Fatalf("VerifyRuntime failed: %v", err)
	}
	t.Log("VerifyRuntime passed")
}

// TestWithHandshakeTimeout tests the WithHandshakeTimeout option
func TestWithHandshakeTimeout(t *testing.T) {
	l := NewWASMLoaderWithOpts(WASMLoaderOpts{
		HandshakeTimeout: 10 * time.Second,
	})
	if l.opt.HandshakeTimeout != 10*time.Second {
		t.Fatalf("Expected HandshakeTimeout 10s, got %v", l.opt.HandshakeTimeout)
	}
	t.Log("WithHandshakeTimeout test passed")
}

// TestWithMaxSteps tests the WithMaxSteps option
func TestWithMaxSteps(t *testing.T) {
	l := NewWASMLoaderWithOpts(WASMLoaderOpts{
		MaxSteps: 2000,
	})
	if l.opt.MaxSteps != 2000 {
		t.Fatalf("Expected MaxSteps 2000, got %v", l.opt.MaxSteps)
	}
	t.Log("WithMaxSteps test passed")
}
