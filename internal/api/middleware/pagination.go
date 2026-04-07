package middleware

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Cursor is an opaque pagination token that encodes the position in a result set.
type Cursor struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

// EncodeCursor converts a cursor to a base64 string for use in API responses.
func EncodeCursor(id string, createdAt time.Time) string {
	c := Cursor{ID: id, CreatedAt: createdAt}
	b, _ := json.Marshal(c)
	return base64.URLEncoding.EncodeToString(b)
}

// DecodeCursor parses a cursor token from a request.
func DecodeCursor(token string) (Cursor, error) {
	if token == "" {
		return Cursor{}, fmt.Errorf("empty cursor")
	}
	b, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, fmt.Errorf("invalid cursor")
	}
	var c Cursor
	if err := json.Unmarshal(b, &c); err != nil {
		return Cursor{}, fmt.Errorf("invalid cursor")
	}
	return c, nil
}

// PageParams extracts standard pagination parameters from a request.
type PageParams struct {
	Limit  int
	Cursor string // Opaque cursor token (after=...)
	Offset int    // Fallback offset-based pagination
}

// ParsePageParams extracts pagination parameters from query string.
// Supports both cursor-based (?after=token) and offset-based (?offset=N).
func ParsePageParams(r *http.Request) PageParams {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	return PageParams{
		Limit:  limit,
		Cursor: r.URL.Query().Get("after"),
		Offset: offset,
	}
}

// PageResponse wraps a list response with pagination metadata.
// Matches Stripe's list response format.
type PageResponse struct {
	Data       any    `json:"data"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor,omitempty"`
	TotalCount int    `json:"total_count,omitempty"`
}

// NewPageResponse creates a paginated response.
// If len(items) > limit, has_more is true and the last item provides the cursor.
func NewPageResponse(data any, count, limit int, lastID string, lastCreatedAt time.Time) PageResponse {
	resp := PageResponse{
		Data:       data,
		TotalCount: count,
	}

	if count > limit {
		resp.HasMore = true
		resp.NextCursor = EncodeCursor(lastID, lastCreatedAt)
	}

	return resp
}
