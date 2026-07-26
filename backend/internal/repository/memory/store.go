package memory

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

// Store is a concurrency-safe in-memory adapter used for tests and local UI
// development. It enforces the important Postgres uniqueness/relationship
// constraints so behavior does not change when switching repositories.
type Store struct {
	mu                sync.RWMutex
	backupOperationMu sync.Mutex

	providerConnections   map[string]domain.ProviderConnection
	providerByIdentity    map[string]string
	appSettings           map[string]domain.AppSetting
	mailboxes             map[string]domain.Mailbox
	mailboxByIdentity     map[string]string
	aliases               map[string]domain.MailboxAlias
	aliasByIdentity       map[string]string
	credentials           map[string]domain.MailboxCredential
	pickupKeys            map[string]domain.MailboxPickupKey
	pickupByDigest        map[string]string
	cachedMessages        map[string]domain.CachedMessage
	messageSyncStates     map[string]domain.MessageSyncState
	retrievalCapabilities map[string]domain.MailboxRetrievalCapability
	accounts              map[string]domain.PlatformAccount
	platformCredentials   map[string]domain.PlatformAccountCredential
	formats               map[string]domain.MailboxFormat
	backupTargets         map[string]domain.BackupTarget
	backupRuns            map[string]domain.BackupRun
}

func New() *Store {
	now := time.Now().UTC()
	return &Store{
		providerConnections: make(map[string]domain.ProviderConnection),
		providerByIdentity:  make(map[string]string),
		appSettings: map[string]domain.AppSetting{
			domain.AppSettingKeyTokenRefresh: {
				Key: domain.AppSettingKeyTokenRefresh, Value: json.RawMessage(`{"enabled":true,"lead_time_minutes":5}`),
				Version: 1, UpdatedAt: now,
			},
			domain.AppSettingKeyBackupScheduler: {
				Key: domain.AppSettingKeyBackupScheduler, Value: json.RawMessage(`{"enabled":false,"max_parallel_runs":1}`),
				Version: 1, UpdatedAt: now,
			},
			domain.AppSettingKeyMessageProbe: {
				Key: domain.AppSettingKeyMessageProbe, Value: json.RawMessage(`{"enabled":false,"interval_minutes":10}`),
				Version: 1, UpdatedAt: now,
			},
		},
		mailboxes:             make(map[string]domain.Mailbox),
		mailboxByIdentity:     make(map[string]string),
		aliases:               make(map[string]domain.MailboxAlias),
		aliasByIdentity:       make(map[string]string),
		credentials:           make(map[string]domain.MailboxCredential),
		pickupKeys:            make(map[string]domain.MailboxPickupKey),
		pickupByDigest:        make(map[string]string),
		cachedMessages:        make(map[string]domain.CachedMessage),
		messageSyncStates:     make(map[string]domain.MessageSyncState),
		retrievalCapabilities: make(map[string]domain.MailboxRetrievalCapability),
		accounts:              make(map[string]domain.PlatformAccount),
		platformCredentials:   make(map[string]domain.PlatformAccountCredential),
		formats:               make(map[string]domain.MailboxFormat),
		backupTargets:         make(map[string]domain.BackupTarget),
		backupRuns:            make(map[string]domain.BackupRun),
	}
}

func (s *Store) GetAppSetting(ctx context.Context, key string) (domain.AppSetting, error) {
	if err := ctx.Err(); err != nil {
		return domain.AppSetting{}, err
	}
	key = strings.TrimSpace(key)
	s.mu.RLock()
	setting, exists := s.appSettings[key]
	s.mu.RUnlock()
	if !exists {
		return domain.AppSetting{}, fmt.Errorf("%w: app setting %q", domain.ErrNotFound, key)
	}
	return cloneAppSetting(setting), nil
}

