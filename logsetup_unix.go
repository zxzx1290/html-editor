//go:build linux || darwin

package main

import "log/syslog"

// initLogging 依 mode 設定 logf 的輸出 sink。
//   - logModeSyslog：改寫到本機 syslog（tag 由 config logTag 指定，留空則預設 "html-editor"），
//     失敗則回傳錯誤由呼叫端 fallback。
//   - 其餘（含空字串 / logModeFmt）：維持預設 stdout 行為。
func initLogging(mode, tag string) error {
	logMode = mode
	if mode == logModeSyslog {
		if tag == "" {
			tag = "html-editor"
		}
		w, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, tag)
		if err != nil {
			return err
		}
		logWriter = w
		return nil
	}
	return nil
}
