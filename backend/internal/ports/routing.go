package ports

import (
	"context"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

// DomainRoutingProvider captures Cloudflare Email Routing-like capabilities.
// Business services depend on this port rather than Cloudflare zone, rule, or
// token payloads.
type DomainRoutingProvider interface {
	CreateRoute(ctx context.Context, request domain.DomainRouteRequest) (domain.DomainRouteResult, error)
	DeleteRoute(ctx context.Context, externalReference string) error
	VerifyDestination(ctx context.Context, destinationAddress string) error
}

// ForwardingRouteRepository persists the internal source-to-destination route
// independently from mailbox credentials and provider configuration.
type ForwardingRouteRepository interface {
	CreateForwardingRoute(ctx context.Context, route domain.ForwardingRoute) error
	GetForwardingRoute(ctx context.Context, id string) (domain.ForwardingRoute, error)
	ListForwardingRoutes(ctx context.Context, mailboxID string, options ListOptions) ([]domain.ForwardingRoute, error)
	UpdateForwardingRoute(ctx context.Context, route domain.ForwardingRoute) error
}