func (s *Store) SaveAppSetting(ctx context.Context, setting domain.AppSetting, expectedVersion int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	setting.Key = strings.TrimSpace(setting.Key)
	if setting.Key == "" || !json.Valid(setting.Value) || expectedVersion < 0 {
		return fmt.Errorf("%w: valid app setting key, value, and version are required", domain.ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.appSettings[setting.Key]
	if !exists {
		if expectedVersion != 0 {
			return fmt.Errorf("%w: app setting %q", domain.ErrNotFound, setting.Key)
		}
		setting.Version = 1
		s.appSettings[setting.Key] = cloneAppSetting(setting)
		return nil
	}
	if current.Version != expectedVersion {
		return fmt.Errorf("%w: app setting version changed", domain.ErrConflict)
	}
	setting.Version = current.Version + 1
	s.appSettings[setting.Key] = cloneAppSetting(setting)
	return nil
}

func (s *Store) Ping(ctx context.Context) error { return ctx.Err() }

func (s *Store) CreateProviderConnection(ctx context.Context, connection domain.ProviderConnection) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	connection.Version = 1
	metadata, err := normalizeProviderConnectionMetadata(connection.Metadata)
	if err != nil {
		return err
	}
	connection.Metadata = metadata
	if err := validateProviderConnection(connection); err != nil {
		return err
	}
	identity := providerConnectionIdentity(connection.Provider, connection.Name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.providerConnections[connection.ID]; exists {
		return fmt.Errorf("%w: provider connection id %q", domain.ErrConflict, connection.ID)
	}
	if _, exists := s.providerByIdentity[identity]; exists {
		return fmt.Errorf("%w: provider connection %s/%s", domain.ErrConflict, connection.Provider, connection.Name)
	}
	s.providerConnections[connection.ID] = cloneProviderConnection(connection)
	s.providerByIdentity[identity] = connection.ID
	return nil
}

func (s *Store) GetProviderConnection(ctx context.Context, id string) (domain.ProviderConnection, error) {
	if err := ctx.Err(); err != nil {
		return domain.ProviderConnection{}, err
	}
	s.mu.RLock()
	connection, ok := s.providerConnections[id]
	s.mu.RUnlock()
	if !ok {
		return domain.ProviderConnection{}, fmt.Errorf("%w: provider connection %q", domain.ErrNotFound, id)
	}
	return cloneProviderConnection(connection), nil
}

func (s *Store) GetProviderConnectionByProviderAndName(ctx context.Context, provider domain.ProviderKey, name string) (domain.ProviderConnection, error) {
	if err := ctx.Err(); err != nil {
		return domain.ProviderConnection{}, err
	}
	identity := providerConnectionIdentity(provider, name)
	s.mu.RLock()
	id, ok := s.providerByIdentity[identity]
	connection := s.providerConnections[id]
	s.mu.RUnlock()
	if !ok {
		return domain.ProviderConnection{}, fmt.Errorf("%w: provider connection %s/%s", domain.ErrNotFound, provider, name)
	}
	return cloneProviderConnection(connection), nil
}

func (s *Store) ListProviderConnections(ctx context.Context, filter ports.ProviderConnectionFilter, options ports.ListOptions) ([]domain.ProviderConnection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	items := make([]domain.ProviderConnection, 0, len(s.providerConnections))
	for _, connection := range s.providerConnections {
		if filter.Provider != "" && connection.Provider != filter.Provider {
			continue
		}
		if filter.Enabled != nil && connection.Enabled != *filter.Enabled {
			continue
		}
		items = append(items, cloneProviderConnection(connection))
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].Provider != items[j].Provider {
			return items[i].Provider < items[j].Provider
		}
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].ID < items[j].ID
	})
	return paginate(items, options), nil
}

func (s *Store) UpdateProviderConnection(ctx context.Context, connection domain.ProviderConnection, expectedVersion int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	metadata, err := normalizeProviderConnectionMetadata(connection.Metadata)
	if err != nil {
		return err
	}
	connection.Metadata = metadata
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.providerConnections[connection.ID]
	if !exists {
		return fmt.Errorf("%w: provider connection %q", domain.ErrNotFound, connection.ID)
	}
	if connection.Provider != current.Provider {
		return fmt.Errorf("%w: provider connection provider is immutable", domain.ErrInvalid)
	}
	if current.Version != expectedVersion {
		return fmt.Errorf("%w: provider connection version changed", domain.ErrConflict)
	}
	connection.Version = current.Version + 1
	connection.CreatedAt = current.CreatedAt
	if err := validateProviderConnection(connection); err != nil {
		return err
	}
	oldIdentity := providerConnectionIdentity(current.Provider, current.Name)
	newIdentity := providerConnectionIdentity(connection.Provider, connection.Name)
	if id, taken := s.providerByIdentity[newIdentity]; taken && id != connection.ID {
		return fmt.Errorf("%w: provider connection %s/%s", domain.ErrConflict, connection.Provider, connection.Name)
	}
	delete(s.providerByIdentity, oldIdentity)
	s.providerByIdentity[newIdentity] = connection.ID
	s.providerConnections[connection.ID] = cloneProviderConnection(connection)
	return nil
}

func (s *Store) CreateMailbox(ctx context.Context, mailbox domain.Mailbox) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	identity := mailboxIdentity(mailbox.Provider, mailbox.NormalizedAddress)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.mailboxes[mailbox.ID]; exists {
		return fmt.Errorf("%w: mailbox id %q", domain.ErrConflict, mailbox.ID)
	}
	if _, exists := s.mailboxByIdentity[identity]; exists {
		return fmt.Errorf("%w: mailbox %s", domain.ErrConflict, identity)
	}
	s.mailboxes[mailbox.ID] = cloneMailbox(mailbox)
	s.mailboxByIdentity[identity] = mailbox.ID
	return nil
}

func (s *Store) GetMailbox(ctx context.Context, id string) (domain.Mailbox, error) {
	if err := ctx.Err(); err != nil {
		return domain.Mailbox{}, err
	}
	s.mu.RLock()
	mailbox, ok := s.mailboxes[id]
	s.mu.RUnlock()
	if !ok {
		return domain.Mailbox{}, fmt.Errorf("%w: mailbox %q", domain.ErrNotFound, id)
	}
	return cloneMailbox(mailbox), nil
}

