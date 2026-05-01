package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"

	"gorm.io/gorm"
)

type gormRepo struct {
	db *gorm.DB
}

func New(db *gorm.DB) Repository {
	return &gormRepo{db: db}
}

// helper: always filter out soft-deleted by default
func base(db *gorm.DB) *gorm.DB {
	return db.Where("users.deleted_at IS NULL")
}

func (r *gormRepo) Create(ctx context.Context, u *domain.User) error {
	if u.LinkedAccounts == nil {
		u.LinkedAccounts = []domain.LinkedAccount{}
	}
	return shared.MapDatabaseError(r.db.WithContext(ctx).Create(u).Error)
}

func (r *gormRepo) GetByID(ctx context.Context, id string) (*domain.User, error) {
	var u domain.User
	if err := base(r.db).WithContext(ctx).First(&u, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, shared.ErrUserNotFound
		}
		return nil, shared.MapDatabaseError(err)
	}
	return &u, nil
}

func (r *gormRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	var u domain.User
	if err := base(r.db).WithContext(ctx).
		First(&u, "email = ?", strings.ToLower(email)).
		Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, shared.ErrUserNotFound
		}
		return nil, shared.MapDatabaseError(err)
	}
	return &u, nil
}

func (r *gormRepo) GetByGoogleID(ctx context.Context, googleID string) (*domain.User, error) {
	var u domain.User
	if err := base(r.db).WithContext(ctx).
		First(&u, "google_id = ?", googleID).
		Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, shared.ErrUserNotFound
		}
		return nil, shared.MapDatabaseError(err)
	}
	return &u, nil
}

func (r *gormRepo) Count(ctx context.Context, f domain.ListUsersFilter) (int64, error) {
	var cnt int64
	q := r.applyFilter(base(r.db), f)
	if err := q.WithContext(ctx).Model(&domain.User{}).Count(&cnt).Error; err != nil {
		return 0, shared.MapDatabaseError(err)
	}
	return cnt, nil
}

func (r *gormRepo) List(ctx context.Context, f domain.ListUsersFilter, p shared.Pagination) (shared.Page[domain.User], error) {
	if p.Page <= 0 {
		p.Page = 1
	}
	if p.PerPage <= 0 || p.PerPage > 200 {
		p.PerPage = 20
	}
	q := r.applyFilter(base(r.db), f)

	// Sorting mặc định
	sort := p.Sort
	if strings.TrimSpace(sort) == "" {
		sort = "last_active_at desc"
	}

	var items []domain.User
	if err := q.WithContext(ctx).
		Order(sort).
		Limit(p.PerPage).
		Offset((p.Page - 1) * p.PerPage).
		Find(&items).Error; err != nil {
		return shared.Page[domain.User]{}, shared.MapDatabaseError(err)
	}

	total, err := r.Count(ctx, f)
	if err != nil {
		return shared.Page[domain.User]{}, err
	}
	totalPages := int((total + int64(p.PerPage) - 1) / int64(p.PerPage))

	return shared.Page[domain.User]{
		Data:         items,
		TotalItems:   total,
		CurrentPage:  p.Page,
		ItemsPerPage: p.PerPage,
		TotalPages:   totalPages,
	}, nil
}

func (r *gormRepo) applyFilter(db *gorm.DB, f domain.ListUsersFilter) *gorm.DB {
	q := db
	if f.Query != "" {
		like := "%" + strings.ToLower(f.Query) + "%"
		q = q.Joins("LEFT JOIN user_profiles up ON up.user_id = users.id AND up.deleted_at IS NULL").
			Where(`lower(users.email) LIKE ? OR lower(up.full_name) LIKE ? OR lower(up.display_name) LIKE ?`, like, like, like)
	}
	if f.Role != nil {
		q = q.Where("role = ?", *f.Role)
	}
	if f.Status != nil {
		q = q.Where("status = ?", *f.Status)
	}
	if f.EmailVerified != nil {
		q = q.Where("email_verified = ?", *f.EmailVerified)
	}
	if !f.ActiveOnly {
		// cho phép lọc cả bản ghi đã xoá
		q = q.Session(&gorm.Session{}).Where("1=1") // giữ nguyên
	}
	return q
}

func (r *gormRepo) Update(ctx context.Context, u *domain.User) error {
	u.UpdatedAt = time.Now()
	return shared.MapDatabaseError(r.db.WithContext(ctx).Save(u).Error)
}

