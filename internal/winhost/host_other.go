//go:build !windows

package winhost

import "errors"

func HandleCommand(Handler) bool { return false }
func Status() State              { return State{Message: "锁屏服务仅支持 Windows"} }
func ElevateInstall(bool) error  { return errors.New("仅支持 Windows") }
func RestartInstalled() error    { return errors.New("仅支持 Windows") }
func IsWorker() bool             { return false }
