package dto

type HandActionResp struct {
	Status string `json:"status"`
	ID     string `json:"id"`
}

type ConfigureStrategyResp struct {
	Status   string `json:"status"`
	Strategy string `json:"strategy"`
}
