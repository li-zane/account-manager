package ports

import (
	"context"
	"io"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

// MailboxProvider is the provider-facing port. Implementations may provision
// mailboxes or aliases, but callers never depend on provider token formats.
type MailboxProvider interface {
	Descriptor(ctx context.Context) domain.ProviderDescriptor
	NormalizeAddress(address string) (string, error)
	Provision(ctx context.Context, request domain.ProvisionMailboxRequest) (domain.ProvisionMailboxResult, error)
}

// MailRetriever is intentionally separate from MailboxProvider so a route can
// delegate retrieval to a destination mailbox provider.
type MailRetriever interface {
	RetrievalMethods() []domain.RetrievalMethod
	Retrieve(ctx context.Context, mailbox domain.Mailbox, credential domain.MailboxCredential, query domain.MessageQuery) ([]domain.Message, error)
	Refresh(ctx context.Context, mailbox domain.Mailbox, credential domain.MailboxCredential) (domain.RefreshedCredential, error)
}

type MethodAccessTokenManager interface {
	EnsureAccessToken(context.Context, domain.Mailbox, domain.MailboxCredential, domain.RetrievalMethod, bool) (domain.RefreshedCredential, bool, error)
}

type IncrementalMailRetriever interface {
	SyncIncremental(context.Context, domain.Mailbox, domain.MailboxCredential, domain.MessageSyncRequest) (domain.MessageSyncResult, error)
}

type ProviderRegistration struct {
	Provider  MailboxProvider
	Retriever MailRetriever
}

type ProviderRegistry interface {
	Register(registration ProviderRegistration) error
	Get(key domain.ProviderKey) (ProviderRegistration, error)
	List(ctx context.Context) []domain.ProviderDescriptor
}

// DestinationResolver allows a routed provider (for example Cloudflare Email
// Routing) to resolve a parent mailbox without exposing provider credentials.
type DestinationResolver interface {
	ResolveDestination(ctx context.Context, alias domain.MailboxAlias) (domain.Mailbox, error)
}

// SecretBroker is the only port allowed to seal credential/config secrets.
type SecretBroker interface {
	Seal(ctx context.Context, plaintext []byte) (sealed []byte, keyVersion string, err error)
	Open(ctx context.Context, sealed []byte, keyVersion string) ([]byte, error)
	CurrentKeyVersion() string
}

// BackupStore abstracts object storage. S3 and WebDAV implementations can be
// registered later without changing the backup service or database schema.
type BackupStore interface {
	Put(ctx context.Context, objectKey string, body io.Reader) (BackupObject, error)
	Get(ctx context.Context, objectKey string) (io.ReadCloser, error)
	Delete(ctx context.Context, objectKey string) error
}

type BackupObject struct {
	ObjectKey string
	ETag      string
	SizeBytes int64
}
