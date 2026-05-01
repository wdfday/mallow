package shared

import (
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

var pgUniqueKeyDetailRe = regexp.MustCompile(`Key \(([^)]+)\)=\(([^)]*)\)`)

// MapDatabaseError maps GORM/pgx(PostgreSQL) errors to application errors.
func MapDatabaseError(err error) error {
	if err == nil {
		return nil
	}

	if IsAppError(err) {
		return err
	}

	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return NewAppError(ErrCodeConflict, "Resource conflict", 409).
			WithDetails("reason", "duplicate_key").
			WithError(err)
	}
	if errors.Is(err, gorm.ErrForeignKeyViolated) {
		return NewAppError(ErrCodeBadRequest, "Bad request", 400).
			WithDetails("reason", "foreign_key_violation").
			WithError(err)
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}

	switch pgErr.Code {
	case "23505": // unique_violation
		appErr := NewAppError(ErrCodeConflict, "Resource conflict", 409).
			WithDetails("reason", "unique_violation")
		if field := extractFieldFromPGError(pgErr); field != "" {
			appErr = appErr.WithDetails("field", field)
		}
		if pgErr.ConstraintName != "" {
			appErr = appErr.WithDetails("constraint", pgErr.ConstraintName)
		}
		return appErr.WithError(err)
	case "23503": // foreign_key_violation
		appErr := NewAppError(ErrCodeBadRequest, "Bad request", 400).
			WithDetails("reason", "foreign_key_violation")
		if field := extractFieldFromPGError(pgErr); field != "" {
			appErr = appErr.WithDetails("field", field)
		}
		return appErr.WithError(err)
	case "23502": // not_null_violation
		appErr := NewAppError(ErrCodeBadRequest, "Bad request", 400).
			WithDetails("reason", "not_null_violation")
		if field := extractFieldFromPGError(pgErr); field != "" {
			appErr = appErr.WithDetails("field", field)
		}
		return appErr.WithError(err)
	case "23514": // check_violation
		return NewAppError(ErrCodeBadRequest, "Bad request", 400).
			WithDetails("reason", "check_violation").
			WithDetails("constraint", pgErr.ConstraintName).
			WithError(err)
	case "22P02": // invalid_text_representation
		return NewAppError(ErrCodeBadRequest, "Bad request", 400).
			WithDetails("reason", "invalid_text_representation").
			WithError(err)
	case "40001", "40P01": // serialization_failure, deadlock_detected
		return NewAppError(ErrCodeConflict, "Resource conflict", 409).
			WithDetails("reason", "transaction_retryable").
			WithError(err)
	default:
		return err
	}
}

func extractFieldFromPGError(pgErr *pgconn.PgError) string {
	if pgErr == nil {
		return ""
	}
	if pgErr.ColumnName != "" {
		return pgErr.ColumnName
	}
	if m := pgUniqueKeyDetailRe.FindStringSubmatch(pgErr.Detail); len(m) >= 2 {
		return m[1]
	}
	if pgErr.ConstraintName == "" {
		return ""
	}
	name := strings.ToLower(pgErr.ConstraintName)
	switch {
	case strings.Contains(name, "email"):
		return "email"
	case strings.Contains(name, "phone"):
		return "phone_number"
	case strings.Contains(name, "google"):
		return "google_id"
	case strings.Contains(name, "user_id"):
		return "user_id"
	default:
		return ""
	}
}
