package secrets

import (
	"errors"
	"testing"
)

func TestMapKeyring_GetSetDelete(t *testing.T) {
	kr := NewMapKeyring()

	// Set and get.
	if err := kr.Set("svc", "key1", "val1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := kr.Get("svc", "key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "val1" {
		t.Errorf("Get = %q, want %q", got, "val1")
	}

	// Delete.
	if err := kr.Delete("svc", "key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = kr.Get("svc", "key1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete: err = %v, want ErrNotFound", err)
	}
}

func TestMapKeyring_ErrNotFound(t *testing.T) {
	kr := NewMapKeyring()

	_, err := kr.Get("svc", "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing: err = %v, want ErrNotFound", err)
	}
}

func TestMapKeyring_Overwrite(t *testing.T) {
	kr := NewMapKeyring()

	_ = kr.Set("svc", "key", "v1")
	_ = kr.Set("svc", "key", "v2")

	got, _ := kr.Get("svc", "key")
	if got != "v2" {
		t.Errorf("Get after overwrite = %q, want %q", got, "v2")
	}
}

func TestMapKeyring_DeleteNonExistent(t *testing.T) {
	kr := NewMapKeyring()

	// Should not error.
	if err := kr.Delete("svc", "nope"); err != nil {
		t.Errorf("Delete non-existent: %v", err)
	}
}

func TestPackageLevelAPI(t *testing.T) {
	mk := NewMapKeyring()
	SetDefault(mk)
	defer SetDefault(OSKeyring{})

	if err := SetSecret("provider/anthropic", "sk-test"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	got, err := GetSecret("provider/anthropic")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got != "sk-test" {
		t.Errorf("GetSecret = %q, want %q", got, "sk-test")
	}

	if err := DeleteSecret("provider/anthropic"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	_, err = GetSecret("provider/anthropic")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSecret after delete: err = %v, want ErrNotFound", err)
	}
}

func TestAvailable(t *testing.T) {
	mk := NewMapKeyring()
	SetDefault(mk)
	defer SetDefault(OSKeyring{})

	if !Available() {
		t.Error("Available() = false for MapKeyring, want true")
	}
}
