package creators

// VolumeOwnership is how much of a ComicVine volume the library holds, and
// which local series holds it (the series with the most of that volume's
// issues, when more than one series matches issues from the same volume).
type VolumeOwnership struct {
	OwnedIssues int
	SeriesID    string
}

// OwnedState classifies ownership of a volume given how many of its issues
// are owned and how many issues ComicVine reports for it. totalIssues == 0
// means the total is unknown (not that the volume has zero issues); with
// ownedIssues > 0 that still reads as "owned" rather than "partial".
func OwnedState(ownedIssues, totalIssues int) string {
	if ownedIssues <= 0 {
		return "none"
	}
	if totalIssues > 0 && ownedIssues < totalIssues {
		return "partial"
	}
	return "owned"
}

// ownedIssueChunk caps the IN-clause size per query, matching the chunking
// used elsewhere against this same localcv schema (see
// match/localcv.ArcCredits).
const ownedIssueChunk = 500

// Ownership maps owned ComicVine issue IDs (as returned by
// store.OwnedCVIssues: CV issue ID -> owning series ID) to per-volume
// ownership counts, keyed by ComicVine volume ID. Volumes with nothing owned
// have no entry. When issues of one volume are owned by more than one local
// series (e.g. a renamed or split series), SeriesID is the series holding
// the most of that volume's owned issues.
func (x *Index) Ownership(owned map[int64]string) (map[int64]VolumeOwnership, error) {
	if len(owned) == 0 {
		return map[int64]VolumeOwnership{}, nil
	}

	issueIDs := make([]int64, 0, len(owned))
	for id := range owned {
		issueIDs = append(issueIDs, id)
	}

	// volumeID -> seriesID -> count of owned issues from that series.
	counts := make(map[int64]map[string]int)
	for len(issueIDs) > 0 {
		n := len(issueIDs)
		if n > ownedIssueChunk {
			n = ownedIssueChunk
		}
		chunk := issueIDs[:n]
		issueIDs = issueIDs[n:]

		args := make([]interface{}, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := x.db.Query(
			`SELECT id, volume_id FROM cv.cv_issue WHERE id IN (`+placeholders(len(chunk))+`)`,
			args...)
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var issueID, volumeID int64
				if err := rows.Scan(&issueID, &volumeID); err != nil {
					return err
				}
				seriesID := owned[issueID]
				bySeries, ok := counts[volumeID]
				if !ok {
					bySeries = make(map[string]int)
					counts[volumeID] = bySeries
				}
				bySeries[seriesID]++
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, err
		}
	}

	out := make(map[int64]VolumeOwnership, len(counts))
	for volumeID, bySeries := range counts {
		var best string
		var bestCount, total int
		for seriesID, n := range bySeries {
			total += n
			if n > bestCount {
				bestCount, best = n, seriesID
			}
		}
		out[volumeID] = VolumeOwnership{OwnedIssues: total, SeriesID: best}
	}
	return out, nil
}

// placeholders builds a "?,?,?" string for n bound parameters.
func placeholders(n int) string {
	b := make([]byte, 0, n*2)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(b)
}
