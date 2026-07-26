package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
)

func TestTransientRetrievalErrorPreservesVerifiedCapability(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 7, 25, 6, 0, 0, 0, time.UTC)
	if err := store.UpsertRetrievalCapability(ctx, domain.MailboxRetrievalCapability{
		MailboxID: "mailbox", Method: domain.RetrievalMicrosoftGraph,
		Status: domain.RetrievalCapabilityAvailable, Preferred: true,
	}); err != nil {
		t.Fatal(err)
	}
	service := &MessageRetrievalService{capabilities: store, clock: func() time.Time { return now }}
	service.recordCapabilityResult(ctx, "mailbox", domain.RetrievalMicrosoftGraph, nil, errors.New("temporary EOF"))
	capability, err := store.GetRetrievalCapability(ctx, "mailbox", domain.RetrievalMicrosoftGraph)
	if err != nil {
		t.Fatal(err)
	}
	if capability.Status != domain.RetrievalCapabilityAvailable || !capability.Preferred || capability.ErrorCode != "sync_failed" {
		t.Fatalf("capability = %+v", capability)
	}
}
