package providers

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

type Registry struct {
	mu      sync.RWMutex
	entries map[domain.ProviderKey]ports.ProviderRegistration
}

func NewRegistry(registrations ...ports.ProviderRegistration) (*Registry, error) {
	r := &Registry{entries: make(map[domain.ProviderKey]ports.ProviderRegistration)}
	for _, registration := range registrations {
		if err := r.Register(registration); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Registry) Register(registration ports.ProviderRegistration) error {
	if registration.Provider == nil {
		return fmt.Errorf("%w: provider is required", domain.ErrInvalid)
	}
	descriptor := registration.Provider.Descriptor(context.Background())
	if descriptor.Key == "" {
		return fmt.Errorf("%w: provider key is required", domain.ErrInvalid)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[descriptor.Key]; exists {
		return fmt.Errorf("%w: provider %q already registered", domain.ErrConflict, descriptor.Key)
	}
	r.entries[descriptor.Key] = registration
	return nil
}

func (r *Registry) Get(key domain.ProviderKey) (ports.ProviderRegistration, error) {
	r.mu.RLock()
	registration, ok := r.entries[key]
	r.mu.RUnlock()
	if !ok {
		return ports.ProviderRegistration{}, fmt.Errorf("%w: provider %q", domain.ErrNotFound, key)
	}
	return registration, nil
}

func (r *Registry) List(ctx context.Context) []domain.ProviderDescriptor {
	r.mu.RLock()
	descriptors := make([]domain.ProviderDescriptor, 0, len(r.entries))
	for _, registration := range r.entries {
		descriptors = append(descriptors, registration.Provider.Descriptor(ctx))
	}
	r.mu.RUnlock()
	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].Key < descriptors[j].Key })
	return descriptors
}
