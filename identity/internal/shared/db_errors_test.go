package shared

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

func TestMapDatabaseError_Nil(t *testing.T) {
	assert.NoError(t, MapDatabaseError(nil))
}

func TestMapDatabaseError_GormDuplicatedKey(t *testing.T) {
	err := MapDatabaseError(gorm.ErrDuplicatedKey)
	appErr := ToAppError(err)
	assert.Equal(t, ErrCodeConflict, appErr.Code)
	assert.Equal(t, 409, appErr.StatusCode)
	assert.Equal(t, "duplicate_key", appErr.Details["reason"])
}

func TestMapDatabaseError_PGUniqueViolation(t *testing.T) {
	raw := &pgconn.PgError{
		Code:           "23505",
		ConstraintName: "uniq_email_active",
		Detail:         "Key (email)=(alice@example.com) already exists.",
	}
	err := MapDatabaseError(raw)
	appErr := ToAppError(err)
	assert.Equal(t, ErrCodeConflict, appErr.Code)
	assert.Equal(t, 409, appErr.StatusCode)
	assert.Equal(t, "unique_violation", appErr.Details["reason"])
	assert.Equal(t, "email", appErr.Details["field"])
	assert.Equal(t, "uniq_email_active", appErr.Details["constraint"])
}

func TestMapDatabaseError_PGForeignKeyViolation(t *testing.T) {
	raw := &pgconn.PgError{
		Code:           "23503",
		ConstraintName: "user_profiles_user_id_fkey",
		ColumnName:     "user_id",
	}
	err := MapDatabaseError(raw)
	appErr := ToAppError(err)
	assert.Equal(t, ErrCodeBadRequest, appErr.Code)
	assert.Equal(t, 400, appErr.StatusCode)
	assert.Equal(t, "foreign_key_violation", appErr.Details["reason"])
	assert.Equal(t, "user_id", appErr.Details["field"])
}

func TestMapDatabaseError_PassThroughAppError(t *testing.T) {
	orig := NewAppError(ErrCodeBadRequest, "Bad request", 400).WithDetails("field", "x")
	err := MapDatabaseError(orig)
	assert.True(t, errors.Is(err, orig))
}
