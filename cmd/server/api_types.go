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

type updateTripRequest struct {
	Title *string `json:"title"`
	Notes *string `json:"notes"`
}

type registeredBatch struct {
	BatchID       string         `json:"batch_id"`
	AcceptedFiles []acceptedFile `json:"accepted_files"`
	RejectedFiles []rejectedFile `json:"rejected_files"`
}

type acceptedFile struct {
	ClientFileID string       `json:"client_file_id"`
	PhotoID      string       `json:"photo_id"`
	Upload       uploadTarget `json:"upload"`
}

type uploadTarget struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type rejectedFile struct {
	ClientFileID string      `json:"client_file_id"`
	Error        errorDetail `json:"error"`
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
