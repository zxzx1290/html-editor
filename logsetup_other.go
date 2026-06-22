//go:build !linux && !darwin

package main

import "errors"

// initLogging 在非 Linux/macOS（無 log/syslog）一律維持 stdout 輸出。
// 若指定 syslog 模式，回傳錯誤由呼叫端記錄 fallback。
func initLogging(mode, _ string) error {
	logMode = mode
	if mode == logModeSyslog {
		return errors.New("syslog not supported on this OS, fallback to stdout")
	}
	return nil
}
