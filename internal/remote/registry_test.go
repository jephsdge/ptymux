package remote

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRegistryPersistsWithoutPlaintextPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "clients.json")
	registry, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	record, password, err := registry.Register("client-1.example")
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != ClientActive || record.CredentialGeneration != 1 {
		t.Fatalf("unexpected record: %+v", record)
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(record.OwnerID); err != nil || len(decoded) != 16 {
		t.Fatalf("invalid owner ID %q: decoded length %d, err %v", record.OwnerID, len(decoded), err)
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(password); err != nil || len(decoded) != 32 {
		t.Fatalf("invalid password: decoded length %d, err %v", len(decoded), err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(password)) {
		t.Fatal("registry contains the plaintext password")
	}
	if !bytes.Contains(data, []byte(`"password_sha256"`)) {
		t.Fatal("registry does not contain a password digest")
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0600 {
		t.Fatalf("registry mode = %04o, want 0600", info.Mode().Perm())
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0700 {
		t.Fatalf("registry directory mode = %04o, want 0700", info.Mode().Perm())
	}

	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	principal, err := reopened.Authenticate(record.Name, password)
	if err != nil {
		t.Fatal(err)
	}
	if principal.OwnerID != record.OwnerID || principal.CredentialGeneration != 1 {
		t.Fatalf("unexpected principal after restart: %+v", principal)
	}
}

func TestRegistryPersistRejectsOversizedData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "clients.json")
	registry, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	clients := map[string]storedClient{
		"alice": {
			Name:           "alice",
			PasswordSHA256: string(bytes.Repeat([]byte("x"), maxRegistrySize)),
		},
	}
	if replaced, err := registry.persist(clients); err == nil {
		t.Fatal("persist accepted registry larger than the load limit")
	} else if replaced {
		t.Fatal("oversized persist replaced the registry")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatal("oversized persist replaced the existing registry")
	}
}

func TestRegistryKeepsCommittedStateAfterDirectorySyncFailure(t *testing.T) {
	registry := openTestRegistry(t)
	_, password, err := registry.Register("alice")
	if err != nil {
		t.Fatal(err)
	}

	registry.mu.RLock()
	clients := cloneClients(registry.clients)
	client := clients["alice"]
	registry.mu.RUnlock()
	client.CredentialGeneration = 2
	client.UpdatedAt = client.UpdatedAt.Add(time.Second)
	clients[client.Name] = client

	syncErr := errors.New("directory sync failed")
	registry.syncDirectory = func(string) error { return syncErr }
	if err := registry.persistAndPublish(clients); !errors.Is(err, syncErr) {
		t.Fatalf("persistAndPublish error = %v, want directory sync failure", err)
	}
	if principal, err := registry.Authenticate("alice", password); err != nil {
		t.Fatal(err)
	} else if principal.CredentialGeneration != 2 {
		t.Fatalf("credential generation = %d, want 2", principal.CredentialGeneration)
	}

	registry.syncDirectory = syncDirectory
	if _, _, err := registry.Register("bob"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(registry.path)
	if err != nil {
		t.Fatal(err)
	}
	var disk registryFile
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	generations := make(map[string]uint64, len(disk.Clients))
	for _, client := range disk.Clients {
		generations[client.Name] = client.CredentialGeneration
	}
	if generations["alice"] != 2 || generations["bob"] != 1 {
		t.Fatalf("persisted generations = %+v, want alice=2 and bob=1", generations)
	}
}

func TestRegistryCommittedMutationErrorsReturnUsableResults(t *testing.T) {
	syncErr := errors.New("directory sync failed")

	t.Run("register", func(t *testing.T) {
		registry := openTestRegistry(t)
		registry.syncDirectory = func(string) error { return syncErr }
		record, password, err := registry.Register("alice")
		if !errors.Is(err, syncErr) || !registryMutationCommitted(err) {
			t.Fatalf("Register error = %v, want committed directory sync failure", err)
		}
		if record.Name != "alice" || password == "" {
			t.Fatalf("Register result = %+v/%q, want usable credentials", record, password)
		}
		if _, err := registry.Authenticate(record.Name, password); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("rotate", func(t *testing.T) {
		registry := openTestRegistry(t)
		record, oldPassword, err := registry.Register("alice")
		if err != nil {
			t.Fatal(err)
		}
		registry.syncDirectory = func(string) error { return syncErr }
		newPassword, principal, err := registry.Rotate(record.Name, oldPassword)
		if !errors.Is(err, syncErr) || !registryMutationCommitted(err) {
			t.Fatalf("Rotate error = %v, want committed directory sync failure", err)
		}
		if newPassword == "" || principal.CredentialGeneration != 2 {
			t.Fatalf("Rotate result = %q/%+v, want usable generation 2 credentials", newPassword, principal)
		}
		if _, err := registry.Authenticate(record.Name, oldPassword); !errors.Is(err, ErrAuthenticationFailed) {
			t.Fatalf("old password error = %v, want ErrAuthenticationFailed", err)
		}
		if _, err := registry.Authenticate(record.Name, newPassword); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("revoke", func(t *testing.T) {
		registry := openTestRegistry(t)
		record, password, err := registry.Register("alice")
		if err != nil {
			t.Fatal(err)
		}
		registry.syncDirectory = func(string) error { return syncErr }
		err = registry.Revoke(record.Name, password)
		if !errors.Is(err, syncErr) || !registryMutationCommitted(err) {
			t.Fatalf("Revoke error = %v, want committed directory sync failure", err)
		}
		if _, err := registry.Authenticate(record.Name, password); !errors.Is(err, ErrAuthenticationFailed) {
			t.Fatalf("revoked authentication error = %v, want ErrAuthenticationFailed", err)
		}
	})
}

func TestRegisterDuplicateAndConcurrent(t *testing.T) {
	registry := openTestRegistry(t)
	if _, _, err := registry.Register("duplicate"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.Register("duplicate"); !errors.Is(err, ErrClientNameUnavailable) {
		t.Fatalf("duplicate Register error = %v, want ErrClientNameUnavailable", err)
	}

	const goroutines = 24
	var wg sync.WaitGroup
	results := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := registry.Register("concurrent")
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	unavailable := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrClientNameUnavailable):
			unavailable++
		default:
			t.Fatalf("unexpected concurrent Register error: %v", err)
		}
	}
	if successes != 1 || unavailable != goroutines-1 {
		t.Fatalf("successes = %d, unavailable = %d", successes, unavailable)
	}
}

func TestRegistryConcurrentMutationsRemainPersistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	registry, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := registry.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	const clients = 12
	passwords := make([]string, clients)
	rotatedPasswords := make([]string, clients)
	freshPasswords := make([]string, clients)
	for i := 0; i < clients; i++ {
		name := fmt.Sprintf("existing-%02d", i)
		_, password, err := registry.Register(name)
		if err != nil {
			t.Fatal(err)
		}
		passwords[i] = password
	}

	var wg sync.WaitGroup
	errs := make(chan error, clients*2)
	for i := 0; i < clients; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("existing-%02d", i)
			if i%2 == 0 {
				password, _, err := registry.Rotate(name, passwords[i])
				rotatedPasswords[i] = password
				if err != nil {
					errs <- fmt.Errorf("rotate %s: %w", name, err)
				}
				return
			}
			if err := registry.Revoke(name, passwords[i]); err != nil {
				errs <- fmt.Errorf("revoke %s: %w", name, err)
			}
		}()
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("fresh-%02d", i)
			_, password, err := registry.Register(name)
			freshPasswords[i] = password
			if err != nil {
				errs <- fmt.Errorf("register %s: %w", name, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	verify := func(t *testing.T, registry *Registry) {
		t.Helper()
		for i := 0; i < clients; i++ {
			existingName := fmt.Sprintf("existing-%02d", i)
			if i%2 == 0 {
				principal, err := registry.Authenticate(existingName, rotatedPasswords[i])
				if err != nil {
					t.Errorf("Authenticate rotated %s: %v", existingName, err)
				} else if principal.CredentialGeneration != 2 {
					t.Errorf("rotated generation for %s = %d, want 2", existingName, principal.CredentialGeneration)
				}
			} else if _, err := registry.Authenticate(existingName, passwords[i]); !errors.Is(err, ErrAuthenticationFailed) {
				t.Errorf("Authenticate revoked %s error = %v, want ErrAuthenticationFailed", existingName, err)
			}

			freshName := fmt.Sprintf("fresh-%02d", i)
			if _, err := registry.Authenticate(freshName, freshPasswords[i]); err != nil {
				t.Errorf("Authenticate %s: %v", freshName, err)
			}
		}
	}
	verify(t, registry)

	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err = OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	verify(t, registry)
}

func TestRotateInvalidatesOldPassword(t *testing.T) {
	registry := openTestRegistry(t)
	record, oldPassword, err := registry.Register("rotate-me")
	if err != nil {
		t.Fatal(err)
	}
	newPassword, principal, err := registry.Rotate(record.Name, oldPassword)
	if err != nil {
		t.Fatal(err)
	}
	if newPassword == oldPassword {
		t.Fatal("rotation returned the old password")
	}
	if principal.OwnerID != record.OwnerID || principal.CredentialGeneration != 2 {
		t.Fatalf("unexpected rotated principal: %+v", principal)
	}
	if _, err := registry.Authenticate(record.Name, oldPassword); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("old password error = %v, want ErrAuthenticationFailed", err)
	}
	if got, err := registry.Authenticate(record.Name, newPassword); err != nil {
		t.Fatal(err)
	} else if got != principal {
		t.Fatalf("Authenticate principal = %+v, want %+v", got, principal)
	}
	data, err := os.ReadFile(registry.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(oldPassword)) || bytes.Contains(data, []byte(newPassword)) {
		t.Fatal("registry contains a plaintext password after rotation")
	}
	if _, _, err := registry.Rotate(record.Name, oldPassword); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("Rotate with old password error = %v, want ErrAuthenticationFailed", err)
	}
}

func TestRevokeAndReregisterUsesNewOwner(t *testing.T) {
	registry := openTestRegistry(t)
	oldRecord, oldPassword, err := registry.Register("reusable")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Revoke(oldRecord.Name, oldPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Authenticate(oldRecord.Name, oldPassword); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("revoked authentication error = %v, want ErrAuthenticationFailed", err)
	}

	newRecord, newPassword, err := registry.Register(oldRecord.Name)
	if err != nil {
		t.Fatal(err)
	}
	if newRecord.OwnerID == oldRecord.OwnerID {
		t.Fatal("re-registered client retained its old owner ID")
	}
	if newRecord.CredentialGeneration != 1 {
		t.Fatalf("new credential generation = %d, want 1", newRecord.CredentialGeneration)
	}
	if _, err := registry.Authenticate(oldRecord.Name, oldPassword); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("old password after re-registration error = %v, want ErrAuthenticationFailed", err)
	}
	if principal, err := registry.Authenticate(newRecord.Name, newPassword); err != nil {
		t.Fatal(err)
	} else if principal.OwnerID != newRecord.OwnerID {
		t.Fatalf("principal owner = %q, want %q", principal.OwnerID, newRecord.OwnerID)
	}
}

func TestRotationAndRevocationPersistAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "registry.json")
	registry, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	record, password, err := registry.Register("persistent")
	if err != nil {
		t.Fatal(err)
	}
	rotatedPassword, rotatedPrincipal, err := registry.Rotate(record.Name, password)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}

	registry, err = OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := registry.Authenticate(record.Name, rotatedPassword)
	if err != nil {
		t.Fatal(err)
	}
	if principal != rotatedPrincipal {
		t.Fatalf("principal after rotation restart = %+v, want %+v", principal, rotatedPrincipal)
	}
	if err := registry.Revoke(record.Name, rotatedPassword); err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}

	registry, err = OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, err := registry.Authenticate(record.Name, rotatedPassword); err != ErrAuthenticationFailed {
		t.Fatalf("revoked authentication after restart error = %v, want exact ErrAuthenticationFailed", err)
	}
}

