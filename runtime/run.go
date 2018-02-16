package runtime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"syscall"

	"github.com/kr/pty"
	"github.com/pkg/errors"

	"github.com/Southclaws/sampctl/print"
	"github.com/Southclaws/sampctl/types"
)

var (
	matchPreamble = regexp.MustCompile(`Loaded [0-9]{1,2} filterscripts\.`)
	matchMainEnd  = regexp.MustCompile(`Number of vehicle models\: [0-9]*`)
	matchTestEnd  = regexp.MustCompile(`\*\*\* Tests: (\d+), Fails: (\d+)`)
)

type testResults struct {
	Tests int
	Fails int
}

// Run handles the actual running of the server process - it collects log output too
func Run(ctx context.Context, cfg types.Runtime, cacheDir string) (err error) {
	if cfg.Container != nil {
		return RunContainer(cfg, cacheDir)
	}

	binary := "./" + getServerBinary(cfg.Platform)
	fullPath := filepath.Join(cfg.WorkingDir, binary)
	print.Verb("starting", binary, "in", cfg.WorkingDir)

	return run(ctx, fullPath, cfg.Mode)
}

func run(ctx context.Context, binary string, runType types.RunMode) (err error) {
	// termination is an internal instruction for communicating successful or failed runs.
	// It contains an error and a boolean to indicate whether or not to terminate the process.
	type termination struct {
		err  error
		exit bool
	}

	outputReader, outputWriter := io.Pipe()
	errChan := make(chan termination)  // channel for sending runtime errors to watchdog
	sigChan := make(chan os.Signal, 1) // channel for capturing host signals

	defer func() {
		errClose := outputWriter.Close()
		if errClose != nil {
			print.Erro("Compiler output read error:", errClose)
		}
	}()

	switch runType {
	case types.MainOnly:
		go func() {
			preamble := true
			preambleSpace := false
			scanner := bufio.NewScanner(outputReader)
			for scanner.Scan() {
				line := scanner.Text()

				if matchPreamble.MatchString(line) {
					preamble = false
					preambleSpace = true
					continue
				}
				if preambleSpace {
					preambleSpace = false
					continue
				}

				if matchMainEnd.MatchString(line) {
					errChan <- termination{nil, true}
					break
				}

				if !preamble {
					fmt.Println(line)
				}
			}
		}()
	case types.YTesting:
		go func() {
			preamble := true
			preambleSpace := false
			scanner := bufio.NewScanner(outputReader)
			for scanner.Scan() {
				line := scanner.Text()

				if matchPreamble.MatchString(line) {
					preamble = false
					preambleSpace = true
					continue
				}
				if preambleSpace {
					preambleSpace = false
					continue
				}

				if matchTestEnd.MatchString(line) {
					testResults := testResultsFromLine(line)
					if testResults.Fails > 0 {
						print.Erro(testResults.Tests, "tests, with:", testResults.Fails, "failures.")
						errChan <- termination{errors.New("tests failed"), true}
					} else {
						print.Info(testResults.Tests, "tests passed!")
						errChan <- termination{nil, true} // end the server process, no error
					}

					break
				}

				if !preamble {
					fmt.Println(line)
				}
			}
		}()
	default:
		go func() {
			scanner := bufio.NewScanner(outputReader)
			for scanner.Scan() {
				line := scanner.Text()
				fmt.Println(line)
			}
		}()
	}

	print.Verb("running with mode", runType)

	var cmd *exec.Cmd
	go func() {
		// on linux, must use unbuffer to disable stream buffering because samp03svr is weird...
		cmd = exec.CommandContext(ctx, binary)
		cmd.Dir = filepath.Dir(binary)

		if runtime.GOOS == "windows" {
			cmd.Stdout = os.Stdout
			errInline := cmd.Run()
			if errInline != nil {
				errChan <- termination{errInline, false}
				return
			}
		} else {
			ptmx, errInline := pty.Start(cmd)
			if errInline != nil {
				errChan <- termination{errInline, false}
				return
			}
			defer ptmx.Close()
			_, errInline = io.Copy(outputWriter, ptmx)
			if errInline != nil {
				errChan <- termination{errInline, false} // no error, no exit
			}
		}

		print.Verb("child exec thread finished, pid:", cmd.Process.Pid)
		errChan <- termination{} // no error, no exit
	}()

	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	var term termination
	select {
	case s := <-sigChan:
		err = errors.Errorf("received signal: %v", s)
	case term = <-errChan:
		break
	}
	print.Verb("finished server execution with:", term)

	err = errors.Wrap(term.err, "received runtime error")

	if term.exit {
		if cmd.Process != nil {
			killErr := cmd.Process.Signal(syscall.SIGINT)
			if killErr != nil {
				print.Erro("Failed to kill", killErr)
			}
			print.Verb("sent a SIGINT to child process")
		} else {
			print.Verb("not attempting to kill server: cmd.Process is nil")
		}
	}

	print.Verb("finished run() with", err)

	return err
}

func testResultsFromLine(line string) (results testResults) {
	match := matchTestEnd.FindStringSubmatch(line)
	results.Tests, _ = strconv.Atoi(match[1])
	results.Fails, _ = strconv.Atoi(match[2])
	return
}
