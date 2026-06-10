package service

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// HelmClient resolves a user's owned resources via helm's NATS req/rep API.
// Used by the WS hub to scope account subscriptions to the authenticated user.
type HelmClient struct {
	nc      *nats.Conn
	timeout time.Duration
}

func NewHelmClient(nc *nats.Conn) *HelmClient {
	return &HelmClient{nc: nc, timeout: 3 * time.Second}
}

// reply mirrors helm's natsapi.Reply envelope.
type reply struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// helmResp is the subset of helm's HelmResp the gateway needs.
type helmResp struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
}

// UserScope is the set of subject keys a user owns.
type UserScope struct {
	HelmIDs    []string // → helm.events.{helm_id}
	AccountIDs []string // → trade.filled.{account_id}, portfolio.synced.{account_id}
}

// ResolveScope calls helm.helms.list scoped to userID and returns the owned
// helm and account ids. The caller identity travels in the request payload
// (CallerMeta) — helm trusts it on the internal NATS network.
func (c *HelmClient) ResolveScope(userID string) (UserScope, error) {
	req, _ := json.Marshal(struct {
		CallerUserID string `json:"caller_user_id"`
		CallerSvc    string `json:"caller_svc"`
	}{CallerUserID: userID, CallerSvc: "gateway"})

	msg, err := c.nc.Request("helm.helms.list", req, c.timeout)
	if err != nil {
		return UserScope{}, fmt.Errorf("helm list request: %w", err)
	}

	var rep reply
	if err := json.Unmarshal(msg.Data, &rep); err != nil {
		return UserScope{}, fmt.Errorf("helm list decode: %w", err)
	}
	if !rep.OK {
		return UserScope{}, fmt.Errorf("helm list error: %s", rep.Error)
	}

	var helms []helmResp
	if err := json.Unmarshal(rep.Data, &helms); err != nil {
		return UserScope{}, fmt.Errorf("helm list data: %w", err)
	}

	scope := UserScope{}
	seenAcct := make(map[string]struct{}, len(helms))
	for _, h := range helms {
		if h.ID != "" {
			scope.HelmIDs = append(scope.HelmIDs, h.ID)
		}
		if h.AccountID != "" {
			if _, dup := seenAcct[h.AccountID]; !dup {
				seenAcct[h.AccountID] = struct{}{}
				scope.AccountIDs = append(scope.AccountIDs, h.AccountID)
			}
		}
	}
	return scope, nil
}
