package runtime

import (
	"encoding/json"
	"time"
)

func timePtr(t time.Time) *time.Time { return &t }

func unmarshalJSON(b []byte, v any) error {
	return json.Unmarshal(b, v)
}
