// Package match turns comic files into confirmed upstream metadata matches.
package match

import (
	"fmt"
	"strconv"

	"github.com/rthaines211/gutter-server/match/localcv"
	"github.com/rthaines211/gutter-server/match/parse"
	"github.com/rthaines211/gutter-server/store"
)

// yearSlack is how far the folder year may sit from the volume's start year
// before it counts as a disagreement. Cover dates and copyright years diverge
// routinely near a year boundary.
const yearSlack = 1

// Verdict is the outcome of checking a resolved upstream issue against what
// the local folder and filename say.
type Verdict struct {
	State     string
	Penalties map[string]string
	// Format is the collection format parsed from the filename ("tpb", "hc",
	// "omnibus"), empty for single issues. Carried so the runner can write
	// it onto the issue row.
	Format string
}

// Verify compares a resolved ComicVine issue against the local names.
//
// An MD5 hit is an exact-bytes match, so this is a guard against a mis-tagged
// scene release rather than a scoring pass: any disagreement demotes the match
// to review instead of rejecting it.
func Verify(folder parse.Folder, file parse.File, iss *localcv.Issue) Verdict {
	pen := map[string]string{}

	localSeries := file.Series
	if localSeries == "" {
		localSeries = folder.Series
	}
	if localSeries != "" && iss.VolumeName != "" {
		if parse.CanonicalKey(localSeries) != parse.CanonicalKey(iss.VolumeName) &&
			parse.CanonicalKey(folder.Series) != parse.CanonicalKey(iss.VolumeName) {
			pen["series"] = fmt.Sprintf("local %q vs ComicVine %q", localSeries, iss.VolumeName)
		}
	}

	// A collection's local number is its volume number. Against a one-issue
	// ComicVine volume (how the dump files a standalone trade) the upstream
	// number is always "1" and says nothing, so it is not compared.
	oneShot := file.IsCollection && iss.VolumeIssueCount > 0 && iss.VolumeIssueCount <= 1
	if !oneShot && file.Number != "" && iss.IssueNumber != "" {
		if parse.NormalizeNumber(file.Number) != parse.NormalizeNumber(iss.IssueNumber) {
			pen["number"] = fmt.Sprintf("local #%s vs ComicVine #%s", file.Number, iss.IssueNumber)
		}
	}

	// Absence is not a conflict: a missing year on either side is unknown, and
	// unknown must score better than wrong.
	if folder.Year != 0 && iss.VolumeStartYear != "" {
		if upstream, err := strconv.Atoi(iss.VolumeStartYear); err == nil && upstream != 0 {
			if diff := folder.Year - upstream; diff > yearSlack || diff < -yearSlack {
				pen["year"] = fmt.Sprintf("local %d vs volume start %d", folder.Year, upstream)
			}
		}
	}

	if len(pen) > 0 {
		return Verdict{State: store.MatchStateAmbiguous, Penalties: pen, Format: file.Format}
	}
	return Verdict{State: store.MatchStateHighConfidence, Penalties: pen, Format: file.Format}
}
