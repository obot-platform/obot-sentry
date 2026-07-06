package cmd

import (
	"fmt"

	"github.com/obot-platform/obocop/pkg/mdmconfig"
)

// ConfigFlags are the deployment-config overrides shared by commands
// that talk to the server. Values resolve flags/env first (handled by
// the command framework), then the platform MDM store.
type ConfigFlags struct {
	ServerURL     string `usage:"Obot server base URL (overrides the MDM-configured value)" env:"OBOCOP_SERVER_URL"`
	EnrollmentKey string `usage:"Device enrollment credential (ode1-...), used when the device is not yet enrolled" env:"OBOCOP_ENROLLMENT_KEY"`
	Username      string `usage:"Override the username reported in scan manifests" env:"OBOCOP_USERNAME"`
	DeviceName    string `usage:"Override the hostname reported to the server" env:"OBOCOP_DEVICE_NAME"`
}

// resolve layers the flag/env values over the MDM store.
func (f ConfigFlags) resolve() (mdmconfig.Config, error) {
	mdm, err := mdmconfig.Load()
	if err != nil {
		return mdmconfig.Config{}, fmt.Errorf("reading MDM configuration: %w", err)
	}
	return mdmconfig.Config{
		ServerURL:     f.ServerURL,
		EnrollmentKey: f.EnrollmentKey,
		Username:      f.Username,
		DeviceName:    f.DeviceName,
	}.Merge(mdm), nil
}

// ExitCodeError carries a specific process exit code so MDM scripts can
// distinguish configuration problems (exit 2) from runtime failures
// (exit 1).
type ExitCodeError struct {
	Code int
	Err  error
}

func (e *ExitCodeError) Error() string { return e.Err.Error() }
func (e *ExitCodeError) Unwrap() error { return e.Err }

// NewConfigError wraps err as a configuration error (exit code 2).
func NewConfigError(err error) error {
	return &ExitCodeError{Code: 2, Err: err}
}