func (r *gormRepo) UpdateColumns(ctx context.Context, id string, cols map[string]any) error {
	cols["updated_at"] = time.Now()
	tx := r.db.WithContext(ctx).Model(&domain.User{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(cols)
	if tx.Error != nil {
		return shared.MapDatabaseError(tx.Error)
	}
	if tx.RowsAffected == 0 {
		return shared.ErrUserNotFound
	}
	return nil
}

func (r *gormRepo) SoftDelete(ctx context.Context, id string) error {
	now := time.Now()
	tx := r.db.WithContext(ctx).Model(&domain.User{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Update("deleted_at", now)
	if tx.Error != nil {
		return shared.MapDatabaseError(tx.Error)
	}
	if tx.RowsAffected == 0 {
		return shared.ErrUserNotFound
	}
	return nil
}

func (r *gormRepo) Restore(ctx context.Context, id string) error {
	tx := r.db.WithContext(ctx).Unscoped().Model(&domain.User{}).
		Where("id = ? AND deleted_at IS NOT NULL", id).
		Update("deleted_at", nil)
	if tx.Error != nil {
		return shared.MapDatabaseError(tx.Error)
	}
	if tx.RowsAffected == 0 {
		return shared.ErrUserNotFound
	}
	return nil
}

func (r *gormRepo) HardDelete(ctx context.Context, id string) error {
	tx := r.db.WithContext(ctx).Unscoped().
		Where("id = ?", id).
		Delete(&domain.User{})
	if tx.Error != nil {
		return shared.MapDatabaseError(tx.Error)
	}
	if tx.RowsAffected == 0 {
		return shared.ErrUserNotFound
	}
	return nil
}

func (r *gormRepo) MarkEmailVerified(ctx context.Context, id string, at time.Time) error {
	return r.UpdateColumns(ctx, id, map[string]any{
		"email_verified":    true,
		"email_verified_at": at,
	})
}

func (r *gormRepo) IncLoginAttempts(ctx context.Context, id string) error {
	tx := r.db.WithContext(ctx).Model(&domain.User{}).
		Where("id = ? AND deleted_at IS NULL", id).
		UpdateColumn("login_attempts", gorm.Expr("login_attempts + 1"))
	if tx.Error != nil {
		return shared.MapDatabaseError(tx.Error)
	}
	if tx.RowsAffected == 0 {
		return shared.ErrUserNotFound
	}
	return nil
}

func (r *gormRepo) ResetLoginAttempts(ctx context.Context, id string) error {
	return r.UpdateColumns(ctx, id, map[string]any{"login_attempts": 0})
}

func (r *gormRepo) SetLockedUntil(ctx context.Context, id string, until *time.Time) error {
	return r.UpdateColumns(ctx, id, map[string]any{"locked_until": until})
}

func (r *gormRepo) UpdateLastLogin(ctx context.Context, id string, at time.Time, ip *string) error {
	return r.UpdateColumns(ctx, id, map[string]any{
		"last_login_at": at,
		"last_login_ip": ip,
	})
}

func (r *gormRepo) GetByLinkedAccount(ctx context.Context, provider, providerID string) (*domain.User, error) {
	var u domain.User
	filter, err := json.Marshal([]map[string]string{{"provider": provider, "provider_id": providerID}})
	if err != nil {
		return nil, err
	}
	if err := base(r.db).WithContext(ctx).
		Where("linked_accounts @> ?::jsonb", string(filter)).
		First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, shared.ErrUserNotFound
		}
		return nil, shared.MapDatabaseError(err)
	}
	return &u, nil
}

func (r *gormRepo) LinkAccount(ctx context.Context, userID string, account domain.LinkedAccount) error {
	account.LinkedAt = time.Now()
	newEntry, err := json.Marshal([]domain.LinkedAccount{account})
	if err != nil {
		return err
	}
	conflict, err := json.Marshal([]map[string]string{{"provider": account.Provider, "provider_id": account.ProviderID}})
	if err != nil {
		return err
	}
	// Atomic append: only if not already linked with the same provider+id.
	// Note: NOT (NULL @> ...) = NULL in Postgres (not TRUE), so we must explicitly
	// allow the update when linked_accounts IS NULL.
	tx := r.db.WithContext(ctx).Exec(`
		UPDATE users
		SET linked_accounts = CASE
			WHEN linked_accounts IS NULL OR linked_accounts::text = 'null' THEN ?::jsonb
			ELSE linked_accounts || ?::jsonb
		END,
		updated_at = NOW()
		WHERE id = ? AND deleted_at IS NULL
		AND (linked_accounts IS NULL OR NOT (linked_accounts @> ?::jsonb))
	`, string(newEntry), string(newEntry), userID, string(conflict))
	if tx.Error != nil {
		return shared.MapDatabaseError(tx.Error)
	}
	// rows_affected == 0 means already linked (idempotent) or user not found.
	// Caller (handler) owns conflict-check logic, so treat as success.
	return nil
}

func (r *gormRepo) UnlinkAccount(ctx context.Context, userID, provider, providerID string) error {
	user, err := r.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	filtered := make([]domain.LinkedAccount, 0, len(user.LinkedAccounts))
	for _, a := range user.LinkedAccounts {
		if !(a.Provider == provider && a.ProviderID == providerID) {
			filtered = append(filtered, a)
		}
	}
	data, err := json.Marshal(filtered)
	if err != nil {
		return err
	}
	return shared.MapDatabaseError(r.db.WithContext(ctx).Exec(
		`UPDATE users SET linked_accounts = ?::jsonb, updated_at = NOW() WHERE id = ? AND deleted_at IS NULL`,
		string(data), userID,
	).Error)
}
