package service

import (
	"context"

	"mallow/identity/internal/module/profile/domain"
	profiledto "mallow/identity/internal/module/profile/dto"
	"mallow/identity/internal/module/profile/repository"
)

// ProfileCreator defines profile creation operations
type ProfileCreator interface {
	CreateProfile(ctx context.Context, userID string, req profiledto.CreateProfileRequest) (*domain.UserProfile, error)
	CreateDefaultProfile(ctx context.Context, userID string) (*domain.UserProfile, error)
}

// ProfileReader defines profile read operations
type ProfileReader interface {
	GetProfile(ctx context.Context, userID string) (*domain.UserProfile, error)
}

// ProfileUpdater defines profile update operations
type ProfileUpdater interface {
	UpdateProfile(ctx context.Context, userID string, req profiledto.UpdateProfileRequest) (*domain.UserProfile, error)
}

// Service is the composite interface for all profile operations
type Service interface {
	ProfileCreator
	ProfileReader
	ProfileUpdater
}

// profileService implements all profile use cases
type profileService struct {
	repo repository.Repository
}

// NewService creates a new profile service
func NewService(repo repository.Repository) Service {
	return &profileService{
		repo: repo,
	}
}
