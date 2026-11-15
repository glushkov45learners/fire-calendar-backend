// Package api implements the HTTP contract 3 (/api/v1).
package api

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/glushkov45learners/fire-calendar-backend/internal/analysis"
	"github.com/glushkov45learners/fire-calendar-backend/internal/digest"
	"github.com/glushkov45learners/fire-calendar-backend/internal/store"
)

const (
	maxBodyBytes   = 5 << 20
	maxHotspotRows = 500_000
	gridStep       = 0.1
)

type Server struct {
	store     *store.Store
	log       *slog.Logger
	digest    *digest.Service
	digestKey string
}

// Config is the optional part of the API.
type Config struct {
	CORSOrigins []string        // empty = same-origin only
	Digest      *digest.Service // monthly e-mail; nil = no demo pages
	DigestKey   string          // key of the /api/digest pages; empty = pages are off
}

// New returns the API handler.
func New(s *store.Store, log *slog.Logger, cfg Config) http.Handler {
	srv := &Server{store: s, log: log, digest: cfg.Digest, digestKey: cfg.DigestKey}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/subscriptions", srv.subscribe)
	mux.HandleFunc("GET /api/v1/unsubscribe", srv.unsubscribe)
	mux.HandleFunc("POST /api/v1/unsubscribe", srv.unsubscribe)
	// Outside /api/v1: demo pages, not part of contract 3.
	mux.HandleFunc("GET /api/digest", srv.digestGuard(srv.digestPage))
	mux.HandleFunc("GET /api/digest/preview", srv.digestGuard(srv.digestPreview))
	mux.HandleFunc("POST /api/digest/send", srv.digestGuard(srv.digestSend))
	mux.HandleFunc("GET /healthz", srv.health)
	mux.HandleFunc("GET /api/v1/meta", srv.meta)
	mux.HandleFunc("POST /api/v1/analyze", srv.analyze)
	mux.HandleFunc("POST /api/v1/hotspots.csv", srv.hotspotsCSV)
	mux.HandleFunc("POST /api/v1/calendar.csv", srv.calendarCSV)
	mux.HandleFunc("GET /api/v1/grid", srv.grid)
	mux.Handle("GET /api/birthday/", birthdayHandler())
	mux.Handle("GET /api/birthday", http.RedirectHandler("/api/birthday/", http.StatusMovedPermanently))
	return withCORS(cfg.CORSOrigins, mux)
}

// apiError is the contract 3 error body.
type apiError struct {
	status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func badRequest(code, format string, args ...any) *apiError {
	return &apiError{status: http.StatusBadRequest, Code: code, Message: fmt.Sprintf(format, args...)}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
	case errors.Is(err, store.ErrInvalidAOI):
		ae = badRequest("invalid_aoi", "%v", err)
	case errors.Is(err, store.ErrOutsideCoverage):
		ae = badRequest("outside_coverage", "AOI does not intersect the data coverage")
	case errors.Is(err, store.ErrNoData):
		ae = &apiError{status: http.StatusServiceUnavailable, Code: "no_data", Message: "no data loaded yet"}
	default:
		s.log.Error("request failed", "path", r.URL.Path, "err", err)
		ae = &apiError{status: http.StatusInternalServerError, Code: "internal", Message: "internal error"}
	}
	writeJSON(w, ae.status, map[string]any{"error": ae})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	cov, err := s.store.Coverage(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	baseline := []int{}
	if first, last, ok := analysis.New(nil, cov.First, cov.Last).BaselineYears(); ok {
		baseline = []int{first, last}
	}
	model := cov.Model
	if model == nil {
		model = json.RawMessage("null")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"coverage_bbox":  cov.BBox,
		"date_min":       cov.First.Format(time.DateOnly),
		"date_max":       cov.Last.Format(time.DateOnly),
		"grid_step":      gridStep,
		"baseline_years": baseline,
		"model":          model,
	})
}

// query is the common request body: AOI plus an inclusive date range.
type query struct {
	AOI         json.RawMessage `json:"aoi"`
	From        string          `json:"from"`
	To          string          `json:"to"`
	Granularity string          `json:"granularity"`

	aoi      store.AOI
	from, to time.Time // clamped to the data range
	cov      store.Coverage
}

// parse decodes and validates the body, resolves the AOI and clamps the range.
func (s *Server) parse(w http.ResponseWriter, r *http.Request) (*query, error) {
	var q query
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&q); err != nil {
		return nil, badRequest("invalid_request", "malformed JSON body: %v", err)
	}

	var shape struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(q.AOI, &shape); err != nil || len(q.AOI) == 0 {
		return nil, badRequest("invalid_aoi", "aoi must be a GeoJSON object")
	}
	if shape.Type != "Polygon" && shape.Type != "MultiPolygon" {
		return nil, badRequest("invalid_aoi", "AOI must be a Polygon or MultiPolygon")
	}
	if len(bytes.TrimSpace(shape.Coordinates)) == 0 {
		return nil, badRequest("invalid_aoi", "aoi has no coordinates")
	}

	from, errF := time.Parse(time.DateOnly, q.From)
	to, errT := time.Parse(time.DateOnly, q.To)
	if errF != nil || errT != nil {
		return nil, badRequest("invalid_range", "from and to must be YYYY-MM-DD")
	}
	if from.After(to) {
		return nil, badRequest("invalid_range", "from is after to")
	}

	cov, err := s.store.Coverage(r.Context())
	if err != nil {
		return nil, err
	}
	if to.Before(cov.First) || from.After(cov.Last) {
		return nil, badRequest("invalid_range", "range is outside the data (%s..%s)",
			cov.First.Format(time.DateOnly), cov.Last.Format(time.DateOnly))
	}
	q.cov = cov
	q.from = maxTime(from, cov.First)
	q.to = minTime(to, cov.Last)

	q.aoi, err = s.store.ResolveAOI(r.Context(), q.AOI)
	return &q, err
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *Server) series(r *http.Request, q *query) (*analysis.Series, error) {
	days, err := s.store.DailySums(r.Context(), q.aoi.Cells)
	if err != nil {
		return nil, err
	}
	return analysis.New(days, q.cov.First, q.cov.Last), nil
}

