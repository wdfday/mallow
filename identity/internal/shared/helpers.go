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

func RespondWithPagination[T any](c *gin.Context, statusCode int, data []T, pagination Page[T]) {
	pkgshared.RespondWithPagination(c, statusCode, data, pagination)
}

func RespondWithPaginationTimeCursor[T any](c *gin.Context, statusCode int, data *T, pagination PaginationTimeCursor[T]) {
	pkgshared.RespondWithPaginationTimeCursor(c, statusCode, data, pagination)
}

func NewPagination[T any](totalItems int64, currentPage, itemsPerPage int) Page[T] {
	return pkgshared.NewPagination[T](totalItems, currentPage, itemsPerPage)
}

func NewPaginationTimeCursor[T any](timeCursor string, hasMore bool, itemsPerPage int) PaginationTimeCursor[T] {
	return pkgshared.NewPaginationTimeCursor[T](timeCursor, hasMore, itemsPerPage)
}

func HandleError(c *gin.Context, err error) {
	pkgshared.HandleError(c, err)
}

func ParseID(c *gin.Context, paramName string) (int64, *AppError) {
	return pkgshared.ParseID(c, paramName)
}
