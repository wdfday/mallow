package dto

import (
	"encoding/json"
	"strings"
	"testing"

	"mallow/identity/internal/module/profile/domain"
)

// FuzzUpdateProfileRequestJSON tests JSON parsing for UpdateProfileRequest
// Run: go test -fuzz=FuzzUpdateProfileRequestJSON -fuzztime=30s
func FuzzUpdateProfileRequestJSON(f *testing.F) {
	// Seed with various JSON inputs
	f.Add(`{"occupation":"Software Engineer"}`)
	f.Add(`{"monthly_income_avg":50000000}`)
	f.Add(`{"risk_tolerance":"aggressive"}`)
	f.Add(`{"onboarding_completed":true}`)
	f.Add(`{}`)
	f.Add(`{"occupation":null}`)
	f.Add(`{"monthly_income_avg":-1000}`)                        // Negative
	f.Add(`{"monthly_income_avg":999999999999}`)                 // Very large
	f.Add(`{"occupation":"` + strings.Repeat("x", 10000) + `"}`) // Very long

	f.Fuzz(func(t *testing.T, jsonData string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("JSON unmarshal panicked: data=%q, panic=%v", jsonData, r)
			}
		}()

		var req UpdateProfileRequest
		err := json.Unmarshal([]byte(jsonData), &req)

		if err != nil {
			// Invalid JSON is acceptable
			return
		}

		// Test marshaling back
		data, err := json.Marshal(req)
		if err != nil {
			t.Errorf("Failed to marshal back: %v", err)
		}

		// Should be valid JSON
		if !json.Valid(data) {
			t.Errorf("Marshaled data is not valid JSON: %s", data)
		}
	})
}

// FuzzRiskToleranceValues tests risk tolerance enum validation
// Run: go test -fuzz=FuzzRiskToleranceValues -fuzztime=30s
func FuzzRiskToleranceValues(f *testing.F) {
	// Seed with valid and invalid values
	f.Add("conservative")
	f.Add("moderate")
	f.Add("aggressive")
	f.Add("invalid")
	f.Add("")
	f.Add("CONSERVATIVE") // Case sensitivity
	f.Add("very_aggressive")
	f.Add(strings.Repeat("a", 1000))

	f.Fuzz(func(t *testing.T, riskValue string) {
		// Skip extremely long inputs
		if len(riskValue) > 500 {
			t.Skip()
		}

		// Create request with risk tolerance
		jsonData := `{"risk_tolerance":"` + riskValue + `"}`

		var req UpdateProfileRequest
		err := json.Unmarshal([]byte(jsonData), &req)

		if err != nil {
			return
		}

		// If parsed successfully, check if it's a valid enum value
		if req.RiskTolerance != nil {
			validValues := []domain.RiskTolerance{
				domain.RiskToleranceConservative,
				domain.RiskToleranceModerate,
				domain.RiskToleranceAggressive,
			}

			isValid := false
			for _, valid := range validValues {
				if *req.RiskTolerance == string(valid) {
					isValid = true
					break
				}
			}

			if !isValid && riskValue != "" {
				t.Logf("Invalid risk tolerance value accepted: %q", riskValue)
			}
		}
	})
}

// FuzzInvestmentHorizonValues tests investment horizon enum
// Run: go test -fuzz=FuzzInvestmentHorizonValues -fuzztime=30s
func FuzzInvestmentHorizonValues(f *testing.F) {
	// Seed corpus
	f.Add("short")
	f.Add("medium")
	f.Add("long")
	f.Add("invalid")
	f.Add("")

	f.Fuzz(func(t *testing.T, horizonValue string) {
		if len(horizonValue) > 500 {
			t.Skip()
		}

		jsonData := `{"investment_horizon":"` + horizonValue + `"}`

		var req UpdateProfileRequest
		_ = json.Unmarshal([]byte(jsonData), &req)
	})
}

// FuzzNumericFields tests numeric field validation
// Run: go test -fuzz=FuzzNumericFields -fuzztime=30s
func FuzzNumericFields(f *testing.F) {
	// Seed with various numeric values
	f.Add(float64(0))
	f.Add(float64(-1))
	f.Add(float64(1000000))
	f.Add(float64(999999999999))
	f.Add(float64(0.01))
	f.Add(float64(-999999999))

	f.Fuzz(func(t *testing.T, amount float64) {
		// Test monthly income
		req := UpdateProfileRequest{
			MonthlyIncomeAvg: &amount,
		}

		// Marshal
		data, err := json.Marshal(req)
		if err != nil {
			t.Errorf("Marshal failed for amount %f: %v", amount, err)
		}

		// Unmarshal back
		var req2 UpdateProfileRequest
		err = json.Unmarshal(data, &req2)
		if err != nil {
			t.Errorf("Unmarshal failed: %v", err)
		}

		// Check preservation (accounting for float precision)
		if req2.MonthlyIncomeAvg != nil {
			diff := *req2.MonthlyIncomeAvg - amount
			if diff > 0.01 || diff < -0.01 {
				t.Errorf("Amount not preserved: original=%f, result=%f", amount, *req2.MonthlyIncomeAvg)
			}
		}
	})
}

