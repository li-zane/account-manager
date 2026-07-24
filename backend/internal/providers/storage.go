package providers

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

// S3BackupConfig is persisted encrypted on a backup target. Credentials are
// deliberately absent from every public target response through the domain's
// json:"-" EncryptedConfig field.
type S3BackupConfig struct {
	Endpoint        string `json:"endpoint,omitempty"`
	Region          string `json:"region,omitempty"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix,omitempty"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	SessionToken    string `json:"session_token,omitempty"`
	UsePathStyle    bool   `json:"use_path_style,omitempty"`
}

type WebDAVBackupConfig struct {
	BaseURL     string `json:"base_url"`
	Prefix      string `json:"prefix,omitempty"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	BearerToken string `json:"bearer_token,omitempty"`
	// InsecureSkipVerify exists for private/self-signed WebDAV deployments and
	// must be explicitly opted into per encrypted target.
	InsecureSkipVerify bool `json:"insecure_skip_verify,omitempty"`
}

// ValidateBackupStoreConfig validates the decrypted shape without performing
// network I/O. It is used before a target config is sealed and again when a
// worker opens the target.
func ValidateBackupStoreConfig(kind domain.BackupTargetKind, raw json.RawMessage) error {
	switch kind {
	case domain.BackupTargetS3:
		var config S3BackupConfig
		if err := decodeStrictConfig(raw, &config); err != nil {
			return fmt.Errorf("%w: invalid S3 backup config", domain.ErrInvalid)
		}
		return config.validate()
	case domain.BackupTargetWebDAV:
		var config WebDAVBackupConfig
		if err := decodeStrictConfig(raw, &config); err != nil {
			return fmt.Errorf("%w: invalid WebDAV backup config", domain.ErrInvalid)
		}
		return config.validate()
	default:
		return fmt.Errorf("%w: unsupported backup target kind %q", domain.ErrInvalid, kind)
	}
}

// RedactBackupStoreConfig returns the editable, non-secret portion of a target
// configuration. Credential values and credential identifiers are represented
// only by boolean or authentication-mode flags.
func RedactBackupStoreConfig(kind domain.BackupTargetKind, raw json.RawMessage) (json.RawMessage, error) {
	var value any
	switch kind {
	case domain.BackupTargetS3:
		var config S3BackupConfig
		if err := decodeStrictConfig(raw, &config); err != nil {
			return nil, fmt.Errorf("%w: invalid S3 backup config", domain.ErrInvalid)
		}
		if err := config.validate(); err != nil {
			return nil, err
		}
		value = struct {
			Endpoint               string `json:"endpoint,omitempty"`
			Region                 string `json:"region,omitempty"`
			Bucket                 string `json:"bucket"`
			Prefix                 string `json:"prefix,omitempty"`
			UsePathStyle           bool   `json:"use_path_style"`
			CredentialsConfigured  bool   `json:"credentials_configured"`
			SessionTokenConfigured bool   `json:"session_token_configured"`
		}{
			Endpoint: config.Endpoint, Region: config.Region, Bucket: config.Bucket,
			Prefix: config.Prefix, UsePathStyle: config.UsePathStyle,
			CredentialsConfigured:  config.AccessKeyID != "" && config.SecretAccessKey != "",
			SessionTokenConfigured: config.SessionToken != "",
		}
	case domain.BackupTargetWebDAV:
		var config WebDAVBackupConfig
		if err := decodeStrictConfig(raw, &config); err != nil {
			return nil, fmt.Errorf("%w: invalid WebDAV backup config", domain.ErrInvalid)
		}
		if err := config.validate(); err != nil {
			return nil, err
		}
		authentication := "none"
		if config.BearerToken != "" {
			authentication = "bearer"
		} else if config.Username != "" {
			authentication = "basic"
		}
		value = struct {
			BaseURL            string `json:"base_url"`
			Prefix             string `json:"prefix,omitempty"`
			Authentication     string `json:"authentication"`
			UsernameConfigured bool   `json:"username_configured"`
			InsecureSkipVerify bool   `json:"insecure_skip_verify"`
		}{
			BaseURL: config.BaseURL, Prefix: config.Prefix, Authentication: authentication,
			UsernameConfigured: config.Username != "", InsecureSkipVerify: config.InsecureSkipVerify,
		}
	default:
		return nil, fmt.Errorf("%w: unsupported backup target kind %q", domain.ErrInvalid, kind)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode redacted backup config: %w", err)
	}
	return json.RawMessage(encoded), nil
}

// NewBackupStore opens an S3-compatible or WebDAV store from decrypted target
// configuration. The returned adapter retains credentials only in memory.
func NewBackupStore(ctx context.Context, kind domain.BackupTargetKind, raw json.RawMessage) (ports.BackupStore, error) {
	switch kind {
	case domain.BackupTargetS3:
		var config S3BackupConfig
		if err := decodeStrictConfig(raw, &config); err != nil {
			return nil, fmt.Errorf("%w: invalid S3 backup config", domain.ErrInvalid)
		}
		return NewS3BackupStore(ctx, config)
	case domain.BackupTargetWebDAV:
		var config WebDAVBackupConfig
		if err := decodeStrictConfig(raw, &config); err != nil {
			return nil, fmt.Errorf("%w: invalid WebDAV backup config", domain.ErrInvalid)
		}
		return NewWebDAVBackupStore(config, nil)
	default:
		return nil, fmt.Errorf("%w: unsupported backup target kind %q", domain.ErrInvalid, kind)
	}
}

