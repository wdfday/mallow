package shared

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	pkgmw "mallow/pkg/middleware"
	pkgshared "mallow/pkg/shared"
)

func RespondWithError(c *gin.Context, statusCode int, message string) {
	pkgshared.RespondWithError(c, statusCode, message)
}

func RespondWithAppError(c *gin.Context, err *AppError) {
	pkgshared.RespondWithAppError(c, err)
}

func RespondWithErrorDetails(c *gin.Context, statusCode int, message string, details map[string]interface{}) {
	pkgshared.RespondWithErrorDetails(c, statusCode, message, details)
}

func RespondWithSuccess[T any](c *gin.Context, statusCode int, message string, data T) {
	pkgshared.RespondWithSuccess(c, statusCode, message, data)
}

func RespondWithSuccessNoData(c *gin.Context, statusCode int, message string) {
	pkgshared.RespondWithSuccessNoData(c, statusCode, message)
}

func RespondWithNoContent(c *gin.Context) {
	pkgshared.RespondWithNoContent(c)
}

func HandleError(c *gin.Context, err error) {
	pkgshared.HandleError(c, err)
}

// CallerUserID extracts and parses the user ID injected by the gateway middleware.
// Responds 401 and returns false when the header is absent or not a valid UUID.
func CallerUserID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(pkgmw.UserID(c))
	if err != nil {
		RespondWithError(c, http.StatusUnauthorized, "invalid user")
		return uuid.Nil, false
	}
	return id, true
}
