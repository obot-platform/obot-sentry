package mdmconfig

import (
	"fmt"
	"os"
	"strconv"

	"howett.net/plist"
)

// Managed-preference locations for the com.obot.obocop payload domain,
// most-authoritative first. MDM-delivered profiles land under
// /Library/Managed Preferences as binary plists; howett.net/plist
// parses binary and XML natively so no `defaults` subprocess is needed.
var plistPaths = []string{
	"/Library/Managed Preferences/com.obot.obocop.plist",
	"/Library/Preferences/com.obot.obocop.plist",
}

func platformSource() Source { return plistSource{paths: plistPaths} }

type plistSource struct {
	paths []string
}

// Read parses the first present plist. Missing files are not errors;
// the machine simply isn't configured.
func (s plistSource) Read() (map[string]string, error) {
	for _, p := range s.paths {
		b, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}

		var raw map[string]any
		if _, err := plist.Unmarshal(b, &raw); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", p, err)
		}
		out := map[string]string{}
		for k, v := range raw {
			// Profiles carry strings, but numeric payload values (e.g.
			// ScanIntervalMinutes as <integer>) decode as ints — keep both.
			switch value := v.(type) {
			case string:
				out[k] = value
			case int64:
				out[k] = strconv.FormatInt(value, 10)
			case uint64:
				out[k] = strconv.FormatUint(value, 10)
			}
		}
		return out, nil
	}
	return map[string]string{}, nil
}
