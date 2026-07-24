package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

const maxImportRows = 5000

type ImportExportService struct {
	mailboxes ports.MailboxRepository
	formats   ports.MailboxFormatRepository
	accounts  ports.PlatformAccountRepository
	providers ports.ProviderRegistry
	secrets   ports.SecretBroker
	pickupKey PickupKeyPreparer
	clock     func() time.Time
}

type PickupKeyPreparer interface {
	PrepareImported(mailboxID, label, token string) (domain.MailboxPickupKey, error)
}

func NewImportExportService(mailboxes ports.MailboxRepository, formats ports.MailboxFormatRepository, accounts ports.PlatformAccountRepository, providers ports.ProviderRegistry, secrets ports.SecretBroker) (*ImportExportService, error) {
	if mailboxes == nil || formats == nil || accounts == nil || providers == nil || secrets == nil {
		return nil, fmt.Errorf("%w: import/export dependencies are required", domain.ErrInvalid)
	}
	return &ImportExportService{
		mailboxes: mailboxes, formats: formats, accounts: accounts,
		providers: providers, secrets: secrets, clock: time.Now,
	}, nil
}

func (s *ImportExportService) SetPickupKeyPreparer(preparer PickupKeyPreparer) {
	s.pickupKey = preparer
}

type MailboxImportRequest struct {
	FormatID         string                  `json:"format_id"`
	Data             string                  `json:"data"`
	ConflictStrategy domain.ConflictStrategy `json:"conflict_strategy"`
}

type ImportRowError struct {
	Line   int      `json:"line"`
	Errors []string `json:"errors"`
}

type ImportPreviewRow struct {
	Line                   int                   `json:"line"`
	Provider               domain.ProviderKey    `json:"provider,omitempty"`
	Address                string                `json:"address,omitempty"`
	CredentialType         domain.CredentialKind `json:"credential_type,omitempty"`
	ClientID               string                `json:"client_id,omitempty"`
	HasPassword            bool                  `json:"has_password"`
	HasRefreshToken        bool                  `json:"has_refresh_token"`
	HasPickupKey           bool                  `json:"has_pickup_key"`
	Platform               string                `json:"platform,omitempty"`
	HasPlatformPassword    bool                  `json:"has_platform_password"`
	HasPlatformAccessToken bool                  `json:"has_platform_access_token"`
	Exists                 bool                  `json:"exists"`
	Duplicate              bool                  `json:"duplicate"`
	Action                 string                `json:"action"`
	Errors                 []string              `json:"errors,omitempty"`
}

type ImportPreview struct {
	FormatID     string             `json:"format_id"`
	Rows         []ImportPreviewRow `json:"rows"`
	TotalRows    int                `json:"total_rows"`
	ValidRows    int                `json:"valid_rows"`
	InvalidRows  int                `json:"invalid_rows"`
	NewRows      int                `json:"new_rows"`
	ConflictRows int                `json:"conflict_rows"`
}

type ImportCommitResult struct {
	domain.MailboxImportResult
	RowErrors        []ImportRowError `json:"row_errors"`
	ValidRows        int              `json:"valid_rows"`
	InvalidRows      int              `json:"invalid_rows"`
	MainMailboxCount int64            `json:"main_mailbox_count"`
	AliasCount       int64            `json:"alias_count"`
}

type parsedImportRow struct {
	preview          ImportPreviewRow
	mailbox          domain.Mailbox
	credentialKind   domain.CredentialKind
	clientID         string
	password         string
	refreshToken     string
	pickupKey        string
	username         string
	imapHost         string
	imapPort         int
	useTLS           *bool
	proxyURL         string
	inboxFolder      string
	junkFolder       string
	expiresAt        *time.Time
	refreshAfter     *time.Time
	platform         string
	platformExternal string
	platformPassword string
	platformToken    string
}

func (s *ImportExportService) Preview(ctx context.Context, request MailboxImportRequest) (ImportPreview, error) {
	format, rows, err := s.parseImport(ctx, request)
	if err != nil {
		return ImportPreview{}, err
	}
	preview := ImportPreview{FormatID: format.ID, Rows: make([]ImportPreviewRow, 0, len(rows)), TotalRows: len(rows)}
	seen := make(map[string]struct{}, len(rows))
	for index := range rows {
		row := &rows[index]
		if len(row.preview.Errors) == 0 {
			identity := string(row.mailbox.Provider) + "\x00" + row.mailbox.NormalizedAddress
			if _, duplicate := seen[identity]; duplicate {
				row.preview.Duplicate = true
			} else {
				seen[identity] = struct{}{}
			}
			_, lookupErr := s.mailboxes.GetMailboxByIdentity(ctx, row.mailbox.Provider, row.mailbox.NormalizedAddress)
			row.preview.Exists = lookupErr == nil
			if lookupErr != nil && !errors.Is(lookupErr, domain.ErrNotFound) {
				return ImportPreview{}, lookupErr
			}
			conflict := row.preview.Exists || row.preview.Duplicate
			if conflict {
				preview.ConflictRows++
				switch request.ConflictStrategy {
				case domain.ConflictUpdate:
					row.preview.Action = "update"
				case domain.ConflictError:
					row.preview.Action = "error"
				default:
					row.preview.Action = "skip"
				}
			} else {
				row.preview.Action = "create"
				preview.NewRows++
			}
			preview.ValidRows++
		} else {
			row.preview.Action = "invalid"
			preview.InvalidRows++
		}
		preview.Rows = append(preview.Rows, row.preview)
	}
	return preview, nil
}

func (s *ImportExportService) Import(ctx context.Context, request MailboxImportRequest) (ImportCommitResult, error) {
	_, rows, err := s.parseImport(ctx, request)
	if err != nil {
		return ImportCommitResult{}, err
	}
	items := make([]domain.MailboxImportItem, 0, len(rows))
	rowErrors := make([]ImportRowError, 0)
	for _, row := range rows {
		if len(row.preview.Errors) > 0 {
			rowErrors = append(rowErrors, ImportRowError{Line: row.preview.Line, Errors: append([]string(nil), row.preview.Errors...)})
			continue
		}
		item, err := s.sealImportRow(ctx, row)
		if err != nil {
			return ImportCommitResult{}, err
		}
		items = append(items, item)
	}
	strategy := normalizeConflictStrategy(request.ConflictStrategy)
	result := domain.MailboxImportResult{MailboxIDs: []string{}}
	if len(items) > 0 {
		result, err = s.mailboxes.ImportMailboxes(ctx, items, strategy)
		if err != nil {
			return ImportCommitResult{}, err
		}
	}
	mailboxCount, err := s.mailboxes.CountMailboxes(ctx)
	if err != nil {
		return ImportCommitResult{}, err
	}
	aliasCount, err := s.mailboxes.CountAliases(ctx, "")
	if err != nil {
		return ImportCommitResult{}, err
	}
	return ImportCommitResult{
		MailboxImportResult: result, RowErrors: rowErrors, ValidRows: len(items),
		InvalidRows: len(rowErrors), MainMailboxCount: mailboxCount, AliasCount: aliasCount,
	}, nil
}

