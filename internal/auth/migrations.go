package auth

import "database/sql"

// Called while the exclusive data-directory lock is held. Version-0 installations
// have the same base tables as version 1. New tables and historical backfill are
// committed together, so an interrupted upgrade can safely be retried.
func migrate(db *sql.DB, version int) error {
	if version >= SchemaVersion {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
CREATE TABLE IF NOT EXISTS user_profiles (
 user_id TEXT PRIMARY KEY REFERENCES users(id),
 phone TEXT NOT NULL DEFAULT '', organization TEXT NOT NULL DEFAULT '',
 department TEXT NOT NULL DEFAULT '', job_title TEXT NOT NULL DEFAULT '',
 location TEXT NOT NULL DEFAULT '', bio TEXT NOT NULL DEFAULT '',
 locale TEXT NOT NULL DEFAULT 'zh-CN', time_zone TEXT NOT NULL DEFAULT 'Asia/Shanghai',
 revision INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS user_annotations (
 user_id TEXT PRIMARY KEY REFERENCES users(id), tags TEXT NOT NULL DEFAULT '[]',
 notes TEXT NOT NULL DEFAULT '', revision INTEGER NOT NULL DEFAULT 0,
 updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS user_activity_daily (
 user_id TEXT NOT NULL REFERENCES users(id), app_id TEXT NOT NULL,
 day INTEGER NOT NULL, logins INTEGER NOT NULL DEFAULT 0,
 first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL,
 PRIMARY KEY(user_id,app_id,day)
);
CREATE INDEX IF NOT EXISTS activity_day_user ON user_activity_daily(day,user_id);
CREATE INDEX IF NOT EXISTS activity_last_seen ON user_activity_daily(last_seen);
CREATE INDEX IF NOT EXISTS audit_user_time ON audit(user_id,created_at);
CREATE INDEX IF NOT EXISTS session_expires_seen ON sessions(expires_at,last_seen,user_id);
CREATE INDEX IF NOT EXISTS users_created ON users(created_at);
INSERT INTO user_activity_daily(user_id,app_id,day,logins,first_seen,last_seen)
 SELECT a.user_id,a.detail,(a.created_at/86400)*86400,COUNT(*),MIN(a.created_at),MAX(a.created_at)
 FROM audit a JOIN users u ON u.id=a.user_id
 WHERE a.action='auth.login' GROUP BY a.user_id,a.detail,(a.created_at/86400)*86400;
INSERT INTO user_activity_daily(user_id,app_id,day,logins,first_seen,last_seen)
 SELECT s.user_id,s.app_id,(s.last_seen/86400)*86400,0,MIN(s.last_seen),MAX(s.last_seen)
 FROM sessions s JOIN users u ON u.id=s.user_id GROUP BY s.user_id,s.app_id,(s.last_seen/86400)*86400
 ON CONFLICT(user_id,app_id,day) DO UPDATE SET
 first_seen=MIN(first_seen,excluded.first_seen),last_seen=MAX(last_seen,excluded.last_seen);
PRAGMA user_version=2;
`)
	if err != nil {
		return err
	}
	return tx.Commit()
}
