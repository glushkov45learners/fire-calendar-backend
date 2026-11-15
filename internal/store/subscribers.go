package store

import (
	"context"
	"crypto/rand"

	"github.com/jackc/pgx/v5"

	"github.com/glushkov45learners/fire-calendar-backend/internal/analysis"
)

// Subscriber is one address that receives the monthly digest.
type Subscriber struct {
	Email string
	Token string // secret of the unsubscribe link
}

// Subscribe records consent for email (already normalized by the caller).
// Subscribing again is not an error; it re-activates an unsubscribed address
// and keeps its token, so old unsubscribe links keep working.
func (s *Store) Subscribe(ctx context.Context, email string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO subscriber (email, token) VALUES ($1, $2)
		ON CONFLICT (email) DO UPDATE
		SET consent_at = CASE WHEN subscriber.unsubscribed_at IS NULL
		                      THEN subscriber.consent_at ELSE now() END,
		    unsubscribed_at = NULL`, email, rand.Text())
	return err
}

// Unsubscribe turns off the subscription behind token. It reports false only
// for an unknown token; unsubscribing twice is fine.
func (s *Store) Unsubscribe(ctx context.Context, token string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE subscriber SET unsubscribed_at = COALESCE(unsubscribed_at, now())
		WHERE token = $1`, token)
	return tag.RowsAffected() > 0, err
}

// Subscribers returns every active subscription.
func (s *Store) Subscribers(ctx context.Context) ([]Subscriber, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT email, token FROM subscriber
		WHERE unsubscribed_at IS NULL ORDER BY consent_at, email`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Subscriber])
}

// ClaimDigest marks period (YYYY-MM) as mailed by the scheduler. It reports
// false if the period was claimed before, so one month is never mailed twice,
// even across restarts or several API instances.
func (s *Store) ClaimDigest(ctx context.Context, period string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO digest_run (period) VALUES ($1) ON CONFLICT DO NOTHING`, period)
	return tag.RowsAffected() > 0, err
}

// FinishDigest stores the outcome of a claimed period.
func (s *Store) FinishDigest(ctx context.Context, period string, sent, failed int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE digest_run SET sent = $2, failed = $3 WHERE period = $1`, period, sent, failed)
	return err
}

// CountryDailySums is DailySums over every cell: the archive is clipped to
// Ukraine's border by the ETL, so the whole table is the country.
func (s *Store) CountryDailySums(ctx context.Context) ([]analysis.Day, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT date, sum(count_h)::float8, sum(frp_h)::float8,
		       sum(modis_count)::int, sum(viirs_count)::int
		FROM cell_day GROUP BY date ORDER BY date`)
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
