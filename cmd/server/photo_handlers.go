package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TravelMapMory/mapmory-backend/internal/photoproc"
	"github.com/TravelMapMory/mapmory-backend/internal/photos"
	"github.com/TravelMapMory/mapmory-backend/internal/photostorage"
	"github.com/TravelMapMory/mapmory-backend/internal/uploads"
	"github.com/google/uuid"
)

// staleProcessingAfter lets a later /complete reclaim a photo whose processing
// was interrupted, for example by a restart. Processing one image takes seconds.
const staleProcessingAfter = 10 * time.Minute

const (
	defaultPhotoPageSize = 20
	maxPhotoPageSize     = 100
)

// photoResponse maps a stored photo to the owner representation. Only a
// processed photo has a display image; the original is never exposed.
func (h *uploadHandlers) photoResponse(photo photos.Photo) (apiPhoto, error) {
	response := apiPhoto{
		ID:               photo.ID.String(),
		TripID:           photo.TripID.String(),
		BatchID:          photo.BatchID.String(),
		ClientFileID:     photo.ClientFileID,
		State:            photo.State,
		ErrorCode:        photo.ErrorCode,
		UploadedAt:       photo.UploadedAt,
		CaptureTimeLocal: photo.CaptureTimeLocal,
		CaptureUTCOffset: photo.CaptureUTCOffset,
		ExifLat:          photo.ExifLat,
		ExifLng:          photo.ExifLng,
		Lat:              photo.Lat,
		Lng:              photo.Lng,
	}
	if photo.DisplayObjectName != nil {
		url, expires, err := h.store.SignDisplay(*photo.DisplayObjectName)
		if err != nil {
			return apiPhoto{}, err
		}
		response.DisplayImage = &displayImage{URL: url, ExpiresAt: expires}
	}
	return response, nil
}

