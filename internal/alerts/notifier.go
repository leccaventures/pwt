package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"lecca.io/pharos-watchtower/internal/config"
	"lecca.io/pharos-watchtower/internal/logger"
)

type Notifier interface {
	Notify(ctx context.Context, event AlertEvent) error
}

type MultiNotifier struct {
	notifiers []Notifier
}

func (m *MultiNotifier) Notify(ctx context.Context, event AlertEvent) error {
	var lastErr error
	for _, n := range m.notifiers {
		if err := n.Notify(ctx, event); err != nil {
			lastErr = err
			logger.Warn("ALERT", "Notifier failed: %v", err)
		}
	}
	return lastErr
}

func NewNotifier(cfg config.AlertsConfig) Notifier {
	var notifiers []Notifier
	logNotifier := &LogNotifier{}
	notifiers = append(notifiers, logNotifier)

	if cfg.Channels.PagerDuty.Enabled && cfg.Channels.PagerDuty.APIKey != "" {
		notifiers = append(notifiers, &PagerDutyNotifier{apiKey: cfg.Channels.PagerDuty.APIKey, defaultSeverity: cfg.Channels.PagerDuty.Severity})
	}
	if cfg.Channels.Discord.Enabled && cfg.Channels.Discord.Webhook != "" {
		notifiers = append(notifiers, &DiscordNotifier{webhook: cfg.Channels.Discord.Webhook})
	}
	if cfg.Channels.Telegram.Enabled && cfg.Channels.Telegram.Token != "" && cfg.Channels.Telegram.ChatID != "" {
		notifiers = append(notifiers, &TelegramNotifier{apiKey: cfg.Channels.Telegram.Token, channel: cfg.Channels.Telegram.ChatID})
	}
	if cfg.Channels.Slack.Enabled && cfg.Channels.Slack.Webhook != "" {
		notifiers = append(notifiers, &SlackNotifier{webhook: cfg.Channels.Slack.Webhook})
	}

	return &MultiNotifier{notifiers: notifiers}
}

type LogNotifier struct{}

func (l *LogNotifier) Notify(ctx context.Context, event AlertEvent) error {
	logger.Warn("ALERT", "%s | %s", event.Status, event.Message)
	return nil
}

