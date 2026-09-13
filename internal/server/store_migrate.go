package server

import "fmt"

// Additive migrations keep existing installations and their history readable.
func (s *Store) migrateFeatures() error {
	columns := map[string]map[string]string{
		"servers": {
			"last_raw_in":  "INTEGER NOT NULL DEFAULT 0",
			"last_raw_out": "INTEGER NOT NULL DEFAULT 0",
			"has_raw":      "INTEGER NOT NULL DEFAULT 0",
		},
		"samples": {
			"mem_total":     "INTEGER NOT NULL DEFAULT 0",
			"swap_total":    "INTEGER NOT NULL DEFAULT 0",
			"disk_total":    "INTEGER NOT NULL DEFAULT 0",
			"net_in_speed":  "REAL",
			"net_out_speed": "REAL",
		},
	}
	for table, additions := range columns {
		rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			return err
		}
		present := map[string]bool{}
		for rows.Next() {
			var cid, notNull, pk int
			var name, typ string
			var def any
			if err := rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
				rows.Close()
				return err
			}
			present[name] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for name, definition := range additions {
			if !present[name] {
				if _, err := s.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + name + " " + definition); err != nil {
					return fmt.Errorf("migrate %s.%s: %w", table, name, err)
				}
			}
		}
	}
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS agent_credentials (
 uuid TEXT PRIMARY KEY, token_hash TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS monitors (
 id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 type TEXT NOT NULL, target TEXT NOT NULL, interval_seconds INTEGER NOT NULL, timeout_ms INTEGER NOT NULL,
 enabled INTEGER NOT NULL, revision INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS monitor_results (
 id INTEGER PRIMARY KEY AUTOINCREMENT, monitor_id INTEGER NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
 at INTEGER NOT NULL, status TEXT NOT NULL, latency_ms REAL NOT NULL, loss REAL NOT NULL,
 status_code INTEGER NOT NULL, error TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_monitor_results ON monitor_results(monitor_id, at, id);
CREATE TABLE IF NOT EXISTS notification_channels (
 id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, type TEXT NOT NULL,
 url TEXT NOT NULL DEFAULT '', token TEXT NOT NULL DEFAULT '', chat_id TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS alert_rules (
 id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, server_id INTEGER REFERENCES servers(id) ON DELETE CASCADE,
 monitor_id INTEGER REFERENCES monitors(id) ON DELETE CASCADE, metric TEXT NOT NULL,
 threshold REAL NOT NULL, duration_seconds INTEGER NOT NULL, channel_id INTEGER NOT NULL REFERENCES notification_channels(id),
 enabled INTEGER NOT NULL, revision INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS alert_states (
 rule_id INTEGER PRIMARY KEY REFERENCES alert_rules(id) ON DELETE CASCADE,
 since INTEGER NOT NULL DEFAULT 0, active INTEGER NOT NULL DEFAULT 0, last_eval INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS alert_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT, rule_id INTEGER REFERENCES alert_rules(id) ON DELETE SET NULL,
 at INTEGER NOT NULL, state TEXT NOT NULL, message TEXT NOT NULL,
 channel_id INTEGER REFERENCES notification_channels(id) ON DELETE SET NULL,
 delivery TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt INTEGER NOT NULL DEFAULT 0, delivery_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_alert_events_pending ON alert_events(delivery, next_attempt);
CREATE INDEX IF NOT EXISTS idx_alert_events_at ON alert_events(at);
`)
	return err
}
