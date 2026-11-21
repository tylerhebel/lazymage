//go:build !unix

package update

import "errors"

func Restart(exe string) error {
	return errors.New("restart is not supported on this platform; start lazymage again")
}
