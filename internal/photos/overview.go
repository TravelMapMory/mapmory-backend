package photos

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TripStats are the aggregates a trip card needs (design doc 3.3), computed
// from processed photos only: pending, uploading and failed photos are not
// part of the collection yet.
type TripStats struct {
	PhotoCount   int
	LocatedCount int
	// Cities and Countries are distinct labels of located photos, in the order
	// they were first photographed.
	Cities    []string
	Countries []string
	// StartDate and EndDate are the first and last capture dates ("YYYY-MM-DD"),
	// nil when no photo has a capture time.
	StartDate *string
	EndDate   *string
	// Cover is the first processed photo in capture order, nil for an empty trip.
	Cover *Photo
	// Public reports a live public share of the trip (design doc 4.2: trips are
	// private until the owner shares them publicly).
	Public bool
	// UnderReview reports a live public share of the trip with an unresolved
	// report, which hides that share from the public map.
	UnderReview bool
}

// MapQuery selects the caller's located photos for the owner map (GET
// /api/map/photos). Zero values mean no filter.
type MapQuery struct {
	// BBox is west, south, east, north. West > east crosses the antimeridian.
	BBox   *[4]float64
	TripID *uuid.UUID
	// From and To are inclusive capture dates, "YYYY-MM-DD".
	From, To *string
	// Text matches place name, city, country or trip title, case-insensitively.
	Text  string
	Limit int
}

// MapPhoto is one located photo as a map marker needs it.
type MapPhoto struct {
	Photo
	TripTitle string
}

// Summary is the Dashboard header for one owner (GET /api/me/summary).
type Summary struct {
	PhotoCount   int
	CountryCount int
	CityCount    int
	// ToReview counts photos without a location plus suggested labels the
	// owner has not confirmed.
	ToReview int
	// Recent are the newest uploads, newest first.
	Recent []Photo
}

// Overview is the read side behind trip cards, the owner map and the
// Dashboard. Every method is scoped to ownerID.
type Overview interface {
	TripStats(ctx context.Context, ownerID uuid.UUID, tripIDs []uuid.UUID) (map[uuid.UUID]TripStats, error)
	MapPhotos(ctx context.Context, ownerID uuid.UUID, q MapQuery) ([]MapPhoto, error)
	Summary(ctx context.Context, ownerID uuid.UUID, recent int) (Summary, error)
}

var _ Overview = (*PostgresRepository)(nil)

const processedStates = `('ready', 'needs-location')`