func (s *Store) GetMailboxByIdentity(ctx context.Context, provider domain.ProviderKey, normalizedAddress string) (domain.Mailbox, error) {
	if err := ctx.Err(); err != nil {
		return domain.Mailbox{}, err
	}
	s.mu.RLock()
	id, ok := s.mailboxByIdentity[mailboxIdentity(provider, normalizedAddress)]
	mailbox := s.mailboxes[id]
	s.mu.RUnlock()
	if !ok {
		return domain.Mailbox{}, fmt.Errorf("%w: mailbox %s/%s", domain.ErrNotFound, provider, normalizedAddress)
	}
	return cloneMailbox(mailbox), nil
}

func (s *Store) ListMailboxes(ctx context.Context, options ports.ListOptions) ([]domain.Mailbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	items := make([]domain.Mailbox, 0, len(s.mailboxes))
	for _, mailbox := range s.mailboxes {
		items = append(items, cloneMailbox(mailbox))
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return paginate(items, options), nil
}

func (s *Store) CountMailboxes(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.RLock()
	count := int64(len(s.mailboxes))
	s.mu.RUnlock()
	return count, nil
}

func (s *Store) CreateAlias(ctx context.Context, alias domain.MailboxAlias) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	identity := mailboxIdentity(alias.Provider, alias.NormalizedAddress)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.mailboxes[alias.MailboxID]; !exists {
		return fmt.Errorf("%w: mailbox %q", domain.ErrNotFound, alias.MailboxID)
	}
	if _, exists := s.aliases[alias.ID]; exists {
		return fmt.Errorf("%w: alias id %q", domain.ErrConflict, alias.ID)
	}
	if _, exists := s.aliasByIdentity[identity]; exists {
		return fmt.Errorf("%w: alias %s", domain.ErrConflict, identity)
	}
	s.aliases[alias.ID] = cloneAlias(alias)
	s.aliasByIdentity[identity] = alias.ID
	return nil
}

func (s *Store) GetAlias(ctx context.Context, id string) (domain.MailboxAlias, error) {
	if err := ctx.Err(); err != nil {
		return domain.MailboxAlias{}, err
	}
	s.mu.RLock()
	alias, ok := s.aliases[id]
	s.mu.RUnlock()
	if !ok {
		return domain.MailboxAlias{}, fmt.Errorf("%w: mailbox alias %q", domain.ErrNotFound, id)
	}
	return cloneAlias(alias), nil
}

func (s *Store) ListAliases(ctx context.Context, mailboxID string, options ports.ListOptions) ([]domain.MailboxAlias, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	items := make([]domain.MailboxAlias, 0)
	for _, alias := range s.aliases {
		if alias.MailboxID == mailboxID {
			items = append(items, cloneAlias(alias))
		}
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return paginate(items, options), nil
}

func (s *Store) CountAliases(ctx context.Context, mailboxID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.RLock()
	var count int64
	for _, alias := range s.aliases {
		if mailboxID == "" || alias.MailboxID == mailboxID {
			count++
		}
	}
	s.mu.RUnlock()
	return count, nil
}

func (s *Store) UpsertCredential(ctx context.Context, credential domain.MailboxCredential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.mailboxes[credential.MailboxID]; !exists {
		return fmt.Errorf("%w: mailbox %q", domain.ErrNotFound, credential.MailboxID)
	}
	identity := credentialIdentity(credential.MailboxID, credential.Kind)
	existing, exists := s.credentials[identity]
	if exists {
		if credential.Version != existing.Version {
			return fmt.Errorf("%w: credential version changed", domain.ErrConflict)
		}
		credential.Version = existing.Version + 1
		credential.CreatedAt = existing.CreatedAt
	} else {
		if credential.Version != 0 {
			return fmt.Errorf("%w: new credential version must be zero", domain.ErrConflict)
		}
		credential.Version = 1
	}
	s.credentials[identity] = cloneCredential(credential)
	return nil
}

func (s *Store) GetCredential(ctx context.Context, mailboxID string, kind domain.CredentialKind) (domain.MailboxCredential, error) {
	if err := ctx.Err(); err != nil {
		return domain.MailboxCredential{}, err
	}
	s.mu.RLock()
	credential, ok := s.credentials[credentialIdentity(mailboxID, kind)]
	s.mu.RUnlock()
	if !ok {
		return domain.MailboxCredential{}, fmt.Errorf("%w: credential %s/%s", domain.ErrNotFound, mailboxID, kind)
	}
	return cloneCredential(credential), nil
}

func (s *Store) ListCredentials(ctx context.Context, mailboxID string) ([]domain.MailboxCredential, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	items := make([]domain.MailboxCredential, 0)
	for _, credential := range s.credentials {
		if credential.MailboxID == mailboxID {
			items = append(items, cloneCredential(credential))
		}
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].Kind < items[j].Kind })
	return items, nil
}

