// Package identity provides a persistent credential registry.
package identity

import (
	"errors"
	"time"

	"ptymux/internal/remote"
)

var (
	ErrAuthenticationFailed = remote.ErrAuthenticationFailed
	ErrNameUnavailable      = remote.ErrClientNameUnavailable
	ErrInvalidName          = remote.ErrInvalidClientName
	ErrClosed               = errors.New("identity: registry closed")
)

// Status is a registration lifecycle status.
type Status = remote.ClientStatus

const (
	StatusPending = remote.StatusPending
	StatusActive  = remote.StatusActive
	StatusRevoked = remote.StatusRevoked
)

// Principal is an authenticated identity.
type Principal struct {
	OwnerID              string
	Name                 string
	CredentialGeneration uint64
}

// Registration contains generated credentials and their persisted state.
type Registration struct {
	OwnerID              string
	Name                 string
	CredentialGeneration uint64
	Status               Status
	CreatedAt            time.Time
	UpdatedAt            time.Time
	RevokedAt            *time.Time
	Password             string
}

// Registry is a process-locked persistent identity registry.
type Registry struct {
	registry *remote.Registry
}

// Open opens or creates a registry at path.
func Open(path string) (*Registry, error) {
	registry, err := remote.OpenRegistry(path)
	if err != nil {
		return nil, mapError(err)
	}
	return &Registry{registry: registry}, nil
}

// ValidName reports whether name is accepted by the registry.
func ValidName(name string) bool {
	return remote.ValidClientName(name)
}

// PrepareRegistration creates pending credentials. With no password it starts
// a fresh preparation, replacing an existing pending password while preserving
// its owner ID. Supplying the prior pending password recovers that preparation.
func (r *Registry) PrepareRegistration(name string, password ...string) (Registration, error) {
	if len(password) > 1 {
		return Registration{}, ErrAuthenticationFailed
	}
	var recoveryPassword string
	if len(password) == 1 {
		recoveryPassword = password[0]
	}
	record, preparedPassword, err := r.registry.PrepareRegistration(name, recoveryPassword)
	registration := registrationFor(record, preparedPassword)
	if err != nil {
		return registration, mapError(err)
	}
	return registration, nil
}

// CommitRegistration verifies pending credentials and activates them.
func (r *Registry) CommitRegistration(name, password string) (Principal, error) {
	principal, err := r.registry.CommitRegistration(name, password)
	return principalFor(principal), mapError(err)
}

// Authenticate authenticates active credentials.
func (r *Registry) Authenticate(name, password string) (Principal, error) {
	principal, err := r.registry.Authenticate(name, password)
	if err != nil {
		return Principal{}, mapError(err)
	}
	return principalFor(principal), nil
}

// Rotate replaces an active password and increments its credential generation.
func (r *Registry) Rotate(name, currentPassword string) (Registration, error) {
	password, principal, err := r.registry.Rotate(name, currentPassword)
	registration := Registration{
		OwnerID:              principal.OwnerID,
		Name:                 principal.Name,
		CredentialGeneration: principal.CredentialGeneration,
		Status:               StatusActive,
		Password:             password,
	}
	if err != nil {
		return registration, mapError(err)
	}
	return registration, nil
}

// Revoke revokes active credentials.
func (r *Registry) Revoke(name, currentPassword string) error {
	return mapError(r.registry.Revoke(name, currentPassword))
}

// Close releases the registry process lock.
func (r *Registry) Close() error {
	return mapError(r.registry.Close())
}

func registrationFor(record remote.ClientRecord, password string) Registration {
	return Registration{
		OwnerID:              record.OwnerID,
		Name:                 record.Name,
		CredentialGeneration: record.CredentialGeneration,
		Status:               record.Status,
		CreatedAt:            record.CreatedAt,
		UpdatedAt:            record.UpdatedAt,
		RevokedAt:            record.RevokedAt,
		Password:             password,
	}
}

func principalFor(principal remote.Principal) Principal {
	return Principal{
		OwnerID:              principal.OwnerID,
		Name:                 principal.Name,
		CredentialGeneration: principal.CredentialGeneration,
	}
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, remote.ErrRegistryClosed) {
		return ErrClosed
	}
	return err
}