func (s *ImportExportService) parseImport(ctx context.Context, request MailboxImportRequest) (domain.MailboxFormat, []parsedImportRow, error) {
	format, err := s.formats.GetMailboxFormat(ctx, strings.TrimSpace(request.FormatID))
	if err != nil {
		return domain.MailboxFormat{}, nil, err
	}
	if !format.Enabled || (format.Direction != domain.MailboxFormatImport && format.Direction != domain.MailboxFormatBoth) {
		return domain.MailboxFormat{}, nil, fmt.Errorf("%w: format is not enabled for import", domain.ErrInvalid)
	}
	request.ConflictStrategy = normalizeConflictStrategy(request.ConflictStrategy)
	var rawRows []rawMappedRow
	switch format.Kind {
	case domain.MailboxFormatDelimited, "":
		rawRows, err = parseDelimitedInput(format, request.Data)
	case domain.MailboxFormatJSON:
		rawRows, err = parseJSONInput(format, request.Data)
	case domain.MailboxFormatTemplate:
		rawRows, err = parseTemplateInput(format, request.Data)
	default:
		return domain.MailboxFormat{}, nil, fmt.Errorf("%w: format kind %q", domain.ErrInvalid, format.Kind)
	}
	if err != nil {
		return domain.MailboxFormat{}, nil, err
	}
	if len(rawRows) > maxImportRows {
		return domain.MailboxFormat{}, nil, fmt.Errorf("%w: import exceeds %d records", domain.ErrInvalid, maxImportRows)
	}
	rows := make([]parsedImportRow, 0, len(rawRows))
	for _, raw := range rawRows {
		rows = append(rows, s.buildImportRow(ctx, format, raw))
	}
	return format, rows, nil
}

type rawMappedRow struct {
	line   int
	values map[string]string
	errors []string
}

func parseDelimitedInput(format domain.MailboxFormat, data string) ([]rawMappedRow, error) {
	data = strings.TrimPrefix(data, "\ufeff")
	lines := strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
	rows := make([]rawMappedRow, 0, len(lines))
	var indices []int
	expectedColumns := len(format.Fields)
	headerReady := !format.HasHeader
	for lineIndex, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		values, parseErr := parseDelimitedLine(strings.TrimSuffix(line, "\r"), format.Delimiter)
		if !headerReady {
			if parseErr != nil {
				return nil, fmt.Errorf("%w: header line %d: %v", domain.ErrInvalid, lineIndex+1, parseErr)
			}
			indices = make([]int, len(format.Fields))
			headerIndex := make(map[string]int, len(values))
			for index, value := range values {
				headerIndex[strings.ToLower(strings.TrimSpace(value))] = index
			}
			expectedColumns = len(values)
			for index, field := range format.Fields {
				column, ok := headerIndex[strings.ToLower(field.Column)]
				if !ok {
					return nil, fmt.Errorf("%w: header is missing column %q", domain.ErrInvalid, field.Column)
				}
				indices[index] = column
			}
			headerReady = true
			continue
		}
		raw := rawMappedRow{line: lineIndex + 1, values: make(map[string]string, len(format.Fields))}
		if parseErr != nil {
			raw.errors = append(raw.errors, parseErr.Error())
		}
		if len(values) != expectedColumns {
			raw.errors = append(raw.errors, fmt.Sprintf("expected %d fields, got %d", expectedColumns, len(values)))
		}
		for fieldIndex, field := range format.Fields {
			columnIndex := fieldIndex
			if format.HasHeader && fieldIndex < len(indices) {
				columnIndex = indices[fieldIndex]
			}
			value := field.Default
			if columnIndex < len(values) && strings.TrimSpace(values[columnIndex]) != "" {
				value = strings.TrimSpace(values[columnIndex])
			}
			if field.Required && value == "" {
				raw.errors = append(raw.errors, fmt.Sprintf("field %q is required", field.Column))
			}
			raw.values[canonicalFormatTarget(field.Target)] = value
		}
		rows = append(rows, raw)
	}
	if !headerReady {
		return nil, fmt.Errorf("%w: import header is missing", domain.ErrInvalid)
	}
	return rows, nil
}

func parseDelimitedLine(line, delimiter string) ([]string, error) {
	if delimiter == "" {
		return nil, fmt.Errorf("delimiter is empty")
	}
	fields := make([]string, 0, 8)
	var field strings.Builder
	inQuotes := false
	for index := 0; index < len(line); {
		if line[index] == '"' {
			if inQuotes && index+1 < len(line) && line[index+1] == '"' {
				field.WriteByte('"')
				index += 2
				continue
			}
			inQuotes = !inQuotes
			index++
			continue
		}
		if !inQuotes && strings.HasPrefix(line[index:], delimiter) {
			fields = append(fields, field.String())
			field.Reset()
			index += len(delimiter)
			continue
		}
		_, size := utf8.DecodeRuneInString(line[index:])
		field.WriteString(line[index : index+size])
		index += size
	}
	fields = append(fields, field.String())
	if inQuotes {
		return fields, fmt.Errorf("unclosed quoted field")
	}
	return fields, nil
}

func parseJSONInput(format domain.MailboxFormat, data string) ([]rawMappedRow, error) {
	var document any
	if err := json.Unmarshal([]byte(data), &document); err != nil {
		return []rawMappedRow{{line: 1, values: map[string]string{}, errors: []string{"invalid JSON document"}}}, nil
	}
	config := formatParserConfig(format)
	var records []any
	switch value := document.(type) {
	case []any:
		records = value
	case map[string]any:
		path := config.RecordsPath
		if path == "" {
			path = "accounts"
		}
		candidate, ok := lookupJSONPath(value, path).([]any)
		if !ok {
			return nil, fmt.Errorf("%w: JSON records path %q is not an array", domain.ErrInvalid, path)
		}
		records = candidate
	default:
		return nil, fmt.Errorf("%w: JSON import must be an array or object", domain.ErrInvalid)
	}
	rows := make([]rawMappedRow, 0, len(records))
	for index, record := range records {
		raw := rawMappedRow{line: index + 1, values: make(map[string]string, len(format.Fields))}
		object, ok := record.(map[string]any)
		if !ok {
			raw.errors = append(raw.errors, "record must be a JSON object")
			rows = append(rows, raw)
			continue
		}
		for _, field := range format.Fields {
			value := jsonValueText(lookupJSONPath(object, field.Column))
			if value == "" {
				value = field.Default
			}
			if field.Required && value == "" {
				raw.errors = append(raw.errors, fmt.Sprintf("field %q is required", field.Column))
			}
			raw.values[canonicalFormatTarget(field.Target)] = value
		}
		rows = append(rows, raw)
	}
	return rows, nil
}

