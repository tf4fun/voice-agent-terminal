// Package diag validates and renders device diagnostic reports: a bounded
// ring of recent terminal events and warning/error log lines that the device
// uploads after a failure. Raw reports are stored; rendering happens on read
// so the wording can improve without losing data.
package diag

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxEvents   = 1024
	MaxLogBytes = 16 << 10
	MaxTagBytes = 32
)

var ErrInvalid = errors.New("invalid_report")

// Report mirrors firmware/main/agent_trace.c agent_trace_core_encode.
type Report struct {
	Firmware    string  `json:"firmware"`
	Build       string  `json:"build"`
	Trigger     string  `json:"trigger"`
	Boot        uint32  `json:"boot"`
	ResetReason uint32  `json:"reset_reason"`
	UptimeMS    uint32  `json:"uptime_ms"`
	HeapFree    uint32  `json:"heap_free"`
	HeapMinimum uint32  `json:"heap_minimum"`
	HeapLargest uint32  `json:"heap_largest"`
	Events      []Event `json:"events"`
	Logs        string  `json:"logs"`
}

// Event is [boot, ms, type, a, b, tag].
type Event struct {
	Boot uint8
	MS   uint32
	Type uint8
	A    uint16
	B    int32
	Tag  string
}

func (e *Event) UnmarshalJSON(data []byte) error {
	var fields [6]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	targets := []any{&e.Boot, &e.MS, &e.Type, &e.A, &e.B, &e.Tag}
	for i, target := range targets {
		if fields[i] == nil {
			return ErrInvalid
		}
		if err := json.Unmarshal(fields[i], target); err != nil {
			return err
		}
	}
	return nil
}

// Parse decodes and bounds an uploaded report.
func Parse(body []byte) (Report, error) {
	var report Report
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return report, ErrInvalid
	}
	if len(report.Events) > MaxEvents || len(report.Logs) > MaxLogBytes || len(report.Firmware) > 64 ||
		len(report.Build) > 64 || len(report.Trigger) > 64 || report.Trigger == "" {
		return report, ErrInvalid
	}
	for _, event := range report.Events {
		if len(event.Tag) > MaxTagBytes || !utf8.ValidString(event.Tag) {
			return report, ErrInvalid
		}
	}
	return report, nil
}

var triggers = map[string]string{
	"abnormal_reset":     "异常重启",
	"record_failed":      "录音失败",
	"capture_error":      "采音错误",
	"send_after_failure": "录音失败后发送了草稿",
	"manual":             "手动上报",
}

func TriggerLabel(trigger string) string {
	if label, ok := triggers[trigger]; ok {
		return label
	}
	return trigger
}

var resetReasons = []string{"未知", "上电", "外部引脚", "软件重启", "崩溃", "中断看门狗", "任务看门狗", "其他看门狗",
	"深度睡眠唤醒", "掉电", "SDIO", "USB", "JTAG", "eFuse 错误", "电源毛刺", "CPU 锁死"}

func ResetLabel(reason uint32) string {
	if int(reason) < len(resetReasons) {
		return resetReasons[reason]
	}
	return fmt.Sprintf("复位 %d", reason)
}

// Summary is a one-line description for report lists.
func Summary(report Report) string {
	parts := []string{TriggerLabel(report.Trigger)}
	for i := len(report.Events) - 1; i >= 0; i-- {
		if e := report.Events[i]; e.Type == typeRecord {
			parts = append(parts, "最后一次录音："+describe(e))
			break
		}
	}
	parts = append(parts, fmt.Sprintf("%d 条事件", len(report.Events)))
	return strings.Join(parts, " · ")
}

const (
	typeBoot = iota + 1
	typeKey
	typeGesture
	typePage
	typeCapture
	typeHTTP
	typeAudio
	typeRecord
	typeSend
	typeHeap
	typeReport
	typeCaptureError
	typeSpeech
	typeVoice
)

var (
	buttons    = []string{"上", "下", "OK"}
	keyEvents  = []string{"按下", "单击", "双击", "长按（驱动）", "松开"}
	pages      = []string{"会话列表", "文字卡片", "设置", "配网", "准备录音", "录音", "语音"}
	voiceModes = []string{"待机", "准备录音", "录音中", "转写中", "草稿", "思考中", "回复中", "朗读中", "回复失败"}
	speechEnds = []string{"读完", "失败", "被停止", "超出上限"}
	captures   = []string{"空闲", "准备中", "录音中", "结束中", "提交中"}
	httpMethod = map[byte]string{'G': "GET", 'P': "POST", 'U': "PATCH", 'D': "DELETE"}
)

func pick(names []string, index int) string {
	if index >= 0 && index < len(names) {
		return names[index]
	}
	return fmt.Sprintf("#%d", index)
}

// pageState expands the gesture tag "p<page> c<capture>".
func pageState(tag string) string {
	var page, capture int
	if _, err := fmt.Sscanf(tag, "p%d c%d", &page, &capture); err != nil {
		return tag
	}
	return fmt.Sprintf("页面=%s，录音=%s", pick(pages, page), pick(captures, capture))
}

