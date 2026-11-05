package store

import (
	"encoding/json"
	"time"
)

// PipelineEvent is one thing the auto pipeline did to one series.
type PipelineEvent struct {
	SeriesID   string `json:"series_id"`
	SeriesName string `json:"series_name"`
	Kind       string `json:"kind"` // new_issues|filled|unbound|misfiled|auto_confirmed|sent_to_review|organized|flagged|error
	Detail     string `json:"detail"`
}

// PipelineRun is one scheduled pass of the auto pipeline.
type PipelineRun struct {
	ID            int64
	StartedAt     int64
	FinishedAt    int64
	SeriesCount   int
	SkippedReason string
	Events        []PipelineEvent
}

func (s *Store) AddPipelinePending(seriesID string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO pipeline_pending(series_id, added_at) VALUES(?, ?)`,
		seriesID, time.Now().UnixMilli())
	return err
}

func (s *Store) ListPipelinePending() ([]string, error) {
	rows, err := s.db.Query(`SELECT series_id FROM pipeline_pending ORDER BY added_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) RemovePipelinePending(seriesID string) error {
	_, err := s.db.Exec(`DELETE FROM pipeline_pending WHERE series_id = ?`, seriesID)
	return err
}

func (s *Store) InsertPipelineRun(r PipelineRun) error {
	if r.Events == nil {
		r.Events = []PipelineEvent{}
	}
	ev, err := json.Marshal(r.Events)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO pipeline_runs(started_at, finished_at, series_count, skipped_reason, events)
		VALUES(?, ?, ?, ?, ?)`, r.StartedAt, r.FinishedAt, r.SeriesCount, r.SkippedReason, string(ev))
	return err
}

func (s *Store) ListPipelineRuns(limit int) ([]PipelineRun, error) {
	rows, err := s.db.Query(`SELECT id, started_at, finished_at, series_count, skipped_reason, events
		FROM pipeline_runs ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PipelineRun
	for rows.Next() {
		var r PipelineRun
		var ev string
		if err := rows.Scan(&r.ID, &r.StartedAt, &r.FinishedAt, &r.SeriesCount, &r.SkippedReason, &ev); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ev), &r.Events) // bad JSON -> no events, never fatal
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) PrunePipelineRuns(olderThanMs int64) error {
	_, err := s.db.Exec(`DELETE FROM pipeline_runs WHERE started_at < ?`, olderThanMs)
	return err
}

// NewIssues is one series' issues never announced before.
type NewIssues struct {
	SeriesID, SeriesName string
	Count                int
}

// ClaimNewIssues returns the issues no "New issues ready" push has covered
// yet, grouped by series, and marks them announced in the same transaction,
// so each comic id is news exactly once.
func (s *Store) ClaimNewIssues() ([]NewIssues, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`
		SELECT i.series_id, s.name, COUNT(*)
		  FROM issues i JOIN series s ON s.id = i.series_id
		 WHERE NOT EXISTS (SELECT 1 FROM announced_issues a WHERE a.comic_id = i.id)
		 GROUP BY i.series_id ORDER BY MIN(i.rowid)`)
	if err != nil {
		return nil, err
	}
	var out []NewIssues
	for rows.Next() {
		var n NewIssues
		if err := rows.Scan(&n.SeriesID, &n.SeriesName, &n.Count); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO announced_issues(comic_id) SELECT id FROM issues`); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