func (s *ImportExportService) buildImportRow(ctx context.Context, format domain.MailboxFormat, raw rawMappedRow) parsedImportRow {
	row := parsedImportRow{preview: ImportPreviewRow{Line: raw.line, Errors: append([]string(nil), raw.errors...)}}
	config := formatParserConfig(format)
	providerValue := raw.values["provider"]
	if format.Provider != nil {
		providerValue = string(*format.Provider)
	}
	var provider domain.ProviderKey
	var err error
	if strings.TrimSpace(providerValue) == "" && format.Provider == nil && config.ProviderFromAddress {
		provider, err = inferProviderFromAddress(raw.values["address"])
	} else {
		provider, err = normalizeProviderKey(providerValue)
	}
	if err != nil {
		row.preview.Errors = append(row.preview.Errors, err.Error())
		return row
	}
	registration, err := s.providers.Get(provider)
	if err != nil {
		row.preview.Errors = append(row.preview.Errors, "provider is not registered")
		return row
	}
	normalizedAddress, err := registration.Provider.NormalizeAddress(raw.values["address"])
	if err != nil {
		row.preview.Errors = append(row.preview.Errors, "invalid mailbox address")
		return row
	}
	id, err := domain.NewMailboxID(string(provider), normalizedAddress)
	if err != nil {
		row.preview.Errors = append(row.preview.Errors, "mailbox id generation failed")
		return row
	}
	status := domain.MailboxStatus(strings.ToLower(raw.values["status"]))
	if status == "" {
		status = domain.MailboxStatusActive
	}
	if status != domain.MailboxStatusActive && status != domain.MailboxStatusDisabled && status != domain.MailboxStatusError {
		row.preview.Errors = append(row.preview.Errors, "invalid mailbox status")
	}
	metadata := make(map[string]string)
	for target, value := range raw.values {
		if strings.HasPrefix(target, "metadata.") && value != "" {
			metadata[strings.TrimPrefix(target, "metadata.")] = value
		}
	}
	metadataJSON, _ := json.Marshal(metadata)
	now := s.clock().UTC()
	row.mailbox = domain.Mailbox{
		ID: id, Provider: provider, Address: normalizedAddress, NormalizedAddress: normalizedAddress,
		DisplayName: raw.values["display_name"], ExternalReference: raw.values["external_reference"],
		Status: status, Metadata: metadataJSON, CreatedAt: now, UpdatedAt: now,
	}
	row.credentialKind = normalizeCredentialKind(raw.values["credential_kind"], provider, raw.values["password"], raw.values["refresh_token"])
	row.clientID, row.password, row.refreshToken = raw.values["client_id"], raw.values["password"], raw.values["refresh_token"]
	row.pickupKey = raw.values["pickup_key"]
	row.username = strings.TrimSpace(raw.values["username"])
	row.imapHost = strings.ToLower(strings.TrimSpace(raw.values["imap_host"]))
	row.proxyURL = strings.TrimSpace(raw.values["proxy_url"])
	row.inboxFolder = strings.TrimSpace(raw.values["inbox_folder"])
	row.junkFolder = strings.TrimSpace(raw.values["junk_folder"])
	row.imapPort, err = parseOptionalPort(raw.values["imap_port"])
	if err != nil {
		row.preview.Errors = append(row.preview.Errors, "imap_port must be an integer between 1 and 65535")
	}
	row.useTLS, err = parseOptionalBool(raw.values["use_tls"])
	if err != nil {
		row.preview.Errors = append(row.preview.Errors, "use_tls must be a boolean")
	}
	row.expiresAt, err = parseOptionalTime(raw.values["expires_at"])
	if err != nil {
		row.preview.Errors = append(row.preview.Errors, "expires_at must be RFC3339")
	}
	row.refreshAfter, err = parseOptionalTime(raw.values["refresh_after"])
	if err != nil {
		row.preview.Errors = append(row.preview.Errors, "refresh_after must be RFC3339")
	}
	imapConnectionDeclared := row.username != "" || strings.TrimSpace(raw.values["imap_host"]) != "" || strings.TrimSpace(raw.values["imap_port"]) != "" || strings.TrimSpace(raw.values["use_tls"]) != "" || row.proxyURL != "" || row.inboxFolder != "" || row.junkFolder != ""
	credentialDeclared := strings.TrimSpace(raw.values["credential_kind"]) != "" || row.clientID != "" || row.password != "" || row.refreshToken != "" || imapConnectionDeclared || row.expiresAt != nil || row.refreshAfter != nil
	if provider == domain.ProviderGmail && credentialDeclared {
		switch row.credentialKind {
		case domain.CredentialGmailOAuth:
			if row.clientID == "" {
				row.preview.Errors = append(row.preview.Errors, "gmail_oauth requires client_id")
			}
			if row.refreshToken == "" {
				row.preview.Errors = append(row.preview.Errors, "gmail_oauth requires refresh_token")
			}
			if imapConnectionDeclared {
				row.preview.Errors = append(row.preview.Errors, "gmail_oauth does not accept IMAP connection fields")
			}
		case domain.CredentialIMAPPassword:
			if row.password == "" {
				row.preview.Errors = append(row.preview.Errors, "imap_password requires password")
			}
			if row.username == "" {
				row.username = normalizedAddress
			}
		default:
			row.preview.Errors = append(row.preview.Errors, "credential kind is not supported by Gmail")
		}
	}
	row.platform = strings.ToLower(strings.TrimSpace(raw.values["platform"]))
	if row.platform == "" {
		row.platform = strings.ToLower(strings.TrimSpace(config.Platform))
	}
	row.platformExternal = raw.values["platform_external_reference"]
	row.platformPassword = raw.values["platform_account_password"]
	row.platformToken = raw.values["platform_access_token"]
	row.preview.Provider, row.preview.Address = provider, normalizedAddress
	row.preview.CredentialType, row.preview.ClientID = row.credentialKind, row.clientID
	row.preview.HasPassword, row.preview.HasRefreshToken = row.password != "", row.refreshToken != ""
	row.preview.HasPickupKey = row.pickupKey != ""
	row.preview.Platform = row.platform
	row.preview.HasPlatformPassword, row.preview.HasPlatformAccessToken = row.platformPassword != "", row.platformToken != ""
	if (row.platformPassword != "" || row.platformToken != "" || row.platformExternal != "") && row.platform == "" {
		row.preview.Errors = append(row.preview.Errors, "platform fields require a platform")
	}
	return row
}

