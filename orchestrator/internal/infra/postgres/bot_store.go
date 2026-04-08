package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"orchestrator/internal/module/bot/domain"
)

// PostgresBotStore implements domain.BotRepo against the bots table.
type PostgresBotStore struct {
	db *sql.DB
}

// NewBotStore creates a PostgresBotStore.
// DDL (run once):
//
//	CREATE TABLE bots (
//	    id              TEXT PRIMARY KEY,
//	    orchestrator_id UUID NOT NULL REFERENCES orchestrators(id) ON DELETE CASCADE,
//	    name            TEXT NOT NULL,
//	    type            TEXT NOT NULL DEFAULT 'signal_follower',
//	    symbols         JSONB NOT NULL DEFAULT '[]',
//	    tactic          JSONB NOT NULL DEFAULT '{}',
//	    risk            JSONB NOT NULL DEFAULT '{}',
//	    status          TEXT NOT NULL DEFAULT 'stopped',
//	    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
//	    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
//	);
//	CREATE INDEX idx_bots_orchestrator_id ON bots(orchestrator_id);
//	CREATE INDEX idx_bots_status          ON bots(status);
//	-- Migration from old schema:
//	ALTER TABLE bots RENAME COLUMN strategy TO tactic;
func NewBotStore(db *sql.DB) *PostgresBotStore {
	return &PostgresBotStore{db: db}
}

const botSelectCols = `id, orchestrator_id, name, type, symbols, tactic, risk, status, created_at`

func (s *PostgresBotStore) GenerateID() string {
	return uuid.New().String()
}

func (s *PostgresBotStore) Save(b *domain.BotInstance) error {
	symbolsJSON, err := json.Marshal(b.Config.Symbols)
	if err != nil {
		return fmt.Errorf("marshal symbols: %w", err)
	}
	stratJSON, err := json.Marshal(b.Config.Tactic)
	if err != nil {
		return fmt.Errorf("marshal tactic: %w", err)
	}
	riskJSON, err := json.Marshal(b.Config.Risk)
	if err != nil {
		return fmt.Errorf("marshal risk: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO bots (id, orchestrator_id, name, type, symbols, tactic, risk, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NOW())
		ON CONFLICT (id) DO UPDATE SET
			name=$3, type=$4, symbols=$5, tactic=$6, risk=$7, status=$8, updated_at=NOW()`,
		b.ID, b.OrchestratorID,
		b.Config.Name, string(b.Config.Type),
		symbolsJSON, stratJSON, riskJSON,
		b.Status, b.CreatedAt,
	)
	return err
}

func (s *PostgresBotStore) Get(id string) (*domain.BotInstance, error) {
	row := s.db.QueryRow(
		`SELECT `+botSelectCols+` FROM bots WHERE id=$1`, id,
	)
	b, err := scanBotRow(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("bot %q not found", id)
	}
	return b, err
}

func (s *PostgresBotStore) All() []*domain.BotInstance {
	rows, err := s.db.Query(`SELECT ` + botSelectCols + ` FROM bots ORDER BY created_at`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []*domain.BotInstance
	for rows.Next() {
		b, err := scanBotRows(rows)
		if err != nil {
			continue
		}
		out = append(out, b)
	}
	return out
}

// AllByOrchestrator returns all bots belonging to the given orchestrator.
func (s *PostgresBotStore) AllByOrchestrator(orchID uuid.UUID) []*domain.BotInstance {
	rows, err := s.db.Query(
		`SELECT `+botSelectCols+` FROM bots WHERE orchestrator_id=$1 ORDER BY created_at`, orchID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []*domain.BotInstance
	for rows.Next() {
		b, err := scanBotRows(rows)
		if err != nil {
			continue
		}
		out = append(out, b)
	}
	return out
}

func (s *PostgresBotStore) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM bots WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("bot %q not found", id)
	}
	return nil
}

func (s *PostgresBotStore) Update(id string, fn func(*domain.BotInstance) error) error {
	b, err := s.Get(id)
	if err != nil {
		return err
	}
	if err := fn(b); err != nil {
		return err
	}
	return s.Save(b)
}

// --- scanners ---

type botRowScanner interface {
	Scan(dest ...any) error
}

func scanBotRow(row *sql.Row) (*domain.BotInstance, error) {
	var b domain.BotInstance
	var orchID uuid.UUID
	var botType string
	var symbolsJSON, stratJSON, riskJSON []byte
	var createdAt time.Time

	if err := row.Scan(
		&b.ID, &orchID, &b.Config.Name, &botType,
		&symbolsJSON, &stratJSON, &riskJSON,
		&b.Status, &createdAt,
	); err != nil {
		return nil, err
	}
	b.OrchestratorID = orchID
	b.Config.OrchestratorID = orchID
	b.Config.Type = domain.BotType(botType)
	b.CreatedAt = createdAt
	_ = json.Unmarshal(symbolsJSON, &b.Config.Symbols)
	_ = json.Unmarshal(stratJSON, &b.Config.Tactic)
	_ = json.Unmarshal(riskJSON, &b.Config.Risk)
	return &b, nil
}

func scanBotRows(rows *sql.Rows) (*domain.BotInstance, error) {
	var b domain.BotInstance
	var orchID uuid.UUID
	var botType string
	var symbolsJSON, stratJSON, riskJSON []byte
	var createdAt time.Time

	if err := rows.Scan(
		&b.ID, &orchID, &b.Config.Name, &botType,
		&symbolsJSON, &stratJSON, &riskJSON,
		&b.Status, &createdAt,
	); err != nil {
		return nil, err
	}
	b.OrchestratorID = orchID
	b.Config.OrchestratorID = orchID
	b.Config.Type = domain.BotType(botType)
	b.CreatedAt = createdAt
	_ = json.Unmarshal(symbolsJSON, &b.Config.Symbols)
	_ = json.Unmarshal(stratJSON, &b.Config.Tactic)
	_ = json.Unmarshal(riskJSON, &b.Config.Risk)
	return &b, nil
}