// FuzzIntegerFields tests integer field validation
// Run: go test -fuzz=FuzzIntegerFields -fuzztime=30s
func FuzzIntegerFields(f *testing.F) {
	// Seed with various integer values
	f.Add(0)
	f.Add(1)
	f.Add(-1)
	f.Add(100)
	f.Add(999)
	f.Add(-999)
	f.Add(2147483647)  // Max int32
	f.Add(-2147483648) // Min int32

	f.Fuzz(func(t *testing.T, value int) {
		// Test credit score
		req := UpdateProfileRequest{
			CreditScore: &value,
		}

		// Marshal and unmarshal
		data, err := json.Marshal(req)
		if err != nil {
			return
		}

		var req2 UpdateProfileRequest
		err = json.Unmarshal(data, &req2)
		if err != nil {
			t.Errorf("Unmarshal failed: %v", err)
		}

		// Verify preservation
		if req2.CreditScore != nil && *req2.CreditScore != value {
			t.Errorf("Value not preserved: original=%d, result=%d", value, *req2.CreditScore)
		}
	})
}

// FuzzStringFields tests string field handling
// Run: go test -fuzz=FuzzStringFields -fuzztime=30s
func FuzzStringFields(f *testing.F) {
	// Seed with various strings
	f.Add("Software Engineer")
	f.Add("")
	f.Add(" ")
	f.Add("\n\t\r")
	f.Add(strings.Repeat("x", 1000))
	f.Add("Test\x00WithNull")
	f.Add("Test\"Quote")
	f.Add("Test'Quote")
	f.Add("<script>alert('xss')</script>")
	f.Add("密码工程师") // Unicode

	f.Fuzz(func(t *testing.T, occupation string) {
		if len(occupation) > 10000 {
			t.Skip()
		}

		req := UpdateProfileRequest{
			Occupation: &occupation,
		}

		// Marshal
		data, err := json.Marshal(req)
		if err != nil {
			return
		}

		// Must be valid JSON
		if !json.Valid(data) {
			t.Errorf("Invalid JSON for occupation: %q", occupation)
		}

		// Unmarshal back
		var req2 UpdateProfileRequest
		err = json.Unmarshal(data, &req2)
		if err != nil {
			t.Errorf("Unmarshal failed: %v", err)
		}

		// Verify exact preservation
		if req2.Occupation != nil && *req2.Occupation != occupation {
			t.Errorf("Occupation not preserved: original=%q, result=%q", occupation, *req2.Occupation)
		}
	})
}

// FuzzBooleanFields tests boolean field handling
// Run: go test -fuzz=FuzzBooleanFields -fuzztime=30s
func FuzzBooleanFields(f *testing.F) {
	// Seed with various boolean representations in JSON
	f.Add(`{"onboarding_completed":true}`)
	f.Add(`{"onboarding_completed":false}`)
	f.Add(`{"onboarding_completed":null}`)
	f.Add(`{"onboarding_completed":1}`)
	f.Add(`{"onboarding_completed":0}`)
	f.Add(`{"onboarding_completed":"true"}`)
	f.Add(`{"onboarding_completed":"false"}`)

	f.Fuzz(func(t *testing.T, jsonData string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Unmarshal panicked: %v", r)
			}
		}()

		var req UpdateProfileRequest
		_ = json.Unmarshal([]byte(jsonData), &req)
	})
}

// FuzzCompleteProfile tests complete profile with all fields
// Run: go test -fuzz=FuzzCompleteProfile -fuzztime=30s
func FuzzCompleteProfile(f *testing.F) {
	// Seed with complete valid profile
	f.Add(`{
		"occupation":"Software Engineer",
		"industry":"Technology",
		"monthly_income_avg":50000000,
		"credit_score":750,
		"risk_tolerance":"moderate",
		"investment_horizon":"medium",
		"onboarding_completed":true
	}`)

	f.Fuzz(func(t *testing.T, jsonData string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Parsing complete profile panicked: %v", r)
			}
		}()

		var req UpdateProfileRequest
		err := json.Unmarshal([]byte(jsonData), &req)

		if err == nil {
			// If successful, should be able to marshal back
			data, err := json.Marshal(req)
			if err != nil {
				t.Errorf("Failed to marshal complete profile: %v", err)
			}

			if !json.Valid(data) {
				t.Error("Marshaled complete profile is not valid JSON")
			}
		}
	})
}
