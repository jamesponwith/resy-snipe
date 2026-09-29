package resy

import (
	"encoding/json"
	"fmt"
	"net/smtp"
	"os"
	"strings"
	"sync"
	"time"
)

// Alert kinds.
const (
	AlertAvailable = "available" // a table appeared; action needed
	AlertBooked    = "booked"    // a table was booked automatically
	AlertFailed    = "failed"    // a booking attempt failed
)

// Alert is one notable event worth telling the user about.
type Alert struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Target  string    `json:"target"`
	Date    string    `json:"date"`
	Message string    `json:"message"`
	Token   string    `json:"token,omitempty"`
}

// Subject is the one-line summary used for email and terminal output.
func (a Alert) Subject() string {
	switch a.Kind {
	case AlertBooked:
		return fmt.Sprintf("BOOKED: %s", a.Target)
	case AlertAvailable:
		return fmt.Sprintf("TABLE OPEN: %s", a.Target)
	case AlertFailed:
		return fmt.Sprintf("FAILED: %s", a.Target)
	default:
		return fmt.Sprintf("%s: %s", strings.ToUpper(a.Kind), a.Target)
	}
}

// EmailConfig describes where alert mail is sent. It is read from the
// environment rather than the plan file so an SMTP password never lands in a
// file that might be committed.
type EmailConfig struct {
	Host string
	Port string
	User string
	Pass string
	From string
	To   []string
}

// EmailConfigFromEnv builds an email config, returning nil when ALERT_EMAIL_TO
// is unset (email alerts are opt-in). An error means email was requested but
// configured incompletely, which is worth failing on rather than silently
// dropping alerts.
func EmailConfigFromEnv() (*EmailConfig, error) {
	to := strings.TrimSpace(os.Getenv("ALERT_EMAIL_TO"))
	if to == "" {
		return nil, nil
	}

	cfg := &EmailConfig{
		Host: envOr("SMTP_HOST", "smtp.gmail.com"),
		Port: envOr("SMTP_PORT", "587"),
		User: strings.TrimSpace(os.Getenv("SMTP_USER")),
		Pass: os.Getenv("SMTP_PASS"),
	}
	cfg.From = envOr("ALERT_EMAIL_FROM", cfg.User)
	for _, addr := range strings.Split(to, ",") {
		if addr = strings.TrimSpace(addr); addr != "" {
			cfg.To = append(cfg.To, addr)
		}
	}

	var missing []string
	if cfg.User == "" {
		missing = append(missing, "SMTP_USER")
	}
	if cfg.Pass == "" {
		missing = append(missing, "SMTP_PASS")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("ALERT_EMAIL_TO is set but %s missing; for Gmail use an App Password, not your account password", strings.Join(missing, " and "))
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// Alerter fans one alert out to the terminal, an append-only log, and email.
// A failing sink never blocks the others: losing an email must not cost you the
// terminal notice about a table that just opened.
type Alerter struct {
	LogPath string
	Email   *EmailConfig
	Bell    bool

	// sendMail is swapped out in tests.
	sendMail func(addr string, a smtp.Auth, from string, to []string, msg []byte) error

	mu sync.Mutex
}

func NewAlerter(logPath string, email *EmailConfig, bell bool) *Alerter {
	return &Alerter{LogPath: logPath, Email: email, Bell: bell, sendMail: smtp.SendMail}
}

// Send delivers an alert to every configured sink.
func (a *Alerter) Send(alert Alert) {
	if alert.Time.IsZero() {
		alert.Time = time.Now()
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	bell := ""
	if a.Bell && alert.Kind != AlertFailed {
		bell = "\a"
	}
	fmt.Printf("%s\n>>> %s %s\n    %s\n", bell, alert.Time.Format("15:04:05"), alert.Subject(), alert.Message)

	if err := a.appendLog(alert); err != nil {
		fmt.Printf("    (log write failed: %v)\n", err)
	}
	if err := a.sendEmail(alert); err != nil {
		fmt.Printf("    (email failed: %v)\n", err)
	}
}

// appendLog writes one JSON object per line, so the log stays both greppable
// and machine-readable for auditing what the tool did unattended.
func (a *Alerter) appendLog(alert Alert) error {
	if a.LogPath == "" {
		return nil
	}

	line, err := json.Marshal(alert)
	if err != nil {
		return err
	}

	file, err := os.OpenFile(a.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.Write(append(line, '\n'))
	return err
}

func (a *Alerter) sendEmail(alert Alert) error {
	if a.Email == nil {
		return nil
	}

	body := fmt.Sprintf("To: %s\r\nFrom: %s\r\nSubject: [resy-snipe] %s\r\n\r\n%s\n\nDate: %s\nWhen: %s\n",
		strings.Join(a.Email.To, ", "), a.Email.From, alert.Subject(),
		alert.Message, alert.Date, alert.Time.Format(time.RFC1123))
	if alert.Token != "" {
		body += fmt.Sprintf("Reservation token: %s\n", alert.Token)
	}

	auth := smtp.PlainAuth("", a.Email.User, a.Email.Pass, a.Email.Host)
	addr := fmt.Sprintf("%s:%s", a.Email.Host, a.Email.Port)
	return a.sendMail(addr, auth, a.Email.From, a.Email.To, []byte(body))
}
