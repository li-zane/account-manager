package providers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/providers"
)

func TestRegistryListsProviderCapabilitiesAndRejectsDuplicates(t *testing.T) {
	registry, err := providers.NewRegistry(
		ports.ProviderRegistration{Provider: providers.MicrosoftAdapter{}, Retriever: providers.MicrosoftAdapter{}},
		ports.ProviderRegistration{Provider: providers.GmailAdapter{}, Retriever: providers.GmailAdapter{}},
		ports.ProviderRegistration{Provider: providers.CloudflareRouteAdapter{}, Retriever: providers.CloudflareRouteAdapter{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	descriptors := registry.List(context.Background())
	if len(descriptors) != 3 {
		t.Fatalf("provider count = %d, want 3", len(descriptors))
	}
	if descriptors[0].Key != domain.ProviderCloudflareRoute || descriptors[1].Key != domain.ProviderGmail || descriptors[2].Key != domain.ProviderMicrosoft {
		t.Fatalf("providers are not sorted by stable key: %+v", descriptors)
	}
	microsoft, err := registry.Get(domain.ProviderMicrosoft)
	if err != nil {
		t.Fatal(err)
	}
	if microsoft.Retriever == nil || len(microsoft.Retriever.RetrievalMethods()) != 3 {
		t.Fatalf("microsoft retrieval methods = %+v", microsoft.Retriever)
	}
	if err := registry.Register(ports.ProviderRegistration{Provider: providers.MicrosoftAdapter{}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate registration error = %v", err)
	}
	if _, err := registry.Get(domain.ProviderKey("missing")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing provider error = %v", err)
	}
}
