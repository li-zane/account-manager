package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

type gmailTestSecretBroker struct{}

func (gmailTestSecretBroker) Seal(_ context.Context, plaintext []byte) ([]byte, string, error) {
	return append([]byte("sealed:"), plaintext...), "test-v1", nil
}

func (gmailTestSecretBroker) Open(_ context.Context, sealed []byte, keyVersion string) ([]byte, error) {
	if keyVersion != "test-v1" || !strings.HasPrefix(string(sealed), "sealed:") {
		return nil, errors.New("bad test envelope")
	}
	return append([]byte(nil), sealed[len("sealed:"):]...), nil
}

func (gmailTestSecretBroker) CurrentKeyVersion() string { return "test-v1" }

func TestGmailRefreshRotatesAndSealsCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("client_id") != "client" || r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("grant_type") != "refresh_token" {
			t.Fatalf("unexpected token form: %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new-access","refresh_token":"rotated-refresh","expires_in":3600}`)
	}))
	t.Cleanup(server.Close)
	broker := gmailTestSecretBroker{}
	credential := sealGmailCredential(t, broker, GmailCredentialSecret{ClientID: "client", RefreshToken: "old-refresh"})
	adapter := NewGmailAdapter(broker, server.Client())
	adapter.TokenEndpoint = server.URL
	refreshed, err := adapter.Refresh(context.Background(), domain.Mailbox{}, credential)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := broker.Open(context.Background(), refreshed.EncryptedSecret, refreshed.KeyVersion)
	if err != nil {
		t.Fatal(err)
	}
	var secret GmailCredentialSecret
	if err := json.Unmarshal(plaintext, &secret); err != nil {
		t.Fatal(err)
	}
	if secret.AccessToken != "new-access" || secret.RefreshToken != "rotated-refresh" || secret.AccessTokenExpiresAt == nil {
		t.Fatalf("unexpected refreshed secret metadata: access=%t refresh=%t expiry=%v", secret.AccessToken != "", secret.RefreshToken != "", secret.AccessTokenExpiresAt)
	}
	encoded, err := json.Marshal(refreshed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "new-access") || strings.Contains(string(encoded), "rotated-refresh") || strings.Contains(string(encoded), "sealed:") {
		t.Fatalf("refreshed credential response leaked secret material: %s", encoded)
	}
}

func TestGmailAPIRetrievalPaginatesAndFiltersExactRecipient(t *testing.T) {
	const accessToken = "gmail-access-token"
	target := "alias-a@rainynight.me"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+accessToken {
			t.Errorf("authorization header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/users/me/messages" {
			if r.URL.Query().Get("pageToken") == "page-2" {
				_, _ = io.WriteString(w, `{"messages":[{"id":"alias-a"},{"id":"unknown"}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"messages":[{"id":"primary"},{"id":"alias-b"}],"nextPageToken":"page-2"}`)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/users/me/messages/")
		headers := []gmailHeader{{Name: "From", Value: "sender@example.net"}, {Name: "Subject", Value: id}, {Name: "Date", Value: "Fri, 24 Jul 2026 12:00:00 +0800"}}
		switch id {
		case "primary":
			headers = append(headers, gmailHeader{Name: "Delivered-To", Value: "route@gmail.com"})
		case "alias-b":
			headers = append(headers, gmailHeader{Name: "X-Original-To", Value: "alias-b@rainynight.me"})
		case "alias-a":
			headers = append(headers,
				gmailHeader{Name: "X-Original-To", Value: target},
				gmailHeader{Name: "X-Original-To", Value: target},
			)
		}
		writeGmailJSON(t, w, gmailAPIMessage{
			ID: id, InternalDate: "1784865600000", LabelIDs: []string{"INBOX", "UNREAD"},
			Payload: gmailPayload{MIMEType: "text/plain", Headers: headers, Body: gmailBody{Data: base64.RawURLEncoding.EncodeToString([]byte("message body"))}},
		})
	}))
	t.Cleanup(server.Close)

	broker := gmailTestSecretBroker{}
	expires := time.Now().UTC().Add(time.Hour)
	credential := sealGmailCredential(t, broker, GmailCredentialSecret{AccessToken: accessToken, AccessTokenExpiresAt: &expires})
	adapter := NewGmailAdapter(broker, server.Client())
	adapter.APIBase = server.URL
	messages, err := adapter.Retrieve(context.Background(), domain.Mailbox{NormalizedAddress: "route@gmail.com"}, credential, domain.MessageQuery{
		Folder: domain.MessageFolderInbox, RecipientAddress: target, Limit: 2, PageSize: 2, MaxPages: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != "alias-a" || len(messages[0].Headers["X-Original-To"]) != 2 {
		t.Fatalf("filtered messages = %+v", messages)
	}
	if !MessageMatchesRecipient(messages[0], target) || MessageMatchesRecipient(messages[0], "alias-b@rainynight.me") {
		t.Fatalf("recipient isolation failed: %+v", messages[0].RecipientAddresses)
	}
}

func TestGmailAPIErrorDoesNotExposeAccessToken(t *testing.T) {
	const token = "sensitive-gmail-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"`+token+`"}`)
	}))
	t.Cleanup(server.Close)
	broker := gmailTestSecretBroker{}
	expires := time.Now().UTC().Add(time.Hour)
	credential := sealGmailCredential(t, broker, GmailCredentialSecret{AccessToken: token, AccessTokenExpiresAt: &expires})
	adapter := NewGmailAdapter(broker, server.Client())
	adapter.APIBase = server.URL
	_, err := adapter.Retrieve(context.Background(), domain.Mailbox{NormalizedAddress: "route@gmail.com"}, credential, domain.MessageQuery{Limit: 1})
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("secret leaked in Gmail error: %v", err)
	}
}

