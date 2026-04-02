package dto

type RebuildRequest struct {
	AccountID string `json:"account_id" binding:"required" example:"550e8400-e29b-41d4-a716-446655440000"`
}

type RebuildResponse struct {
	AccountID      string `json:"account_id" example:"550e8400-e29b-41d4-a716-446655440000"`
	EventsReplayed int    `json:"events_replayed" example:"42"`
}
