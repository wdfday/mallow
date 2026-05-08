package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"mallow/helm/internal/module/helm/domain"
)

// PostgresHelmStore implements domain.HelmRepo against the orchestrators table.
type PostgresHelmStore struct {
	db *sql.DB
}

// NewHelmStore creates a PostgresHelmStore.
// DDL (run once):
//
//	CREATE TABLE helms (
//	    id              UUID PRIMARY KEY,
//	    user_id         UUID NOT NULL,
//	    account_id      UUID NOT NULL UNIQUE,
//	    name            TEXT NOT NULL,
//	    capital         NUMERIC(28,10) NOT NULL DEFAULT 0,
//	    exchange_config JSONB NOT NULL DEFAULT '{}',
//	    risk_config     JSONB NOT NULL DEFAULT '{}',
//	    portfolio       JSONB NOT NULL DEFAULT '{}',
//	    enabled         BOOLEAN NOT NULL DEFAULT FALSE,
//	    status          TEXT NOT NULL DEFAULT 'active',
//	    last_synced_at  TIMESTAMPTZ,
//	    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
//	    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
//	);
//	CREATE INDEX idx_helms_user_id ON helms(user_id);
//	-- migration for existing tables:
//	ALTER TABLE helms ADD COLUMN IF NOT EXISTS last_synced_at TIMESTAMPTZ;
//	ALTER TABLE helms ADD COLUMN IF NOT EXISTS portfolio JSONB NOT NULL DEFAULT '{}';
//	ALTER TABLE helms ALTER COLUMN capital TYPE NUMERIC(28,10) USING capital::NUMERIC;
func NewHelmStore(db *sql.DB) *PostgresHelmStore {
	return &PostgresHelmStore{db: db}
}

const orchSelectCols = `id, user_id, account_id, name, capital, exchange_config, risk_config, portfolio, enabled, status, last_synced_at, created_at, updated_at`

func (s *PostgresHelmStore) Save(o *domain.HelmConfig) error {
	exchJSON, err := json.Marshal(o.Exchange)
	if err != nil {
		return fmt.Errorf("marshal exchange: %w", err)
	}
	riskJSON, err := json.Marshal(o.Risk)
	if err != nil {
		return fmt.Errorf("marshal risk: %w", err)
	}
	portfolioJSON, err := json.Marshal(o.Portfolio)
	if err != nil {
		return fmt.Errorf("marshal portfolio: %w", err)
	}

	var lastSyncedAt *time.Time
	if o.LastSyncedAt != nil {
		t := o.LastSyncedAt.UTC()
		lastSyncedAt = &t
	}
	_, err = s.db.Exec(`
		INSERT INTO helms (id, user_id, account_id, name, capital, exchange_config, risk_config, portfolio, enabled, status, last_synced_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (id) DO UPDATE SET
			name=$4, capital=$5, exchange_config=$6, risk_config=$7, portfolio=$8, enabled=$9, status=$10, last_synced_at=$11, updated_at=$13`,
		o.ID, o.UserID, o.AccountID, o.Name, o.Capital,
		exchJSON, riskJSON, portfolioJSON, o.Enabled,
		o.Status, lastSyncedAt, o.CreatedAt, o.UpdatedAt,
	)
	return err
}

func (s *PostgresHelmStore) Get(id uuid.UUID) (*domain.HelmConfig, error) {
	row := s.db.QueryRow(
		`SELECT `+orchSelectCols+` FROM helms WHERE id=$1`, id,
	)
	o, err := scanOrchRow(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("helm %q not found", id)
	}
	return o, err
}

func (s *PostgresHelmStore) GetByAccountID(accountID uuid.UUID) (*domain.HelmConfig, error) {
	row := s.db.QueryRow(
		`SELECT `+orchSelectCols+` FROM helms WHERE account_id=$1`, accountID,
	)
	o, err := scanOrchRow(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("helm for account %q not found", accountID)
	}
	return o, err
}

func (s *PostgresHelmStore) All() ([]*domain.HelmConfig, error) {
	rows, err := s.db.Query(`SELECT ` + orchSelectCols + ` FROM helms ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*domain.HelmConfig
	for rows.Next() {
		o, err := scanOrchRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *PostgresHelmStore) AllByUser(userID uuid.UUID) ([]*domain.HelmConfig, error) {
	rows, err := s.db.Query(
		`SELECT `+orchSelectCols+` FROM helms WHERE user_id=$1 ORDER BY created_at`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*domain.HelmConfig
	for rows.Next() {
		o, err := scanOrchRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *PostgresHelmStore) Update(id uuid.UUID, fn func(*domain.HelmConfig) error) error {
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

func (s *PostgresHelmStore) Delete(id uuid.UUID) error {
	res, err := s.db.Exec(`DELETE FROM helms WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("helm %q not found", id)
	}
	return nil
}

// --- scanners ---

type orchScanner interface {
	Scan(dest ...any) error
}

func scanOrchRow(row *sql.Row) (*domain.HelmConfig, error) {
	var o domain.HelmConfig
	var exchJSON, riskJSON, portfolioJSON []byte
	var lastSyncedAt sql.NullTime
	if err := row.Scan(
		&o.ID, &o.UserID, &o.AccountID, &o.Name, &o.Capital,
		&exchJSON, &riskJSON, &portfolioJSON, &o.Enabled, &o.Status,
		&lastSyncedAt, &o.CreatedAt, &o.UpdatedAt,
	); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(exchJSON, &o.Exchange)
	_ = json.Unmarshal(riskJSON, &o.Risk)
	_ = json.Unmarshal(portfolioJSON, &o.Portfolio)
	if lastSyncedAt.Valid {
		t := lastSyncedAt.Time.UTC()
		o.LastSyncedAt = &t
	}
	return &o, nil
}

func scanOrchRows(rows *sql.Rows) (*domain.HelmConfig, error) {
	var o domain.HelmConfig
	var exchJSON, riskJSON, portfolioJSON []byte
	var lastSyncedAt sql.NullTime
	if err := rows.Scan(
		&o.ID, &o.UserID, &o.AccountID, &o.Name, &o.Capital,
		&exchJSON, &riskJSON, &portfolioJSON, &o.Enabled, &o.Status,
		&lastSyncedAt, &o.CreatedAt, &o.UpdatedAt,
	); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(exchJSON, &o.Exchange)
	_ = json.Unmarshal(riskJSON, &o.Risk)
	_ = json.Unmarshal(portfolioJSON, &o.Portfolio)
	if lastSyncedAt.Valid {
		t := lastSyncedAt.Time.UTC()
		o.LastSyncedAt = &t
	}
	return &o, nil
}

func (s *PostgresHelmStore) UpdateLastSyncedAt(id uuid.UUID, t time.Time) error {
	_, err := s.db.Exec(
		`UPDATE helms SET last_synced_at=$1, updated_at=$2
		 WHERE id=$3 AND (last_synced_at IS NULL OR last_synced_at < $1)`,
		t.UTC(), time.Now().UTC(), id,
	)
	return err
}
