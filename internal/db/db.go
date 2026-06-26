package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	conn *sql.DB
	mu   sync.RWMutex
}

func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	conn, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

func (db *DB) Close() {
	if db.conn != nil {
		db.conn.Close()
	}
}

func (db *DB) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS request_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			request_id TEXT NOT NULL,
			timestamp INTEGER NOT NULL,
			channel_id TEXT NOT NULL,
			client_api TEXT NOT NULL,
			upstream_api TEXT NOT NULL,
			model TEXT NOT NULL,
			status_code INTEGER NOT NULL,
			latency_ms INTEGER NOT NULL,
			key_name TEXT,
			error_code TEXT,
			error_message TEXT,
			prompt_tokens INTEGER,
			completion_tokens INTEGER,
			total_tokens INTEGER,
			is_stream INTEGER NOT NULL DEFAULT 0,
			request_body TEXT,
			response_body TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_timestamp ON request_logs(timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_channel ON request_logs(channel_id, timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_status ON request_logs(status_code, timestamp DESC)`,
		`DROP TABLE IF EXISTS key_stats`,
		`CREATE TABLE IF NOT EXISTS key_stats (
			channel_id TEXT NOT NULL,
			key_name TEXT NOT NULL,
			key_value TEXT NOT NULL,
			request_count INTEGER NOT NULL DEFAULT 0,
			error_count INTEGER NOT NULL DEFAULT 0,
			error_400 INTEGER NOT NULL DEFAULT 0,
			error_401 INTEGER NOT NULL DEFAULT 0,
			error_403 INTEGER NOT NULL DEFAULT 0,
			error_404 INTEGER NOT NULL DEFAULT 0,
			error_429 INTEGER NOT NULL DEFAULT 0,
			error_4xx INTEGER NOT NULL DEFAULT 0,
			error_5xx INTEGER NOT NULL DEFAULT 0,
			error_network INTEGER NOT NULL DEFAULT 0,
			error_stream INTEGER NOT NULL DEFAULT 0,
			total_latency_ms INTEGER NOT NULL DEFAULT 0,
			last_error TEXT,
			last_error_time INTEGER,
			last_success_time INTEGER,
			paused INTEGER NOT NULL DEFAULT 0,
			pause_until INTEGER,
			PRIMARY KEY (channel_id, key_value)
		)`,
	}

	for _, m := range migrations {
		if _, err := db.conn.Exec(m); err != nil {
			return fmt.Errorf("exec migration: %w", err)
		}
	}
	return nil
}

func (db *DB) InsertLog(reqID, channelID, clientModel, upstreamModel string, status int, latencyMs int64, key, errorCode, errorMsg string) {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(
		`INSERT INTO request_logs (request_id, timestamp, channel_id, client_api, upstream_api, model, status_code, latency_ms, key_name, error_code, error_message)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		reqID, time.Now().UnixMilli(), channelID, "responses", "chat",
		upstreamModel, status, latencyMs, maskKey(key), errorCode, errorMsg,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to insert log: %v\n", err)
	}
}

type LogEntry struct {
	ID           int64  `json:"id"`
	RequestID    string `json:"request_id"`
	Timestamp    int64  `json:"timestamp"`
	ChannelID    string `json:"channel_id"`
	Model        string `json:"model"`
	StatusCode   int    `json:"status_code"`
	LatencyMs    int64  `json:"latency_ms"`
	KeyName      string `json:"key_name"`
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

func (db *DB) QueryLogs(channelID string, statusMin, statusMax int, from, to int64, limit, offset int) ([]LogEntry, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := "SELECT id, request_id, timestamp, channel_id, model, status_code, latency_ms, key_name, error_code, error_message FROM request_logs WHERE 1=1"
	args := []interface{}{}

	if channelID != "" {
		query += " AND channel_id = ?"
		args = append(args, channelID)
	}
	if statusMin > 0 {
		query += " AND status_code >= ?"
		args = append(args, statusMin)
	}
	if statusMax > 0 {
		query += " AND status_code <= ?"
		args = append(args, statusMax)
	}
	if from > 0 {
		query += " AND timestamp >= ?"
		args = append(args, from)
	}
	if to > 0 {
		query += " AND timestamp <= ?"
		args = append(args, to)
	}

	query += " ORDER BY timestamp DESC"
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	if offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", offset)
	}

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []LogEntry
	for rows.Next() {
		var l LogEntry
		if err := rows.Scan(&l.ID, &l.RequestID, &l.Timestamp, &l.ChannelID, &l.Model, &l.StatusCode, &l.LatencyMs, &l.KeyName, &l.ErrorCode, &l.ErrorMessage); err != nil {
			continue
		}
		logs = append(logs, l)
	}
	return logs, nil
}

func (db *DB) DeleteLogsBefore(timestamp int64) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	result, err := db.conn.Exec("DELETE FROM request_logs WHERE timestamp < ?", timestamp)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (db *DB) GetLogCount() (int64, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var count int64
	err := db.conn.QueryRow("SELECT COUNT(*) FROM request_logs").Scan(&count)
	return count, err
}