func decodeStrictConfig(raw []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (c S3BackupConfig) validate() error {
	if strings.TrimSpace(c.Bucket) == "" {
		return fmt.Errorf("%w: S3 bucket is required", domain.ErrInvalid)
	}
	if (c.AccessKeyID == "") != (c.SecretAccessKey == "") {
		return fmt.Errorf("%w: S3 access key id and secret access key must be provided together", domain.ErrInvalid)
	}
	if c.SessionToken != "" && c.AccessKeyID == "" {
		return fmt.Errorf("%w: S3 session token requires static credentials", domain.ErrInvalid)
	}
	if c.Endpoint != "" {
		endpoint, err := url.Parse(c.Endpoint)
		if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil {
			return fmt.Errorf("%w: S3 endpoint must be an HTTP(S) URL without embedded credentials", domain.ErrInvalid)
		}
	}
	_, err := normalizeObjectPrefix(c.Prefix)
	return err
}

type s3Client interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

type S3BackupStore struct {
	client s3Client
	bucket string
	prefix string
}

func NewS3BackupStore(ctx context.Context, config S3BackupConfig) (*S3BackupStore, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	region := strings.TrimSpace(config.Region)
	if region == "" {
		region = "us-east-1"
	}
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if config.AccessKeyID != "" {
		provider := credentials.NewStaticCredentialsProvider(config.AccessKeyID, config.SecretAccessKey, config.SessionToken)
		options = append(options, awsconfig.WithCredentialsProvider(provider))
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("initialize S3 client: %w", err)
	}
	client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.UsePathStyle = config.UsePathStyle
		if config.Endpoint != "" {
			options.BaseEndpoint = aws.String(strings.TrimRight(config.Endpoint, "/"))
		}
	})
	prefix, _ := normalizeObjectPrefix(config.Prefix)
	return &S3BackupStore{client: client, bucket: config.Bucket, prefix: prefix}, nil
}

func newS3BackupStoreForTest(client s3Client, bucket, prefix string) *S3BackupStore {
	normalized, _ := normalizeObjectPrefix(prefix)
	return &S3BackupStore{client: client, bucket: bucket, prefix: normalized}
}

func (s *S3BackupStore) Put(ctx context.Context, objectKey string, body io.Reader) (ports.BackupObject, error) {
	key, err := s.key(objectKey)
	if err != nil {
		return ports.BackupObject{}, err
	}
	counter := &countingReader{reader: body}
	result, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: counter})
	if err != nil {
		return ports.BackupObject{}, fmt.Errorf("upload S3 backup object: %w", err)
	}
	return ports.BackupObject{ObjectKey: objectKey, ETag: aws.ToString(result.ETag), SizeBytes: counter.count}, nil
}

func (s *S3BackupStore) Get(ctx context.Context, objectKey string) (io.ReadCloser, error) {
	key, err := s.key(objectKey)
	if err != nil {
		return nil, err
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("download S3 backup object: %w", err)
	}
	return result.Body, nil
}

func (s *S3BackupStore) Delete(ctx context.Context, objectKey string) error {
	key, err := s.key(objectKey)
	if err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("delete S3 backup object: %w", err)
	}
	return nil
}

func (s *S3BackupStore) key(objectKey string) (string, error) {
	clean, err := normalizeObjectKey(objectKey)
	if err != nil {
		return "", err
	}
	if s.prefix == "" {
		return clean, nil
	}
	return s.prefix + "/" + clean, nil
}

func (c WebDAVBackupConfig) validate() error {
	base, err := url.Parse(c.BaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("%w: WebDAV base_url must be an HTTP(S) URL without credentials, query, or fragment", domain.ErrInvalid)
	}
	if c.Password != "" && c.Username == "" {
		return fmt.Errorf("%w: WebDAV password requires a username", domain.ErrInvalid)
	}
	if c.BearerToken != "" && (c.Username != "" || c.Password != "") {
		return fmt.Errorf("%w: choose either WebDAV bearer or basic authentication", domain.ErrInvalid)
	}
	_, err = normalizeObjectPrefix(c.Prefix)
	return err
}

type WebDAVBackupStore struct {
	baseURL     *url.URL
	prefix      string
	username    string
	password    string
	bearerToken string
	client      *http.Client
}