func TestAuthenticationFailuresAreIndistinguishable(t *testing.T) {
	if ErrAuthenticationFailed.Error() != "authentication_failed" {
		t.Fatalf("authentication error text = %q", ErrAuthenticationFailed)
	}
	registry := openTestRegistry(t)
	_, password, err := registry.Register("known")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		password string
	}{
		{name: "known", password: password + "x"},
		{name: "unknown", password: password},
		{name: "INVALID", password: password},
	} {
		if _, err := registry.Authenticate(test.name, test.password); err != ErrAuthenticationFailed {
			t.Fatalf("Authenticate(%q) error = %v, want exact ErrAuthenticationFailed", test.name, err)
		}
	}
}

func TestClientNameValidation(t *testing.T) {
	registry := openTestRegistry(t)
	valid := []string{"a", "0", ".", "_", "-", "-a", "a-", ".a", "a.", "a.b_c-d9", string(bytes.Repeat([]byte{'a'}, 64))}
	for _, name := range valid {
		if _, _, err := registry.Register(name); err != nil {
			t.Errorf("Register(%q): %v", name, err)
		}
	}
	invalid := []string{"", "A", "a/b", "a b", "é", string(bytes.Repeat([]byte{'a'}, 65))}
	for _, name := range invalid {
		if _, _, err := registry.Register(name); err != ErrInvalidClientName {
			t.Errorf("Register(%q) error = %v, want exact ErrInvalidClientName", name, err)
		}
	}
}

