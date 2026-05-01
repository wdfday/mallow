package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	authDomain "mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Helper to set admin user in context
func setAdminUser(c *gin.Context, userID uuid.UUID) {
	authUser := authDomain.AuthUser{
		ID:   userID,
		Role: domain.UserRoleAdmin,
	}
	c.Set("current_user", authUser)
}

func TestAdminHandler_List(t *testing.T) {
	t.Run("successfully list users", func(t *testing.T) {
		router, mockService, _ := setupTestRouter()
		handler := NewAdminHandler(mockService)
		adminID := uuid.New()

		users := []domain.User{
			*createTestUser(),
			*createTestUser(),
		}
		page := shared.Page[domain.User]{
			Data:         users,
			TotalItems:   2,
			CurrentPage:  1,
			ItemsPerPage: 20,
			TotalPages:   1,
		}

		mockService.On("List", mock.Anything, mock.AnythingOfType("domain.ListUsersFilter"), mock.AnythingOfType("shared.Pagination")).Return(page, nil)

		router.GET("/api/v1/user/manage", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.list(c)
		})

		req := httptest.NewRequest(http.MethodGet, "/api/v1/user/manage", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)

		assert.Equal(t, float64(http.StatusOK), response["status"])
		mockService.AssertExpectations(t)
	})

	t.Run("list users with role filter", func(t *testing.T) {
		router, mockService, _ := setupTestRouter()
		handler := NewAdminHandler(mockService)
		adminID := uuid.New()

		page := shared.Page[domain.User]{
			Data:         []domain.User{},
			TotalItems:   0,
			CurrentPage:  1,
			ItemsPerPage: 20,
			TotalPages:   0,
		}

		mockService.On("List", mock.Anything, mock.MatchedBy(func(filter domain.ListUsersFilter) bool {
			return filter.Role != nil && *filter.Role == domain.UserRoleAdmin
		}), mock.AnythingOfType("shared.Pagination")).Return(page, nil)

		router.GET("/api/v1/user/manage", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.list(c)
		})

		req := httptest.NewRequest(http.MethodGet, "/api/v1/user/manage?role=admin", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("list users with status filter", func(t *testing.T) {
		router, mockService, _ := setupTestRouter()
		handler := NewAdminHandler(mockService)
		adminID := uuid.New()

		page := shared.Page[domain.User]{
			Data:         []domain.User{},
			TotalItems:   0,
			CurrentPage:  1,
			ItemsPerPage: 20,
			TotalPages:   0,
		}

		mockService.On("List", mock.Anything, mock.MatchedBy(func(filter domain.ListUsersFilter) bool {
			return filter.Status != nil && *filter.Status == domain.UserStatusSuspended
		}), mock.AnythingOfType("shared.Pagination")).Return(page, nil)

		router.GET("/api/v1/user/manage", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.list(c)
		})

		req := httptest.NewRequest(http.MethodGet, "/api/v1/user/manage?status=suspended", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("return error for invalid role filter", func(t *testing.T) {
		router, _, _ := setupTestRouter()
		handler := NewAdminHandler(nil)
		adminID := uuid.New()

		router.GET("/api/v1/user/manage", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.list(c)
		})

		req := httptest.NewRequest(http.MethodGet, "/api/v1/user/manage?role=invalid", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("return error for invalid status filter", func(t *testing.T) {
		router, _, _ := setupTestRouter()
		handler := NewAdminHandler(nil)
		adminID := uuid.New()

		router.GET("/api/v1/user/manage", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.list(c)
		})

		req := httptest.NewRequest(http.MethodGet, "/api/v1/user/manage?status=invalid", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("handle service error", func(t *testing.T) {
		router, mockService, _ := setupTestRouter()
		handler := NewAdminHandler(mockService)
		adminID := uuid.New()

		mockService.On("List", mock.Anything, mock.Anything, mock.Anything).
			Return(shared.Page[domain.User]{}, errors.New("database error"))

		router.GET("/api/v1/user/manage", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.list(c)
		})

		req := httptest.NewRequest(http.MethodGet, "/api/v1/user/manage", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		mockService.AssertExpectations(t)
	})
}

func TestAdminHandler_Suspend(t *testing.T) {
	t.Run("successfully suspend user", func(t *testing.T) {
		router, mockService, _ := setupTestRouter()
		handler := NewAdminHandler(mockService)
		adminID := uuid.New()
		userID := uuid.New()

		mockService.On("UpdateColumns", mock.Anything, userID.String(), mock.MatchedBy(func(cols map[string]any) bool {
			return cols["status"] == string(domain.UserStatusSuspended)
		})).Return(nil)

		router.PATCH("/api/v1/user/manage/:id/suspend", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.suspend(c)
		})

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/user/manage/"+userID.String()+"/suspend", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("return error for invalid user ID", func(t *testing.T) {
		router, _, _ := setupTestRouter()
		handler := NewAdminHandler(nil)
		adminID := uuid.New()

		router.PATCH("/api/v1/user/manage/:id/suspend", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.suspend(c)
		})

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/user/manage/invalid-uuid/suspend", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("handle service error", func(t *testing.T) {
		router, mockService, _ := setupTestRouter()
		handler := NewAdminHandler(mockService)
		adminID := uuid.New()
		userID := uuid.New()

		mockService.On("UpdateColumns", mock.Anything, userID.String(), mock.Anything).
			Return(errors.New("database error"))

		router.PATCH("/api/v1/user/manage/:id/suspend", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.suspend(c)
		})

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/user/manage/"+userID.String()+"/suspend", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		mockService.AssertExpectations(t)
	})
}

func TestAdminHandler_Reinstate(t *testing.T) {
	t.Run("successfully reinstate user", func(t *testing.T) {
		router, mockService, _ := setupTestRouter()
		handler := NewAdminHandler(mockService)
		adminID := uuid.New()
		userID := uuid.New()

		mockService.On("UpdateColumns", mock.Anything, userID.String(), mock.MatchedBy(func(cols map[string]any) bool {
			return cols["status"] == string(domain.UserStatusActive) && cols["locked_until"] == nil
		})).Return(nil)

		router.PATCH("/api/v1/user/manage/:id/reinstate", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.reinstate(c)
		})

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/user/manage/"+userID.String()+"/reinstate", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("return error for invalid user ID", func(t *testing.T) {
		router, _, _ := setupTestRouter()
		handler := NewAdminHandler(nil)
		adminID := uuid.New()

		router.PATCH("/api/v1/user/manage/:id/reinstate", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.reinstate(c)
		})

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/user/manage/invalid-uuid/reinstate", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestAdminHandler_ChangeRole(t *testing.T) {
	t.Run("successfully change user role to admin", func(t *testing.T) {
		router, mockService, _ := setupTestRouter()
		handler := NewAdminHandler(mockService)
		adminID := uuid.New()
		userID := uuid.New()

		requestBody := map[string]string{"role": "admin"}
		body, _ := json.Marshal(requestBody)

		mockService.On("UpdateColumns", mock.Anything, userID.String(), mock.MatchedBy(func(cols map[string]any) bool {
			return cols["role"] == string(domain.UserRoleAdmin)
		})).Return(nil)

		router.PATCH("/api/v1/user/manage/:id/role", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.changeRole(c)
		})

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/user/manage/"+userID.String()+"/role", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("successfully change user role to user", func(t *testing.T) {
		router, mockService, _ := setupTestRouter()
		handler := NewAdminHandler(mockService)
		adminID := uuid.New()
		userID := uuid.New()

		requestBody := map[string]string{"role": "user"}
		body, _ := json.Marshal(requestBody)

		mockService.On("UpdateColumns", mock.Anything, userID.String(), mock.MatchedBy(func(cols map[string]any) bool {
			return cols["role"] == string(domain.UserRoleUser)
		})).Return(nil)

		router.PATCH("/api/v1/user/manage/:id/role", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.changeRole(c)
		})

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/user/manage/"+userID.String()+"/role", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("return error for invalid role", func(t *testing.T) {
		router, _, _ := setupTestRouter()
		handler := NewAdminHandler(nil)
		adminID := uuid.New()
		userID := uuid.New()

		requestBody := map[string]string{"role": "superadmin"}
		body, _ := json.Marshal(requestBody)

		router.PATCH("/api/v1/user/manage/:id/role", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.changeRole(c)
		})

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/user/manage/"+userID.String()+"/role", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("return error for invalid JSON", func(t *testing.T) {
		router, _, _ := setupTestRouter()
		handler := NewAdminHandler(nil)
		adminID := uuid.New()
		userID := uuid.New()

		router.PATCH("/api/v1/user/manage/:id/role", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.changeRole(c)
		})

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/user/manage/"+userID.String()+"/role", bytes.NewBufferString("invalid json"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("return error for invalid user ID", func(t *testing.T) {
		router, _, _ := setupTestRouter()
		handler := NewAdminHandler(nil)
		adminID := uuid.New()

		router.PATCH("/api/v1/user/manage/:id/role", func(c *gin.Context) {
			setAdminUser(c, adminID)
			handler.changeRole(c)
		})

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/user/manage/invalid-uuid/role", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// Mock for Delete function (not used but needed for interface completeness)
func (m *MockUserService) Delete(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}
