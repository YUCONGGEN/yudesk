//go:build !windows

package viewerapp

// Native menu-bar/status-item implementations are platform-specific.
type appTray struct{}

func newAppTray(show, exit func()) (*appTray, error) { return &appTray{}, nil }
func (t *appTray) Visible(bool)                      {}
func (t *appTray) Close()                            {}