func (s *Store) ImportMailboxes(ctx context.Context, items []domain.MailboxImportItem, strategy domain.ConflictStrategy) (domain.MailboxImportResult, error) {
	if strategy != domain.ConflictSkip && strategy != domain.ConflictUpdate && strategy != domain.ConflictError {
		return domain.MailboxImportResult{}, fmt.Errorf("%w: conflict strategy %q", domain.ErrInvalid, strategy)
	}
	if err := ctx.Err(); err != nil {
		return domain.MailboxImportResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	mailboxes := make(map[string]domain.Mailbox, len(s.mailboxes)+len(items))
	for id, mailbox := range s.mailboxes {
		mailboxes[id] = cloneMailbox(mailbox)
	}
	identities := make(map[string]string, len(s.mailboxByIdentity)+len(items))
	for identity, id := range s.mailboxByIdentity {
		identities[identity] = id
	}
	credentials := make(map[string]domain.MailboxCredential, len(s.credentials)+len(items))
	for identity, credential := range s.credentials {
		credentials[identity] = cloneCredential(credential)
	}
	accounts := make(map[string]domain.PlatformAccount, len(s.accounts)+len(items))
	for id, account := range s.accounts {
		accounts[id] = cloneAccount(account)
	}
	platformCredentials := make(map[string]domain.PlatformAccountCredential, len(s.platformCredentials)+len(items))
	for identity, credential := range s.platformCredentials {
		platformCredentials[identity] = clonePlatformCredential(credential)
	}
	pickupKeys := make(map[string]domain.MailboxPickupKey, len(s.pickupKeys)+len(items))
	for id, key := range s.pickupKeys {
		pickupKeys[id] = clonePickupKey(key)
	}
	pickupByDigest := make(map[string]string, len(s.pickupByDigest)+len(items))
	for digest, id := range s.pickupByDigest {
		pickupByDigest[digest] = id
	}

	result := domain.MailboxImportResult{MailboxIDs: make([]string, 0, len(items))}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return domain.MailboxImportResult{}, err
		}
		identity := mailboxIdentity(item.Mailbox.Provider, item.Mailbox.NormalizedAddress)
		existingID, exists := identities[identity]
		if exists {
			switch strategy {
			case domain.ConflictSkip:
				result.Skipped++
				continue
			case domain.ConflictError:
				return domain.MailboxImportResult{}, fmt.Errorf("%w: mailbox %s", domain.ErrConflict, identity)
			case domain.ConflictUpdate:
				existing := mailboxes[existingID]
				updated := cloneMailbox(item.Mailbox)
				updated.ID = existingID
				updated.CreatedAt = existing.CreatedAt
				mailboxes[existingID] = updated
				result.Updated++
				result.MailboxIDs = append(result.MailboxIDs, existingID)
				if item.Credential != nil {
					credential := cloneCredential(*item.Credential)
					credential.MailboxID = existingID
					key := credentialIdentity(existingID, credential.Kind)
					if current, ok := credentials[key]; ok {
						credential.ID = current.ID
						credential.Version = current.Version + 1
						credential.CreatedAt = current.CreatedAt
					} else {
						credential.Version = 1
					}
					credentials[key] = credential
				}
				applyImportedPlatform(item, existingID, accounts, platformCredentials)
				if err := applyImportedPickupKey(item, existingID, pickupKeys, pickupByDigest); err != nil {
					return domain.MailboxImportResult{}, err
				}
			}
			continue
		}
		if _, idCollision := mailboxes[item.Mailbox.ID]; idCollision {
			return domain.MailboxImportResult{}, fmt.Errorf("%w: mailbox id collision %q", domain.ErrConflict, item.Mailbox.ID)
		}
		mailbox := cloneMailbox(item.Mailbox)
		mailboxes[mailbox.ID] = mailbox
		identities[identity] = mailbox.ID
		result.Created++
		result.MailboxIDs = append(result.MailboxIDs, mailbox.ID)
		if item.Credential != nil {
			credential := cloneCredential(*item.Credential)
			credential.MailboxID = mailbox.ID
			credential.Version = 1
			credentials[credentialIdentity(mailbox.ID, credential.Kind)] = credential
		}
		applyImportedPlatform(item, mailbox.ID, accounts, platformCredentials)
		if err := applyImportedPickupKey(item, mailbox.ID, pickupKeys, pickupByDigest); err != nil {
			return domain.MailboxImportResult{}, err
		}
	}
	s.mailboxes = mailboxes
	s.mailboxByIdentity = identities
	s.credentials = credentials
	s.accounts = accounts
	s.platformCredentials = platformCredentials
	s.pickupKeys = pickupKeys
	s.pickupByDigest = pickupByDigest
	return result, nil
}

func applyImportedPickupKey(item domain.MailboxImportItem, mailboxID string, keys map[string]domain.MailboxPickupKey, byDigest map[string]string) error {
	if item.PickupKey == nil {
		return nil
	}
	key := clonePickupKey(*item.PickupKey)
	key.MailboxID = mailboxID
	digest := hex.EncodeToString(key.Digest)
	if existingID, exists := byDigest[digest]; exists {
		if keys[existingID].MailboxID == mailboxID {
			return nil
		}
		return fmt.Errorf("%w: imported pickup key belongs to another mailbox", domain.ErrConflict)
	}
	if _, exists := keys[key.ID]; exists {
		return fmt.Errorf("%w: pickup key id %q", domain.ErrConflict, key.ID)
	}
	keys[key.ID] = key
	byDigest[digest] = key.ID
	return nil
}

