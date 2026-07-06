//go:build !windows && !darwin

package mdmconfig

// No MDM store on this platform; configuration comes from flags/env.
func platformSource() Source { return mapSource{} }
