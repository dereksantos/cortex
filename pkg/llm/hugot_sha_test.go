package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestVerifyModelSHA exercises the SHA-pinning verification used to
// defend against tampered Hugging Face Hub downloads. A poisoned
// embedding model is a high-leverage attack: an attacker who can
// swap the weights blob invisibly biases every retrieval forever.
// Pinning the SHA of the ONNX model file lets us refuse to use a
// model whose weights don't match the operator's expected value.
func TestVerifyModelSHA(t *testing.T) {
	dir, err := os.MkdirTemp("", "cortex-model-sha-*")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	defer os.RemoveAll(dir)

	content := []byte("pretend this is an onnx model")
	modelFile := filepath.Join(dir, "model.onnx")
	if err := os.WriteFile(modelFile, content, 0600); err != nil {
		t.Fatalf("write model file: %v", err)
	}
	sum := sha256.Sum256(content)
	good := hex.EncodeToString(sum[:])

	t.Run("matching SHA → nil", func(t *testing.T) {
		if err := verifyModelSHA(dir, good); err != nil {
			t.Errorf("unexpected error for correct SHA: %v", err)
		}
	})

	t.Run("mismatching SHA → error mentioning both digests", func(t *testing.T) {
		err := verifyModelSHA(dir, "deadbeef"+good[8:])
		if err == nil {
			t.Fatal("expected error for wrong SHA")
		}
		if !contains(err.Error(), "expected") || !contains(err.Error(), "got") {
			t.Errorf("error should name both digests; got %q", err.Error())
		}
	})

	t.Run("empty expected SHA → nil (verification opt-out)", func(t *testing.T) {
		// An empty expected SHA means the operator hasn't pinned the
		// model yet — verification is skipped rather than rejecting,
		// so existing setups don't break. Document this contract in
		// the function: pin == "" → no check.
		if err := verifyModelSHA(dir, ""); err != nil {
			t.Errorf("empty expected SHA should be a no-op; got: %v", err)
		}
	})

	t.Run("missing model.onnx → error", func(t *testing.T) {
		emptyDir, err := os.MkdirTemp("", "cortex-model-empty-*")
		if err != nil {
			t.Fatalf("mkdir temp: %v", err)
		}
		defer os.RemoveAll(emptyDir)
		if err := verifyModelSHA(emptyDir, good); err == nil {
			t.Error("expected error when model.onnx is missing")
		}
	})
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