func (s *ImportExportService) sealImportRow(ctx context.Context, row parsedImportRow) (domain.MailboxImportItem, error) {
	item := domain.MailboxImportItem{Mailbox: row.mailbox}
	if row.pickupKey != "" {
		if s.pickupKey == nil {
			return domain.MailboxImportItem{}, fmt.Errorf("%w: pickup-key import preparer", domain.ErrNotConfigured)
		}
		pickupKey, err := s.pickupKey.PrepareImported(row.mailbox.ID, "legacy import", row.pickupKey)
		if err != nil {
			return domain.MailboxImportItem{}, err
		}
		item.PickupKey = &pickupKey
	}
	if row.hasMailboxCredential() {
		secret, err := importMailboxCredentialSecret(row)
		if err != nil {
			return domain.MailboxImportItem{}, err
		}
		payload, err := json.Marshal(secret)
		if err != nil {
			return domain.MailboxImportItem{}, fmt.Errorf("encode imported mailbox credential: %w", err)
		}
		sealed, keyVersion, err := s.secrets.Seal(ctx, payload)
		if err != nil {
			return domain.MailboxImportItem{}, err
		}
		id, err := domain.NewRandomID("cred")
		if err != nil {
			return domain.MailboxImportItem{}, err
		}
		status := "configured"
		if row.refreshToken != "" {
			status = "active"
		}
		now := s.clock().UTC()
		item.Credential = &domain.MailboxCredential{
			ID: id, MailboxID: row.mailbox.ID, Kind: row.credentialKind, ClientID: row.clientID,
			EncryptedSecret: sealed, KeyVersion: keyVersion, ExpiresAt: row.expiresAt,
			RefreshAfter: row.refreshAfter, RefreshStatus: status, Metadata: json.RawMessage(`{}`),
			CreatedAt: now, UpdatedAt: now,
		}
	}
	if row.platform != "" && (row.platformPassword != "" || row.platformToken != "" || row.platformExternal != "") {
		external := row.platformExternal
		if external == "" {
			external = "import:" + row.mailbox.ID
		}
		accountID, err := domain.NewPlatformAccountID(row.platform, external)
		if err != nil {
			return domain.MailboxImportItem{}, err
		}
		now := s.clock().UTC()
		item.PlatformAccount = &domain.PlatformAccount{
			ID: accountID, Platform: row.platform, ExternalReference: external,
			MailboxID: row.mailbox.ID, LoginAddress: row.mailbox.Address, Status: "active",
			Metadata: json.RawMessage(`{"source":"mailbox_import"}`), CreatedAt: now, UpdatedAt: now,
		}
		payload, _ := json.Marshal(domain.PlatformAccountCredentialSecret{Password: row.platformPassword, AccessToken: row.platformToken})
		sealed, keyVersion, err := s.secrets.Seal(ctx, payload)
		if err != nil {
			return domain.MailboxImportItem{}, err
		}
		credentialID, err := domain.NewRandomID("pcred")
		if err != nil {
			return domain.MailboxImportItem{}, err
		}
		item.PlatformCredential = &domain.PlatformAccountCredential{
			ID: credentialID, PlatformAccountID: accountID, Kind: "login",
			EncryptedSecret: sealed, KeyVersion: keyVersion, Metadata: json.RawMessage(`{}`),
			CreatedAt: now, UpdatedAt: now,
		}
	}
	return item, nil
}

func importMailboxCredentialSecret(row parsedImportRow) (domain.MailboxCredentialSecret, error) {
	switch row.mailbox.Provider {
	case domain.ProviderGmail:
		switch row.credentialKind {
		case domain.CredentialGmailOAuth:
			if row.clientID == "" || row.refreshToken == "" {
				return domain.MailboxCredentialSecret{}, fmt.Errorf("%w: gmail_oauth requires client_id and refresh_token", domain.ErrInvalid)
			}
			return domain.MailboxCredentialSecret{ClientID: row.clientID, RefreshToken: row.refreshToken}, nil
		case domain.CredentialIMAPPassword:
			if row.password == "" {
				return domain.MailboxCredentialSecret{}, fmt.Errorf("%w: imap_password requires password", domain.ErrInvalid)
			}
			return domain.MailboxCredentialSecret{
				Username: row.username, Password: row.password, Host: row.imapHost, Port: row.imapPort,
				UseTLS: row.useTLS, ProxyURL: row.proxyURL, InboxFolder: row.inboxFolder, JunkFolder: row.junkFolder,
			}, nil
		default:
			return domain.MailboxCredentialSecret{}, fmt.Errorf("%w: credential kind %q is not supported by Gmail", domain.ErrInvalid, row.credentialKind)
		}
	case domain.ProviderMicrosoft:
		// The Microsoft adapter deliberately accepts this schema-version-zero
		// envelope so existing Outlook four-part imports remain compatible.
		return domain.MailboxCredentialSecret{
			ClientID: row.clientID, RefreshToken: row.refreshToken, Username: row.username, Password: row.password,
			Host: row.imapHost, Port: row.imapPort, UseTLS: row.useTLS, ProxyURL: row.proxyURL,
			InboxFolder: row.inboxFolder, JunkFolder: row.junkFolder,
		}, nil
	default:
		username := row.username
		if username == "" && row.password != "" {
			username = row.mailbox.NormalizedAddress
		}
		return domain.MailboxCredentialSecret{
			ClientID: row.clientID, RefreshToken: row.refreshToken,
			Username: username, Password: row.password, Host: row.imapHost, Port: row.imapPort,
			UseTLS: row.useTLS, ProxyURL: row.proxyURL, InboxFolder: row.inboxFolder, JunkFolder: row.junkFolder,
		}, nil
	}
}

func (row parsedImportRow) hasMailboxCredential() bool {
	return row.clientID != "" || row.password != "" || row.refreshToken != "" || row.username != "" ||
		row.imapHost != "" || row.imapPort != 0 || row.useTLS != nil || row.proxyURL != "" ||
		row.inboxFolder != "" || row.junkFolder != "" || row.expiresAt != nil || row.refreshAfter != nil
}

type MailboxExportRequest struct {
	FormatID         string   `json:"format_id"`
	MailboxIDs       []string `json:"mailbox_ids"`
	IncludeSensitive bool     `json:"include_sensitive"`
}

type MailboxExportResult struct {
	Filename          string `json:"filename"`
	Content           string `json:"content"`
	Count             int    `json:"count"`
	SensitiveIncluded bool   `json:"sensitive_included"`
}