func NewWebDAVBackupStore(config WebDAVBackupConfig, client *http.Client) (*WebDAVBackupStore, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	base, _ := url.Parse(strings.TrimRight(config.BaseURL, "/"))
	prefix, _ := normalizeObjectPrefix(config.Prefix)
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if config.InsecureSkipVerify {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // explicit per-target opt-in
		}
		client = &http.Client{Transport: transport, Timeout: 30 * time.Minute}
	}
	copyClient := *client
	previousRedirect := copyClient.CheckRedirect
	copyClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 0 && !sameWebDAVOrigin(request.URL, via[0].URL) {
			return errors.New("cross-origin WebDAV redirect rejected")
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		if len(via) >= 10 {
			return errors.New("too many WebDAV redirects")
		}
		return nil
	}
	return &WebDAVBackupStore{
		baseURL: base, prefix: prefix, username: config.Username, password: config.Password,
		bearerToken: config.BearerToken, client: &copyClient,
	}, nil
}

func (s *WebDAVBackupStore) Put(ctx context.Context, objectKey string, body io.Reader) (ports.BackupObject, error) {
	clean, err := normalizeObjectKey(objectKey)
	if err != nil {
		return ports.BackupObject{}, err
	}
	if err := s.ensureCollections(ctx, path.Dir(clean)); err != nil {
		return ports.BackupObject{}, err
	}
	counter := &countingReader{reader: body}
	request, err := s.request(ctx, http.MethodPut, clean, counter)
	if err != nil {
		return ports.BackupObject{}, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return ports.BackupObject{}, fmt.Errorf("upload WebDAV backup object: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ports.BackupObject{}, fmt.Errorf("upload WebDAV backup object: unexpected HTTP status %d", response.StatusCode)
	}
	return ports.BackupObject{ObjectKey: objectKey, ETag: response.Header.Get("ETag"), SizeBytes: counter.count}, nil
}

func (s *WebDAVBackupStore) Get(ctx context.Context, objectKey string) (io.ReadCloser, error) {
	clean, err := normalizeObjectKey(objectKey)
	if err != nil {
		return nil, err
	}
	request, err := s.request(ctx, http.MethodGet, clean, nil)
	if err != nil {
		return nil, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download WebDAV backup object: %w", err)
	}
	if response.StatusCode == http.StatusNotFound {
		response.Body.Close()
		return nil, fmt.Errorf("%w: WebDAV backup object", domain.ErrNotFound)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, fmt.Errorf("download WebDAV backup object: unexpected HTTP status %d", response.StatusCode)
	}
	return response.Body, nil
}

func (s *WebDAVBackupStore) Delete(ctx context.Context, objectKey string) error {
	clean, err := normalizeObjectKey(objectKey)
	if err != nil {
		return err
	}
	request, err := s.request(ctx, http.MethodDelete, clean, nil)
	if err != nil {
		return err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("delete WebDAV backup object: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("delete WebDAV backup object: unexpected HTTP status %d", response.StatusCode)
	}
	return nil
}

func (s *WebDAVBackupStore) ensureCollections(ctx context.Context, objectDirectory string) error {
	directory := strings.Trim(path.Join(s.prefix, objectDirectory), "/")
	if directory == "" || directory == "." {
		return nil
	}
	segments := strings.Split(directory, "/")
	for index := range segments {
		collection := strings.Join(segments[:index+1], "/")
		request, err := s.requestAbsolute(ctx, "MKCOL", collection, nil)
		if err != nil {
			return err
		}
		response, err := s.client.Do(request)
		if err != nil {
			return fmt.Errorf("create WebDAV backup collection: %w", err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusMethodNotAllowed && response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
			return fmt.Errorf("create WebDAV backup collection: unexpected HTTP status %d", response.StatusCode)
		}
	}
	return nil
}

func (s *WebDAVBackupStore) request(ctx context.Context, method, objectKey string, body io.Reader) (*http.Request, error) {
	return s.requestAbsolute(ctx, method, strings.Trim(path.Join(s.prefix, objectKey), "/"), body)
}

func (s *WebDAVBackupStore) requestAbsolute(ctx context.Context, method, relativePath string, body io.Reader) (*http.Request, error) {
	destination := *s.baseURL
	destination.Path = strings.TrimRight(s.baseURL.Path, "/") + "/" + relativePath
	destination.RawPath = ""
	request, err := http.NewRequestWithContext(ctx, method, destination.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create WebDAV request: %w", err)
	}
	if s.bearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+s.bearerToken)
	} else if s.username != "" {
		request.SetBasicAuth(s.username, s.password)
	}
	request.Header.Set("Accept", "application/octet-stream")
	if method == http.MethodPut {
		request.Header.Set("Content-Type", "application/octet-stream")
	}
	return request, nil
}

func normalizeObjectPrefix(value string) (string, error) {
	value = strings.Trim(strings.ReplaceAll(value, "\\", "/"), "/")
	if value == "" {
		return "", nil
	}
	return normalizeObjectKey(value)
}

func normalizeObjectKey(value string) (string, error) {
	if strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("%w: backup object key must be relative", domain.ErrInvalid)
	}
	clean := path.Clean(strings.TrimSpace(value))
	if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: invalid backup object key", domain.ErrInvalid)
	}
	return clean, nil
}

func sameWebDAVOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	r.count += int64(count)
	return count, err
}

var _ ports.BackupStore = (*S3BackupStore)(nil)
var _ ports.BackupStore = (*WebDAVBackupStore)(nil)
