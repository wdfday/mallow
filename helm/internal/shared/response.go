package shared

// SuccessResponse wraps a successful response with data.
type SuccessResponse[T any] struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	Data    T      `json:"data,omitempty"`
}

// Success is a successful response without data.
type Success struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

type Page[T any] struct {
	TotalItems   int64 `json:"totalItems"`
	TotalPages   int   `json:"totalPages"`
	CurrentPage  int   `json:"currentPage"`
	ItemsPerPage int   `json:"itemsPerPage"`
	Data         []T   `json:"data"`
}

type PaginationTimeCursor[T any] struct {
	TimeCursor   string `json:"timeCursor"`
	HasMore      bool   `json:"hasMore"`
	ItemsPerPage int    `json:"itemsPerPage"`
	Data         *T     `json:"data"`
}