func (s *ImportExportService) Export(ctx context.Context, request MailboxExportRequest, adminAuthorized bool) (MailboxExportResult, error) {
	format, err := s.formats.GetMailboxFormat(ctx, strings.TrimSpace(request.FormatID))
	if err != nil {
		return MailboxExportResult{}, err
	}
	if !format.Enabled || (format.Direction != domain.MailboxFormatExport && format.Direction != domain.MailboxFormatBoth) {
		return MailboxExportResult{}, fmt.Errorf("%w: format is not enabled for export", domain.ErrInvalid)
	}
	hasSensitive := formatHasSensitiveFields(format)
	if request.IncludeSensitive && hasSensitive && !adminAuthorized {
		return MailboxExportResult{}, domain.ErrUnauthorized
	}
	mailboxes, err := s.exportMailboxes(ctx, request.MailboxIDs, format.Provider)
	if err != nil {
		return MailboxExportResult{}, err
	}
	records := make([]map[string]string, 0, len(mailboxes))
	for _, mailbox := range mailboxes {
		values, err := s.exportValues(ctx, mailbox, format, request.IncludeSensitive && adminAuthorized)
		if err != nil {
			return MailboxExportResult{}, err
		}
		records = append(records, values)
	}
	content, extension, err := renderExport(format, records)
	if err != nil {
		return MailboxExportResult{}, err
	}
	filename := "mailboxes-" + s.clock().UTC().Format("20060102-150405") + "." + extension
	return MailboxExportResult{
		Filename: filename, Content: content, Count: len(records),
		SensitiveIncluded: request.IncludeSensitive && adminAuthorized && hasSensitive,
	}, nil
}