// Discord embed structures
type discordEmbed struct {
	Title     string         `json:"title"`
	Color     int            `json:"color"`
	Fields    []discordField `json:"fields"`
	Timestamp string         `json:"timestamp"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordPayload struct {
	Embeds []discordEmbed `json:"embeds"`
}

func truncateID(id string) string {
	if len(id) <= 16 {
		return id
	}
	return id[:6] + "..." + id[len(id)-4:]
}

func formatSubject(event AlertEvent) string {
	if event.SubjectName != "" {
		return fmt.Sprintf("%s (%s)", event.SubjectName, truncateID(event.SubjectID))
	}
	return truncateID(event.SubjectID)
}
func formatDiscordEmbed(event AlertEvent) discordPayload {
	emoji := "🚨"
	color := 0xFF0000 // red
	if event.Status == AlertResolved {
		emoji = "💚"
		color = 0x00FF00 // green
	}

	title := fmt.Sprintf("%s %s", emoji, event.Title)

	fields := []discordField{
		{Name: "Subject", Value: formatSubject(event), Inline: false},
		{Name: "Chain", Value: event.ChainID, Inline: false},
	}
	for _, detail := range event.Details {
		fields = append(fields, discordField{Name: detail.Label, Value: detail.Value, Inline: false})
	}

	return discordPayload{
		Embeds: []discordEmbed{{
			Title:     title,
			Color:     color,
			Fields:    fields,
			Timestamp: event.Timestamp.Format(time.RFC3339),
		}},
	}
}

type DiscordNotifier struct {
	webhook string
}

func (d *DiscordNotifier) Notify(ctx context.Context, event AlertEvent) error {
	if d.webhook == "" {
		return nil
	}
	payload := formatDiscordEmbed(event)
	return postJSON(ctx, d.webhook, payload)
}

// Slack Block Kit structures
type slackPayload struct {
	Blocks []slackBlock `json:"blocks"`
}

type slackBlock struct {
	Type   string      `json:"type"`
	Text   *slackText  `json:"text,omitempty"`
	Fields []slackText `json:"fields,omitempty"`
}

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func formatSlackBlocks(event AlertEvent) slackPayload {
	emoji := "🚨"
	if event.Status == AlertResolved {
		emoji = "💚"
	}

	header := fmt.Sprintf("%s %s", emoji, event.Title)
	fields := []slackText{
		{Type: "mrkdwn", Text: fmt.Sprintf("*Subject:*\n%s", formatSubject(event))},
		{Type: "mrkdwn", Text: fmt.Sprintf("*Chain:*\n%s", event.ChainID)},
	}
	for _, detail := range event.Details {
		fields = append(fields, slackText{Type: "mrkdwn", Text: fmt.Sprintf("*%s:*\n%s", detail.Label, detail.Value)})
	}

	return slackPayload{
		Blocks: []slackBlock{
			{
				Type: "header",
				Text: &slackText{Type: "plain_text", Text: header},
			},
			{
				Type:   "section",
				Fields: fields,
			},
		},
	}
}

type SlackNotifier struct {
	webhook string
}

func (s *SlackNotifier) Notify(ctx context.Context, event AlertEvent) error {
	if s.webhook == "" {
		return nil
	}
	payload := formatSlackBlocks(event)
	return postJSON(ctx, s.webhook, payload)
}

func formatTelegramHTML(event AlertEvent) string {
	emoji := "🚨"
	if event.Status == AlertResolved {
		emoji = "💚"
	}
	message := fmt.Sprintf(
		"<b>%s %s</b>\n\n<b>Subject:</b> %s\n<b>Chain:</b> %s",
		emoji, event.Title, formatSubject(event), event.ChainID,
	)
	for _, detail := range event.Details {
		message += fmt.Sprintf("\n<b>%s:</b> %s", detail.Label, detail.Value)
	}

	return message
}

type TelegramNotifier struct {
	apiKey  string
	channel string
}

func (t *TelegramNotifier) Notify(ctx context.Context, event AlertEvent) error {
	if t.apiKey == "" || t.channel == "" {
		return nil
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.apiKey)
	payload := map[string]string{
		"chat_id":    t.channel,
		"text":       formatTelegramHTML(event),
		"parse_mode": "HTML",
	}
	return postJSON(ctx, url, payload)
}

type PagerDutyNotifier struct {
	apiKey          string
	defaultSeverity string
}

type pagerDutyPayload struct {
	RoutingKey  string        `json:"routing_key"`
	EventAction string        `json:"event_action"`
	DedupKey    string        `json:"dedup_key"`
	Payload     pagerDutyBody `json:"payload"`
}

type pagerDutyBody struct {
	Summary   string `json:"summary"`
	Source    string `json:"source"`
	Severity  string `json:"severity"`
	Timestamp string `json:"timestamp"`
	Custom    any    `json:"custom_details,omitempty"`
}

func (p *PagerDutyNotifier) Notify(ctx context.Context, event AlertEvent) error {
	if p.apiKey == "" {
		return nil
	}
	action := "trigger"
	if event.Status == AlertResolved {
		action = "resolve"
	}
	severity := p.defaultSeverity
	if severity == "" {
		severity = "critical"
	}
	if event.Severity != "" {
		severity = event.Severity
	}
	payload := pagerDutyPayload{
		RoutingKey:  p.apiKey,
		EventAction: action,
		DedupKey:    event.Key,
		Payload: pagerDutyBody{
			Summary:   event.Message,
			Source:    event.ChainID,
			Severity:  severity,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Custom:    formatPagerDutyDetails(event),
		},
	}
	
	return postJSON(ctx, "https://events.pagerduty.com/v2/enqueue", payload)
}

func formatPagerDutyDetails(event AlertEvent) map[string]string {
	if len(event.Details) == 0 {
		return nil
	}
	custom := make(map[string]string, len(event.Details))
	for _, detail := range event.Details {
		custom[detail.Label] = detail.Value
	}
	return custom
}

func formatMessage(event AlertEvent) string {
	return fmt.Sprintf("[%s][%s] %s", event.Status, event.RuleID, event.Message)
}

func postJSON(ctx context.Context, url string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
