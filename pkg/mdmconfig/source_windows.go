package mdmconfig

import (
	"golang.org/x/sys/windows/registry"
)

// registryKeyPath is where the MSI's registry component writes the
// deployment values (see packaging/windows/obocop.wxs and
// packaging/CONTRACT.md).
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
	for _, name := range []string{KeyServerURL, KeyEnrollmentKey, KeyUsername, KeyDeviceName} {
		if v, _, err := k.GetStringValue(name); err == nil {
			out[name] = v
		}
	}
	return out, nil
}
