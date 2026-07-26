package httpapi

import (
	"testing"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func TestOverviewMicrosoftModesOnlyIncludeVerifiedCapabilities(t *testing.T) {
	auth := overviewCredentialAuth("microsoft", []service.CredentialSummary{{
		RetrievalMethods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth},
		RetrievalCapabilities: []service.RetrievalCapabilitySummary{
			{Method: domain.RetrievalMicrosoftGraph, Status: "failed"},
			{Method: domain.RetrievalIMAPOAuth, Status: "verified"},
		},
	}})
	if len(auth.Modes) != 1 || auth.Modes[0] != "imap" {
		t.Fatalf("modes = %v", auth.Modes)
	}
}

func TestOverviewMicrosoftModesHideUnverifiedCapabilities(t *testing.T) {
	auth := overviewCredentialAuth("microsoft", []service.CredentialSummary{{
		RetrievalMethods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth},
		RetrievalCapabilities: []service.RetrievalCapabilitySummary{
			{Method: domain.RetrievalMicrosoftGraph, Status: "configured"},
			{Method: domain.RetrievalIMAPOAuth, Status: "unknown"},
		},
	}})
	if len(auth.Modes) != 0 {
		t.Fatalf("modes = %v", auth.Modes)
	}
}
