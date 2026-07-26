package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func TestFormatServiceOnlyEnsuresSupportedBuiltins(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	registry, err := providers.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	formats, err := service.NewFormatService(store, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := formats.EnsureBuiltins(ctx); err != nil {
		t.Fatal(err)
	}
	if err := formats.EnsureBuiltins(ctx); err != nil {
		t.Fatalf("ensure builtins is not idempotent: %v", err)
	}

	items, err := formats.List(ctx, ports.ListOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "fmt_builtin_outlook4" || items[1].ID != "fmt_builtin_pickup2" {
		t.Fatalf("built-in formats = %+v", items)
	}
	if items[0].Name != "Outlook 邮箱凭证" || items[1].Name != "平台取件格式" {
		t.Fatalf("built-in format names = %q, %q", items[0].Name, items[1].Name)
	}
	for _, id := range []string{"fmt_builtin_registered6", "fmt_builtin_simple3", "fmt_builtin_cf_routed3"} {
		if _, err := formats.Get(ctx, id); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("removed format %q error = %v", id, err)
		}
	}
}