func (s *ImportExportService) exportMailboxes(ctx context.Context, ids []string, provider *domain.ProviderKey) ([]domain.Mailbox, error) {
	items := make([]domain.Mailbox, 0)
	if len(ids) > 0 {
		seen := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			mailbox, err := s.mailboxes.GetMailbox(ctx, id)
			if err != nil {
				return nil, err
			}
			if provider == nil || mailbox.Provider == *provider {
				items = append(items, mailbox)
			}
		}
		return items, nil
	}
	for offset := 0; ; offset += 500 {
		page, err := s.mailboxes.ListMailboxes(ctx, ports.ListOptions{Limit: 500, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, mailbox := range page {
			if provider == nil || mailbox.Provider == *provider {
				items = append(items, mailbox)
			}
		}
		if len(page) < 500 {
			break
		}
	}
	return items, nil
}

func (s *ImportExportService) exportValues(ctx context.Context, mailbox domain.Mailbox, format domain.MailboxFormat, includeSensitive bool) (map[string]string, error) {
	values := map[string]string{
		"address": mailbox.Address, "display_name": mailbox.DisplayName,
		"external_reference": mailbox.ExternalReference, "provider": string(mailbox.Provider), "status": string(mailbox.Status),
	}
	credentials, err := s.mailboxes.ListCredentials(ctx, mailbox.ID)
	if err != nil {
		return nil, err
	}
	if len(credentials) > 0 {
		credential := credentials[0]
		values["credential_kind"] = string(credential.Kind)
		values["client_id"] = credential.ClientID
		if credential.ExpiresAt != nil {
			values["expires_at"] = credential.ExpiresAt.Format(time.RFC3339)
		}
		if credential.RefreshAfter != nil {
			values["refresh_after"] = credential.RefreshAfter.Format(time.RFC3339)
		}
		if credentialSecretRequired(format, includeSensitive) {
			plaintext, err := s.secrets.Open(ctx, credential.EncryptedSecret, credential.KeyVersion)
			if err != nil {
				return nil, err
			}
			defer clear(plaintext)
			var secret domain.MailboxCredentialSecret
			if err := json.Unmarshal(plaintext, &secret); err != nil {
				return nil, fmt.Errorf("%w: mailbox credential payload", domain.ErrInvalid)
			}
			if values["client_id"] == "" {
				values["client_id"] = secret.ClientID
			}
			values["username"] = secret.Username
			values["imap_host"] = secret.Host
			if secret.Port != 0 {
				values["imap_port"] = strconv.Itoa(secret.Port)
			}
			if secret.UseTLS != nil {
				values["use_tls"] = strconv.FormatBool(*secret.UseTLS)
			}
			values["inbox_folder"], values["junk_folder"] = secret.InboxFolder, secret.JunkFolder
			if includeSensitive {
				values["refresh_token"], values["password"], values["proxy_url"] = secret.RefreshToken, secret.Password, secret.ProxyURL
			}
		}
	}
	for key, value := range metadataValues(mailbox.Metadata) {
		values["metadata."+key] = value
	}
	accounts, err := s.accounts.ListPlatformAccountsByMailbox(ctx, mailbox.ID, ports.ListOptions{Limit: 500})
	if err != nil {
		return nil, err
	}
	if len(accounts) > 0 {
		account := accounts[0]
		values["platform"] = account.Platform
		values["platform_external_reference"] = account.ExternalReference
		if includeSensitive {
			credential, err := s.accounts.GetPlatformAccountCredential(ctx, account.ID, "login")
			if err != nil && !errors.Is(err, domain.ErrNotFound) {
				return nil, err
			}
			if err == nil {
				plaintext, err := s.secrets.Open(ctx, credential.EncryptedSecret, credential.KeyVersion)
				if err != nil {
					return nil, err
				}
				var secret domain.PlatformAccountCredentialSecret
				if err := json.Unmarshal(plaintext, &secret); err != nil {
					return nil, fmt.Errorf("%w: platform credential payload", domain.ErrInvalid)
				}
				values["platform_account_password"], values["platform_access_token"] = secret.Password, secret.AccessToken
			}
		}
	}
	return values, nil
}

func renderExport(format domain.MailboxFormat, records []map[string]string) (string, string, error) {
	switch format.Kind {
	case domain.MailboxFormatDelimited, "":
		lines := make([]string, 0, len(records)+1)
		if format.HasHeader {
			headers := make([]string, len(format.Fields))
			for index, field := range format.Fields {
				headers[index] = field.Column
			}
			lines = append(lines, encodeDelimitedLine(headers, format.Delimiter))
		}
		for _, record := range records {
			values := make([]string, len(format.Fields))
			for index, field := range format.Fields {
				values[index] = record[canonicalFormatTarget(field.Target)]
			}
			lines = append(lines, encodeDelimitedLine(values, format.Delimiter))
		}
		return strings.Join(lines, "\n"), "txt", nil
	case domain.MailboxFormatJSON:
		objects := make([]map[string]string, 0, len(records))
		for _, record := range records {
			object := make(map[string]string, len(format.Fields))
			for _, field := range format.Fields {
				object[field.Column] = record[canonicalFormatTarget(field.Target)]
			}
			objects = append(objects, object)
		}
		config := formatParserConfig(format)
		var output any = objects
		if config.RecordsPath != "" {
			output = map[string]any{config.RecordsPath: objects}
		}
		encoded, err := json.MarshalIndent(output, "", "  ")
		return string(encoded), "json", err
	case domain.MailboxFormatTemplate:
		content, err := renderTemplateExport(format, records)
		return content, "txt", err
	default:
		return "", "", fmt.Errorf("%w: export format kind %q", domain.ErrInvalid, format.Kind)
	}
}

func encodeDelimitedLine(values []string, delimiter string) string {
	escaped := make([]string, len(values))
	for index, value := range values {
		if strings.Contains(value, delimiter) || strings.ContainsAny(value, "\"\r\n") {
			value = "\"" + strings.ReplaceAll(value, "\"", "\"\"") + "\""
		}
		escaped[index] = value
	}
	return strings.Join(escaped, delimiter)
}

var (
	mailboxTemplateVariablePattern = regexp.MustCompile(`{{\s*([^{}]+?)\s*}}`)
	mailboxTemplateRepeatPattern   = regexp.MustCompile(`(?s)%begin([^%]*)%(.*?)%end%`)
)

type mailboxTemplateCapture struct {
	name        string
	target      string
	jsonEncoded bool
}

type compiledMailboxTemplateRecord struct {
	source      string
	pattern     string
	standalone  *regexp.Regexp
	captures    []mailboxTemplateCapture
	fieldByName map[string]string
}

type compiledMailboxTemplate struct {
	repeated  bool
	prefix    string
	suffix    string
	separator string
	record    compiledMailboxTemplateRecord
}

func parseTemplateInput(format domain.MailboxFormat, data string) ([]rawMappedRow, error) {
	compiled, err := compileMailboxTemplate(format)
	if err != nil {
		return nil, err
	}
	payload := normalizeTemplateNewlines(strings.TrimPrefix(data, "\ufeff"))
	separator := "\n"
	startLine := 1
	if compiled.repeated {
		if len(payload) < len(compiled.prefix)+len(compiled.suffix) || !strings.HasPrefix(payload, compiled.prefix) {
			return nil, fmt.Errorf("%w: template content does not match the literal prefix before %%begin%%", domain.ErrInvalid)
		}
		payload = payload[len(compiled.prefix):]
		if len(payload) < len(compiled.suffix) || !strings.HasSuffix(payload, compiled.suffix) {
			return nil, fmt.Errorf("%w: template content does not match the literal suffix after %%end%%", domain.ErrInvalid)
		}
		payload = payload[:len(payload)-len(compiled.suffix)]
		separator = compiled.separator
		startLine += strings.Count(compiled.prefix, "\n")
	}
	return parseTemplateRecordSequence(format, compiled.record, payload, separator, startLine)
}

func renderTemplateExport(format domain.MailboxFormat, records []map[string]string) (string, error) {
	compiled, err := compileMailboxTemplate(format)
	if err != nil {
		return "", err
	}
	rendered := make([]string, 0, len(records))
	for _, values := range records {
		record, err := renderCompiledTemplateRecord(compiled.record, values)
		if err != nil {
			return "", err
		}
		rendered = append(rendered, record)
	}
	if compiled.repeated {
		return compiled.prefix + strings.Join(rendered, compiled.separator) + compiled.suffix, nil
	}
	return strings.Join(rendered, "\n"), nil
}

func compileMailboxTemplate(format domain.MailboxFormat) (compiledMailboxTemplate, error) {
	template := normalizeTemplateNewlines(format.Template)
	if strings.TrimSpace(template) == "" {
		return compiledMailboxTemplate{}, fmt.Errorf("%w: template is empty", domain.ErrInvalid)
	}
	beginCount := strings.Count(template, "%begin")
	endCount := strings.Count(template, "%end%")
	blocks := mailboxTemplateRepeatPattern.FindAllStringSubmatchIndex(template, -1)
	if beginCount != endCount || beginCount != len(blocks) {
		return compiledMailboxTemplate{}, fmt.Errorf("%w: template structure has unmatched %%begin%% or %%end%% marker", domain.ErrInvalid)
	}
	if len(blocks) > 1 {
		return compiledMailboxTemplate{}, fmt.Errorf("%w: template structure supports at most one %%begin%% ... %%end%% block", domain.ErrInvalid)
	}
	fieldByName, err := mailboxTemplateFieldMap(format.Fields)
	if err != nil {
		return compiledMailboxTemplate{}, err
	}
	compiled := compiledMailboxTemplate{separator: "\n"}
	recordSource := strings.Trim(template, "\n")
	if len(blocks) == 1 {
		block := blocks[0]
		compiled.repeated = true
		compiled.prefix = template[:block[0]]
		compiled.suffix = template[block[1]:]
		compiled.separator, err = parseMailboxTemplateSeparator(template[block[2]:block[3]])
		if err != nil {
			return compiledMailboxTemplate{}, err
		}
		recordSource = strings.Trim(template[block[4]:block[5]], "\n")
		if err := validateTemplateOuterLiteral(compiled.prefix, fieldByName, "before %begin%"); err != nil {
			return compiledMailboxTemplate{}, err
		}
		if err := validateTemplateOuterLiteral(compiled.suffix, fieldByName, "after %end%"); err != nil {
			return compiledMailboxTemplate{}, err
		}
	}
	compiled.record, err = compileMailboxTemplateRecord(recordSource, fieldByName)
	if err != nil {
		return compiledMailboxTemplate{}, err
	}
	return compiled, nil
}

func compileMailboxTemplateRecord(source string, fieldByName map[string]string) (compiledMailboxTemplateRecord, error) {
	matches := mailboxTemplateVariablePattern.FindAllStringSubmatchIndex(source, -1)
	if strings.Contains(mailboxTemplateVariablePattern.ReplaceAllString(source, ""), "{{") {
		return compiledMailboxTemplateRecord{}, fmt.Errorf("%w: template contains an unterminated variable", domain.ErrInvalid)
	}
	if len(matches) == 0 {
		return compiledMailboxTemplateRecord{}, fmt.Errorf("%w: template record must contain at least one mapped variable", domain.ErrInvalid)
	}
	captures := make([]mailboxTemplateCapture, 0, len(matches))
	seenTargets := make(map[string]string, len(matches))
	var pattern strings.Builder
	cursor := 0
	for index, match := range matches {
		literal := source[cursor:match[0]]
		if index > 0 && literal == "" {
			return compiledMailboxTemplateRecord{}, fmt.Errorf("%w: adjacent template variables require a literal separator", domain.ErrInvalid)
		}
		pattern.WriteString(regexp.QuoteMeta(literal))
		name := strings.TrimSpace(source[match[2]:match[3]])
		capture, err := resolveMailboxTemplateCapture(name, fieldByName)
		if err != nil {
			return compiledMailboxTemplateRecord{}, err
		}
		if previous, duplicate := seenTargets[capture.target]; duplicate {
			return compiledMailboxTemplateRecord{}, fmt.Errorf(
				"%w: template target %q is captured more than once by %q and %q",
				domain.ErrInvalid, capture.target, previous, capture.name,
			)
		}
		seenTargets[capture.target] = capture.name
		captures = append(captures, capture)
		pattern.WriteString(`(?s:(.*?))`)
		cursor = match[1]
	}
	pattern.WriteString(regexp.QuoteMeta(source[cursor:]))
	standalone, err := regexp.Compile(`^(?:` + pattern.String() + `)$`)
	if err != nil {
		return compiledMailboxTemplateRecord{}, fmt.Errorf("%w: compile template record: %v", domain.ErrInvalid, err)
	}
	return compiledMailboxTemplateRecord{
		source: source, pattern: pattern.String(), standalone: standalone,
		captures: captures, fieldByName: fieldByName,
	}, nil
}

func mailboxTemplateFieldMap(fields []domain.MailboxFormatField) (map[string]string, error) {
	fieldByName := make(map[string]string, len(fields)*2)
	for _, field := range fields {
		target := canonicalFormatTarget(field.Target)
		for _, alias := range []string{strings.TrimSpace(field.Column), target} {
			key := strings.ToLower(alias)
			if key == "" {
				continue
			}
			if existing, ok := fieldByName[key]; ok && existing != target {
				return nil, fmt.Errorf("%w: template variable %q maps to both %q and %q", domain.ErrInvalid, alias, existing, target)
			}
			fieldByName[key] = target
		}
	}
	return fieldByName, nil
}

func resolveMailboxTemplateCapture(name string, fieldByName map[string]string) (mailboxTemplateCapture, error) {
	if name == "" {
		return mailboxTemplateCapture{}, fmt.Errorf("%w: template variable name is empty", domain.ErrInvalid)
	}
	baseName := name
	jsonEncoded := strings.HasSuffix(strings.ToLower(baseName), "_json")
	if jsonEncoded {
		baseName = baseName[:len(baseName)-len("_json")]
	}
	target, ok := fieldByName[strings.ToLower(strings.TrimSpace(baseName))]
	if !ok {
		return mailboxTemplateCapture{}, fmt.Errorf("%w: template variable %q is not mapped by format fields", domain.ErrInvalid, name)
	}
	return mailboxTemplateCapture{name: name, target: target, jsonEncoded: jsonEncoded}, nil
}

func validateTemplateOuterLiteral(source string, fieldByName map[string]string, location string) error {
	if strings.Contains(mailboxTemplateVariablePattern.ReplaceAllString(source, ""), "{{") {
		return fmt.Errorf("%w: template contains an unterminated variable %s", domain.ErrInvalid, location)
	}
	matches := mailboxTemplateVariablePattern.FindAllStringSubmatchIndex(source, -1)
	if len(matches) == 0 {
		return nil
	}
	name := strings.TrimSpace(source[matches[0][2]:matches[0][3]])
	if _, err := resolveMailboxTemplateCapture(name, fieldByName); err != nil {
		return err
	}
	return fmt.Errorf("%w: record variable %q is outside the %%begin%% ... %%end%% block", domain.ErrInvalid, name)
}

func parseMailboxTemplateSeparator(attributes string) (string, error) {
	attributes = strings.TrimSpace(attributes)
	if attributes == "" {
		return "\n", nil
	}
	if !strings.HasPrefix(attributes, "sep") {
		return "", fmt.Errorf("%w: template repeat block only supports a sep attribute", domain.ErrInvalid)
	}
	if len(attributes) > len("sep") && attributes[len("sep")] != '=' && !strings.ContainsRune(" \t\r\n", rune(attributes[len("sep")])) {
		return "", fmt.Errorf("%w: template repeat block only supports a sep attribute", domain.ErrInvalid)
	}
	remainder := strings.TrimSpace(attributes[len("sep"):])
	if !strings.HasPrefix(remainder, "=") {
		return "", fmt.Errorf("%w: template repeat separator must use sep='...' or sep=\"...\"", domain.ErrInvalid)
	}
	remainder = strings.TrimSpace(remainder[1:])
	if len(remainder) < 2 || (remainder[0] != '\'' && remainder[0] != '"') || remainder[len(remainder)-1] != remainder[0] {
		return "", fmt.Errorf("%w: template repeat separator must use matching quotes", domain.ErrInvalid)
	}
	separator, err := decodeMailboxTemplateSeparator(remainder[1:len(remainder)-1], remainder[0])
	if err != nil {
		return "", err
	}
	if separator == "" {
		return "", fmt.Errorf("%w: template repeat separator cannot be empty", domain.ErrInvalid)
	}
	return separator, nil
}

func decodeMailboxTemplateSeparator(value string, quote byte) (string, error) {
	var decoded strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == quote {
			return "", fmt.Errorf("%w: template repeat separator contains an unescaped quote", domain.ErrInvalid)
		}
		if character != '\\' {
			decoded.WriteByte(character)
			continue
		}
		if index+1 >= len(value) {
			return "", fmt.Errorf("%w: template repeat separator ends with an incomplete escape", domain.ErrInvalid)
		}
		index++
		switch value[index] {
		case 'n':
			decoded.WriteByte('\n')
		case 'r':
			decoded.WriteByte('\r')
		case 't':
			decoded.WriteByte('\t')
		case '\\', '\'', '"':
			decoded.WriteByte(value[index])
		default:
			return "", fmt.Errorf("%w: template repeat separator has unsupported escape \\%c", domain.ErrInvalid, value[index])
		}
	}
	return decoded.String(), nil
}

