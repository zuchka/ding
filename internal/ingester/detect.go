package ingester

import "strings"

// DetectFormat determines which parser to use.
// serverFormat: "json", "prometheus", or "auto".
// contentType: the HTTP Content-Type header value (may be "").
// data: first bytes of the body (used for heuristic when auto and no content-type).
func DetectFormat(data []byte, contentType, serverFormat string) string {
	if serverFormat != "auto" && serverFormat != "" {
		return serverFormat
	}
	// auto: use content-type if provided
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "application/json") {
		return "json"
	}
	if strings.Contains(ct, "text/plain") {
		return "prometheus"
	}
	// heuristic: if first non-whitespace char is '{', it's JSON
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "{") {
		return "json"
	}
	return "prometheus"
}
