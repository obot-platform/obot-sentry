package mdmconfig

import (
	"strconv"

	"golang.org/x/sys/windows/registry"
)

// registryKeyPath is where the MSI's registry component writes the
// deployment values (see build/windows/obocop.wxs).
const registryKeyPath = `SOFTWARE\Obot\Obocop`

func platformSource() Source { return registrySource{} }

type registrySource struct{}

// Read pulls the deployment values from HKLM. A missing key or missing
// values are not errors — the machine simply isn't configured.
func (registrySource) Read() (map[string]string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, registryKeyPath, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return map[string]string{}, nil
	}
	defer k.Close()

	out := map[string]string{}
	for _, name := range []string{KeyServerURL, KeyEnrollmentKey, KeyScanIntervalMinutes} {
		// The MSI writes REG_SZ, but MDM custom registry policies often
		// push numbers as REG_DWORD — accept both.
		if v, _, err := k.GetStringValue(name); err == nil {
			out[name] = v
		} else if n, _, err := k.GetIntegerValue(name); err == nil {
			out[name] = strconv.FormatUint(n, 10)
		}
	}
	return out, nil
}
