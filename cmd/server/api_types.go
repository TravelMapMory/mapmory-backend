package main

import "time"

type trip struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Notes      string     `json:"notes"`
	CreatedAt  time.Time  `json:"created_at"`
	PhotoCount int        `json:"photo_count"`
	Cover      *tripCover `json:"cover"`
}

type tripCover struct {
	PhotoID string       `json:"photo_id"`
	Image   displayImage `json:"image"`
}

type displayImage struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type tripPage struct {
	Items      []trip  `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

type createTripRequest struct {
	Title string `json:"title"`
	Notes string `json:"notes"`
}

type healthResponse struct {
	Status string `json:"status"`
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
