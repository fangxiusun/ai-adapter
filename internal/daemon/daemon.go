package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Daemonize restarts the current process in the background.
func Daemonize(logFile string) error {
	if os.Getenv("AI_ADAPTER_DAEMON") == "1" {
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

func daemonizeUnix(execPath string, args []string, logFile string) error {
	if logFile == "" {
		logFile = "/dev/null"
	}

	cmd := exec.Command(execPath, args...)
	cmd.Env = append(os.Environ(), "AI_ADAPTER_DAEMON=1")

	if logFile != "/dev/null" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		cmd.Stdout = f
		cmd.Stderr = f
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
	var psScript string

	// Build argument list portion
	argsPart := ""
	if len(args) > 0 {
		psArgs := make([]string, 0, len(args))
		for _, arg := range args {
			psArgs = append(psArgs, fmt.Sprintf("'%s'", arg))
		}
		argsPart = fmt.Sprintf(" -ArgumentList @(%s)", strings.Join(psArgs, ", "))
	}

	// Start-Process does not allow RedirectStandardOutput and
	// RedirectStandardError to point to the same file, so we
	// only redirect stdout; stderr inherits the hidden window
	// (effectively discarded) which is acceptable for a daemon.
	if logFile != "" {
		psScript = fmt.Sprintf("Start-Process -FilePath '%s'%s -WindowStyle Hidden -RedirectStandardOutput '%s'", execPath, argsPart, logFile)
	} else {
		psScript = fmt.Sprintf("Start-Process -FilePath '%s'%s -WindowStyle Hidden", execPath, argsPart)
	}

	cmd := exec.Command("powershell", "-Command", psScript)
	cmd.Env = append(os.Environ(), "AI_ADAPTER_DAEMON=1")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("start daemon: %w, output: %s", err, string(output))
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