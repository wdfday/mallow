package dto

import "mallow/helm/internal/infra/journal/eventlog"

// EventsPageResp is a page of helm behavioral events, ordered newest-first.
// Paging is a backward time cursor: pass Next back as ?before= to fetch older
// events. Empty Next means no more history.
type EventsPageResp struct {
	Events  []eventlog.EventRecord `json:"events"`
	Next    string                 `json:"next,omitempty"` // RFC3339 cursor; empty = no more
	HasMore bool                   `json:"has_more"`
	Limit   int                    `json:"limit"`
}
