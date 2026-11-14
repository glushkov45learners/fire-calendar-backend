// Package store owns the PostGIS schema and every SQL query of the API.
package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/glushkov45learners/fire-calendar-backend/internal/analysis"
)

//go:embed schema.sql
var schema string

var (
	ErrInvalidAOI      = errors.New("invalid aoi")
	ErrOutsideCoverage = errors.New("aoi outside coverage")
	ErrNoData          = errors.New("no data loaded")
)

type Store struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Coverage is what /meta reports about the loaded data.
type Coverage struct {
	BBox  [4]float64
	First time.Time
	Last  time.Time
	Model json.RawMessage // latest model_meta.body, nil if none loaded
}

// Coverage returns the grid extent, the data date range (over raw points and
// harmonized cells, whichever is wider) and the latest model description.
func (s *Store) Coverage(ctx context.Context) (Coverage, error) {
	var c Coverage
	var first, last *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT ST_XMin(e), ST_YMin(e), ST_XMax(e), ST_YMax(e),
		       LEAST((SELECT min(acq_date) FROM hotspot), (SELECT min(date) FROM cell_day)),
		       GREATEST((SELECT max(acq_date) FROM hotspot), (SELECT max(date) FROM cell_day)),
		       (SELECT body FROM model_meta ORDER BY loaded_at DESC LIMIT 1)
		FROM (SELECT ST_Extent(geom) AS e FROM grid_cell) g`,
	).Scan(&c.BBox[0], &c.BBox[1], &c.BBox[2], &c.BBox[3], &first, &last, &c.Model)
	if err != nil {
		return c, err
	}
	if first == nil || last == nil {
		return c, ErrNoData
	}
	c.First, c.Last = first.UTC(), last.UTC()
	return c, nil
}

// AOI is a validated area of interest.
type AOI struct {
	GeoJSON []byte
	AreaKm2 float64
	Cells   []int32 // grid cells whose centre is inside, or that intersect it
}

// ResolveAOI validates a GeoJSON polygon and picks its grid cells per
// contract 3: centres inside the AOI, or, if none, cells intersecting it.
func (s *Store) ResolveAOI(ctx context.Context, geojson []byte) (AOI, error) {
	a := AOI{GeoJSON: geojson}
	var valid bool
	var reason string
	err := s.pool.QueryRow(ctx, `
		WITH a AS (SELECT ST_SetSRID(ST_GeomFromGeoJSON($1), 4326) AS g)
		SELECT ST_IsValid(g)
		         AND ST_XMin(g) >= -180 AND ST_XMax(g) <= 180
		         AND ST_YMin(g) >= -90  AND ST_YMax(g) <= 90,
		       ST_IsValidReason(g)
		FROM a`, string(geojson)).Scan(&valid, &reason)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return a, fmt.Errorf("%w: %s", ErrInvalidAOI, pgErr.Message)
		}
		return a, err
	}
	if !valid {
		return a, fmt.Errorf("%w: %s", ErrInvalidAOI, reason)
	}

	err = s.pool.QueryRow(ctx, `
		WITH a AS (SELECT ST_SetSRID(ST_GeomFromGeoJSON($1), 4326) AS g)
		SELECT ST_Area(g::geography) / 1e6,
		       COALESCE(
		         (SELECT array_agg(cell_id ORDER BY cell_id) FROM grid_cell WHERE ST_Contains(a.g, center)),
		         (SELECT array_agg(cell_id ORDER BY cell_id) FROM grid_cell WHERE ST_Intersects(a.g, geom)),
		         '{}')
		FROM a`, string(geojson)).Scan(&a.AreaKm2, &a.Cells)
	if err != nil {
		return a, err
	}
	if len(a.Cells) == 0 {
		return a, ErrOutsideCoverage
	}
	return a, nil
}

// DailySums returns harmonized and raw counts summed over cells, one entry
// per day that has any hotspot.
func (s *Store) DailySums(ctx context.Context, cells []int32) ([]analysis.Day, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT date, sum(count_h)::float8, sum(frp_h)::float8,
		       sum(modis_count)::int, sum(viirs_count)::int
		FROM cell_day WHERE cell_id = ANY($1)
		GROUP BY date ORDER BY date`, cells)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (analysis.Day, error) {
		var d analysis.Day
		err := r.Scan(&d.Date, &d.CountH, &d.FrpH, &d.Modis, &d.Viirs)
		d.Date = d.Date.UTC()
		return d, err
	})
}

// Hotspot is one raw FIRMS point (contract 3, hotspots.csv).
type Hotspot struct {
	Lat, Lon   float64
	AcqDate    time.Time
	AcqTime    string
	Satellite  string
	Instrument string
	Conf       string
	FRP        *float32
	DayNight   *string
	Type       *int16
	Product    string
}

const hotspotFilter = `
	FROM hotspot
	WHERE acq_date BETWEEN $2 AND $3
	  AND ST_Intersects(geom, ST_SetSRID(ST_GeomFromGeoJSON($1), 4326))`

// CountHotspots counts raw points exactly inside the AOI, stopping at limit.
func (s *Store) CountHotspots(ctx context.Context, aoi AOI, from, to time.Time, limit int) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM (SELECT 1 `+hotspotFilter+` LIMIT $4) t`,
		string(aoi.GeoJSON), from, to, limit,
	).Scan(&n)
	return n, err
}

// EachHotspot streams raw points exactly inside the AOI in time order.
func (s *Store) EachHotspot(ctx context.Context, aoi AOI, from, to time.Time, fn func(Hotspot) error) error {
	rows, err := s.pool.Query(ctx, `
		SELECT ST_Y(geom), ST_X(geom), acq_date, to_char(acq_ts AT TIME ZONE 'UTC', 'HH24MI'),
		       satellite, instrument, conf, frp, daynight, type, product`+hotspotFilter+`
		ORDER BY acq_ts, id`,
		string(aoi.GeoJSON), from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var h Hotspot
		if err := rows.Scan(&h.Lat, &h.Lon, &h.AcqDate, &h.AcqTime, &h.Satellite, &h.Instrument,
			&h.Conf, &h.FRP, &h.DayNight, &h.Type, &h.Product); err != nil {
			return err
		}
		if err := fn(h); err != nil {
			return err
		}
	}
	return rows.Err()
}
