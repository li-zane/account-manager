package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/li-zane/account-manager/backend/internal/domain"
)

type fakeS3Client struct {
	putInput    *s3.PutObjectInput
	getInput    *s3.GetObjectInput
	deleteInput *s3.DeleteObjectInput
	stored      []byte
}

func (f *fakeS3Client) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.putInput = input
	var err error
	f.stored, err = io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	return &s3.PutObjectOutput{ETag: aws.String(`"fixture-etag"`)}, nil
}

func (f *fakeS3Client) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.getInput = input
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(string(f.stored)))}, nil
}

func (f *fakeS3Client) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deleteInput = input
	return &s3.DeleteObjectOutput{}, nil
}

func TestS3BackupStoreMapsObjectOperations(t *testing.T) {
	client := &fakeS3Client{}
	store := newS3BackupStoreForTest(client, "fixture-bucket", "account-manager/backups/")
	ctx := context.Background()

	object, err := store.Put(ctx, "daily/database.dump", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if object.ObjectKey != "daily/database.dump" || object.ETag != `"fixture-etag"` || object.SizeBytes != 7 {
		t.Fatalf("put object = %+v", object)
	}
	if aws.ToString(client.putInput.Bucket) != "fixture-bucket" || aws.ToString(client.putInput.Key) != "account-manager/backups/daily/database.dump" {
		t.Fatalf("put input bucket=%q key=%q", aws.ToString(client.putInput.Bucket), aws.ToString(client.putInput.Key))
	}

	body, err := store.Get(ctx, "daily/database.dump")
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(body)
	body.Close()
	if err != nil || string(content) != "payload" {
		t.Fatalf("get content=%q err=%v", content, err)
	}
	if aws.ToString(client.getInput.Key) != "account-manager/backups/daily/database.dump" {
		t.Fatalf("get key = %q", aws.ToString(client.getInput.Key))
	}
	if err := store.Delete(ctx, "daily/database.dump"); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(client.deleteInput.Key) != "account-manager/backups/daily/database.dump" {
		t.Fatalf("delete key = %q", aws.ToString(client.deleteInput.Key))
	}

	if _, err := store.Put(ctx, "../outside.dump", strings.NewReader("x")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("traversal key error = %v", err)
	}
}

func TestRedactBackupStoreConfigOmitsCredentials(t *testing.T) {
	tests := []struct {
		name    string
		kind    domain.BackupTargetKind
		config  string
		want    []string
		secrets []string
	}{
		{
			name: "s3", kind: domain.BackupTargetS3,
			config:  `{"endpoint":"https://s3.example.test","bucket":"mail-backups","prefix":"daily","access_key_id":"access-id","secret_access_key":"secret-value","session_token":"session-value"}`,
			want:    []string{`"bucket":"mail-backups"`, `"credentials_configured":true`, `"session_token_configured":true`},
			secrets: []string{"access-id", "secret-value", "session-value", "access_key_id", "secret_access_key"},
		},
		{
			name: "webdav", kind: domain.BackupTargetWebDAV,
			config:  `{"base_url":"https://dav.example.test/backups","prefix":"daily","username":"backup-user","password":"password-value"}`,
			want:    []string{`"base_url":"https://dav.example.test/backups"`, `"authentication":"basic"`, `"username_configured":true`},
			secrets: []string{"backup-user", "password-value", `"password"`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			redacted, err := RedactBackupStoreConfig(test.kind, json.RawMessage(test.config))
			if err != nil {
				t.Fatal(err)
			}
			for _, expected := range test.want {
				if !strings.Contains(string(redacted), expected) {
					t.Errorf("redacted config %s is missing %s", redacted, expected)
				}
			}
			for _, secret := range test.secrets {
				if strings.Contains(string(redacted), secret) {
					t.Errorf("redacted config exposed %q: %s", secret, redacted)
				}
			}
		})
	}
}

func TestWebDAVBackupStoreMapsCollectionsPathsAndAuthentication(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.Method {
		case "MKCOL":
			w.WriteHeader(http.StatusCreated)
		case http.MethodPut:
			payload, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			if string(payload) != "payload" {
				t.Errorf("put payload = %q", payload)
			}
			w.Header().Set("ETag", `"webdav-etag"`)
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			_, _ = io.WriteString(w, "payload")
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	store, err := NewWebDAVBackupStore(WebDAVBackupConfig{
		BaseURL: server.URL + "/dav%20root", Prefix: "snapshots", BearerToken: "fixture-token",
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	object, err := store.Put(ctx, "daily/mail backup.dump", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if object.ETag != `"webdav-etag"` || object.SizeBytes != 7 {
		t.Fatalf("put object = %+v", object)
	}
	body, err := store.Get(ctx, "daily/mail backup.dump")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(body)
	body.Close()
	if err != nil || string(payload) != "payload" {
		t.Fatalf("get payload=%q err=%v", payload, err)
	}
	if err := store.Delete(ctx, "daily/mail backup.dump"); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"MKCOL /dav%20root/snapshots",
		"MKCOL /dav%20root/snapshots/daily",
		"PUT /dav%20root/snapshots/daily/mail%20backup.dump",
		"GET /dav%20root/snapshots/daily/mail%20backup.dump",
		"DELETE /dav%20root/snapshots/daily/mail%20backup.dump",
	}
	if strings.Join(requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(requests, "\n"), strings.Join(want, "\n"))
	}
}

func TestBackupStoreConfigValidationIsStrict(t *testing.T) {
	tests := []struct {
		name string
		kind domain.BackupTargetKind
		raw  string
	}{
		{name: "unknown S3 field", kind: domain.BackupTargetS3, raw: `{"bucket":"fixture","extra":true}`},
		{name: "partial S3 credentials", kind: domain.BackupTargetS3, raw: `{"bucket":"fixture","access_key_id":"key"}`},
		{name: "S3 traversal prefix", kind: domain.BackupTargetS3, raw: `{"bucket":"fixture","prefix":"../outside"}`},
		{name: "WebDAV query", kind: domain.BackupTargetWebDAV, raw: `{"base_url":"https://dav.example.test/root?token=value"}`},
		{name: "mixed WebDAV auth", kind: domain.BackupTargetWebDAV, raw: `{"base_url":"https://dav.example.test","username":"user","bearer_token":"token"}`},
		{name: "trailing JSON", kind: domain.BackupTargetS3, raw: `{"bucket":"fixture"} {"bucket":"second"}`},
		{name: "unknown kind", kind: domain.BackupTargetKind("fixture"), raw: `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateBackupStoreConfig(test.kind, json.RawMessage(test.raw)); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}

	for kind, raw := range map[domain.BackupTargetKind]string{
		domain.BackupTargetS3:     `{"bucket":"fixture","region":"us-east-1","prefix":"backups"}`,
		domain.BackupTargetWebDAV: `{"base_url":"https://dav.example.test/root","prefix":"backups","username":"user","password":"pass"}`,
	} {
		if err := ValidateBackupStoreConfig(kind, json.RawMessage(raw)); err != nil {
			t.Fatalf("valid %s config: %v", kind, err)
		}
	}
}
