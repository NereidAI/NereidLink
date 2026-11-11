// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// templates.go holds the built-in English/Chinese notification templates
// and the render fallback.

package notify

import (
	"strings"
)

// alarmEvent is the JSON shape consumed from nl.alarm subjects. It mirrors
// alarm.Event across the package boundary.
type alarmEvent struct {
	AlarmID   string         `json:"alarm_id"`
	TenantID  string         `json:"tenant_id"`
	DeviceID  string         `json:"device_id"`
	Type      string         `json:"alarm_type"`
	Severity  string         `json:"severity"`
	State     string         `json:"state"`
	DedupeKey string         `json:"dedupe_key"`
	Created   bool           `json:"created"`
	Escalated bool           `json:"escalated"`
	Details   map[string]any `json:"details"`
	StartedAt string         `json:"started_at"`
	ClearedAt string         `json:"cleared_at"`
}

// templates maps language -> event kind -> template text. Templates use
// brace tokens over a fixed variable set; tenants may override copy later
// but not the variable set (ADR-0019).
var templates = map[string]map[string]string{
	LanguageEnglish: {
		EventTrigger: "NereidLink alarm: [{severity}] {alarm_type}\n" +
			"Device: {device_id}\nStarted: {started_at}",
		EventClear: "NereidLink alarm cleared: [{severity}] {alarm_type}\n" +
			"Device: {device_id}\nCleared: {cleared_at}",
	},
	LanguageChinese: {
		EventTrigger: "NereidLink 告警通知：[{severity}] {alarm_type}\n" +
			"设备：{device_id}\n触发时间：{started_at}",
		EventClear: "NereidLink 告警恢复：[{severity}] {alarm_type}\n" +
			"设备：{device_id}\n恢复时间：{cleared_at}",
	},
}

// renderTemplate substitutes brace tokens from the event. Render failure
// falls back to plain text, per ADR-0019.
func renderTemplate(language, kind string, event alarmEvent) (string, string) {
	subject := "NereidLink Alarm"
	langSet, ok := templates[language]
	if !ok {
		langSet = templates[LanguageEnglish]
	}
	body, ok := langSet[kind]
	if !ok {
		return subject, fallbackText(event)
	}

	values := map[string]string{
		"alarm_id":   event.AlarmID,
		"tenant_id":  event.TenantID,
		"device_id":  event.DeviceID,
		"alarm_type": event.Type,
		"severity":   localizeSeverity(language, event.Severity),
		"started_at": event.StartedAt,
		"cleared_at": event.ClearedAt,
	}
	for token, value := range values {
		body = strings.ReplaceAll(body, "{"+token+"}", value)
	}
	subject = "[" + values["severity"] + "] " + event.Type
	return subject, body
}

// fallbackText renders a minimal plain-text notification.
func fallbackText(event alarmEvent) string {
	return "alarm_id=" + event.AlarmID + " severity=" + event.Severity +
		" type=" + event.Type + " device=" + event.DeviceID
}

// localizeSeverity maps a severity to its localized label.
func localizeSeverity(language, severity string) string {
	if language != LanguageChinese {
		return severity
	}
	switch severity {
	case "CRITICAL":
		return "严重"
	case "MAJOR":
		return "主要"
	case "MINOR":
		return "次要"
	case "WARNING":
		return "警告"
	default:
		return severity
	}
}
