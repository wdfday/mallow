package shared

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { gin.SetMode(gin.TestMode) }

// ---------------------------------------------------------------------------
// AppError tests
// ---------------------------------------------------------------------------

func TestNewAppError(t *testing.T) {
	err := NewAppError("TEST_CODE", "test message", http.StatusBadRequest)
	assert.Equal(t, "TEST_CODE", err.Code)
	assert.Equal(t, "test message", err.Message)
	assert.Equal(t, http.StatusBadRequest, err.StatusCode)
	assert.NotNil(t, err.Details)
}

func TestAppError_Error_WithWrapped(t *testing.T) {
	inner := errors.New("inner error")
	err := NewAppError("CODE", "message", 500).WithError(inner)
	assert.Equal(t, "inner error", err.Error())
}

func TestAppError_Error_WithoutWrapped(t *testing.T) {
	err := NewAppError("CODE", "message", 500)
	assert.Equal(t, "message", err.Error())
}

func TestAppError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	err := NewAppError("CODE", "msg", 500).WithError(inner)
	assert.Equal(t, inner, errors.Unwrap(err))
}

func TestAppError_WithDetails(t *testing.T) {
	err := NewAppError("CODE", "msg", 400)
	errWithDetails := err.WithDetails("field", "email").WithDetails("reason", "duplicate")
	assert.Empty(t, err.Details)
	assert.Equal(t, "email", errWithDetails.Details["field"])
	assert.Equal(t, "duplicate", errWithDetails.Details["reason"])
}

func TestAppError_ToResponse(t *testing.T) {
	err := NewAppError("NOT_FOUND", "User not found", http.StatusNotFound)
	err.WithDetails("id", "123")
	resp := err.ToResponse()
	assert.Equal(t, http.StatusNotFound, resp.Status)
	assert.Equal(t, "NOT_FOUND", resp.Code)
	assert.Equal(t, "User not found", resp.Message)
	assert.Empty(t, resp.Details)
}

func TestAppError_ToResponse_UsesDetailsMessageOverride(t *testing.T) {
	err := NewAppError("UNAUTHORIZED", "Unauthorized", http.StatusUnauthorized).
		WithDetails("message", "invalid credentials").
		WithDetails("provider", "password")
	resp := err.ToResponse()
	assert.Equal(t, http.StatusUnauthorized, resp.Status)
	assert.Equal(t, "UNAUTHORIZED", resp.Code)
	assert.Equal(t, "invalid credentials", resp.Message)
	assert.Equal(t, "password", resp.Details["provider"])
	_, exists := resp.Details["message"]
	assert.False(t, exists)
}

func TestAppError_ToResponse_NilDetails(t *testing.T) {
	err := &AppError{Code: "CODE", Message: "msg", StatusCode: 500, Details: nil}
	resp := err.ToResponse()
	assert.NotNil(t, resp.Details)
}

func TestIsAppError_True(t *testing.T) {
	err := NewAppError("X", "x", 400)
	assert.True(t, IsAppError(err))
}

func TestIsAppError_False(t *testing.T) {
	assert.False(t, IsAppError(errors.New("plain error")))
}

func TestToAppError_DirectCast(t *testing.T) {
	original := NewAppError("CODE", "msg", 400)
	result := ToAppError(original)
	assert.Equal(t, original, result)
}

func TestToAppError_FallbackToInternal(t *testing.T) {
	plain := errors.New("something broke")
	result := ToAppError(plain)
	assert.Equal(t, http.StatusInternalServerError, result.StatusCode)
	assert.Equal(t, plain, result.Err)
}

func TestPredefinedErrors(t *testing.T) {
	cases := []struct {
		err  *AppError
		code int
	}{
		{ErrValidation, http.StatusBadRequest},
		{ErrNotFound, http.StatusNotFound},
		{ErrUnauthorized, http.StatusUnauthorized},
		{ErrForbidden, http.StatusForbidden},
		{ErrConflict, http.StatusConflict},
		{ErrInternal, http.StatusInternalServerError},
		{ErrBadRequest, http.StatusBadRequest},
		{ErrUserNotFound, http.StatusNotFound},
		{ErrUserExists, http.StatusConflict},
		{ErrTokenNotFound, http.StatusNotFound},
		{ErrTokenExpired, http.StatusUnauthorized},
		{ErrTokenUsed, http.StatusUnauthorized},
		{ErrTokenInvalid, http.StatusUnauthorized},
		{ErrProfileNotFound, http.StatusNotFound},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.code, tc.err.StatusCode, tc.err.Code)
	}
}

// ---------------------------------------------------------------------------
// NewPagination tests
// ---------------------------------------------------------------------------

