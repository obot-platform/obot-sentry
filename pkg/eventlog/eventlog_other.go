//go:build !windows

package eventlog

func write(level, uint32, string) {}
