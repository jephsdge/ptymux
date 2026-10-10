package identity_test

import (
	"errors"
	"path/filepath"
	"testing"

	"ptymux/identity"
)

func TestRegistryFacadeRegistrationLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	registry, err := identity.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := registry.PrepareRegistration("facade")
	if err != nil {
		t.Fatal(err)
	}
	if registration.Status != identity.StatusPending || registration.OwnerID == "" || registration.Password == "" {
		t.Fatalf("registration = %+v", registration)
	}
	if _, err := registry.Authenticate(registration.Name, registration.Password); !errors.Is(err, identity.ErrAuthenticationFailed) {
		t.Fatalf("pending authentication error = %v", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}

	registry, err = identity.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	recovered, err := registry.PrepareRegistration(registration.Name, registration.Password)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.OwnerID != registration.OwnerID || recovered.Password != registration.Password {
		t.Fatalf("recovered = %+v, want owner/password from %+v", recovered, registration)
	}
	principal, err := registry.CommitRegistration(recovered.Name, recovered.Password)
	if err != nil {
		t.Fatal(err)
	}
	if principal.OwnerID != recovered.OwnerID || principal.CredentialGeneration != 1 {
		t.Fatalf("principal = %+v", principal)
	}

	rotated, err := registry.Rotate(principal.Name, recovered.Password)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Password == recovered.Password || rotated.CredentialGeneration != 2 || rotated.Status != identity.StatusActive {
		t.Fatalf("rotated registration = %+v", rotated)
	}
	if _, err := registry.Authenticate(rotated.Name, recovered.Password); !errors.Is(err, identity.ErrAuthenticationFailed) {
		t.Fatalf("old password error = %v", err)
	}
	if _, err := registry.Authenticate(rotated.Name, rotated.Password); err != nil {
		t.Fatal(err)
	}
	if err := registry.Revoke(rotated.Name, rotated.Password); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Authenticate(rotated.Name, rotated.Password); !errors.Is(err, identity.ErrAuthenticationFailed) {
		t.Fatalf("revoked password error = %v", err)
	}
}

func TestRegistryFacadeValidationAndErrors(t *testing.T) {
	if !identity.ValidName("valid-name") || identity.ValidName("INVALID") {
		t.Fatal("ValidName returned unexpected results")
	}
	registry, err := identity.Open(filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PrepareRegistration("INVALID"); !errors.Is(err, identity.ErrInvalidName) {
		t.Fatalf("invalid name error = %v", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Authenticate("valid-name", "password"); !errors.Is(err, identity.ErrClosed) {
		t.Fatalf("closed registry error = %v", err)
	}
}
