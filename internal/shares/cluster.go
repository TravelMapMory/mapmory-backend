package shares

import (
	"fmt"
	"math"
	"sort"

	"github.com/google/uuid"
)

// Public-map grouping (design doc 5.2), the same rules as the frontend's
// src/api/publicGrid.ts so the demo and the server answer alike: photos fall
// into square cells of CellPx screen pixels at the requested zoom (Web
// Mercator, like the map tiles), and the ClusterLimit fullest cells are
// returned, largest first.
const (
	CellPx              = 64
	ClusterLimit        = 200
	MaxSharesPerCluster = 5
	tilePx              = 256
	maxLat              = 85.05112878
	MaxZoom             = 22
)

// ShareRef is a public share with photos inside one cluster.
type ShareRef struct {
	ID    uuid.UUID
	Title string
	// CoverObject is the display object of one of its photos in the cluster.
	CoverObject string
	// PhotoCount counts the share's photos inside the cluster only.
	PhotoCount int
}

// Cluster is one grid cell of public photos.
type Cluster struct {
	ID    string
	Lat   float64
	Lng   float64
	Count int
	// Label is the most common city, else the most common country, else "".
	Label       string
	CoverObject string
	// Bounds is west, south, east, north of the photos' coarse positions.
	Bounds     [4]float64
	ShareCount int
	Shares     []ShareRef
}

func clamp(value, low, high float64) float64 {
	return math.Min(high, math.Max(low, value))
}

func worldPixel(lat, lng float64, zoom int) (float64, float64) {
	size := tilePx * math.Pow(2, float64(zoom))
	sin := math.Sin(clamp(lat, -maxLat, maxLat) * math.Pi / 180)
	return (lng + 180) / 360 * size, (0.5 - math.Log((1+sin)/(1-sin))/(4*math.Pi)) * size
}

// mostCommon returns the most frequent non-empty value; ties go to the
// alphabetically first, so labels do not flicker between requests.
func mostCommon(values []*string) string {
	counts := make(map[string]int)
	for _, value := range values {
		if value != nil && *value != "" {
			counts[*value]++
		}
	}
	best, bestCount := "", 0
	for value, count := range counts {
		if count > bestCount || (count == bestCount && value < best) {
			best, bestCount = value, count
		}
	}
	return best
}

// ClusterPoints groups points (already inside the requested box) at zoom and
// returns at most limit clusters plus whether more cells had photos.
func ClusterPoints(points []PublicPoint, zoom, limit int) ([]Cluster, bool) {
	zoom = int(clamp(float64(zoom), 0, MaxZoom))
	cells := make(map[string][]PublicPoint)
	order := make([]string, 0)
	for _, point := range points {
		x, y := worldPixel(point.Lat, point.Lng, zoom)
		id := fmt.Sprintf("%d/%d/%d", zoom, int(math.Floor(x/CellPx)), int(math.Floor(y/CellPx)))
		if _, ok := cells[id]; !ok {
			order = append(order, id)
		}
		cells[id] = append(cells[id], point)
	}
	clusters := make([]Cluster, 0, len(cells))
	for _, id := range order {
		clusters = append(clusters, toCluster(id, cells[id]))
	}
	sort.Slice(clusters, func(i, j int) bool {
		if clusters[i].Count != clusters[j].Count {
			return clusters[i].Count > clusters[j].Count
		}
		return clusters[i].ID < clusters[j].ID
	})
	if len(clusters) > limit {
		return clusters[:limit], true
	}
	return clusters, false
}

func toCluster(id string, points []PublicPoint) Cluster {
	// A photo in two public shares is one photo on the map; both shares are listed.
	seen := make(map[uuid.UUID]bool)
	unique := make([]PublicPoint, 0, len(points))
	for _, p := range points {
		if !seen[p.PhotoID] {
			seen[p.PhotoID] = true
			unique = append(unique, p)
		}
	}

	type shareAcc struct {
		ref    ShareRef
		photos map[uuid.UUID]bool
	}
	byShare := make(map[uuid.UUID]*shareAcc)
	for _, p := range points {
		acc, ok := byShare[p.ShareID]
		if !ok {
			acc = &shareAcc{ref: ShareRef{ID: p.ShareID, Title: p.ShareTitle, CoverObject: p.DisplayObjectName}, photos: map[uuid.UUID]bool{}}
			byShare[p.ShareID] = acc
		}
		acc.photos[p.PhotoID] = true
		acc.ref.PhotoCount = len(acc.photos)
	}
	refs := make([]ShareRef, 0, len(byShare))
	for _, acc := range byShare {
		refs = append(refs, acc.ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := refs[i], refs[j]
		if a.PhotoCount != b.PhotoCount {
			return a.PhotoCount > b.PhotoCount
		}
		if a.Title != b.Title {
			return a.Title < b.Title
		}
		return a.ID.String() < b.ID.String()
	})

	c := Cluster{ID: id, Count: len(unique), ShareCount: len(refs)}
	if len(refs) > MaxSharesPerCluster {
		refs = refs[:MaxSharesPerCluster]
	}
	c.Shares = refs
	cities := make([]*string, len(unique))
	countries := make([]*string, len(unique))
	c.Bounds = [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for i, p := range unique {
		c.Lat += p.Lat / float64(len(unique))
		c.Lng += p.Lng / float64(len(unique))
		cities[i], countries[i] = p.City, p.Country
		c.Bounds[0], c.Bounds[1] = math.Min(c.Bounds[0], p.Lng), math.Min(c.Bounds[1], p.Lat)
		c.Bounds[2], c.Bounds[3] = math.Max(c.Bounds[2], p.Lng), math.Max(c.Bounds[3], p.Lat)
		if c.CoverObject == "" {
			c.CoverObject = p.DisplayObjectName
		}
	}
	c.Label = mostCommon(cities)
	if c.Label == "" {
		c.Label = mostCommon(countries)
	}
	return c
}

// CoarsePosition is the position shared and public views may show (doc 5.4):
// the city's centre when known, otherwise the exact position rounded to a
// 0.01 degree grid, about 1 km.
func CoarsePosition(lat, lng float64, cityCentre *[2]float64) (float64, float64) {
	if cityCentre != nil {
		return cityCentre[0], cityCentre[1]
	}
	return math.Round(lat*100) / 100, math.Round(lng*100) / 100
}
