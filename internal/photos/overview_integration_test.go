//go:build integration

package photos

import (
	"testing"

	"github.com/google/uuid"
)

// labelled marks a photo processed at lat, lng with labels and a capture time.
func (f fixture) labelled(t *testing.T, lat, lng float64, city, country, capture, labelStatus string) uuid.UUID {
	t.Helper()
	id := f.processedPhoto(t, StateReady, &lat, &lng)
	var captureArg any
	if capture != "" {
		captureArg = capture
	}
	if _, err := f.db.ExecContext(testContext(t), `
		UPDATE photos SET city = $2, country = $3, capture_time_local = $4::timestamp, label_status = $5
		WHERE id = $1
	`, id, city, country, captureArg, labelStatus); err != nil {
		t.Fatalf("label photo: %v", err)
	}
	return id
}

func TestTripStatsIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	f := newFixture(t, db)
	repo := NewPostgresRepository(db)
	ctx := testContext(t)

	empty := uuid.New()
	osaka := f.labelled(t, 34.69, 135.50, "Osaka", "Japan", "2025-05-14T10:00:00", LabelConfirmed)
	f.labelled(t, 35.01, 135.77, "Kyoto", "Japan", "2025-05-12T09:00:00", LabelSuggested)
	f.labelled(t, 35.02, 135.78, "Kyoto", "Japan", "2025-05-13T09:00:00", LabelSuggested)
	f.processedPhoto(t, StateNeedsLocation, nil, nil)
	f.addPhoto(t, "still-pending", nil)

	stats, err := repo.TripStats(ctx, f.owner, []uuid.UUID{f.tripID, empty})
	if err != nil {
		t.Fatal(err)
	}
	s := stats[f.tripID]
	if s.PhotoCount != 4 || s.LocatedCount != 3 {
		t.Fatalf("counts = %d photos, %d located; pending photos must not count", s.PhotoCount, s.LocatedCount)
	}
	if len(s.Cities) != 2 || s.Cities[0] != "Kyoto" || s.Cities[1] != "Osaka" || len(s.Countries) != 1 {
		t.Fatalf("cities = %v countries = %v; want first-photographed order", s.Cities, s.Countries)
	}
	if s.StartDate == nil || *s.StartDate != "2025-05-12" || *s.EndDate != "2025-05-14" {
		t.Fatalf("dates = %v..%v", s.StartDate, s.EndDate)
	}
	if s.Cover == nil || s.Cover.ID == osaka || s.Cover.CaptureTimeLocal == nil || *s.Cover.CaptureTimeLocal != "2025-05-12T09:00:00" {
		t.Fatalf("cover = %+v; want the first photo in capture order", s.Cover)
	}
	if e := stats[empty]; e.PhotoCount != 0 || e.Cover != nil || e.Cities == nil {
		t.Fatalf("empty trip = %+v", e)
	}

	// Another owner sees nothing of this trip.
	other, err := repo.TripStats(ctx, uuid.New(), []uuid.UUID{f.tripID})
	if err != nil || other[f.tripID].PhotoCount != 0 || other[f.tripID].Cover != nil {
		t.Fatalf("stranger stats = %+v, %v", other[f.tripID], err)
	}
}

func TestMapPhotosIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	f := newFixture(t, db)
	repo := NewPostgresRepository(db)
	ctx := testContext(t)

	kyoto := f.labelled(t, 35.01, 135.77, "Kyoto", "Japan", "2025-05-12T09:00:00", LabelConfirmed)
	fiji := f.labelled(t, -17.7, 179.5, "Suva", "Fiji", "2025-06-01T09:00:00", LabelSuggested)
	samoa := f.labelled(t, -13.8, -171.7, "Apia", "Samoa_%", "", LabelSuggested)
	f.processedPhoto(t, StateNeedsLocation, nil, nil)

	ids := func(q MapQuery) []uuid.UUID {
		t.Helper()
		if q.Limit == 0 {
			q.Limit = 10
		}
		photos, err := repo.MapPhotos(ctx, f.owner, q)
		if err != nil {
			t.Fatal(err)
		}
		result := make([]uuid.UUID, len(photos))
		for i, p := range photos {
			if p.TripTitle != "Photos" || p.Lat == nil {
				t.Fatalf("map photo = %+v", p)
			}
			result[i] = p.ID
		}
		return result
	}

	if got := ids(MapQuery{}); len(got) != 3 || got[0] != fiji || got[1] != kyoto || got[2] != samoa {
		t.Fatalf("all located, newest capture first, undated last: %v", got)
	}
	if got := ids(MapQuery{BBox: &[4]float64{130, 30, 140, 40}}); len(got) != 1 || got[0] != kyoto {
		t.Fatalf("bbox: %v", got)
	}
	// West > east crosses the antimeridian: Fiji and Samoa, not Kyoto.
	if got := ids(MapQuery{BBox: &[4]float64{170, -30, -160, 0}}); len(got) != 2 {
		t.Fatalf("antimeridian bbox: %v", got)
	}
	from, to := "2025-05-12", "2025-05-12"
	if got := ids(MapQuery{From: &from, To: &to}); len(got) != 1 || got[0] != kyoto {
		t.Fatalf("inclusive date range: %v", got)
	}
	if got := ids(MapQuery{Text: "kyo"}); len(got) != 1 || got[0] != kyoto {
		t.Fatalf("text: %v", got)
	}
	if got := ids(MapQuery{Text: "photos"}); len(got) != 3 {
		t.Fatalf("text matches the trip title: %v", got)
	}
	// LIKE wildcards in user text match literally.
	if got := ids(MapQuery{Text: "_%"}); len(got) != 1 || got[0] != samoa {
		t.Fatalf("escaped wildcards: %v", got)
	}
	otherTrip := uuid.New()
	if got := ids(MapQuery{TripID: &otherTrip}); len(got) != 0 {
		t.Fatalf("trip filter: %v", got)
	}
	if got := ids(MapQuery{Limit: 2}); len(got) != 2 {
		t.Fatalf("limit: %v", got)
	}
	if photos, err := repo.MapPhotos(ctx, uuid.New(), MapQuery{Limit: 10}); err != nil || len(photos) != 0 {
		t.Fatalf("stranger sees %d photos, %v", len(photos), err)
	}
}

func TestSummaryIntegration(t *testing.T) {
	db := openIntegrationDB(t)
	f := newFixture(t, db)
	repo := NewPostgresRepository(db)
	ctx := testContext(t)

	f.labelled(t, 35.01, 135.77, "Kyoto", "Japan", "2025-05-12T09:00:00", LabelConfirmed)
	f.labelled(t, 34.69, 135.50, "Osaka", "Japan", "2025-05-14T10:00:00", LabelSuggested)
	f.processedPhoto(t, StateNeedsLocation, nil, nil)
	f.addPhoto(t, "pending", nil)

	s, err := repo.Summary(ctx, f.owner, 2)
	if err != nil {
		t.Fatal(err)
	}
	if s.PhotoCount != 3 || s.CountryCount != 1 || s.CityCount != 2 || s.ToReview != 2 {
		t.Fatalf("summary = %+v; to review is one needs-location plus one suggestion", s)
	}
	if len(s.Recent) != 2 {
		t.Fatalf("recent = %d, want the limit", len(s.Recent))
	}
	if other, err := repo.Summary(ctx, uuid.New(), 8); err != nil || other.PhotoCount != 0 || len(other.Recent) != 0 {
		t.Fatalf("stranger summary = %+v, %v", other, err)
	}
}
