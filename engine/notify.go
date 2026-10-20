package main

// Desktop notifications, used where the agent can't be woken (Codex). Text goes through environment
// variables, never through a command line, so it can't inject anything into the notifier.

import (
	"os"
	"os/exec"
	"runtime"
	"time"
)

const toastPS = `
$ErrorActionPreference = 'Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$xml = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$t = $xml.GetElementsByTagName('text')
[void]$t.Item(0).AppendChild($xml.CreateTextNode($env:CREW_TITLE))
[void]$t.Item(1).AppendChild($xml.CreateTextNode($env:CREW_BODY))
$app = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($app).Show([Windows.UI.Notifications.ToastNotification]::new($xml))
`

const osaScript = `on run argv
display notification (system attribute "CREW_BODY") with title (system attribute "CREW_TITLE")
end run`

func notify(title, body string) {
	if os.Getenv("CREW_NO_NOTIFY") == "1" {
		return
	}
	title, body = cleanIncoming(title, 80), cleanIncoming(body, 240)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", toastPS)
	case "darwin":
		cmd = exec.Command("osascript", "-e", osaScript)
	default:
		cmd = exec.Command("notify-send", "--app-name=crew", "--", title, body)
	}
	cmd.Env = append(os.Environ(), "CREW_TITLE="+title, "CREW_BODY="+body)
	hideWindow(cmd)
	if cmd.Start() == nil {
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select { // give the notifier a moment; never hang the hook on it
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}