func TestNormalizeIMAPMessageRetainsRepeatedEnvelopeHeaders(t *testing.T) {
	raw := strings.Join([]string{
		"From: Sender <sender@example.net>",
		"To: Route <route@gmail.com>",
		"X-Original-To: alias-a@rainynight.me",
		"X-Original-To: ALIAS-A@rainynight.me",
		"Subject: =?UTF-8?B?5rWL6K+V?=",
		"Message-ID: <message-1@example.net>",
		"Date: Fri, 24 Jul 2026 12:00:00 +0800",
		"Content-Type: multipart/alternative; boundary=mail-boundary",
		"",
		"--mail-boundary",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"plain body",
		"--mail-boundary",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>html body</p>",
		"--mail-boundary--",
		"",
	}, "\r\n")
	message, err := normalizeIMAPMessage(42, nil, time.Now(), strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Headers["X-Original-To"]) != 2 || !MessageMatchesRecipient(message, "alias-a@rainynight.me") || MessageMatchesRecipient(message, "alias-b@rainynight.me") {
		t.Fatalf("recipient headers were not isolated: %+v", message.Headers["X-Original-To"])
	}
	if message.Subject != "测试" || !strings.Contains(message.Text, "plain body") || !strings.Contains(message.HTML, "html body") {
		t.Fatalf("MIME normalization failed: subject=%q text=%q html=%q", message.Subject, message.Text, message.HTML)
	}
}

func TestGmailIMAPCredentialUsesInjectedRetriever(t *testing.T) {
	broker := gmailTestSecretBroker{}
	credential := sealGmailCredential(t, broker, GmailCredentialSecret{Username: "route@gmail.com", Password: "app-password"})
	adapter := NewGmailAdapter(broker, nil)
	called := false
	adapter.imapFetch = func(_ context.Context, secret GmailCredentialSecret, query domain.MessageQuery) ([]domain.Message, error) {
		called = secret.Username == "route@gmail.com" && secret.Password != "" && query.RecipientAddress == "alias@rainynight.me"
		return []domain.Message{{ID: "imap:1"}}, nil
	}
	messages, err := adapter.Retrieve(context.Background(), domain.Mailbox{NormalizedAddress: "route@gmail.com"}, credential, domain.MessageQuery{RecipientAddress: "alias@rainynight.me"})
	if err != nil || !called || len(messages) != 1 {
		t.Fatalf("called=%v messages=%v err=%v", called, messages, err)
	}
}

func TestGmailAdapterDeserializesDomainIMAPConnectionEnvelope(t *testing.T) {
	useTLS := false
	domainSecret := domain.MailboxCredentialSecret{
		Username: "x1-login@gmail.com", Password: "app-password", Host: "imap.x1.test", Port: 1993,
		UseTLS: &useTLS, ProxyURL: "socks5://proxy.test:1080", InboxFolder: "Inbox.Custom", JunkFolder: "Spam.Custom",
	}
	plaintext, err := json.Marshal(domainSecret)
	if err != nil {
		t.Fatal(err)
	}
	broker := gmailTestSecretBroker{}
	sealed, keyVersion, err := broker.Seal(context.Background(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	credential := domain.MailboxCredential{
		Kind: domain.CredentialIMAPPassword, EncryptedSecret: sealed, KeyVersion: keyVersion,
	}
	adapter := NewGmailAdapter(broker, nil)
	adapter.imapFetch = func(_ context.Context, secret GmailCredentialSecret, _ domain.MessageQuery) ([]domain.Message, error) {
		if secret.Username != domainSecret.Username || secret.Password != domainSecret.Password || secret.Host != domainSecret.Host || secret.Port != domainSecret.Port {
			t.Fatalf("Gmail adapter connection secret = %+v", secret)
		}
		if secret.UseTLS == nil || *secret.UseTLS || secret.ProxyURL != domainSecret.ProxyURL || secret.InboxFolder != domainSecret.InboxFolder || secret.JunkFolder != domainSecret.JunkFolder {
			t.Fatalf("Gmail adapter transport secret = %+v", secret)
		}
		return []domain.Message{{ID: "imap:domain-envelope"}}, nil
	}
	messages, err := adapter.Retrieve(context.Background(), domain.Mailbox{}, credential, domain.MessageQuery{})
	if err != nil || len(messages) != 1 {
		t.Fatalf("Gmail domain envelope retrieval messages=%v err=%v", messages, err)
	}
}

func sealGmailCredential(t *testing.T, broker gmailTestSecretBroker, secret GmailCredentialSecret) domain.MailboxCredential {
	t.Helper()
	plaintext, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	sealed, version, err := broker.Seal(context.Background(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	kind := domain.CredentialGmailOAuth
	if secret.Username != "" {
		kind = domain.CredentialIMAPPassword
	}
	return domain.MailboxCredential{Kind: kind, EncryptedSecret: sealed, KeyVersion: version}
}

func writeGmailJSON(t *testing.T, w io.Writer, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}
