package diag

import (
	"os"
	"strings"
	"testing"
	"time"
)

// testdata/report.json is produced by the firmware encoder
// (agent_trace_core_encode), so this test pins the wire contract.
func TestFirmwareReportParsesAndRenders(t *testing.T) {
	body, err := os.ReadFile("testdata/report.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if report.Trigger != "send_after_failure" || report.Boot != 4 || len(report.Events) != 16 {
		t.Fatalf("parsed %+v", report)
	}
	location := time.FixedZone("CST", 8*3600)
	received := time.Date(2026, 11, 15, 17, 0, 17, 0, location)
	text := Render(report, received, location)
	for _, want := range []string{
		"触发：录音失败后发送了草稿",
		"上次启动 #3",
		"17:00:12.000  +  12.000s  按键 OK 按下", // 5 s before the 17 s uptime at receipt.
		"手势 OK 长按（页面=文字卡片，录音=空闲）",
		"HTTP POST segments → 201（240 ms）",
		"PCM 队列已满",
		"录音结果 saved=0 resolved=1 started=1 cancelled=0 capture_error=257",
		"手势 OK 短按（页面=文字卡片，录音=空闲）",
		"发送草稿（修订 191）",
		"W (15700) agent_capture: PCM queue full",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if summary := Summary(report); !strings.Contains(summary, "录音失败后发送了草稿") || !strings.Contains(summary, "saved=0") {
		t.Fatal(summary)
	}
}

func TestVoiceAndSpeechEventsRender(t *testing.T) {
	if got := describe(Event{Type: typeVoice, A: 5}); got != "语音页 → 思考中" {
		t.Fatal(got)
	}
	if got := describe(Event{Type: typeSpeech, A: 0, B: 3, Tag: "dry 2"}); got != "朗读结束：读完（到第 4 句），断流 2 次" {
		t.Fatal(got)
	}
	if got := describe(Event{Type: typeSpeech, A: 0, B: 1, Tag: "dry 1 r1"}); got != "朗读结束：读完（到第 2 句），断流 1 次，断线续播 1 次" {
		t.Fatal(got)
	}
	if got := describe(Event{Type: typeSpeech, A: 2, B: 6, Tag: "d6 g82 r2"}); got != "朗读结束：被停止（到第 7 句），断流 6 次，最长 8.2 秒没收到数据，断线续播 2 次" {
		t.Fatal(got)
	}
	if got := describe(Event{Type: typeWifi, A: 200, B: -54}); got != "Wi-Fi 断开：信标超时（信号 -54 dBm）" {
		t.Fatal(got)
	}
	if got := describe(Event{Type: typeSpeech, A: 2, B: 3}); got != "朗读结束：被停止（到第 4 句）" {
		t.Fatal(got)
	}
	if got := describe(Event{Type: typePage, A: 6}); got != "页面 → 语音" {
		t.Fatal(got)
	}
}

func TestParseRejectsMalformedReports(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"trigger":"x","events":[[1,2,3]]}`,
		`{"trigger":"x","events":[[1,2,3,4,5,"` + strings.Repeat("a", 40) + `"]]}`,
		`{"trigger":"x","unknown":1}`,
		`{"trigger":"x","logs":"` + strings.Repeat("a", MaxLogBytes+1) + `"}`,
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("accepted %.60s", body)
		}
	}
}
