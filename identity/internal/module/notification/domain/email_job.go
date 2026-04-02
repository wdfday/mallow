package domain

// EmailJobType identifies the kind of email to send.
type EmailJobType string

const (
	EmailJobVerification  EmailJobType = "verification"
	EmailJobPasswordReset EmailJobType = "password_reset"
)

// EmailJob is the payload published to NATS and consumed by the email worker.
type EmailJob struct {
	Type  EmailJobType `json:"type"`
	To    string       `json:"to"`
	Name  string       `json:"name"`
	Token string       `json:"token"`
}
