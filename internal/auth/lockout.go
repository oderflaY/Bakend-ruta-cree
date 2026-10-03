package auth

import (
	"context"
	"database/sql"
	"time"
)

// Lockout policy: after maxFailures wrong passwords/PINs the account is locked for lockDuration.
// Combined with the per-IP rate limit this makes brute-forcing a 6-digit PIN impractical.
const (
	maxFailures  = 5
	lockDuration = 15 * time.Minute
)

// Account is a table with failed_logins / locked_until columns. The table name is a constant
// chosen in code, never user input.
type Account string

const (
	AdminAccounts  Account = "admins"
	DriverAccounts Account = "drivers"
	RiderAccounts  Account = "riders"
)

func Locked(lockedUntil sql.NullTime) bool {
	return lockedUntil.Valid && lockedUntil.Time.After(time.Now())
}

func (a Account) RecordFailure(ctx context.Context, db *sql.DB, id any) error {
	_, err := db.ExecContext(ctx, `UPDATE `+string(a)+` SET
		failed_logins = failed_logins + 1,
		locked_until = IF(failed_logins >= ?, ?, locked_until)
		WHERE id = ?`, maxFailures, time.Now().Add(lockDuration).UTC(), id)
	return err
}

func (a Account) RecordSuccess(ctx context.Context, db *sql.DB, id any) error {
	_, err := db.ExecContext(ctx, `UPDATE `+string(a)+` SET failed_logins = 0, locked_until = NULL WHERE id = ?`, id)
	return err
}
