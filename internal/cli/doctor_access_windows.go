//go:build windows

package cli

import "golang.org/x/sys/windows"

func doctorCanSearch(path string) error {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		path16,
		windows.FILE_TRAVERSE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return err
	}
	return windows.CloseHandle(handle)
}

func doctorSplitPlatformCommand(input string) ([]string, error) {
	return windows.DecomposeCommandLine(input)
}

func doctorShellQuote(value string) string {
	return windows.EscapeArg(value)
}
