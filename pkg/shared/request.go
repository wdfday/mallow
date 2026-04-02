package shared

// PageRequest is a common query struct for paginated list endpoints.
type PageRequest struct {
	Page     int `json:"page"     form:"page"`
	PageSize int `json:"pageSize" form:"pageSize"`
}

// Pagination holds resolved pagination parameters.
type Pagination struct {
	Page    int
	PerPage int
	Sort    string // e.g. "created_at desc"
}
