package main

import (
	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/viewerapp"
	"github.com/yudesk/yudesk/internal/winhost"
)

func main() {
	if winhost.HandleCommand(desktop.PrivilegedOperation) {
		return
	}
	viewerapp.UnifiedMain()
}
