package identity

import (
	"context"
	"errors"
)

// AllowAuthAttempt consumes one attempt in a shared, database-clocked fixed window.
// A database failure must be treated as denial by the HTTP caller.
func (s *Service) AllowAuthAttempt(ctx context.Context, key string, windowSeconds, maxAttempts int) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errors.New("identity rate limit storage unavailable")
	}
	var attempts int
	err := s.pool.QueryRow(ctx, `
WITH timing AS (SELECT clock_timestamp() AS now)
INSERT INTO identity_auth_limits (key, window_started_at, attempts)
SELECT $1, timing.now, 1 FROM timing
ON CONFLICT (key) DO UPDATE SET
  attempts = CASE
    WHEN identity_auth_limits.window_started_at <= EXCLUDED.window_started_at - $2::int * interval '1 second' THEN 1
    ELSE LEAST(identity_auth_limits.attempts, $3::int) + 1
  END,
  window_started_at = CASE
    WHEN identity_auth_limits.window_started_at <= EXCLUDED.window_started_at - $2::int * interval '1 second'
    THEN EXCLUDED.window_started_at
    ELSE identity_auth_limits.window_started_at
  END
RETURNING attempts`, key, windowSeconds, maxAttempts).Scan(&attempts)
	if err != nil {
		return false, err
	}
	if s.limitRequests.Add(1)%256 == 0 {
		// Bound table growth from one-off IP and email keys without a cron dependency.
		_, _ = s.pool.Exec(ctx, `DELETE FROM identity_auth_limits
WHERE window_started_at < clock_timestamp() - interval '2 days' AND key IN (
  SELECT key FROM identity_auth_limits
  WHERE window_started_at < clock_timestamp() - interval '2 days'
  ORDER BY window_started_at LIMIT 1000
)`)
	}
	return attempts <= maxAttempts, nil
}
