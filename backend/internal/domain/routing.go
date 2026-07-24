package domain

import (
	"encoding/json"
	"time"
)

type ForwardingRouteStatus string

const (
	ForwardingRoutePending  ForwardingRouteStatus = "pending"
	ForwardingRouteActive   ForwardingRouteStatus = "active"
	ForwardingRouteDisabled ForwardingRouteStatus = "disabled"
	ForwardingRouteError    ForwardingRouteStatus = "error"
)

// ForwardingRoute maps a provider-managed source address to a destination
// mailbox owned by this platform. DestinationMailboxID is the stable route;
// provider-native destination IDs stay in metadata.
type ForwardingRoute struct {
	ID                   string                `json:"id"`
	AliasID              string                `json:"alias_id"`
	DestinationMailboxID string                `json:"destination_mailbox_id"`
	SourceAddress        string                `json:"source_address"`
	DestinationAddress   string                `json:"destination_address"`
	Status               ForwardingRouteStatus `json:"status"`
	ExternalReference    string                `json:"external_reference,omitempty"`
	Metadata             json.RawMessage       `json:"metadata,omitempty"`
	CreatedAt            time.Time             `json:"created_at"`
	UpdatedAt            time.Time             `json:"updated_at"`
}

type DomainRouteRequest struct {
	Zone                 string
	LocalPart            string
	DestinationMailboxID string
	DestinationAddress   string
}

type DomainRouteResult struct {
	SourceAddress     string
	ExternalReference string
	Metadata          json.RawMessage
}