func (s *Server) analyze(w http.ResponseWriter, r *http.Request) {
	q, err := s.parse(w, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	series, err := s.series(r, q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		AreaKm2 float64 `json:"area_km2"`
		Cells   int     `json:"cells"`
		analysis.Report
	}{
		AreaKm2: float64(int64(q.aoi.AreaKm2*10+0.5)) / 10,
		Cells:   len(q.aoi.Cells),
		Report:  series.Report(q.from, q.to),
	})
}

func startCSV(w http.ResponseWriter, name string, header []string) *csv.Writer {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	cw := csv.NewWriter(w)
	_ = cw.Write(header)
	return cw
}

func csvName(kind string, q *query) string {
	return fmt.Sprintf("%s_%s_%s.csv", kind, q.from.Format(time.DateOnly), q.to.Format(time.DateOnly))
}

func (s *Server) hotspotsCSV(w http.ResponseWriter, r *http.Request) {
	q, err := s.parse(w, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	n, err := s.store.CountHotspots(r.Context(), q.aoi, q.from, q.to, maxHotspotRows+1)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if n > maxHotspotRows {
		s.fail(w, r, &apiError{status: http.StatusRequestEntityTooLarge, Code: "too_many_rows",
			Message: fmt.Sprintf("more than %d hotspots; narrow the area or the dates", maxHotspotRows)})
		return
	}

	cw := startCSV(w, csvName("hotspots", q), []string{"latitude", "longitude", "acq_date", "acq_time",
		"satellite", "instrument", "confidence", "frp", "daynight", "type", "product"})
	err = s.store.EachHotspot(r.Context(), q.aoi, q.from, q.to, func(h store.Hotspot) error {
		return cw.Write([]string{
			fmtFloat(h.Lat), fmtFloat(h.Lon), h.AcqDate.Format(time.DateOnly), h.AcqTime,
			h.Satellite, h.Instrument, h.Conf, optFloat32(h.FRP), optString(h.DayNight),
			optInt16(h.Type), h.Product,
		})
	})
	cw.Flush()
	if err == nil {
		err = cw.Error()
	}
	if err != nil {
		// Headers are already sent; the truncated download is all we can do.
		s.log.Error("hotspots.csv stream failed", "err", err)
	}
}

func (s *Server) calendarCSV(w http.ResponseWriter, r *http.Request) {
	q, err := s.parse(w, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if q.Granularity != "month" && q.Granularity != "day" {
		s.fail(w, r, badRequest("invalid_request", "granularity must be month or day"))
		return
	}
	series, err := s.series(r, q)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	if q.Granularity == "day" {
		cw := startCSV(w, csvName("calendar_day", q), []string{"period", "count_h", "frp_h", "modis_raw", "viirs_raw"})
		for _, d := range series.Daily(q.from, q.to) {
			_ = cw.Write([]string{d.Date, fmtFloat(d.CountH), fmtFloat(d.FrpH),
				strconv.Itoa(d.ModisRaw), strconv.Itoa(d.ViirsRaw)})
		}
		cw.Flush()
		return
	}

	cw := startCSV(w, csvName("calendar_month", q), []string{"period", "count_h", "frp_h", "modis_raw",
		"viirs_raw", "median", "z", "pct", "anomaly"})
	for _, m := range series.Monthly(q.from, q.to) {
		pct := ""
		if m.Pct != nil {
			pct = strconv.Itoa(*m.Pct)
		}
		_ = cw.Write([]string{m.Period, fmtFloat(m.CountH), fmtFloat(m.FrpH), strconv.Itoa(m.ModisRaw),
			strconv.Itoa(m.ViirsRaw), fmtFloat(m.Median), optFloat(m.Z), pct, m.Anomaly})
	}
	cw.Flush()
}

// grid serves the map layer: totals per 0.1° cell over from..to for one
// record (harmonized, modis or viirs), for the whole coverage.
func (s *Server) grid(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query()
	record := p.Get("record")
	if record == "" {
		record = "harmonized"
	}
	if !store.ValidGridRecord(record) {
		s.fail(w, r, badRequest("invalid_request", "record must be harmonized, modis or viirs"))
		return
	}
	from, errF := time.Parse(time.DateOnly, p.Get("from"))
	to, errT := time.Parse(time.DateOnly, p.Get("to"))
	if errF != nil || errT != nil {
		s.fail(w, r, badRequest("invalid_range", "from and to must be YYYY-MM-DD"))
		return
	}
	if from.After(to) {
		s.fail(w, r, badRequest("invalid_range", "from is after to"))
		return
	}
	cells, err := s.store.GridSums(r.Context(), record, from, to)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if cells == nil {
		cells = []store.GridCell{} // [] rather than null
	}
	for i := range cells {
		cells[i].Count = float64(int64(cells[i].Count*100+0.5)) / 100
	}
	// The archive changes once a day at most; let the browser reuse the layer.
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, cells)
}

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func optFloat(f *float64) string {
	if f == nil {
		return ""
	}
	return fmtFloat(*f)
}

func optFloat32(f *float32) string {
	if f == nil {
		return ""
	}
	return strconv.FormatFloat(float64(*f), 'f', -1, 32)
}

func optString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func optInt16(i *int16) string {
	if i == nil {
		return ""
	}
	return strconv.Itoa(int(*i))
}
