package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// LinkedAccount helper method tests
// ---------------------------------------------------------------------------

func TestUser_GetLinkedAccount_Found(t *testing.T) {
	linkedAt := time.Now()
	user := &User{
		ID: uuid.New(),
		LinkedAccounts: []LinkedAccount{
			{Provider: "telegram", ProviderID: "123456", Username: "@alice", LinkedAt: linkedAt},
			{Provider: "discord", ProviderID: "789", Username: "alice#0001", LinkedAt: linkedAt},
		},
	}

	acc := user.GetLinkedAccount("telegram")
	require.NotNil(t, acc)
	assert.Equal(t, "telegram", acc.Provider)
	assert.Equal(t, "123456", acc.ProviderID)
	assert.Equal(t, "@alice", acc.Username)
}

func TestUser_GetLinkedAccount_NotFound(t *testing.T) {
	user := &User{
		ID: uuid.New(),
		LinkedAccounts: []LinkedAccount{
			{Provider: "discord", ProviderID: "789"},
		},
	}

	acc := user.GetLinkedAccount("telegram")
	assert.Nil(t, acc)
}

func TestUser_GetLinkedAccount_EmptySlice(t *testing.T) {
	user := &User{ID: uuid.New(), LinkedAccounts: nil}
	assert.Nil(t, user.GetLinkedAccount("telegram"))
}

func TestUser_GetLinkedAccount_MultipleSameProvider(t *testing.T) {
	// Returns the first match when multiple entries share the same provider.
	user := &User{
		LinkedAccounts: []LinkedAccount{
			{Provider: "telegram", ProviderID: "first"},
			{Provider: "telegram", ProviderID: "second"},
		},
	}

	acc := user.GetLinkedAccount("telegram")
	require.NotNil(t, acc)
	assert.Equal(t, "first", acc.ProviderID)
}

func TestLinkedAccount_Fields(t *testing.T) {
	now := time.Now()
	acc := LinkedAccount{
		Provider:   "telegram",
		ProviderID: "1234567890",
		Username:   "@john",
		LinkedAt:   now,
	}

	assert.Equal(t, "telegram", acc.Provider)
	assert.Equal(t, "1234567890", acc.ProviderID)
	assert.Equal(t, "@john", acc.Username)
	assert.Equal(t, now, acc.LinkedAt)
}
