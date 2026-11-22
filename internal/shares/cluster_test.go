package shares

import (
	"testing"

	"github.com/google/uuid"
)

func point(share uuid.UUID, title string, lat, lng float64, city string) PublicPoint {
	c := city
	return PublicPoint{PhotoID: uuid.New(), ShareID: share, ShareTitle: title, DisplayObjectName: "display/" + title,
		Lat: lat, Lng: lng, City: &c}
}

func TestClusterPointsGroupsByCellAndRanks(t *testing.T) {
	kyoto, osaka := uuid.New(), uuid.New()
	points := []PublicPoint{
		point(kyoto, "Kyoto days", 35.01, 135.77, "Kyoto"),
		point(kyoto, "Kyoto days", 35.01, 135.77, "Kyoto"),
		point(osaka, "Osaka", 35.01, 135.77, "Kyoto"),
		point(osaka, "Osaka", 34.69, 135.50, "Osaka"),
	}
	// Zoom 4: the whole Kansai area is one 64 px cell.
	clusters, truncated := ClusterPoints(points, 4, ClusterLimit)
	if truncated || len(clusters) != 1 {
		t.Fatalf("zoom 4: %d clusters, truncated=%v", len(clusters), truncated)
	}
	c := clusters[0]
	if c.Count != 4 || c.ShareCount != 2 || c.Label != "Kyoto" || c.Shares[0].ID != kyoto || c.Shares[0].PhotoCount != 2 {
		t.Fatalf("cluster = %+v", c)
	}
	if c.Bounds != [4]float64{135.50, 34.69, 135.77, 35.01} || c.ID[:2] != "4/" {
		t.Fatalf("bounds=%v id=%s", c.Bounds, c.ID)
	}

	// Zoom 12 separates Kyoto and Osaka; the fuller cell comes first.
	clusters, _ = ClusterPoints(points, 12, ClusterLimit)
	if len(clusters) != 2 || clusters[0].Count != 3 || clusters[1].Label != "Osaka" {
		t.Fatalf("zoom 12: %+v", clusters)
	}
}

func TestClusterPointsCountsAPhotoInTwoSharesOnce(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	shared := point(a, "A", 1, 1, "X")
	again := shared
	again.ShareID, again.ShareTitle = b, "B"
	clusters, _ := ClusterPoints([]PublicPoint{shared, again}, 5, ClusterLimit)
	if len(clusters) != 1 || clusters[0].Count != 1 || clusters[0].ShareCount != 2 {
		t.Fatalf("clusters = %+v", clusters)
	}
}

func TestClusterPointsLimitAndShareCap(t *testing.T) {
	points := make([]PublicPoint, 0)
	for i := 0; i < 8; i++ {
		points = append(points, point(uuid.New(), "S", float64(i*10), float64(i*10), ""))
	}
	clusters, truncated := ClusterPoints(points, 3, 5)
	if len(clusters) != 5 || !truncated {
		t.Fatalf("limit: %d clusters, truncated=%v", len(clusters), truncated)
	}

	many := make([]PublicPoint, 0)
	for i := 0; i < 7; i++ {
		many = append(many, point(uuid.New(), string(rune('A'+i)), 1, 1, ""))
	}
	clusters, _ = ClusterPoints(many, 3, ClusterLimit)
	if clusters[0].ShareCount != 7 || len(clusters[0].Shares) != MaxSharesPerCluster || clusters[0].Label != "" {
		t.Fatalf("share cap: %+v", clusters[0])
	}
}

func TestCoarsePosition(t *testing.T) {
	if lat, lng := CoarsePosition(35.01234, 135.77654, nil); lat != 35.01 || lng != 135.78 {
		t.Fatalf("rounded = %v,%v", lat, lng)
	}
	centre := [2]float64{35.0116, 135.7681}
	if lat, lng := CoarsePosition(35.04, 135.70, &centre); lat != centre[0] || lng != centre[1] {
		t.Fatalf("city centre = %v,%v", lat, lng)
	}
}

func TestNewTokenIsLongAndRandom(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewToken()
	if len(a) != 22 || a == b {
		t.Fatalf("tokens %q %q", a, b)
	}
}
