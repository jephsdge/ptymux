package remote

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

const (
	registryVersion = 1
	maxRegistrySize = 4 << 20
)

var (
	ErrAuthenticationFailed  = errors.New("authentication_failed")
	ErrClientNameUnavailable = errors.New("remote: client name unavailable")
	ErrInvalidClientName     = errors.New("remote: invalid client name")

	errRegistryClosed = errors.New("remote: registry closed")
	dummyDigest       = sha256.Sum256([]byte("ptymux remote registry dummy credential"))
)

type ClientStatus string

const (
	StatusActive  ClientStatus = "active"
	StatusRevoked ClientStatus = "revoked"

	ClientActive  = StatusActive
	ClientRevoked = StatusRevoked
)

type ClientRecord struct {
	OwnerID              string       `json:"owner_id"`
	Name                 string       `json:"name"`
	CredentialGeneration uint64       `json:"credential_generation"`
	Status               ClientStatus `json:"status"`
	CreatedAt            time.Time    `json:"created_at"`
	UpdatedAt            time.Time    `json:"updated_at"`
	RevokedAt            *time.Time   `json:"revoked_at,omitempty"`
}

type Principal struct {
	OwnerID              string `json:"owner_id"`
	Name                 string `json:"name"`
	CredentialGeneration uint64 `json:"credential_generation"`
}

type committedRegistryError struct {
	err error
}

func (e *committedRegistryError) Error() string {
	return e.err.Error()
}

func (e *committedRegistryError) Unwrap() error {
	return e.err
}

func registryMutationCommitted(err error) bool {
	var committed *committedRegistryError
	return errors.As(err, &committed)
}

type storedClient struct {
	OwnerID              string       `json:"owner_id"`
	Name                 string       `json:"name"`
	PasswordSHA256       string       `json:"password_sha256"`
	CredentialGeneration uint64       `json:"credential_generation"`
	Status               ClientStatus `json:"status"`
	CreatedAt            time.Time    `json:"created_at"`
	UpdatedAt            time.Time    `json:"updated_at"`
	RevokedAt            *time.Time   `json:"revoked_at,omitempty"`
}

type registryFile struct {
	Version int            `json:"version"`
	Clients []storedClient `json:"clients"`
}

type Registry struct {
	mu            sync.RWMutex
	writeMu       sync.Mutex
	path          string
	lockFile      *os.File
	clients       map[string]storedClient
	syncDirectory func(string) error
	closed        bool
}

func OpenRegistry(path string) (*Registry, error) {
	if path == "" {
		return nil, errors.New("remote: empty registry path")
	}
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	if err := ensureSecureDirectory(parent); err != nil {
		return nil, err
	}

	lockFile, err := openLockFile(path + ".lock")
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
			_ = lockFile.Close()
		}
	}()

	r := &Registry{
		path:          path,
		lockFile:      lockFile,
		clients:       make(map[string]storedClient),
		syncDirectory: syncDirectory,
	}
	if err := r.load(); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if _, err := r.persist(r.clients); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("remote: inspect registry: %w", err)
	}

	cleanup = false
	return r, nil
}

func (r *Registry) Register(name string) (ClientRecord, string, error) {
	if !ValidClientName(name) {
		return ClientRecord{}, "", ErrInvalidClientName
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return ClientRecord{}, "", errRegistryClosed
	}
	if existing, ok := r.clients[name]; ok && existing.Status == ClientActive {
		r.mu.RUnlock()
		return ClientRecord{}, "", ErrClientNameUnavailable
	}
	clients := cloneClients(r.clients)
	r.mu.RUnlock()

	ownerID, err := randomToken(16)
	if err != nil {
		return ClientRecord{}, "", fmt.Errorf("remote: generate owner ID: %w", err)
	}
	password, err := randomToken(32)
	if err != nil {
		return ClientRecord{}, "", fmt.Errorf("remote: generate password: %w", err)
	}
	now := time.Now().UTC()
	client := storedClient{
		OwnerID:              ownerID,
		Name:                 name,
		PasswordSHA256:       digestString(password),
		CredentialGeneration: 1,
		Status:               ClientActive,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	clients[name] = client
	record := publicRecord(client)
	if err := r.persistAndPublish(clients); err != nil {
		if registryMutationCommitted(err) {
			return record, password, err
		}
		return ClientRecord{}, "", err
	}
	return record, password, nil
}

func (r *Registry) Authenticate(name, password string) (Principal, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return Principal{}, errRegistryClosed
	}
	return r.authenticateLocked(name, password)
}

