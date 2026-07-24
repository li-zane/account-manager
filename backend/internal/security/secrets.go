package security

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

// AESGCMBroker seals upstream provider credentials before they are persisted.
// The key is supplied by deployment configuration and is never serialized with
// a mailbox or backup target.
type AESGCMBroker struct {
	key        []byte
	keyVersion string
}

func NewAESGCMBroker(key []byte, keyVersion string) (*AESGCMBroker, error) {
	if len(key) != 16 && len(key) != 24 && len(key) != 32 {
		return nil, fmt.Errorf("%w: secret key must be 16, 24, or 32 bytes", domain.ErrInvalid)
	}
	if keyVersion == "" {
		return nil, fmt.Errorf("%w: key version is required", domain.ErrInvalid)
	}
	clone := append([]byte(nil), key...)
	return &AESGCMBroker{key: clone, keyVersion: keyVersion}, nil
}

func (b *AESGCMBroker) CurrentKeyVersion() string { return b.keyVersion }

func (b *AESGCMBroker) Seal(_ context.Context, plaintext []byte) ([]byte, string, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, "", fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", fmt.Errorf("create gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return sealed, b.keyVersion, nil
}

func (b *AESGCMBroker) Open(_ context.Context, sealed []byte, keyVersion string) ([]byte, error) {
	if keyVersion != b.keyVersion {
		return nil, fmt.Errorf("%w: unsupported secret key version %q", domain.ErrInvalid, keyVersion)
	}
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, fmt.Errorf("%w: sealed value is truncated", domain.ErrInvalid)
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("open secret: %w", err)
	}
	return plaintext, nil
}

var _ ports.SecretBroker = (*AESGCMBroker)(nil)

// PickupKeyService always stores a keyed digest for lookup. When a secret
// broker is configured it also stores an encrypted copy that is available only
// to explicit administrator reveal/export operations.
type PickupKeyService struct {
	repository  ports.PickupKeyRepository
	secrets     ports.SecretBroker
	pepper      []byte
	clock       func() time.Time
	ensureLocks sync.Map
}

func (s *PickupKeyService) SetSecretBroker(secrets ports.SecretBroker) {
	s.secrets = secrets
}

func NewPickupKeyService(repository ports.PickupKeyRepository, pepper []byte) (*PickupKeyService, error) {
	if repository == nil {
		return nil, fmt.Errorf("%w: pickup key repository is required", domain.ErrInvalid)
	}
	if len(pepper) < 16 {
		return nil, fmt.Errorf("%w: pickup key pepper must be at least 16 bytes", domain.ErrInvalid)
	}
	return &PickupKeyService{
		repository: repository,
		pepper:     append([]byte(nil), pepper...),
		clock:      time.Now,
	}, nil
}

func (s *PickupKeyService) SetClock(clock func() time.Time) {
	if clock != nil {
		s.clock = clock
	}
}

// Issue creates a random token. The raw token is returned to the caller and
// must not be logged or included in later mailbox responses.
func (s *PickupKeyService) Issue(ctx context.Context, mailboxID, label string, expiresAt *time.Time) (domain.MailboxPickupKey, string, error) {
	if mailboxID == "" {
		return domain.MailboxPickupKey{}, "", fmt.Errorf("%w: mailbox id is required", domain.ErrInvalid)
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return domain.MailboxPickupKey{}, "", fmt.Errorf("generate pickup key: %w", err)
	}
	token := "am_pk_" + base64.RawURLEncoding.EncodeToString(raw)
	digest := s.digest(token)
	idRaw := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, idRaw); err != nil {
		return domain.MailboxPickupKey{}, "", fmt.Errorf("generate pickup key id: %w", err)
	}
	id := "pkey_" + base64.RawURLEncoding.EncodeToString(idRaw)
	prefix := token
	if len(prefix) > 15 {
		prefix = prefix[:15]
	}
	key := domain.MailboxPickupKey{
		ID: id, MailboxID: mailboxID, Digest: digest, Prefix: prefix,
		Label: label, ExpiresAt: expiresAt, CreatedAt: s.clock().UTC(),
	}
	if s.secrets != nil {
		sealed, keyVersion, err := s.secrets.Seal(ctx, []byte(token))
		if err != nil {
			return domain.MailboxPickupKey{}, "", fmt.Errorf("seal pickup key: %w", err)
		}
		key.EncryptedToken, key.KeyVersion = sealed, keyVersion
	}
	if err := s.repository.CreatePickupKey(ctx, key); err != nil {
		return domain.MailboxPickupKey{}, "", err
	}
	return key, token, nil
}

// PrepareImported converts a legacy raw retrieval key into the same one-way
// digest used by newly issued platform keys. Persistence is left to the
// mailbox import transaction so the raw value never crosses into a repository.
func (s *PickupKeyService) PrepareImported(mailboxID, label, token string) (domain.MailboxPickupKey, error) {
	return s.PrepareImportedKey(context.Background(), mailboxID, label, token)
}