func describe(e Event) string {
	switch e.Type {
	case typeBoot:
		return fmt.Sprintf("启动 #%d，复位原因：%s", e.B, ResetLabel(uint32(e.A)))
	case typeKey:
		if e.A == 0xff {
			return "按键松开事件丢失，按键状态已重置"
		}
		return fmt.Sprintf("按键 %s %s", pick(buttons, int(e.A)), pick(keyEvents, int(e.B)))
	case typeGesture:
		kind := "短按"
		if e.B == 1 {
			kind = "长按"
		}
		return fmt.Sprintf("手势 %s %s（%s）", pick(buttons, int(e.A)), kind, pageState(e.Tag))
	case typePage:
		if e.A == 0xfe {
			return "切换到更新的一轮对话（来自其他客户端）"
		}
		return "页面 → " + pick(pages, int(e.A))
	case typeCapture:
		return "录音状态 → " + pick(captures, int(e.A))
	case typeHTTP:
		method, path := e.Tag, ""
		if len(e.Tag) > 2 {
			method, path = httpMethod[e.Tag[0]], e.Tag[2:]
			if method == "" {
				method = e.Tag[:1]
			}
		}
		status := fmt.Sprintf("%d", e.A)
		if e.A == 0 {
			status = "连接失败"
		}
		return fmt.Sprintf("HTTP %s %s → %s（%d ms）", method, path, status, e.B)
	case typeAudio:
		status := fmt.Sprintf("%d", e.A)
		if e.A == 0 {
			status = "无响应"
		}
		return fmt.Sprintf("音频上传结束：响应 %s，%d 字节（约 %.1f 秒）", status, e.B, float64(e.B)/32000)
	case typeRecord:
		flag := func(bit uint16) int {
			if e.A&bit != 0 {
				return 1
			}
			return 0
		}
		return fmt.Sprintf("录音结果 saved=%d resolved=%d started=%d cancelled=%d capture_error=%d",
			flag(1), flag(2), flag(4), flag(8), e.B)
	case typeSend:
		if e.A == 0 {
			return fmt.Sprintf("请求发送草稿（修订 %d），条件不满足，未发送", e.B)
		}
		return fmt.Sprintf("发送草稿（修订 %d）", e.B)
	case typeHeap:
		return fmt.Sprintf("内存 空闲 %d KB，最大连续块 %d KB", e.B/1024, e.A)
	case typeReport:
		result := "失败"
		if e.B == 1 {
			result = "成功"
		}
		return fmt.Sprintf("诊断上报（%s）%s", TriggerLabel(triggerName(int(e.A))), result)
	case typeSpeech:
		text := fmt.Sprintf("朗读结束：%s（到第 %d 句）", pick(speechEnds, int(e.A)), e.B+1)
		var dry int
		if _, err := fmt.Sscanf(e.Tag, "dry %d", &dry); err == nil {
			text += fmt.Sprintf("，断流 %d 次", dry)
		}
		return text
	case typeVoice:
		return "语音页 → " + pick(voiceModes, int(e.A))
	case typeCaptureError:
		if e.A == 1 {
			return fmt.Sprintf("麦克风读取失败（错误 0x%x）", e.B)
		}
		return "PCM 队列已满：上传跟不上采音"
	}
	return fmt.Sprintf("未知事件 %d a=%d b=%d %q", e.Type, e.A, e.B, e.Tag)
}

func triggerName(code int) string {
	return pick([]string{"none", "abnormal_reset", "record_failed", "capture_error", "send_after_failure", "manual"}, code)
}

// Render formats a stored report as plain text. Events of the reporting boot
// get wall-clock times derived from when the gateway received the report.
func Render(report Report, received time.Time, location *time.Location) string {
	var out strings.Builder
	local := received.In(location)
	fmt.Fprintf(&out, "设备诊断报告  %s\n", local.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&out, "触发：%s\n", TriggerLabel(report.Trigger))
	fmt.Fprintf(&out, "固件：%s（构建 %s）  启动次数：%d  本次复位原因：%s  已运行：%.1f 秒\n",
		report.Firmware, report.Build, report.Boot, ResetLabel(report.ResetReason), float64(report.UptimeMS)/1000)
	fmt.Fprintf(&out, "内存：空闲 %d KB，历史最低 %d KB，最大连续块 %d KB\n\n",
		report.HeapFree/1024, report.HeapMinimum/1024, report.HeapLargest/1024)
	fmt.Fprintf(&out, "事件（%d 条，从旧到新）\n", len(report.Events))
	current := uint8(report.Boot)
	for _, e := range report.Events {
		uptime := fmt.Sprintf("+%8.3fs", float64(e.MS)/1000)
		var stamp string
		if e.Boot == current && e.MS <= report.UptimeMS {
			at := received.Add(-time.Duration(report.UptimeMS-e.MS) * time.Millisecond).In(location)
			stamp = at.Format("15:04:05.000")
		} else {
			stamp = fmt.Sprintf("上次启动 #%-3d", e.Boot)
		}
		fmt.Fprintf(&out, "%s  %s  %s\n", stamp, uptime, describe(e))
	}
	out.WriteString("\n最近的警告/错误日志\n")
	if strings.TrimSpace(report.Logs) == "" {
		out.WriteString("（无）\n")
	} else {
		out.WriteString(report.Logs)
		if !strings.HasSuffix(report.Logs, "\n") {
			out.WriteString("\n")
		}
	}
	return out.String()
}
