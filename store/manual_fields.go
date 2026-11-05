package store

import (
	"database/sql"
	"encoding/json"
)

// keepManual returns the ON CONFLICT expression for col: the stored value when
// the user hand-edited it (listed in manual_fields), otherwise the new one.
func keepManual(table, col string) string {
	return `CASE WHEN ` + table + `.manual_fields LIKE '%"` + col + `"%' THEN ` +
		table + `.` + col + ` ELSE excluded.` + col + ` END`
}

// markManual adds cols to the row's manual_fields set. The row must exist.
func markManual(tx *sql.Tx, table, keyCol, id string, cols ...string) error {
	if len(cols) == 0 {
		return nil
	}
	var raw string
	if err := tx.QueryRow(`SELECT manual_fields FROM `+table+` WHERE `+keyCol+` = ?`, id).Scan(&raw); err != nil {
		return err
	}
	var set []string
	_ = json.Unmarshal([]byte(raw), &set) // bad JSON -> start fresh
	for _, c := range cols {
		found := false
		for _, have := range set {
			if have == c {
				found = true
				break
			}
		}
		if !found {
			set = append(set, c)
		}
	}
	out, err := json.Marshal(set)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE `+table+` SET manual_fields = ? WHERE `+keyCol+` = ?`, string(out), id)
	return err
}
