package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

// Daemonize restarts the current process in the background.
func Daemonize(logFile string) error {
	if os.Getenv("AI_ADAPTER_DAEMON") == "1" {
		// Child process: detach stdin from terminal.
		detachStdin()
		return nil
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	args := os.Args[1:]
	newArgs := make([]string, 0, len(args))
	for _, arg := range args {
		if arg != "-daemon" && arg != "--daemon" {
			newArgs = append(newArgs, arg)
		}
	}

	if runtime.GOOS == "windows" {
		return daemonizeWindows(execPath, newArgs, logFile)
	}
	return daemonizeUnix(execPath, newArgs, logFile)
}

// detachStdin replaces os.Stdin with /dev/null (or NUL on Windows)
// so the child process does not hold the parent terminal.
func detachStdin() {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return
	}
	os.Stdin = devNull
}

func daemonizeUnix(execPath string, args []string, logFile string) error {
	if logFile == "" {
		logFile = "/dev/null"
	}

	cmd := exec.Command(execPath, args...)
	cmd.Env = append(os.Environ(), "AI_ADAPTER_DAEMON=1")

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open /dev/null: %w", err)
	}
	defer devNull.Close()
	cmd.Stdin = devNull

	if logFile != "/dev/null" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		cmd.Stdout = f
		cmd.Stderr = f
	} else {
		cmd.Stdout = devNull
		cmd.Stderr = devNull
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}

	fmt.Printf("Daemon started with PID %d\n", cmd.Process.Pid)
	if logFile != "/dev/null" {
		fmt.Printf("Log file: %s\n", logFile)
	}

	os.Exit(0)
	return nil
}

func daemonizeWindows(execPath string, args []string, logFile string) error {
	cmd := exec.Command(execPath, args...)
	cmd.Env = append(os.Environ(), "AI_ADAPTER_DAEMON=1")

	// CREATE_NO_WINDOW: no console window, no inherited console handles.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
		HideWindow:    true,
	}

	// Detach stdio so the child does not hold the parent terminal.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open NUL: %w", err)
	}
	defer devNull.Close()
	cmd.Stdin = devNull
	cmd.Stderr = devNull

	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		cmd.Stdout = f
	} else {
		cmd.Stdout = devNull
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}

	fmt.Println("Daemon started in background")
	if logFile != "" {
		fmt.Printf("Log file: %s\n", logFile)
	}

	os.Exit(0)
	return nil
}

func GetDefaultLogFile() string {
	execPath, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(execPath), "ai-adapter.log")
}

func IsDaemon() bool {
	return os.Getenv("AI_ADAPTER_DAEMON") == "1"
}