// TripStats returns aggregates for the caller's trips in tripIDs. A trip with
// no processed photo is present with zero values.
func (r *PostgresRepository) TripStats(ctx context.Context, ownerID uuid.UUID, tripIDs []uuid.UUID) (map[uuid.UUID]TripStats, error) {
	stats := make(map[uuid.UUID]TripStats, len(tripIDs))
	if len(tripIDs) == 0 {
		return stats, nil
	}
	ids := make([]string, len(tripIDs))
	for i, id := range tripIDs {
		ids[i] = id.String()
		stats[id] = TripStats{Cities: []string{}, Countries: []string{}}
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT p.trip_id,
			count(*),
			count(*) FILTER (WHERE p.lat IS NOT NULL),
			to_char(min(p.capture_time_local), 'YYYY-MM-DD'),
			to_char(max(p.capture_time_local), 'YYYY-MM-DD')
		FROM photos p JOIN trips t ON t.id = p.trip_id
		WHERE t.owner_id = $1 AND p.trip_id = ANY($2::uuid[]) AND p.state IN `+processedStates+`
		GROUP BY p.trip_id
	`, ownerID, "{"+strings.Join(ids, ",")+"}")
	if err != nil {
		return nil, fmt.Errorf("trip stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var s TripStats
		if err := rows.Scan(&id, &s.PhotoCount, &s.LocatedCount, &s.StartDate, &s.EndDate); err != nil {
			return nil, fmt.Errorf("trip stats: %w", err)
		}
		s.Cities, s.Countries = []string{}, []string{}
		stats[id] = s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("trip stats: %w", err)
	}

	// Distinct labels in the order they were first photographed.
	labels, err := r.db.QueryContext(ctx, `
		SELECT trip_id, kind, label FROM (
			SELECT p.trip_id, 'city' AS kind, p.city AS label,
				min(COALESCE(p.capture_time_local, 'infinity')) AS first_seen
			FROM photos p JOIN trips t ON t.id = p.trip_id
			WHERE t.owner_id = $1 AND p.trip_id = ANY($2::uuid[]) AND p.state IN `+processedStates+`
			  AND p.lat IS NOT NULL AND p.city IS NOT NULL
			GROUP BY p.trip_id, p.city
			UNION ALL
			SELECT p.trip_id, 'country', p.country,
				min(COALESCE(p.capture_time_local, 'infinity'))
			FROM photos p JOIN trips t ON t.id = p.trip_id
			WHERE t.owner_id = $1 AND p.trip_id = ANY($2::uuid[]) AND p.state IN `+processedStates+`
			  AND p.lat IS NOT NULL AND p.country IS NOT NULL
			GROUP BY p.trip_id, p.country
		) l ORDER BY trip_id, kind, first_seen, label
	`, ownerID, "{"+strings.Join(ids, ",")+"}")
	if err != nil {
		return nil, fmt.Errorf("trip labels: %w", err)
	}
	defer labels.Close()
	for labels.Next() {
		var id uuid.UUID
		var kind, label string
		if err := labels.Scan(&id, &kind, &label); err != nil {
			return nil, fmt.Errorf("trip labels: %w", err)
		}
		s := stats[id]
		if kind == "city" {
			s.Cities = append(s.Cities, label)
		} else {
			s.Countries = append(s.Countries, label)
		}
		stats[id] = s
	}
	if err := labels.Err(); err != nil {
		return nil, fmt.Errorf("trip labels: %w", err)
	}

	public, err := r.db.QueryContext(ctx, `
		SELECT s.trip_id, bool_or(EXISTS (
			SELECT 1 FROM share_reports sr WHERE sr.share_id = s.id AND sr.resolved_at IS NULL
		))
		FROM shares s JOIN trips t ON t.id = s.trip_id
		WHERE t.owner_id = $1 AND s.trip_id = ANY($2::uuid[]) AND s.mode = 'public' AND s.revoked_at IS NULL
		GROUP BY s.trip_id
	`, ownerID, "{"+strings.Join(ids, ",")+"}")
	if err != nil {
		return nil, fmt.Errorf("trip visibility: %w", err)
	}
	defer public.Close()
	for public.Next() {
		var id uuid.UUID
		var underReview bool
		if err := public.Scan(&id, &underReview); err != nil {
			return nil, fmt.Errorf("trip visibility: %w", err)
		}
		s := stats[id]
		s.Public, s.UnderReview = true, underReview
		stats[id] = s
	}
	if err := public.Err(); err != nil {
		return nil, fmt.Errorf("trip visibility: %w", err)
	}

	// The cover is the first processed photo in the trip's capture order.
	covers, err := r.queryPhotos(ctx, `
		SELECT DISTINCT ON (p.trip_id) `+photoColumns+`
		FROM photos p JOIN trips t ON t.id = p.trip_id
		WHERE t.owner_id = $1 AND p.trip_id = ANY($2::uuid[]) AND p.state IN `+processedStates+`
		ORDER BY p.trip_id, COALESCE(p.capture_time_local, 'infinity'), COALESCE(p.uploaded_at, 'infinity'), p.id
	`, ownerID, "{"+strings.Join(ids, ",")+"}")
	if err != nil {
		return nil, fmt.Errorf("trip covers: %w", err)
	}
	for _, cover := range covers {
		s := stats[cover.TripID]
		photo := cover
		s.Cover = &photo
		stats[cover.TripID] = s
	}
	return stats, nil
}

// MapPhotos returns at most q.Limit of the caller's located, processed photos
// matching q, newest capture first.
func (r *PostgresRepository) MapPhotos(ctx context.Context, ownerID uuid.UUID, q MapQuery) ([]MapPhoto, error) {
	args := []any{ownerID}
	where := []string{"t.owner_id = $1", "p.state IN " + processedStates, "p.lat IS NOT NULL"}
	arg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if q.BBox != nil {
		west, south, east, north := q.BBox[0], q.BBox[1], q.BBox[2], q.BBox[3]
		where = append(where, "p.lat BETWEEN "+arg(south)+" AND "+arg(north))
		if west <= east {
			where = append(where, "p.lng BETWEEN "+arg(west)+" AND "+arg(east))
		} else {
			where = append(where, "(p.lng >= "+arg(west)+" OR p.lng <= "+arg(east)+")")
		}
	}
	if q.TripID != nil {
		where = append(where, "p.trip_id = "+arg(*q.TripID))
	}
	if q.From != nil {
		where = append(where, "p.capture_time_local >= "+arg(*q.From)+"::date")
	}
	if q.To != nil {
		where = append(where, "p.capture_time_local < "+arg(*q.To)+"::date + 1")
	}
	if text := strings.TrimSpace(q.Text); text != "" {
		pattern := arg("%" + escapeLike(text) + "%")
		where = append(where, "(p.place_name ILIKE "+pattern+" OR p.city ILIKE "+pattern+
			" OR p.country ILIKE "+pattern+" OR t.title ILIKE "+pattern+")")
	}
	limit := arg(q.Limit)

	rows, err := r.db.QueryContext(ctx, `
		SELECT `+photoColumns+`, t.title
		FROM photos p JOIN trips t ON t.id = p.trip_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY p.capture_time_local DESC NULLS LAST, p.uploaded_at DESC NULLS LAST, p.id
		LIMIT `+limit, args...)
	if err != nil {
		return nil, fmt.Errorf("map photos: %w", err)
	}
	defer rows.Close()
	result := make([]MapPhoto, 0)
	for rows.Next() {
		var title string
		photo, err := scanPhoto(scanWithExtra{rows, &title})
		if err != nil {
			return nil, fmt.Errorf("map photos: %w", err)
		}
		result = append(result, MapPhoto{Photo: photo, TripTitle: title})
	}
	return result, rows.Err()
}

// scanWithExtra lets scanPhoto read a row that has extra trailing columns.
type scanWithExtra struct {
	row   rowScanner
	extra *string
}

func (s scanWithExtra) Scan(dest ...any) error {
	return s.row.Scan(append(dest, s.extra)...)
}

// escapeLike makes user text match literally inside an ILIKE pattern.
func escapeLike(text string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text)
}

// Summary returns the caller's Dashboard totals and their recent uploads.
func (r *PostgresRepository) Summary(ctx context.Context, ownerID uuid.UUID, recent int) (Summary, error) {
	var s Summary
	err := r.db.QueryRowContext(ctx, `
		SELECT count(*),
			count(DISTINCT p.country) FILTER (WHERE p.lat IS NOT NULL),
			count(DISTINCT p.city) FILTER (WHERE p.lat IS NOT NULL),
			count(*) FILTER (WHERE p.state = 'needs-location' OR p.label_status = 'suggested')
		FROM photos p JOIN trips t ON t.id = p.trip_id
		WHERE t.owner_id = $1 AND p.state IN `+processedStates,
		ownerID).Scan(&s.PhotoCount, &s.CountryCount, &s.CityCount, &s.ToReview)
	if err != nil {
		return Summary{}, fmt.Errorf("summary: %w", err)
	}
	s.Recent, err = r.queryPhotos(ctx, `
		SELECT `+photoColumns+`
		FROM photos p JOIN trips t ON t.id = p.trip_id
		WHERE t.owner_id = $1 AND p.state IN `+processedStates+`
		ORDER BY p.uploaded_at DESC NULLS LAST, p.id DESC
		LIMIT $2
	`, ownerID, recent)
	if err != nil {
		return Summary{}, fmt.Errorf("recent uploads: %w", err)
	}
	return s, nil
}

// dateLayout is the format of capture-date filters and trip date ranges.
const dateLayout = "2006-01-02"

// ValidDate reports whether value is a real "YYYY-MM-DD" date.
func ValidDate(value string) bool {
	_, err := time.Parse(dateLayout, value)
	return err == nil
}