func applyImportedPlatform(item domain.MailboxImportItem, mailboxID string, accounts map[string]domain.PlatformAccount, credentials map[string]domain.PlatformAccountCredential) {
	if item.PlatformAccount == nil {
		return
	}
	account := cloneAccount(*item.PlatformAccount)
	account.MailboxID = mailboxID
	if current, exists := accounts[account.ID]; exists {
		account.CreatedAt = current.CreatedAt
	}
	accounts[account.ID] = account
	if item.PlatformCredential == nil {
		return
	}
	credential := clonePlatformCredential(*item.PlatformCredential)
	credential.PlatformAccountID = account.ID
	key := platformCredentialIdentity(account.ID, credential.Kind)
	if current, exists := credentials[key]; exists {
		credential.ID = current.ID
		credential.CreatedAt = current.CreatedAt
	}
	credentials[key] = credential
}

func (s *Store) CreateMailboxFormat(ctx context.Context, format domain.MailboxFormat) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.formats[format.ID]; exists {
		return fmt.Errorf("%w: mailbox format id %q", domain.ErrConflict, format.ID)
	}
	for _, existing := range s.formats {
		if existing.Name == format.Name {
			return fmt.Errorf("%w: mailbox format name %q", domain.ErrConflict, format.Name)
		}
	}
	format.Version = 1
	s.formats[format.ID] = cloneFormat(format)
	return nil
}

func (s *Store) GetMailboxFormat(ctx context.Context, id string) (domain.MailboxFormat, error) {
	if err := ctx.Err(); err != nil {
		return domain.MailboxFormat{}, err
	}
	s.mu.RLock()
	format, ok := s.formats[id]
	s.mu.RUnlock()
	if !ok {
		return domain.MailboxFormat{}, fmt.Errorf("%w: mailbox format %q", domain.ErrNotFound, id)
	}
	return cloneFormat(format), nil
}

func (s *Store) ListMailboxFormats(ctx context.Context, options ports.ListOptions) ([]domain.MailboxFormat, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	items := make([]domain.MailboxFormat, 0, len(s.formats))
	for _, format := range s.formats {
		items = append(items, cloneFormat(format))
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].ID < items[j].ID
		}
		return items[i].Name < items[j].Name
	})
	return paginate(items, options), nil
}

func (s *Store) UpdateMailboxFormat(ctx context.Context, format domain.MailboxFormat, expectedVersion int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.formats[format.ID]
	if !exists {
		return fmt.Errorf("%w: mailbox format %q", domain.ErrNotFound, format.ID)
	}
	if current.Version != expectedVersion {
		return fmt.Errorf("%w: mailbox format version changed", domain.ErrConflict)
	}
	for id, existing := range s.formats {
		if id != format.ID && existing.Name == format.Name {
			return fmt.Errorf("%w: mailbox format name %q", domain.ErrConflict, format.Name)
		}
	}
	format.Version = current.Version + 1
	format.CreatedAt = current.CreatedAt
	s.formats[format.ID] = cloneFormat(format)
	return nil
}

func (s *Store) CreatePickupKey(ctx context.Context, key domain.MailboxPickupKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	digest := hex.EncodeToString(key.Digest)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.mailboxes[key.MailboxID]; !exists {
		return fmt.Errorf("%w: mailbox %q", domain.ErrNotFound, key.MailboxID)
	}
	if _, exists := s.pickupKeys[key.ID]; exists {
		return fmt.Errorf("%w: pickup key id %q", domain.ErrConflict, key.ID)
	}
	if _, exists := s.pickupByDigest[digest]; exists {
		return fmt.Errorf("%w: pickup key digest", domain.ErrConflict)
	}
	s.pickupKeys[key.ID] = clonePickupKey(key)
	s.pickupByDigest[digest] = key.ID
	return nil
}

func (s *Store) FindPickupKeyByDigest(ctx context.Context, digest []byte) (domain.MailboxPickupKey, error) {
	if err := ctx.Err(); err != nil {
		return domain.MailboxPickupKey{}, err
	}
	s.mu.RLock()
	id, ok := s.pickupByDigest[hex.EncodeToString(digest)]
	key := s.pickupKeys[id]
	s.mu.RUnlock()
	if !ok || !bytes.Equal(key.Digest, digest) {
		return domain.MailboxPickupKey{}, fmt.Errorf("%w: pickup key", domain.ErrNotFound)
	}
	return clonePickupKey(key), nil
}

func (s *Store) RevokePickupKey(ctx context.Context, mailboxID, keyID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.pickupKeys[keyID]
	if !ok || key.MailboxID != mailboxID {
		return fmt.Errorf("%w: pickup key %q", domain.ErrNotFound, keyID)
	}
	if key.RevokedAt == nil {
		now := time.Now().UTC()
		key.RevokedAt = &now
		s.pickupKeys[keyID] = key
	}
	return nil
}

