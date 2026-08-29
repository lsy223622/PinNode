package main

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

const (
	apiVersion       = "v1"
	protocolVersion  = 1
	maxJSONBodyBytes = 16 << 10
)

var serverFeatures = []string{
	"idempotent-session-start-v1",
	"revisioned-session-config-v1",
	"session-sync-v1",
	"client-state-report-v1",
	"client-logs-v1",
	"structured-errors-v1",
}

type apiErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type apiErrorResponse struct {
	Error     apiErrorBody `json:"error"`
	RequestID string       `json:"requestId"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiErrorResponse{
		Error: apiErrorBody{
			Code:      code,
			Message:   message,
			Retryable: retryableAPIError(code),
		},
		RequestID: w.Header().Get("X-Request-ID"),
	})
}

func retryableAPIError(code string) bool {
	switch code {
	case "rate_limited",
		"pow_unavailable",
		"tailscale_rate_limited",
		"tailscale_unavailable",
		"device_not_ready",
		"session_cleanup_failed",
		"session_create_failed",
		"session_sync_failed":
		return true
	default:
		return false
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "content_type_invalid", "请求必须使用 application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "json_invalid", "JSON 请求无效")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "json_invalid", "JSON 请求只能包含一个值")
		return false
	}
	return true
}

func decodeJSONBytes(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON 只能包含一个值")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func formatOptionalTime(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}