func TestOpenRegistryRejectsMalformedDuplicateAndUnsupportedData(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "registry.json")
		writeRegistryFile(t, path, []byte(`{"version":1,"clients":[`), 0600)
		if registry, err := OpenRegistry(path); err == nil {
			registry.Close()
			t.Fatal("OpenRegistry accepted malformed JSON")
		}
	})

	t.Run("unsupported version", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "registry.json")
		writeRegistryFile(t, path, []byte(`{"version":2,"clients":[]}`), 0600)
		if registry, err := OpenRegistry(path); err == nil {
			registry.Close()
			t.Fatal("OpenRegistry accepted an unsupported version")
		}
	})

	t.Run("duplicate record", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "registry.json")
		registry, err := OpenRegistry(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := registry.Register("duplicate"); err != nil {
			t.Fatal(err)
		}
		if err := registry.Close(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var disk registryFile
		if err := json.Unmarshal(data, &disk); err != nil {
			t.Fatal(err)
		}
		disk.Clients = append(disk.Clients, disk.Clients[0])
		data, err = json.Marshal(disk)
		if err != nil {
			t.Fatal(err)
		}
		writeRegistryFile(t, path, data, 0600)
		if registry, err := OpenRegistry(path); err == nil {
			registry.Close()
			t.Fatal("OpenRegistry accepted duplicate records")
		}
	})
}

