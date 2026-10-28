//go:build darwin

package eyes

func defaultOS() OS {
	return Remote{}
}
