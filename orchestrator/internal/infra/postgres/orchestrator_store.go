package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"orchestrator/internal/module/orchesrator/domain"
)

// PostgresOrchestratorStore implements domain.OrchestratorRepo against the orchestrators table.
type PostgresOrchestratorStore struct {
	db *sql.DB
}

// NewOrchestratorStore creates a PostgresOrchestratorStore.
// DDL (run once):
//
//	CREATE TABLE orchestrators (
//	    id              UUID PRIMARY KEY,
//	    user_id         UUID NOT NULL,
//	    account_id      UUID NOT NULL UNIQUE,
//	    name            TEXT NOT NULL,
//	    capital         DOUBLE PRECISION NOT NULL DEFAULT 0,
//	    exchange_config JSONB NOT NULL DEFAULT '{}',
//	    risk_config     JSONB NOT NULL DEFAULT '{}',
//	    enabled         BOOLEAN NOT NULL DEFAULT FALSE,
//	    status          TEXT NOT NULL DEFAULT 'active',
//	    last_synced_at  TIMESTAMPTZ,
//	    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
//	    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
//	);
//	CREATE INDEX idx_orchestrators_user_id ON orchestrators(user_id);
//	-- migration for existing tables:
//	ALTER TABLE orchestrators ADD COLUMN IF NOT EXISTS last_synced_at TIMESTAMPTZ;
func NewOrchestratorStore(db *sql.DB) *PostgresOrchestratorStore {
	return &PostgresOrchestratorStore{db: db}
}

const orchSelectCols = `id, user_id, account_id, name, capital, exchange_config, risk_config, enabled, status, last_synced_at, created_at, updated_at`

func (s *PostgresOrchestratorStore) Save(o *domain.OrchestratorConfig) error {
	exchJSON, err := json.Marshal(o.Exchange)
	if err != nil {
		return fmt.Errorf("marshal exchange: %w", err)
	}
	riskJSON, err := json.Marshal(o.Risk)
	if err != nil {
		return fmt.Errorf("marshal risk: %w", err)
	}

	var lastSyncedAt *time.Time
	if o.LastSyncedAt != nil {
		t := o.LastSyncedAt.UTC()
		lastSyncedAt = &t
	}
	_, err = s.db.Exec(`
		INSERT INTO orchestrators (id, user_id, account_id, name, capital, exchange_config, risk_config, enabled, status, last_synced_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO UPDATE SET
			name=$4, capital=$5, exchange_config=$6, risk_config=$7, enabled=$8, status=$9, last_synced_at=$10, updated_at=$12`,
		o.ID, o.UserID, o.AccountID, o.Name, o.Capital,
		exchJSON, riskJSON, o.Enabled,
		o.Status, lastSyncedAt, o.CreatedAt, o.UpdatedAt,
	)
	return err
}

func (s *PostgresOrchestratorStore) Get(id uuid.UUID) (*domain.OrchestratorConfig, error) {
	row := s.db.QueryRow(
		`SELECT `+orchSelectCols+` FROM orchestrators WHERE id=$1`, id,
	)
	o, err := scanOrchRow(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("orchestrator %q not found", id)
	}
	return o, err
}

func (s *PostgresOrchestratorStore) GetByAccountID(accountID uuid.UUID) (*domain.OrchestratorConfig, error) {
	row := s.db.QueryRow(
		`SELECT `+orchSelectCols+` FROM orchestrators WHERE account_id=$1`, accountID,
	)
	o, err := scanOrchRow(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("orchestrator for account %q not found", accountID)
	}
	return o, err
}

func (s *PostgresOrchestratorStore) All() ([]*domain.OrchestratorConfig, error) {
	rows, err := s.db.Query(`SELECT ` + orchSelectCols + ` FROM orchestrators ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*domain.OrchestratorConfig
	for rows.Next() {
		o, err := scanOrchRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *PostgresOrchestratorStore) AllByUser(userID uuid.UUID) ([]*domain.OrchestratorConfig, error) {
	rows, err := s.db.Query(
		`SELECT `+orchSelectCols+` FROM orchestrators WHERE user_id=$1 ORDER BY created_at`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*domain.OrchestratorConfig
	for rows.Next() {
		o, err := scanOrchRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *PostgresOrchestratorStore) Update(id uuid.UUID, fn func(*domain.OrchestratorConfig) error) error {
	o, err := s.Get(id)
	if err != nil {
		return err
	}
	if err := fn(o); err != nil {
		return err
	}
	o.UpdatedAt = time.Now().UTC()
	return s.Save(o)
}

func (s *PostgresOrchestratorStore) Delete(id uuid.UUID) error {
	res, err := s.db.Exec(`DELETE FROM orchestrators WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("orchestrator %q not found", id)
	}
	return nil
}

// --- scanners ---

type orchScanner interface {
	Scan(dest ...any) error
}

func scanOrchRow(row *sql.Row) (*domain.OrchestratorConfig, error) {
	var o domain.OrchestratorConfig
	var exchJSON, riskJSON []byte
	var lastSyncedAt sql.NullTime
	if err := row.Scan(
		&o.ID, &o.UserID, &o.AccountID, &o.Name, &o.Capital,
		&exchJSON, &riskJSON, &o.Enabled, &o.Status,
		&lastSyncedAt, &o.CreatedAt, &o.UpdatedAt,
	); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(exchJSON, &o.Exchange)
	_ = json.Unmarshal(riskJSON, &o.Risk)
	if lastSyncedAt.Valid {
		t := lastSyncedAt.Time.UTC()
		o.LastSyncedAt = &t
	}
	return &o, nil
}

func scanOrchRows(rows *sql.Rows) (*domain.OrchestratorConfig, error) {
	var o domain.OrchestratorConfig
	var exchJSON, riskJSON []byte
	var lastSyncedAt sql.NullTime
	if err := rows.Scan(
		&o.ID, &o.UserID, &o.AccountID, &o.Name, &o.Capital,
		&exchJSON, &riskJSON, &o.Enabled, &o.Status,
		&lastSyncedAt, &o.CreatedAt, &o.UpdatedAt,
	); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(exchJSON, &o.Exchange)
	_ = json.Unmarshal(riskJSON, &o.Risk)
	if lastSyncedAt.Valid {
		t := lastSyncedAt.Time.UTC()
		o.LastSyncedAt = &t
	}
	return &o, nil
}

func (s *PostgresOrchestratorStore) UpdateLastSyncedAt(id uuid.UUID, t time.Time) error {
	_, err := s.db.Exec(
		`UPDATE orchestrators SET last_synced_at=$1, updated_at=$2
		 WHERE id=$3 AND (last_synced_at IS NULL OR last_synced_at < $1)`,
		t.UTC(), time.Now().UTC(), id,
	)
	return err
}
