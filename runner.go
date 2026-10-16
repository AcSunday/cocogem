package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

type eventParser func([]byte, io.Writer) (string, error)

func runCLI(name string, cmd *exec.Cmd, parse eventParser, output io.Writer) (Result, error) {
	result := Result{CLI: name}
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return result, fmt.Errorf("open %s stdout: %w", name, err)
	}
	if err := cmd.Start(); err != nil {
		return result, fmt.Errorf("start %s: %w", name, err)
	}

	sessionID, streamErr := scanEvents(name, stdout, output, parse)
	result.SessionID = sessionID
	if streamErr != nil {
		// 读取提前失败时，先停止子进程，再通过 Wait 回收它。
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			streamErr = errors.Join(streamErr, fmt.Errorf("stop %s: %w", name, err))
		}
	}
	waitErr := cmd.Wait()
	if streamErr != nil {
		return result, streamErr
	}
	if waitErr != nil {
		return result, fmt.Errorf("%s exited: %w", name, waitErr)
	}
	if err := validateSessionID(sessionID); err != nil {
		return result, fmt.Errorf("%s did not return a valid session ID: %w", name, err)
	}
	return result, nil
}

func scanEvents(name string, r io.Reader, output io.Writer, parse eventParser) (string, error) {
	var sessionID string
	scanner := bufio.NewScanner(r)
	// shortcut: 单行 JSON 上限为 4 MiB，处理更大的事件时再调整。
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		id, err := parse(line, output)
		if err != nil {
			return sessionID, fmt.Errorf("parse %s event: %w", name, err)
		}
		if id != "" {
			sessionID = id
		}
	}
	if err := scanner.Err(); err != nil {
		return sessionID, fmt.Errorf("read %s output: %w", name, err)
	}
	return sessionID, nil
}
