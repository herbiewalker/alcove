//go:build !linux

package store

// isTmpfsBacked has no meaningful implementation off Linux: Windows and macOS expose no
// comparable "this mount is RAM-backed" flag. On those platforms the caller must set
// Config.AllowNonTmpfsTmp explicitly (a dev box); the opt-in is what governs whether Open proceeds.
func isTmpfsBacked(path string) (bool, error) { return false, nil }