func TestNewPagination_Basic(t *testing.T) {
	p := NewPagination[int](25, 1, 10)
	assert.Equal(t, int64(25), p.TotalItems)
	assert.Equal(t, 3, p.TotalPages)
	assert.Equal(t, 1, p.CurrentPage)
	assert.Equal(t, 10, p.ItemsPerPage)
}

func TestNewPagination_ExactDivision(t *testing.T) {
	p := NewPagination[string](20, 2, 10)
	assert.Equal(t, 2, p.TotalPages)
}

func TestNewPagination_ZeroItems(t *testing.T) {
	p := NewPagination[string](0, 1, 10)
	assert.Equal(t, 0, p.TotalPages)
}

func TestNewPagination_InvalidPerPage(t *testing.T) {
	p := NewPagination[string](5, 1, 0)
	assert.Equal(t, 10, p.ItemsPerPage) // falls back to 10
}

func TestNewPagination_SingleItem(t *testing.T) {
	p := NewPagination[string](1, 1, 10)
	assert.Equal(t, 1, p.TotalPages)
}

func TestNewPaginationTimeCursor(t *testing.T) {
	p := NewPaginationTimeCursor[string]("2024-01-01T00:00:00Z", true, 20)
	assert.Equal(t, "2024-01-01T00:00:00Z", p.TimeCursor)
	assert.True(t, p.HasMore)
	assert.Equal(t, 20, p.ItemsPerPage)
}

func TestNewPaginationTimeCursor_InvalidPerPage(t *testing.T) {
	p := NewPaginationTimeCursor[string]("", false, -1)
	assert.Equal(t, 10, p.ItemsPerPage)
}

// ---------------------------------------------------------------------------
// Gin helper responders
// ---------------------------------------------------------------------------

func newTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
	return c, w
}

func TestRespondWithError(t *testing.T) {
	c, w := newTestContext()
	RespondWithError(c, http.StatusBadRequest, "bad request")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "bad request")
}

func TestRespondWithAppError(t *testing.T) {
	c, w := newTestContext()
	appErr := NewAppError("NOT_FOUND", "not found", http.StatusNotFound)
	appErr.WithDetails("id", "42")
	RespondWithAppError(c, appErr)
	assert.Equal(t, http.StatusNotFound, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "NOT_FOUND")
	assert.Contains(t, body, "not found")
	assert.Contains(t, body, "\"status\":404")
}

func TestRespondWithSuccess(t *testing.T) {
	c, w := newTestContext()
	RespondWithSuccess(c, http.StatusOK, "ok", map[string]string{"key": "value"})
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "value")
}

func TestRespondWithSuccess_EmptyMessage(t *testing.T) {
	c, w := newTestContext()
	RespondWithSuccess(c, http.StatusCreated, "", "data")
	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Body.String(), "Created")
}

func TestRespondWithSuccessNoData(t *testing.T) {
	c, w := newTestContext()
	RespondWithSuccessNoData(c, http.StatusOK, "done")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "done")
}

func TestRespondWithNoContent(t *testing.T) {
	router := gin.New()
	router.GET("/nc", func(c *gin.Context) {
		RespondWithNoContent(c)
	})
	req := httptest.NewRequest(http.MethodGet, "/nc", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestRespondWithPagination(t *testing.T) {
	c, w := newTestContext()
	data := []string{"a", "b"}
	page := NewPagination[string](2, 1, 10)
	RespondWithPagination(c, http.StatusOK, data, page)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"a"`)
}

func TestHandleError_AppError(t *testing.T) {
	c, w := newTestContext()
	HandleError(c, ErrUnauthorized)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandleError_PlainError(t *testing.T) {
	c, w := newTestContext()
	HandleError(c, errors.New("something went wrong"))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestParseID_Valid(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/test/42", nil)
	c.Params = gin.Params{{Key: "id", Value: "42"}}

	id, appErr := ParseID(c, "id")
	require.Nil(t, appErr)
	assert.Equal(t, int64(42), id)
}

func TestParseID_Missing(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/test/", nil)

	_, appErr := ParseID(c, "id")
	require.NotNil(t, appErr)
	assert.Equal(t, http.StatusBadRequest, appErr.StatusCode)
}

func TestParseID_NotANumber(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/test/abc", nil)
	c.Params = gin.Params{{Key: "id", Value: "abc"}}

	_, appErr := ParseID(c, "id")
	require.NotNil(t, appErr)
	assert.Equal(t, http.StatusBadRequest, appErr.StatusCode)
}

func TestParseID_NonPositive(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/test/0", nil)
	c.Params = gin.Params{{Key: "id", Value: "0"}}

	_, appErr := ParseID(c, "id")
	require.NotNil(t, appErr)
	assert.Equal(t, http.StatusBadRequest, appErr.StatusCode)
}