func (r *Registry) Rotate(name, currentPassword string) (string, Principal, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return "", Principal{}, errRegistryClosed
	}
	principal, err := r.authenticateLocked(name, currentPassword)
	if err != nil {
		r.mu.RUnlock()
		return "", Principal{}, err
	}
	client := r.clients[name]
	clients := cloneClients(r.clients)
	r.mu.RUnlock()

	password, err := randomToken(32)
	if err != nil {
		return "", Principal{}, fmt.Errorf("remote: generate password: %w", err)
	}
	client.PasswordSHA256 = digestString(password)
	client.CredentialGeneration++
	client.UpdatedAt = time.Now().UTC()
	clients[name] = client
	principal.CredentialGeneration = client.CredentialGeneration
	if err := r.persistAndPublish(clients); err != nil {
		if registryMutationCommitted(err) {
			return password, principal, err
		}
		return "", Principal{}, err
	}
	return password, principal, nil
}

func (r *Registry) Revoke(name, currentPassword string) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return errRegistryClosed
	}
	if _, err := r.authenticateLocked(name, currentPassword); err != nil {
		r.mu.RUnlock()
		return err
	}
	client := r.clients[name]
	clients := cloneClients(r.clients)
	r.mu.RUnlock()

	now := time.Now().UTC()
	client.Status = ClientRevoked
	client.UpdatedAt = now
	client.RevokedAt = &now
	clients[name] = client
	return r.persistAndPublish(clients)
}

func (r *Registry) Close() error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()

	unlockErr := syscall.Flock(int(r.lockFile.Fd()), syscall.LOCK_UN)
	closeErr := r.lockFile.Close()
	if unlockErr != nil {
		return fmt.Errorf("remote: unlock registry: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("remote: close registry lock: %w", closeErr)
	}
	return nil
}

func (r *Registry) authenticateLocked(name, password string) (Principal, error) {
	actual := sha256.Sum256([]byte(password))
	expected := dummyDigest[:]
	client, exists := r.clients[name]
	valid := exists && client.Status == ClientActive && ValidClientName(name)
	if valid {
		decoded, err := base64.RawURLEncoding.DecodeString(client.PasswordSHA256)
		if err == nil && len(decoded) == sha256.Size {
			expected = decoded
		} else {
			valid = false
		}
	}
	matched := subtle.ConstantTimeCompare(actual[:], expected)
	if !valid || matched != 1 {
		return Principal{}, ErrAuthenticationFailed
	}
	return principalFor(client), nil
}

func (r *Registry) load() error {
	file, err := openSecureRegular(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("remote: stat registry: %w", err)
	}
	if info.Size() > maxRegistrySize {
		return fmt.Errorf("remote: registry exceeds %d bytes", maxRegistrySize)
	}

	var disk registryFile
	if err := decodeStrictJSON(io.LimitReader(file, maxRegistrySize), &disk); err != nil {
		return fmt.Errorf("remote: decode registry: %w", err)
	}
	if disk.Version != registryVersion {
		return fmt.Errorf("remote: unsupported registry version %d", disk.Version)
	}
	if disk.Clients == nil {
		return errors.New("remote: registry clients must be an array")
	}
	owners := make(map[string]struct{}, len(disk.Clients))
	for _, client := range disk.Clients {
		if err := validateStoredClient(client); err != nil {
			return fmt.Errorf("remote: invalid client record: %w", err)
		}
		if _, exists := r.clients[client.Name]; exists {
			return fmt.Errorf("remote: duplicate client name %q", client.Name)
		}
		if _, exists := owners[client.OwnerID]; exists {
			return fmt.Errorf("remote: duplicate owner ID %q", client.OwnerID)
		}
		owners[client.OwnerID] = struct{}{}
		r.clients[client.Name] = client
	}
	return nil
}

func (r *Registry) persistAndPublish(clients map[string]storedClient) error {
	replaced, err := r.persist(clients)
	if replaced {
		// The rename may be visible even when the directory sync fails. Keep
		// memory aligned with disk so a later mutation cannot overwrite it.
		r.mu.Lock()
		r.clients = clients
		r.mu.Unlock()
		if err != nil {
			return &committedRegistryError{err: err}
		}
	}
	return err
}

