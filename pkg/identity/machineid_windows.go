package identity

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// machineID reads the stable MachineGuid Windows generates at install
// time. WOW64_64KEY makes the read view-independent for a 32-bit build
// on 64-bit Windows.
func machineID() (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", err
	}
	defer k.Close()

	v, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(v), nil
}