func (s *Store) ListPickupKeys(ctx context.Context, mailboxID string, options ports.ListOptions) ([]domain.MailboxPickupKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	items := make([]domain.MailboxPickupKey, 0)
	for _, key := range s.pickupKeys {
		if key.MailboxID == mailboxID {
			items = append(items, clonePickupKey(key))
		}
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return paginate(items, options), nil
}

func (s *Store) CreatePlatformAccount(ctx context.Context, account domain.PlatformAccount) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.accounts[account.ID]; exists {
		return fmt.Errorf("%w: platform account %q", domain.ErrConflict, account.ID)
	}
	if account.MailboxID != "" {
		if _, exists := s.mailboxes[account.MailboxID]; !exists {
			return fmt.Errorf("%w: mailbox %q", domain.ErrNotFound, account.MailboxID)
		}
	}
	if account.MailboxAliasID != nil {
		if account.MailboxID == "" {
			return fmt.Errorf("%w: mailbox id is required when alias is set", domain.ErrInvalid)
		}
		alias, exists := s.aliases[*account.MailboxAliasID]
		if !exists || alias.MailboxID != account.MailboxID {
			return fmt.Errorf("%w: mailbox alias %q is not attached to mailbox", domain.ErrInvalid, *account.MailboxAliasID)
		}
	}
	s.accounts[account.ID] = cloneAccount(account)
	return nil
}

func (s *Store) GetPlatformAccount(ctx context.Context, id string) (domain.PlatformAccount, error) {
	if err := ctx.Err(); err != nil {
		return domain.PlatformAccount{}, err
	}
	s.mu.RLock()
	account, ok := s.accounts[id]
	s.mu.RUnlock()
	if !ok {
		return domain.PlatformAccount{}, fmt.Errorf("%w: platform account %q", domain.ErrNotFound, id)
	}
	return cloneAccount(account), nil
}

