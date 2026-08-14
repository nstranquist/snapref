package browser

import "context"

// requestBrowserClose asks Chromium to shut down through CDP. Windows has no
// Unix-style SIGTERM equivalent, so this is its graceful lifecycle path before
// a bounded TerminateProcess fallback.
func requestBrowserClose(ctx context.Context, session *Session) error {
	conn, err := dialFirstPage(ctx, session)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = cdpCall(ctx, conn, 1, "Browser.close", nil)
	return err
}
