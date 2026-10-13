// Native launcher: Go's PKG_CONFIG accepts one executable, not shell arguments.
// Windows cannot execute a shell script directly. Keep the policy in the shared
// shell adapter and run it through Bash on every supported platform.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(self)))
	script := filepath.ToSlash(filepath.Join(root, "scripts", "ffmpeg-pkg-config.sh"))
	cmd := exec.Command("bash", append([]string{script}, os.Args[1:]...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