func parseTemplateRecordSequence(format domain.MailboxFormat, record compiledMailboxTemplateRecord, content, separator string, startLine int) ([]rawMappedRow, error) {
	if content == "" {
		return []rawMappedRow{}, nil
	}
	sequence, err := regexp.Compile(`^(?:` + record.pattern + `)(?:((?:` + regexp.QuoteMeta(separator) + `))|$)`)
	if err != nil {
		return nil, fmt.Errorf("%w: compile template record sequence: %v", domain.ErrInvalid, err)
	}
	rows := make([]rawMappedRow, 0)
	remaining := content
	line := startLine
	separatorGroup := len(record.captures) + 1
	for remaining != "" {
		match := sequence.FindStringSubmatchIndex(remaining)
		if match == nil || match[0] != 0 || match[1] == 0 {
			return nil, fmt.Errorf("%w: template content does not match record at line %d", domain.ErrInvalid, line)
		}
		row := rawTemplateRow(format, record, remaining, match, line)
		rows = append(rows, row)
		consumed := remaining[:match[1]]
		separatorOffset := separatorGroup * 2
		hadSeparator := separatorOffset+1 < len(match) && match[separatorOffset] >= 0
		remaining = remaining[match[1]:]
		line += strings.Count(consumed, "\n")
		if !hadSeparator && remaining != "" {
			return nil, fmt.Errorf("%w: template record at line %d did not consume the remaining content", domain.ErrInvalid, line)
		}
	}
	return rows, nil
}

