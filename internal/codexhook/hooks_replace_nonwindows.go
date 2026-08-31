//go:build !windows

package codexhook

import "os"

func replaceHooksFile(replacementPath, destinationPath string) error {
	return os.Rename(replacementPath, destinationPath)
}
