package pipeline

import "testing"

func TestCleanLog(t *testing.T) {
	raw := "" +
		"2026-11-24T06:40:30.000000Z 00O section_start:1795502430:step_script\r\x1b[0K\x1b[0K\x1b[36;1mExecuting step_script\x1b[0;m\n" +
		"2026-11-24T06:40:31.000000Z 01O $ make test\n" +
		"2026-11-24T06:40:32.000000Z 01O downloading 10%\rdownloading 100%\n" +
		"2026-11-24T06:40:33.000000Z 01O \n" +
		"2026-11-24T06:40:33.988563Z 01E Error: \x1b[31mtenant failed\x1b[0m\n" +
		"section_end:1795502434:step_script\r\x1b[0K\n" +
		"section_start:1795502435:upload_artifacts_on_failure\r\x1b[0K\n" +
		"2026-11-24T06:40:36.734444Z 01E Uploading artifacts as \"archive\" to coordinator... 201 Created\n" +
		"2026-11-24T06:40:37.900890Z 00O ERROR: Job failed: exit code 1\n" +
		"2026-11-24T06:40:37.900894Z 00O \n"
	// 2 lines of the script's output, then GitLab's verdict.
	got := CleanLog(raw, 2)
	want := "downloading 100%\nError: tenant failed\nERROR: Job failed: exit code 1"
	if got != want {
		t.Errorf("CleanLog:\n%q\nwant\n%q", got, want)
	}

	// Without timestamps or sections, it is the last lines.
	if got := CleanLog("a\nb\nc\n\n", 2); got != "b\nc" {
		t.Errorf("plain log: %q", got)
	}
}