// PrepareImportedKey preserves the digest lookup contract and, when a secret
// broker is configured, also prepares a recoverable ciphertext for exports.
func (s *PickupKeyService) PrepareImportedKey(ctx context.Context, mailboxID, label, token string) (domain.MailboxPickupKey, error) {
	mailboxID = strings.TrimSpace(mailboxID)
	token = strings.TrimSpace(token)
	if mailboxID == "" || token == "" {
		return domain.MailboxPickupKey{}, fmt.Errorf("%w: mailbox id and imported pickup key are required", domain.ErrInvalid)
	}
	idRaw := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, idRaw); err != nil {
		return domain.MailboxPickupKey{}, fmt.Errorf("generate imported pickup key id: %w", err)
	}
	if strings.TrimSpace(label) == "" {
		label = "legacy import"
	}
	key := domain.MailboxPickupKey{
		ID: "pkey_" + base64.RawURLEncoding.EncodeToString(idRaw), MailboxID: mailboxID,
		Digest: s.digest(token), Prefix: "legacy", Label: strings.TrimSpace(label), CreatedAt: s.clock().UTC(),
	}
	if s.secrets != nil {
		sealed, keyVersion, err := s.secrets.Seal(ctx, []byte(token))
		if err != nil {
			return domain.MailboxPickupKey{}, fmt.Errorf("seal imported pickup key: %w", err)
		}
		key.EncryptedToken, key.KeyVersion = sealed, keyVersion
	}
	return key, nil
}

// Ensure creates an exportable automatic key only when the mailbox has no
// active encrypted key. Existing digest-only keys remain valid for lookup.
func (s *PickupKeyService) Ensure(ctx context.Context, mailboxID string) (domain.MailboxPickupKey, error) {
	if s.secrets == nil {
		return domain.MailboxPickupKey{}, fmt.Errorf("%w: pickup key secret broker", domain.ErrNotConfigured)
	}
	mailboxID = strings.TrimSpace(mailboxID)
	if mailboxID == "" {
		return domain.MailboxPickupKey{}, fmt.Errorf("%w: mailbox id is required", domain.ErrInvalid)
	}
	lockValue, _ := s.ensureLocks.LoadOrStore(mailboxID, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	items, err := s.List(ctx, mailboxID, ports.ListOptions{Limit: 500})
	if err != nil {
		return domain.MailboxPickupKey{}, err
	}
	now := s.clock().UTC()
	if item, ok := activeExportablePickupKey(items, now); ok {
		return item, nil
	}
	key, _, err := s.Issue(ctx, mailboxID, "automatic", nil)
	if errors.Is(err, domain.ErrConflict) {
		items, listErr := s.List(ctx, mailboxID, ports.ListOptions{Limit: 500})
		if listErr == nil {
			if item, ok := activeExportablePickupKey(items, now); ok {
				return item, nil
			}
		}
	}
	return key, err
}

func activeExportablePickupKey(items []domain.MailboxPickupKey, now time.Time) (domain.MailboxPickupKey, bool) {
	for _, item := range items {
		if item.RevokedAt == nil && (item.ExpiresAt == nil || item.ExpiresAt.After(now)) && len(item.EncryptedToken) > 0 && item.KeyVersion != "" {
			return item, true
		}
	}
	return domain.MailboxPickupKey{}, false
}

func (s *PickupKeyService) Reveal(ctx context.Context, mailboxID string) (string, error) {
	key, err := s.Ensure(ctx, mailboxID)
	if err != nil {
		return "", err
	}
	plaintext, err := s.secrets.Open(ctx, key.EncryptedToken, key.KeyVersion)
	if err != nil {
		return "", fmt.Errorf("open pickup key: %w", err)
	}
	defer clear(plaintext)
	return string(plaintext), nil
}

func (s *PickupKeyService) Lookup(ctx context.Context, token string) (domain.MailboxPickupKey, error) {
	if token == "" {
		return domain.MailboxPickupKey{}, fmt.Errorf("%w: pickup key is required", domain.ErrInvalid)
	}
	key, err := s.repository.FindPickupKeyByDigest(ctx, s.digest(token))
	if err != nil {
		return domain.MailboxPickupKey{}, err
	}
	now := s.clock().UTC()
	if key.RevokedAt != nil {
		return domain.MailboxPickupKey{}, domain.ErrKeyRevoked
	}
	if key.ExpiresAt != nil && !key.ExpiresAt.After(now) {
		return domain.MailboxPickupKey{}, domain.ErrKeyExpired
	}
	return key, nil
}

func (s *PickupKeyService) Revoke(ctx context.Context, mailboxID, keyID string) error {
	if mailboxID == "" || keyID == "" {
		return fmt.Errorf("%w: mailbox id and pickup key id are required", domain.ErrInvalid)
	}
	return s.repository.RevokePickupKey(ctx, mailboxID, keyID)
}

func (s *PickupKeyService) List(ctx context.Context, mailboxID string, options ports.ListOptions) ([]domain.MailboxPickupKey, error) {
	if mailboxID == "" {
		return nil, fmt.Errorf("%w: mailbox id is required", domain.ErrInvalid)
	}
	return s.repository.ListPickupKeys(ctx, mailboxID, options)
}

func (s *PickupKeyService) digest(token string) []byte {
	mac := hmac.New(sha256.New, s.pepper)
	_, _ = mac.Write([]byte(token))
	return mac.Sum(nil)
}
