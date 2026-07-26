package service

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

func TestCachedMessageNormalizesInvalidUTF8(t *testing.T) {
	now := time.Now().UTC()
	message, ok := cachedMessageFrom("mailbox", domain.MessageFolderInbox, domain.RetrievalIMAPOAuth, domain.Message{
		ID: "imap:1", InternetMessageID: "<one@example.test>", Subject: string([]byte{0xc4, 0xe3}),
		Text: "before" + string([]byte{0xff}) + "after", ReceivedAt: now,
	}, now)
	if !ok || !utf8.ValidString(message.Subject) || !utf8.ValidString(message.Text) || !strings.Contains(message.Text, "before") || !strings.Contains(message.Text, "after") {
		t.Fatalf("normalized message=%+v", message)
	}
}