func (h *uploadHandlers) writePhoto(w http.ResponseWriter, photo photos.Photo) {
	response, err := h.photoResponse(photo)
	if err != nil {
		uploadInternalError(w, "display signing", err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func isProcessed(state string) bool {
	return state == photos.StateReady || state == photos.StateNeedsLocation || state == photos.StateFailed
}

// completeUpload processes an uploaded file within the request (contract
// completeUpload). A processed photo is returned as stored, so repeating the
// call never reprocesses or duplicates it.
func (h *uploadHandlers) completeUpload(w http.ResponseWriter, r *http.Request) {
	owner, ok := tripOwner(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	batchID, err := uuid.Parse(r.PathValue("batch_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "photo not found")
		return
	}
	ctx := r.Context()
	photo, err := h.photos.GetByClientFile(ctx, owner, batchID, r.PathValue("client_file_id"))
	if errors.Is(err, photos.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "photo not found")
		return
	}
	if err != nil {
		uploadInternalError(w, "photo lookup", err)
		return
	}
	if isProcessed(photo.State) {
		h.writePhoto(w, photo)
		return
	}

	info, err := h.store.Stat(ctx, photo.ObjectName)
	if errors.Is(err, photostorage.ErrObjectNotFound) {
		writeError(w, http.StatusConflict, "upload_missing", "the file has not been uploaded yet")
		return
	}
	if err != nil {
		uploadInternalError(w, "object lookup", err)
		return
	}

	claimed, err := h.photos.Claim(ctx, photo.ID, h.now().Add(-staleProcessingAfter))
	if err != nil {
		uploadInternalError(w, "photo claim", err)
		return
	}
	if !claimed {
		// Another request is processing it, or has just finished.
		current, err := h.photos.GetByClientFile(ctx, owner, batchID, photo.ClientFileID)
		if err == nil && isProcessed(current.State) {
			h.writePhoto(w, current)
			return
		}
		writeError(w, http.StatusConflict, "processing_in_progress", "the photo is already being processed")
		return
	}
	h.process(w, r, photo, info)
}

// process runs after a successful claim. Every exit either records an
// outcome or releases the claim, so the photo never stays stuck in processing.
func (h *uploadHandlers) process(w http.ResponseWriter, r *http.Request, photo photos.Photo, info photostorage.ObjectInfo) {
	ctx := r.Context()
	// Persist outcomes even if the client disconnects mid-way.
	persist := context.WithoutCancel(ctx)
	release := func() {
		if err := h.photos.Release(persist, photo.ID); err != nil {
			log.Printf("release photo %s: %v", photo.ID, err)
		}
	}
	fail := func(code string) {
		failed, err := h.photos.Fail(persist, photo.ID, info.Created, code)
		if err != nil {
			uploadInternalError(w, "photo failure record", err)
			return
		}
		h.writePhoto(w, failed)
	}

	// The signed URL binds type and exact size, so these only catch tampering.
	if info.Size > uploads.MaxFileBytes {
		fail(photoproc.CodeFileTooLarge)
		return
	}
	if info.Size != photo.SizeBytes {
		log.Printf("photo %s: object has %d bytes, %d registered", photo.ID, info.Size, photo.SizeBytes)
		fail(photoproc.CodeInvalidImage)
		return
	}

	select {
	case h.processing <- struct{}{}:
		defer func() { <-h.processing }()
	case <-ctx.Done():
		release()
		return
	}

	data, err := h.store.Read(ctx, photo.ObjectName, uploads.MaxFileBytes)
	if errors.Is(err, photostorage.ErrObjectTooLarge) {
		fail(photoproc.CodeFileTooLarge)
		return
	}
	if err != nil {
		release()
		uploadInternalError(w, "object read", err)
		return
	}

	display, err := photoproc.DisplayJPEG(data)
	var imageErr *photoproc.Error
	if errors.As(err, &imageErr) {
		log.Printf("photo %s: %v", photo.ID, imageErr)
		fail(imageErr.Code)
		return
	}
	if err != nil {
		release()
		uploadInternalError(w, "display rendering", err)
		return
	}
	// Cannot fail once DisplayJPEG accepted the image.
	meta, _ := photoproc.ExtractMetadata(data)
	data = nil
	for _, note := range meta.Diagnostics {
		log.Printf("photo %s metadata: %s", photo.ID, note)
	}

	displayName := uploads.DisplayObjectName(photo.TripID, photo.ID)
	if err := h.store.Write(persist, displayName, "image/jpeg", display); err != nil {
		release()
		uploadInternalError(w, "display write", err)
		return
	}

	result := photos.Processed{UploadedAt: info.Created, DisplayObjectName: displayName}
	if meta.GPSStatus == photoproc.Usable && meta.GPS != nil {
		result.ExifLat, result.ExifLng = &meta.GPS.Lat, &meta.GPS.Lng
	}
	if meta.CaptureTimeStatus == photoproc.Usable {
		result.CaptureTimeLocal = &meta.CaptureTimeLocal
	}
	if meta.UTCOffsetStatus == photoproc.Usable {
		result.CaptureUTCOffset = &meta.CaptureUTCOffset
	}
	processed, err := h.photos.Finish(persist, photo.ID, result)
	if err != nil {
		release()
		uploadInternalError(w, "photo result record", err)
		return
	}
	h.writePhoto(w, processed)
}

// getBatch restores upload status after a reload (contract getBatch).
func (h *uploadHandlers) getBatch(w http.ResponseWriter, r *http.Request) {
	owner, ok := tripOwner(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	batchID, err := uuid.Parse(r.PathValue("batch_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "batch not found")
		return
	}
	tripID, records, err := h.photos.GetBatch(r.Context(), owner, batchID)
	if errors.Is(err, photos.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "batch not found")
		return
	}
	if err != nil {
		uploadInternalError(w, "batch lookup", err)
		return
	}
	response := batchResponse{ID: batchID.String(), TripID: tripID.String(), Photos: make([]apiPhoto, 0, len(records))}
	for _, record := range records {
		photo, err := h.photoResponse(record)
		if err != nil {
			uploadInternalError(w, "display signing", err)
			return
		}
		response.Photos = append(response.Photos, photo)
	}
	writeJSON(w, http.StatusOK, response)
}

// listTripPhotos returns one page of the caller's trip photos (contract
// listTripPhotos): newest upload first, never-uploaded photos last.
func (h *uploadHandlers) listTripPhotos(w http.ResponseWriter, r *http.Request) {
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
	query := r.URL.Query()
	limit := defaultPhotoPageSize
	if value := query.Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > maxPhotoPageSize {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be an integer from 1 to 100")
			return
		}
	}
	var after *photos.Cursor
	if value := strings.TrimSpace(query.Get("cursor")); value != "" {
		cursor, err := photos.DecodeCursor(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid cursor")
			return
		}
		after = &cursor
	}

	// One extra row tells whether another page exists.
	records, err := h.photos.ListByTrip(r.Context(), owner, tripID, after, limit+1)
	if errors.Is(err, photos.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "trip not found")
		return
	}
	if err != nil {
		uploadInternalError(w, "photo listing", err)
		return
	}
	page := photoPage{Items: make([]apiPhoto, 0, min(len(records), limit))}
	if len(records) > limit {
		records = records[:limit]
		next := photos.CursorAfter(records[limit-1]).Encode()
		page.NextCursor = &next
	}
	for _, record := range records {
		photo, err := h.photoResponse(record)
		if err != nil {
			uploadInternalError(w, "display signing", err)
			return
		}
		page.Items = append(page.Items, photo)
	}
	writeJSON(w, http.StatusOK, page)
}
