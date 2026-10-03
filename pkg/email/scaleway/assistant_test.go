package scaleway

import (
	"strings"
	"testing"
	"time"
)

func TestRenderAssistantDisconnected(t *testing.T) {
	data := prepareAssistantDisconnectedData("https://dashboard.example/zh/assistant", time.Date(2026, 10, 3, 13, 5, 0, 0, time.UTC))
	for name, loader := range map[string]func(string) (templateExecutor, error){"html": loadHTMLTemplate, "text": loadTextTemplate} {
		out, err := renderEmail("templates/zh/assistant-disconnected", data, loader)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// 13:05 UTC is 15:05 in Brussels (summer time).
		if !strings.Contains(out, "10月3日 15:05") || !strings.Contains(out, "https://dashboard.example/zh/assistant") {
			t.Errorf("%s output:\n%s", name, out)
		}
	}
}
