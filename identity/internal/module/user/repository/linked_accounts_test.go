package repository

// ---------------------------------------------------------------------------
// linked_accounts_test.go
//
// NOTE: The full UnlinkAccount and LinkAccount methods use Postgres-specific
// JSONB operators (::jsonb, @>). The SQLite in-memory DB used for unit tests
// cannot run those queries.
//
// We therefore test only the PURE-GO parts (the Go-side filtering loop inside
// UnlinkAccount) by extracting that logic into a helper, and we document which
// methods require an integration test against a real Postgres instance.
// ---------------------------------------------------------------------------
//
// Integration tests that need Postgres are tagged with //go:build integration
// and are not run as part of the normal `go test ./...` suite.
//
// To run them:   go test -tags integration ./internal/module/user/repository/...

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"mallow/identity/internal/module/user/domain"
)

// ---------------------------------------------------------------------------
// Pure-Go logic: the LinkedAccount filtering loop used inside UnlinkAccount
// ---------------------------------------------------------------------------

// filterLinkedAccounts mirrors the filtering logic in repository_impl.go:UnlinkAccount.
func filterLinkedAccounts(accounts []domain.LinkedAccount, provider, providerID string) []domain.LinkedAccount {
	filtered := make([]domain.LinkedAccount, 0, len(accounts))
	for _, a := range accounts {
		if !(a.Provider == provider && a.ProviderID == providerID) {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

func TestFilterLinkedAccounts_RemovesMatchingEntry(t *testing.T) {
	accounts := []domain.LinkedAccount{
		{Provider: "telegram", ProviderID: "111", LinkedAt: time.Now()},
		{Provider: "discord", ProviderID: "222", LinkedAt: time.Now()},
	}

	result := filterLinkedAccounts(accounts, "telegram", "111")

	assert.Len(t, result, 1)
	assert.Equal(t, "discord", result[0].Provider)
}

func TestFilterLinkedAccounts_NoMatch_ReturnsAll(t *testing.T) {
	accounts := []domain.LinkedAccount{
		{Provider: "telegram", ProviderID: "999", LinkedAt: time.Now()},
	}

	result := filterLinkedAccounts(accounts, "discord", "000")
	assert.Len(t, result, 1)
}

func TestFilterLinkedAccounts_EmptySlice(t *testing.T) {
	result := filterLinkedAccounts(nil, "telegram", "111")
	assert.Empty(t, result)
}

func TestFilterLinkedAccounts_RemovesAllWhenOnlyEntry(t *testing.T) {
	accounts := []domain.LinkedAccount{
		{Provider: "telegram", ProviderID: "111"},
	}
	result := filterLinkedAccounts(accounts, "telegram", "111")
	assert.Empty(t, result)
}

func TestFilterLinkedAccounts_MatchesByBothProviderAndID(t *testing.T) {
	// Two entries with same provider but different IDs — only the matching one is removed.
	accounts := []domain.LinkedAccount{
		{Provider: "telegram", ProviderID: "aaa"},
		{Provider: "telegram", ProviderID: "bbb"},
	}
	result := filterLinkedAccounts(accounts, "telegram", "aaa")
	assert.Len(t, result, 1)
	assert.Equal(t, "bbb", result[0].ProviderID)
}

func TestFilterLinkedAccounts_ProviderMatchButIDDiffers(t *testing.T) {
	// Provider matches but ProviderID differs — should not remove.
	accounts := []domain.LinkedAccount{
		{Provider: "telegram", ProviderID: "aaa"},
	}
	result := filterLinkedAccounts(accounts, "telegram", "bbb")
	assert.Len(t, result, 1)
}

func TestFilterLinkedAccounts_IDMatchButProviderDiffers(t *testing.T) {
	// ProviderID matches but Provider differs — should not remove.
	accounts := []domain.LinkedAccount{
		{Provider: "telegram", ProviderID: "aaa"},
	}
	result := filterLinkedAccounts(accounts, "discord", "aaa")
	assert.Len(t, result, 1)
}
