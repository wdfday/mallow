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

// Page is a paginated response envelope.
type Page[T any] struct {
	TotalItems   int64 `json:"totalItems"`
	TotalPages   int   `json:"totalPages"`
	CurrentPage  int   `json:"currentPage"`
	ItemsPerPage int   `json:"itemsPerPage"`
	Data         []T   `json:"data"`
}

// PageCursor is a time-based cursor pagination envelope.
type PageCursor[T any] struct {
	TimeCursor   string `json:"timeCursor"`
	HasMore      bool   `json:"hasMore"`
	ItemsPerPage int    `json:"itemsPerPage"`
	Data         *T     `json:"data"`
}

// PaginationTimeCursor is kept for backward compatibility.
type PaginationTimeCursor[T any] = PageCursor[T]

// NewPage builds a Page metadata object (without data).
func NewPage[T any](totalItems int64, currentPage, itemsPerPage int) Page[T] {
	if itemsPerPage <= 0 {
		itemsPerPage = DefaultPageSize
	}
	totalPages := int(totalItems) / itemsPerPage
	if int(totalItems)%itemsPerPage > 0 {
		totalPages++
	}
	return Page[T]{
		TotalItems:   totalItems,
		TotalPages:   totalPages,
		CurrentPage:  currentPage,
		ItemsPerPage: itemsPerPage,
	}
}

// NewPagination is kept for backward compatibility.
func NewPagination[T any](totalItems int64, currentPage, itemsPerPage int) Page[T] {
	if itemsPerPage <= 0 {
		itemsPerPage = 10
	}

	totalPages := int(totalItems) / itemsPerPage
	if int(totalItems)%itemsPerPage > 0 {
		totalPages++
	}
	if totalPages == 0 && totalItems > 0 {
		totalPages = 1
	}

	return Page[T]{
		TotalItems:   totalItems,
		TotalPages:   totalPages,
		CurrentPage:  currentPage,
		ItemsPerPage: itemsPerPage,
	}
}

// NewPaginationTimeCursor is kept for backward compatibility.
func NewPaginationTimeCursor[T any](timeCursor string, hasMore bool, itemsPerPage int) PaginationTimeCursor[T] {
	if itemsPerPage <= 0 {
		itemsPerPage = 10
	}
	return PaginationTimeCursor[T]{
		TimeCursor:   timeCursor,
		HasMore:      hasMore,
		ItemsPerPage: itemsPerPage,
	}
}
