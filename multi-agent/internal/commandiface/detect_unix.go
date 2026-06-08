//go:build !windows

package commandiface

func defaultWSLHasDistro() bool {
	return false
}