func (s *Store) ListPlatformAccounts(ctx context.Context, platform string, options ports.ListOptions) ([]domain.PlatformAccount, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	items := make([]domain.PlatformAccount, 0)
	for _, account := range s.accounts {
		if platform == "" || account.Platform == platform {
			items = append(items, cloneAccount(account))
		}
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return paginate(items, options), nil
}

func (s *Store) ListPlatformAccountsByMailbox(ctx context.Context, mailboxID string, options ports.ListOptions) ([]domain.PlatformAccount, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	items := make([]domain.PlatformAccount, 0)
	for _, account := range s.accounts {
		if account.MailboxID == mailboxID {
			items = append(items, cloneAccount(account))
		}
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return paginate(items, options), nil
}

func (s *Store) UpsertPlatformAccountCredential(ctx context.Context, credential domain.PlatformAccountCredential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.accounts[credential.PlatformAccountID]; !exists {
		return fmt.Errorf("%w: platform account %q", domain.ErrNotFound, credential.PlatformAccountID)
	}
	key := platformCredentialIdentity(credential.PlatformAccountID, credential.Kind)
	if current, exists := s.platformCredentials[key]; exists {
		credential.ID = current.ID
		credential.CreatedAt = current.CreatedAt
	}
	s.platformCredentials[key] = clonePlatformCredential(credential)
	return nil
}

func (s *Store) GetPlatformAccountCredential(ctx context.Context, platformAccountID, kind string) (domain.PlatformAccountCredential, error) {
	if err := ctx.Err(); err != nil {
		return domain.PlatformAccountCredential{}, err
	}
	s.mu.RLock()
	credential, ok := s.platformCredentials[platformCredentialIdentity(platformAccountID, kind)]
	s.mu.RUnlock()
	if !ok {
		return domain.PlatformAccountCredential{}, fmt.Errorf("%w: platform account credential %s/%s", domain.ErrNotFound, platformAccountID, kind)
	}
	return clonePlatformCredential(credential), nil
}

func (s *Store) CreateBackupTarget(ctx context.Context, target domain.BackupTarget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.backupTargets[target.ID]; exists {
		return fmt.Errorf("%w: backup target %q", domain.ErrConflict, target.ID)
	}
	for _, existing := range s.backupTargets {
		if existing.Name == target.Name {
			return fmt.Errorf("%w: backup target name %q", domain.ErrConflict, target.Name)
		}
	}
	target.Version = 1
	s.backupTargets[target.ID] = cloneBackupTarget(target)
	return nil
}

func (s *Store) GetBackupTarget(ctx context.Context, id string) (domain.BackupTarget, error) {
	if err := ctx.Err(); err != nil {
		return domain.BackupTarget{}, err
	}
	s.mu.RLock()
	target, ok := s.backupTargets[id]
	s.mu.RUnlock()
	if !ok {
		return domain.BackupTarget{}, fmt.Errorf("%w: backup target %q", domain.ErrNotFound, id)
	}
	return cloneBackupTarget(target), nil
}

func (s *Store) ListBackupTargets(ctx context.Context, options ports.ListOptions) ([]domain.BackupTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	items := make([]domain.BackupTarget, 0, len(s.backupTargets))
	for _, target := range s.backupTargets {
		items = append(items, cloneBackupTarget(target))
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return paginate(items, options), nil
}

func (s *Store) UpdateBackupTarget(ctx context.Context, target domain.BackupTarget, expectedVersion int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.backupTargets[target.ID]
	if !exists {
		return fmt.Errorf("%w: backup target %q", domain.ErrNotFound, target.ID)
	}
	if current.Version != expectedVersion {
		return fmt.Errorf("%w: backup target version changed", domain.ErrConflict)
	}
	for id, existing := range s.backupTargets {
		if id != target.ID && existing.Name == target.Name {
			return fmt.Errorf("%w: backup target name %q", domain.ErrConflict, target.Name)
		}
	}
	target.Version = current.Version + 1
	target.CreatedAt = current.CreatedAt
	s.backupTargets[target.ID] = cloneBackupTarget(target)
	return nil
}

func (s *Store) CreateBackupRun(ctx context.Context, run domain.BackupRun) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.backupTargets[run.TargetID]; !exists {
		return fmt.Errorf("%w: backup target %q", domain.ErrNotFound, run.TargetID)
	}
	if _, exists := s.backupRuns[run.ID]; exists {
		return fmt.Errorf("%w: backup run %q", domain.ErrConflict, run.ID)
	}
	for _, existing := range s.backupRuns {
		if existing.TargetID == run.TargetID && (existing.State == domain.BackupRunPending || existing.State == domain.BackupRunRunning) {
			return fmt.Errorf("%w: backup target %q already has an active run", domain.ErrConflict, run.TargetID)
		}
	}
	s.backupRuns[run.ID] = cloneBackupRun(run)
	return nil
}

func (s *Store) CreateScheduledBackupRun(ctx context.Context, run domain.BackupRun, dueAt time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.backupTargets[run.TargetID]; !exists {
		return false, fmt.Errorf("%w: backup target %q", domain.ErrNotFound, run.TargetID)
	}
	for _, existing := range s.backupRuns {
		if existing.TargetID == run.TargetID && (existing.State == domain.BackupRunPending || existing.State == domain.BackupRunRunning) {
			return false, nil
		}
		if existing.TargetID == run.TargetID && !existing.CreatedAt.Before(dueAt) {
			return false, nil
		}
	}
	if _, exists := s.backupRuns[run.ID]; exists {
		return false, fmt.Errorf("%w: backup run %q", domain.ErrConflict, run.ID)
	}
	s.backupRuns[run.ID] = cloneBackupRun(run)
	return true, nil
}

func (s *Store) ClaimPendingBackupRun(ctx context.Context, startedAt time.Time) (domain.BackupRun, error) {
	if err := ctx.Err(); err != nil {
		return domain.BackupRun{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var selected domain.BackupRun
	found := false
	for _, run := range s.backupRuns {
		if run.State != domain.BackupRunPending {
			continue
		}
		if !found || run.CreatedAt.Before(selected.CreatedAt) || (run.CreatedAt.Equal(selected.CreatedAt) && run.ID < selected.ID) {
			selected = run
			found = true
		}
	}
	if !found {
		return domain.BackupRun{}, fmt.Errorf("%w: pending backup run", domain.ErrNotFound)
	}
	startedAt = startedAt.UTC()
	selected.State = domain.BackupRunRunning
	selected.StartedAt = &startedAt
	selected.UpdatedAt = startedAt
	s.backupRuns[selected.ID] = cloneBackupRun(selected)
	return cloneBackupRun(selected), nil
}

func (s *Store) GetBackupRun(ctx context.Context, id string) (domain.BackupRun, error) {
	if err := ctx.Err(); err != nil {
		return domain.BackupRun{}, err
	}
	s.mu.RLock()
	run, ok := s.backupRuns[id]
	s.mu.RUnlock()
	if !ok {
		return domain.BackupRun{}, fmt.Errorf("%w: backup run %q", domain.ErrNotFound, id)
	}
	return cloneBackupRun(run), nil
}

func (s *Store) ListBackupRuns(ctx context.Context, targetID string, options ports.ListOptions) ([]domain.BackupRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	items := make([]domain.BackupRun, 0)
	for _, run := range s.backupRuns {
		if targetID == "" || run.TargetID == targetID {
			items = append(items, cloneBackupRun(run))
		}
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return paginate(items, options), nil
}

func (s *Store) UpdateBackupRun(ctx context.Context, run domain.BackupRun) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.backupRuns[run.ID]
	if !exists {
		return fmt.Errorf("%w: backup run %q", domain.ErrNotFound, run.ID)
	}
	if current.TargetID != run.TargetID {
		return fmt.Errorf("%w: backup run target is immutable", domain.ErrInvalid)
	}
	s.backupRuns[run.ID] = cloneBackupRun(run)
	return nil
}

func (s *Store) TryAcquireBackupOperation(ctx context.Context) (func(), bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !s.backupOperationMu.TryLock() {
		return nil, false, nil
	}
	var once sync.Once
	return func() { once.Do(s.backupOperationMu.Unlock) }, true, nil
}

func mailboxIdentity(provider domain.ProviderKey, address string) string {
	return string(provider) + "\x00" + address
}

func credentialIdentity(mailboxID string, kind domain.CredentialKind) string {
	return mailboxID + "\x00" + string(kind)
}

func platformCredentialIdentity(accountID, kind string) string {
	return accountID + "\x00" + kind
}

func providerConnectionIdentity(provider domain.ProviderKey, name string) string {
	return string(provider) + "\x00" + name
}

func normalizeProviderConnectionMetadata(value json.RawMessage) (json.RawMessage, error) {
	if len(value) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: provider connection metadata must be a JSON object", domain.ErrInvalid)
	}
	return cloneBytes(value), nil
}

func validateProviderConnection(connection domain.ProviderConnection) error {
	if strings.TrimSpace(connection.ID) == "" || strings.TrimSpace(string(connection.Provider)) == "" || strings.TrimSpace(connection.Name) == "" {
		return fmt.Errorf("%w: provider connection id, provider, and name are required", domain.ErrInvalid)
	}
	if len(connection.EncryptedConfig) == 0 || strings.TrimSpace(connection.KeyVersion) == "" {
		return fmt.Errorf("%w: encrypted provider config and key version are required", domain.ErrInvalid)
	}
	if connection.Version <= 0 {
		return fmt.Errorf("%w: provider connection version must be positive", domain.ErrInvalid)
	}
	return nil
}

func paginate[T any](items []T, options ports.ListOptions) []T {
	options = options.Normalize(100, 500)
	if options.Offset >= len(items) {
		return []T{}
	}
	end := options.Offset + options.Limit
	if end > len(items) {
		end = len(items)
	}
	return items[options.Offset:end]
}

func cloneBytes(value []byte) []byte { return append([]byte(nil), value...) }

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneMailbox(value domain.Mailbox) domain.Mailbox {
	value.Metadata = cloneBytes(value.Metadata)
	return value
}

func cloneProviderConnection(value domain.ProviderConnection) domain.ProviderConnection {
	value.EncryptedConfig = cloneBytes(value.EncryptedConfig)
	value.Capabilities.RetrievalMethods = append([]domain.RetrievalMethod(nil), value.Capabilities.RetrievalMethods...)
	value.Metadata = cloneBytes(value.Metadata)
	return value
}

func cloneAppSetting(value domain.AppSetting) domain.AppSetting {
	value.Value = cloneBytes(value.Value)
	return value
}

func cloneAlias(value domain.MailboxAlias) domain.MailboxAlias {
	value.Metadata = cloneBytes(value.Metadata)
	return value
}

func cloneCredential(value domain.MailboxCredential) domain.MailboxCredential {
	value.EncryptedSecret = cloneBytes(value.EncryptedSecret)
	value.Metadata = cloneBytes(value.Metadata)
	value.ExpiresAt = cloneTime(value.ExpiresAt)
	value.RefreshAfter = cloneTime(value.RefreshAfter)
	value.LastRefreshedAt = cloneTime(value.LastRefreshedAt)
	return value
}

func cloneFormat(value domain.MailboxFormat) domain.MailboxFormat {
	value.Fields = append([]domain.MailboxFormatField(nil), value.Fields...)
	value.ParserConfig = cloneBytes(value.ParserConfig)
	if value.Provider != nil {
		provider := *value.Provider
		value.Provider = &provider
	}
	return value
}

func clonePlatformCredential(value domain.PlatformAccountCredential) domain.PlatformAccountCredential {
	value.EncryptedSecret = cloneBytes(value.EncryptedSecret)
	value.Metadata = cloneBytes(value.Metadata)
	return value
}

func clonePickupKey(value domain.MailboxPickupKey) domain.MailboxPickupKey {
	value.Digest = cloneBytes(value.Digest)
	value.EncryptedToken = cloneBytes(value.EncryptedToken)
	value.ExpiresAt = cloneTime(value.ExpiresAt)
	value.RevokedAt = cloneTime(value.RevokedAt)
	return value
}

func cloneAccount(value domain.PlatformAccount) domain.PlatformAccount {
	value.Metadata = cloneBytes(value.Metadata)
	value.Routes = append([]domain.MailboxRoute(nil), value.Routes...)
	if value.MailboxAliasID != nil {
		aliasID := *value.MailboxAliasID
		value.MailboxAliasID = &aliasID
	}
	return value
}

func cloneBackupTarget(value domain.BackupTarget) domain.BackupTarget {
	value.EncryptedConfig = cloneBytes(value.EncryptedConfig)
	value.Metadata = cloneBytes(value.Metadata)
	return value
}

func cloneBackupRun(value domain.BackupRun) domain.BackupRun {
	value.StartedAt = cloneTime(value.StartedAt)
	value.FinishedAt = cloneTime(value.FinishedAt)
	return value
}

var _ ports.Store = (*Store)(nil)
