package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"unicode/utf8"

	"github.com/TravelMapMory/mapmory-backend/internal/photostorage"
	"github.com/TravelMapMory/mapmory-backend/internal/trips"
	"github.com/TravelMapMory/mapmory-backend/internal/uploads"
	"github.com/google/uuid"
)

// maxRegisterBodyBytes comfortably fits 100 files with 100-character IDs.
const maxRegisterBodyBytes = 256 << 10

// uploadHandlers registers upload batches for trips owned by the caller.
type uploadHandlers struct {
	trips   trips.Repository
	batches uploads.Repository
	signer  photostorage.Signer
}

type uploadFileInput struct {
	clientFileID string
	sizeBytes    int64
	contentType  string
}

// decodeRegisterUpload validates the RegisterUpload schema. It returns a
// client-facing message when the request is malformed; unsupported types and
// oversized files are not malformed and are rejected per file instead.
func decodeRegisterUpload(body io.Reader) ([]uploadFileInput, string) {
	decoder := json.NewDecoder(body)
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return nil, "invalid request body"
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, "invalid request body"
	}
	for key := range raw {
		if key != "files" {
			return nil, "unknown field"
		}
	}
	rawFiles, ok := raw["files"]
	if !ok || string(rawFiles) == "null" {
		return nil, "files is required"
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(rawFiles, &entries); err != nil {
		return nil, "files must be an array of objects"
	}
	if len(entries) == 0 {
		return nil, "files must contain at least one file"
	}
	if len(entries) > uploads.MaxFilesPerBatch {
		return nil, "files must contain at most 100 files"
	}

	files := make([]uploadFileInput, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		file, message := decodeUploadFile(entry)
		if message != "" {
			return nil, message
		}
		if seen[file.clientFileID] {
			return nil, "client_file_id values must be unique within a batch"
		}
		seen[file.clientFileID] = true
		files = append(files, file)
	}
	return files, ""
}

func decodeUploadFile(entry map[string]json.RawMessage) (uploadFileInput, string) {
	if entry == nil {
		return uploadFileInput{}, "files must be an array of objects"
	}
	var file uploadFileInput
	for _, key := range []string{"client_file_id", "size_bytes", "content_type"} {
		if value, ok := entry[key]; !ok || string(value) == "null" {
			return uploadFileInput{}, key + " is required"
		}
	}
	for key, value := range entry {
		switch key {
		case "client_file_id":
			if err := json.Unmarshal(value, &file.clientFileID); err != nil {
				return uploadFileInput{}, "client_file_id must be a string"
			}
			if length := utf8.RuneCountInString(file.clientFileID); length < 1 || length > 100 {
				return uploadFileInput{}, "client_file_id must be 1 to 100 characters"
			}
		case "size_bytes":
			// A JSON string such as "12" must not pass as a number.
			var number json.Number
			if value[0] == '"' || json.Unmarshal(value, &number) != nil {
				return uploadFileInput{}, "size_bytes must be an integer"
			}
			size, err := number.Int64()
			if err != nil {
				return uploadFileInput{}, "size_bytes must be an integer"
			}
			if size < 1 {
				return uploadFileInput{}, "size_bytes must be at least 1"
			}
			file.sizeBytes = size
		case "content_type":
			if err := json.Unmarshal(value, &file.contentType); err != nil {
				return uploadFileInput{}, "content_type must be a string"
			}
		default:
			return uploadFileInput{}, "unknown field"
		}
	}
	return file, ""
}

func uploadInternalError(w http.ResponseWriter, operation string, err error) {
	log.Printf("%s failed: %v", operation, err)
	writeError(w, http.StatusInternalServerError, "internal_error", "an unexpected error occurred")
}

// registerBatch signs one upload URL per accepted file and records the files
// as pending photos. URLs are signed before anything is stored, so a signing
// failure leaves no photo records without an upload target.
func (h *uploadHandlers) registerBatch(w http.ResponseWriter, r *http.Request) {
	owner, ok := tripOwner(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	tripID, err := uuid.Parse(r.PathValue("trip_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
		return
	}
	files, message := decodeRegisterUpload(http.MaxBytesReader(w, r.Body, maxRegisterBodyBytes))
	if message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}

	// Check ownership before signing, so other users' trips cost no IAM calls.
	if _, err := h.trips.GetByID(r.Context(), owner, tripID); errors.Is(err, trips.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
		return
	} else if err != nil {
		uploadInternalError(w, "trip lookup", err)
		return
	}

	batchID := uuid.New()
	response := registeredBatch{
		BatchID:       batchID.String(),
		AcceptedFiles: make([]acceptedFile, 0, len(files)),
		RejectedFiles: make([]rejectedFile, 0),
	}
	records := make([]uploads.File, 0, len(files))
	for _, file := range files {
		contentType := uploads.NormalizeContentType(file.contentType)
		switch {
		case contentType == "":
			response.RejectedFiles = append(response.RejectedFiles, rejectedFile{
				ClientFileID: file.clientFileID,
				Error:        errorDetail{Code: "unsupported_type", Message: "only JPEG and PNG images are supported"},
			})
		case file.sizeBytes > uploads.MaxFileBytes:
			response.RejectedFiles = append(response.RejectedFiles, rejectedFile{
				ClientFileID: file.clientFileID,
				Error:        errorDetail{Code: "file_too_large", Message: "files must be at most 25,000,000 bytes"},
			})
		default:
			photoID := uuid.New()
			record := uploads.File{
				PhotoID:      photoID,
				ClientFileID: file.clientFileID,
				ContentType:  contentType,
				SizeBytes:    file.sizeBytes,
				ObjectName:   uploads.ObjectName(tripID, photoID),
			}
			upload, err := h.signer.SignUpload(record.ObjectName, record.ContentType, record.SizeBytes)
			if err != nil {
				uploadInternalError(w, "upload signing", err)
				return
			}
			records = append(records, record)
			response.AcceptedFiles = append(response.AcceptedFiles, acceptedFile{
				ClientFileID: file.clientFileID,
				PhotoID:      photoID.String(),
				Upload: uploadTarget{
					Method:    upload.Method,
					URL:       upload.URL,
					Headers:   upload.Headers,
					ExpiresAt: upload.ExpiresAt,
				},
			})
		}
	}

	err = h.batches.CreateBatch(r.Context(), owner, tripID, batchID, records)
	if errors.Is(err, uploads.ErrTripNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
		return
	}
	if err != nil {
		uploadInternalError(w, "batch storage", err)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}
