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

// apiPhoto is the owner representation of a photo (Photo in the contract).
// Unknown values are explicit nulls.
type apiPhoto struct {
	ID               string        `json:"id"`
	TripID           string        `json:"trip_id"`
	BatchID          string        `json:"batch_id"`
	ClientFileID     string        `json:"client_file_id"`
	State            string        `json:"state"`
	ErrorCode        *string       `json:"error_code"`
	UploadedAt       *time.Time    `json:"uploaded_at"`
	CaptureTimeLocal *string       `json:"capture_time_local"`
	CaptureUTCOffset *string       `json:"capture_utc_offset"`
	ExifLat          *float64      `json:"exif_lat"`
	ExifLng          *float64      `json:"exif_lng"`
	Lat              *float64      `json:"lat"`
	Lng              *float64      `json:"lng"`
	DisplayImage     *displayImage `json:"display_image"`
}

type photoPage struct {
	Items      []apiPhoto `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

type batchResponse struct {
	ID     string     `json:"id"`
	TripID string     `json:"trip_id"`
	Photos []apiPhoto `json:"photos"`
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