func TestOpenRegistryRejectsInsecureAndNonRegularFiles(t *testing.T) {
	t.Run("insecure permissions", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "registry.json")
		writeRegistryFile(t, path, []byte(`{"version":1,"clients":[]}`), 0644)
		if registry, err := OpenRegistry(path); err == nil {
			registry.Close()
			t.Fatal("OpenRegistry accepted insecure file permissions")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.json")
		writeRegistryFile(t, target, []byte(`{"version":1,"clients":[]}`), 0600)
		path := filepath.Join(dir, "registry.json")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if registry, err := OpenRegistry(path); err == nil {
			registry.Close()
			t.Fatal("OpenRegistry accepted a symlink")
		}
	})

	t.Run("directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "registry.json")
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if registry, err := OpenRegistry(path); err == nil {
			registry.Close()
			t.Fatal("OpenRegistry accepted a directory")
		}
	})
}

func TestRegistryLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	registry, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	if second, err := OpenRegistry(path); err == nil {
		second.Close()
		t.Fatal("second OpenRegistry acquired an already-held process lock")
	}

	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRegistry(path)
	if err != nil {
		t.Fatalf("OpenRegistry after lock release: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRegistrySecuresExistingParentDirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	registry, err := OpenRegistry(filepath.Join(parent, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("registry parent mode = %04o, want 0700", info.Mode().Perm())
	}
}

func TestOpenRegistryRejectsCorruptClientRecords(t *testing.T) {
	validClient := storedClient{
		OwnerID:              base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16)),
		Name:                 "client",
		PasswordSHA256:       base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)),
		CredentialGeneration: 1,
		Status:               ClientActive,
		CreatedAt:            testTime(1),
		UpdatedAt:            testTime(1),
	}

	tests := []struct {
		name   string
		mutate func(*registryFile)
	}{
		{
			name: "invalid digest",
			mutate: func(disk *registryFile) {
				disk.Clients[0].PasswordSHA256 = "plaintext"
			},
		},
		{
			name: "invalid status",
			mutate: func(disk *registryFile) {
				disk.Clients[0].Status = "unknown"
			},
		},
		{
			name: "zero generation",
			mutate: func(disk *registryFile) {
				disk.Clients[0].CredentialGeneration = 0
			},
		},
		{
			name: "duplicate owner",
			mutate: func(disk *registryFile) {
				duplicate := disk.Clients[0]
				duplicate.Name = "other"
				disk.Clients = append(disk.Clients, duplicate)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			disk := registryFile{Version: registryVersion, Clients: []storedClient{validClient}}
			test.mutate(&disk)
			data, err := json.Marshal(disk)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "registry.json")
			writeRegistryFile(t, path, data, 0600)
			if registry, err := OpenRegistry(path); err == nil {
				registry.Close()
				t.Fatal("OpenRegistry accepted corrupt client data")
			}
		})
	}
}

func testTime(second int64) time.Time {
	return time.Unix(second, 0).UTC()
}

func openTestRegistry(t *testing.T) *Registry {
	t.Helper()
	registry, err := OpenRegistry(filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return registry
}

func writeRegistryFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