func (db *DB) Vacuum() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec("VACUUM")
	return err
}

type KeyStatsRow struct {
	ChannelID      string
	KeyName        string
	KeyValue       string
	RequestCount   int64
	ErrorCount     int64
	Error400       int64
	Error401       int64
	Error403       int64
	Error404       int64
	Error429       int64
	Error4xx       int64
	Error5xx       int64
	ErrorNetwork   int64
	ErrorStream    int64
	TotalLatencyMs int64
	LastError      string
	LastErrorTime  int64
	LastSuccessTime int64
	Paused         bool
	PauseUntil     int64
}

func (db *DB) LoadKeyStats(channelID string) ([]KeyStatsRow, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	rows, err := db.conn.Query(
		`SELECT channel_id, key_name, key_value, request_count, error_count,
		        error_400, error_401, error_403, error_404, error_429, error_4xx, error_5xx, error_network, error_stream,
		        total_latency_ms,
		        last_error, last_error_time, last_success_time, paused, pause_until
		 FROM key_stats WHERE channel_id = ?`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []KeyStatsRow
	for rows.Next() {
		var r KeyStatsRow
		if err := rows.Scan(&r.ChannelID, &r.KeyName, &r.KeyValue, &r.RequestCount, &r.ErrorCount,
			&r.Error400, &r.Error401, &r.Error403, &r.Error404, &r.Error429, &r.Error4xx, &r.Error5xx, &r.ErrorNetwork, &r.ErrorStream,
			&r.TotalLatencyMs, &r.LastError, &r.LastErrorTime, &r.LastSuccessTime, &r.Paused, &r.PauseUntil); err != nil {
			continue
		}
		result = append(result, r)
	}
	return result, nil
}

func (db *DB) UpsertKeyStats(row KeyStatsRow) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(
		`INSERT INTO key_stats (channel_id, key_name, key_value, request_count, error_count,
		        error_400, error_401, error_403, error_404, error_429, error_4xx, error_5xx, error_network, error_stream,
		        total_latency_ms, last_error, last_error_time, last_success_time, paused, pause_until)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(channel_id, key_value) DO UPDATE SET
		        key_name = excluded.key_name,
		        request_count = excluded.request_count,
		        error_count = excluded.error_count,
		        error_400 = excluded.error_400,
		        error_401 = excluded.error_401,
		        error_403 = excluded.error_403,
		        error_404 = excluded.error_404,
		        error_429 = excluded.error_429,
		        error_4xx = excluded.error_4xx,
		        error_5xx = excluded.error_5xx,
		        error_network = excluded.error_network,
		        error_stream = excluded.error_stream,
		        total_latency_ms = excluded.total_latency_ms,
		        last_error = excluded.last_error,
		        last_error_time = excluded.last_error_time,
		        last_success_time = excluded.last_success_time,
		        paused = excluded.paused,
		        pause_until = excluded.pause_until`,
		row.ChannelID, row.KeyName, row.KeyValue, row.RequestCount, row.ErrorCount,
		row.Error400, row.Error401, row.Error403, row.Error404, row.Error429, row.Error4xx, row.Error5xx, row.ErrorNetwork, row.ErrorStream,
		row.TotalLatencyMs, row.LastError, row.LastErrorTime, row.LastSuccessTime, row.Paused, row.PauseUntil)
	return err
}

func (db *DB) SaveKeyStatsBatch(rows []KeyStatsRow) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(
		`INSERT INTO key_stats (channel_id, key_name, key_value, request_count, error_count,
		        error_400, error_401, error_403, error_404, error_429, error_4xx, error_5xx, error_network, error_stream,
		        total_latency_ms, last_error, last_error_time, last_success_time, paused, pause_until)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(channel_id, key_value) DO UPDATE SET
		        key_name = excluded.key_name,
		        request_count = excluded.request_count,
		        error_count = excluded.error_count,
		        error_400 = excluded.error_400,
		        error_401 = excluded.error_401,
		        error_403 = excluded.error_403,
		        error_404 = excluded.error_404,
		        error_429 = excluded.error_429,
		        error_4xx = excluded.error_4xx,
		        error_5xx = excluded.error_5xx,
		        error_network = excluded.error_network,
		        error_stream = excluded.error_stream,
		        total_latency_ms = excluded.total_latency_ms,
		        last_error = excluded.last_error,
		        last_error_time = excluded.last_error_time,
		        last_success_time = excluded.last_success_time,
		        paused = excluded.paused,
		        pause_until = excluded.pause_until`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range rows {
		if _, err := stmt.Exec(r.ChannelID, r.KeyName, r.KeyValue, r.RequestCount, r.ErrorCount,
			r.Error400, r.Error401, r.Error403, r.Error404, r.Error429, r.Error4xx, r.Error5xx, r.ErrorNetwork, r.ErrorStream,
			r.TotalLatencyMs, r.LastError, r.LastErrorTime, r.LastSuccessTime, r.Paused, r.PauseUntil); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "***" + key[len(key)-4:]
}