func rawTemplateRow(format domain.MailboxFormat, record compiledMailboxTemplateRecord, content string, match []int, line int) rawMappedRow {
	raw := rawMappedRow{line: line, values: make(map[string]string, len(format.Fields))}
	for _, field := range format.Fields {
		raw.values[canonicalFormatTarget(field.Target)] = field.Default
	}
	for index, capture := range record.captures {
		groupOffset := (index + 1) * 2
		if groupOffset+1 >= len(match) || match[groupOffset] < 0 {
			continue
		}
		value := content[match[groupOffset]:match[groupOffset+1]]
		if capture.jsonEncoded {
			var decoded any
			if err := json.Unmarshal([]byte(value), &decoded); err != nil {
				raw.errors = append(raw.errors, fmt.Sprintf("template variable %q is not valid JSON", capture.name))
				continue
			}
			value = jsonValueText(decoded)
		}
		raw.values[capture.target] = value
	}
	for _, field := range format.Fields {
		if field.Required && strings.TrimSpace(raw.values[canonicalFormatTarget(field.Target)]) == "" {
			raw.errors = append(raw.errors, fmt.Sprintf("field %q is required", field.Column))
		}
	}
	return raw
}

func renderCompiledTemplateRecord(record compiledMailboxTemplateRecord, values map[string]string) (string, error) {
	matches := mailboxTemplateVariablePattern.FindAllStringSubmatchIndex(record.source, -1)
	if len(matches) != len(record.captures) {
		return "", fmt.Errorf("%w: compiled template capture count changed", domain.ErrInvalid)
	}
	var rendered strings.Builder
	cursor := 0
	for index, match := range matches {
		rendered.WriteString(record.source[cursor:match[0]])
		capture := record.captures[index]
		value := values[capture.target]
		if capture.jsonEncoded {
			encoded, err := json.Marshal(value)
			if err != nil {
				return "", fmt.Errorf("encode template variable %q: %w", capture.name, err)
			}
			rendered.Write(encoded)
		} else {
			rendered.WriteString(value)
		}
		cursor = match[1]
	}
	rendered.WriteString(record.source[cursor:])
	return rendered.String(), nil
}

func normalizeTemplateNewlines(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
}

type parserConfig struct {
	Platform            string `json:"platform"`
	RecordsPath         string `json:"records_path"`
	ProviderFromAddress bool   `json:"provider_from_address"`
}

func formatParserConfig(format domain.MailboxFormat) parserConfig {
	var config parserConfig
	_ = json.Unmarshal(format.ParserConfig, &config)
	return config
}

func normalizeConflictStrategy(strategy domain.ConflictStrategy) domain.ConflictStrategy {
	switch strategy {
	case domain.ConflictUpdate, domain.ConflictError, domain.ConflictSkip:
		return strategy
	default:
		return domain.ConflictSkip
	}
}

func normalizeProviderKey(value string) (domain.ProviderKey, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "microsoft", "outlook", "hotmail":
		return domain.ProviderMicrosoft, nil
	case "gmail", "google":
		return domain.ProviderGmail, nil
	case "cloudflare", "cloudflare_route", "cf", "domain":
		return domain.ProviderCloudflareRoute, nil
	default:
		return "", fmt.Errorf("provider %q is invalid", value)
	}
}

func inferProviderFromAddress(address string) (domain.ProviderKey, error) {
	normalized := strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndexByte(normalized, '@')
	if at <= 0 || at == len(normalized)-1 {
		return "", fmt.Errorf("provider for address %q cannot be inferred; set a fixed provider or provider field", address)
	}
	domainName := normalized[at+1:]
	switch {
	case strings.HasPrefix(domainName, "outlook."), strings.HasPrefix(domainName, "hotmail."):
		return domain.ProviderMicrosoft, nil
	case strings.HasPrefix(domainName, "gmail."), strings.HasPrefix(domainName, "googlemail."):
		return domain.ProviderGmail, nil
	default:
		return "", fmt.Errorf("provider for address %q cannot be inferred; set a fixed provider or provider field", address)
	}
}

func normalizeCredentialKind(value string, provider domain.ProviderKey, password, refreshToken string) domain.CredentialKind {
	if strings.TrimSpace(value) != "" {
		return domain.CredentialKind(strings.TrimSpace(value))
	}
	if password != "" && refreshToken == "" {
		return domain.CredentialIMAPPassword
	}
	if provider == domain.ProviderGmail {
		return domain.CredentialGmailOAuth
	}
	return domain.CredentialMicrosoftGraphOAuth
}

func parseOptionalTime(value string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseOptionalPort(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port")
	}
	return port, nil
}

func parseOptionalBool(value string) (*bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func lookupJSONPath(object map[string]any, path string) any {
	var current any = object
	for _, part := range strings.Split(path, ".") {
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapped[part]
	}
	return current
}

func jsonValueText(value any) string {
	switch value := value.(type) {
	case nil:
		return ""
	case string:
		return value
	case bool, float64:
		return fmt.Sprint(value)
	default:
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
}

func metadataValues(raw json.RawMessage) map[string]string {
	var values map[string]any
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = jsonValueText(value)
	}
	return result
}

func formatHasSensitiveFields(format domain.MailboxFormat) bool {
	for _, field := range format.Fields {
		if sensitiveFormatTarget(field.Target) {
			return true
		}
	}
	return false
}

func credentialSecretRequired(format domain.MailboxFormat, includeSensitive bool) bool {
	for _, field := range format.Fields {
		switch canonicalFormatTarget(field.Target) {
		case "username", "imap_host", "imap_port", "use_tls", "inbox_folder", "junk_folder":
			return true
		case "refresh_token", "password", "proxy_url":
			if includeSensitive {
				return true
			}
		}
	}
	return false
}
