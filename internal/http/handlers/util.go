package handlers

import (
	"encoding/json"
	"math"
	"net/http"
)

// decimalPlaces is the precision we keep for all money/quantity/cost values so the
// UI (which rounds inputs to 4dp) and server-computed totals agree to the same
// scale and float artefacts don't leak past the 4th decimal.
const decimalPlaces = 4

// roundDecimal rounds a float to 4 decimal places. Server-side totals (line
// totals, PO amounts, rebates) are products/sums of client values and can carry
// binary-float noise; rounding on write keeps stored values clean to 4dp.
func roundDecimal(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	factor := math.Pow(10, decimalPlaces)
	return math.Round(v*factor) / factor
}

// writeJSON writes the provided payload as a JSON response with the given status code.
// All handlers in this package should use this helper for consistency.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// respondJSON is an alias for writeJSON kept for health.go compatibility.
func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	writeJSON(w, status, data)
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// writeError writes a structured JSON error response.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Code: code, Message: message})
}