func (r *Registry) persist(clients map[string]storedClient) (bool, error) {
	disk := registryFile{Version: registryVersion, Clients: make([]storedClient, 0, len(clients))}
	names := make([]string, 0, len(clients))
	for name := range clients {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		disk.Clients = append(disk.Clients, clients[name])
	}
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return false, fmt.Errorf("remote: encode registry: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxRegistrySize {
		return false, fmt.Errorf("remote: registry exceeds %d bytes", maxRegistrySize)
	}

	parent := filepath.Dir(r.path)
	temp, err := os.CreateTemp(parent, ".registry-*")
	if err != nil {
		return false, fmt.Errorf("remote: create registry temp file: %w", err)
	}
	tempName := temp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return false, fmt.Errorf("remote: chmod registry temp file: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return false, fmt.Errorf("remote: write registry temp file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return false, fmt.Errorf("remote: sync registry temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return false, fmt.Errorf("remote: close registry temp file: %w", err)
	}
	if err := os.Rename(tempName, r.path); err != nil {
		return false, fmt.Errorf("remote: replace registry: %w", err)
	}
	removeTemp = false
	if err := r.syncDirectory(parent); err != nil {
		return true, err
	}
	return true, nil
}

func ensureSecureDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0700); err != nil {
			return fmt.Errorf("remote: create registry directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("remote: inspect registry directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("remote: registry parent is not a regular directory")
	}
	if info.Mode().Perm() != 0700 {
		if err := os.Chmod(path, 0700); err != nil {
			return fmt.Errorf("remote: chmod registry directory: %w", err)
		}
	}
	return nil
}

func openLockFile(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("remote: open registry lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if err := validateRegularFile(file, "registry lock"); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("remote: chmod registry lock: %w", err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("remote: lock registry: %w", err)
	}
	return file, nil
}

func openSecureRegular(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if err == syscall.ENOENT {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("remote: open registry: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if err := validateRegularFile(file, "registry"); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func validateRegularFile(file *os.File, description string) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("remote: stat %s: %w", description, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("remote: %s is not a regular file", description)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("remote: insecure %s permissions %04o", description, info.Mode().Perm())
	}
	return nil
}

func syncDirectory(path string) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("remote: open registry directory for sync: %w", err)
	}
	dir := os.NewFile(uintptr(fd), path)
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("remote: sync registry directory: %w", err)
	}
	return nil
}

func validateStoredClient(client storedClient) error {
	if !ValidClientName(client.Name) {
		return ErrInvalidClientName
	}
	ownerID, err := base64.RawURLEncoding.DecodeString(client.OwnerID)
	if err != nil || (len(ownerID) != 16 && len(ownerID) != 32) {
		return errors.New("invalid owner ID")
	}
	digest, err := base64.RawURLEncoding.DecodeString(client.PasswordSHA256)
	if err != nil || len(digest) != sha256.Size {
		return errors.New("invalid password digest")
	}
	if client.CredentialGeneration == 0 {
		return errors.New("invalid credential generation")
	}
	if client.CreatedAt.IsZero() || client.UpdatedAt.IsZero() || client.UpdatedAt.Before(client.CreatedAt) {
		return errors.New("invalid timestamps")
	}
	switch client.Status {
	case ClientActive:
		if client.RevokedAt != nil {
			return errors.New("active client has revocation timestamp")
		}
	case ClientRevoked:
		if client.RevokedAt == nil || client.RevokedAt.Before(client.CreatedAt) || client.UpdatedAt.Before(*client.RevokedAt) {
			return errors.New("revoked client has invalid revocation timestamp")
		}
	default:
		return errors.New("invalid client status")
	}
	return nil
}

func ValidClientName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !asciiAlphanumeric(c) && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func asciiAlphanumeric(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

func randomToken(size int) (string, error) {
	data := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func digestString(password string) string {
	digest := sha256.Sum256([]byte(password))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func publicRecord(client storedClient) ClientRecord {
	return ClientRecord{
		OwnerID:              client.OwnerID,
		Name:                 client.Name,
		CredentialGeneration: client.CredentialGeneration,
		Status:               client.Status,
		CreatedAt:            client.CreatedAt,
		UpdatedAt:            client.UpdatedAt,
		RevokedAt:            client.RevokedAt,
	}
}

func principalFor(client storedClient) Principal {
	return Principal{
		OwnerID:              client.OwnerID,
		Name:                 client.Name,
		CredentialGeneration: client.CredentialGeneration,
	}
}

func cloneClients(clients map[string]storedClient) map[string]storedClient {
	clone := make(map[string]storedClient, len(clients))
	for name, client := range clients {
		clone[name] = client
	}
	return clone
}
