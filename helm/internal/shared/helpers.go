package shared

import (
	pkgshared "mallow/pkg/shared"

	"github.com/gin-gonic/gin"
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
