package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mailtrap/mailtrap-go"
)

// Mailer sends the plain-text emails menged needs (password reset links).
type Mailer interface {
	Send(ctx context.Context, to, subject, text string) error
}

// NewMailer builds the mailer from config. Without a provider, emails are written
// to the log instead, which is enough for local development.
func NewMailer(cfg MailConfig) (Mailer, error) {
	switch cfg.Provider {
	case "", "log":
		slog.Warn("mail.provider is log: reset links are written to the server log, not emailed")
		return logMailer{}, nil
	case "mailtrap":
		if cfg.APIToken == "" {
			return nil, fmt.Errorf("mail.api_token is empty (set MAILTRAP_API_TOKEN)")
		}
		if cfg.FromEmail == "" {
			return nil, fmt.Errorf("mail.from_email is required for mailtrap")
		}
		var opts []mailtrap.Option
		if cfg.SandboxID != 0 {
			// Delivers into a Mailtrap testing inbox instead of the recipient.
			opts = append(opts, mailtrap.WithSandbox(true), mailtrap.WithSandboxID(cfg.SandboxID))
		}
		client, err := mailtrap.NewClient(cfg.APIToken, opts...)
		if err != nil {
			return nil, err
		}
		return &mailtrapMailer{client: client, from: mailtrap.Address{Email: cfg.FromEmail, Name: cfg.FromName}}, nil
	default:
		return nil, fmt.Errorf("unknown mail provider %q", cfg.Provider)
	}
}

type mailtrapMailer struct {
	client *mailtrap.Client
	from   mailtrap.Address
}

func (m *mailtrapMailer) Send(ctx context.Context, to, subject, text string) error {
	resp, _, err := m.client.Send(ctx, &mailtrap.SendRequest{
		From:     m.from,
		To:       []mailtrap.Address{{Email: to}},
		Subject:  subject,
		Text:     text,
		Category: "menged",
	})
	if err != nil {
		return err
	}
	slog.Info("email sent", "subject", subject, "mailtrap_ids", resp.MessageIDs)
	return nil
}

type logMailer struct{}

func (logMailer) Send(_ context.Context, to, subject, text string) error {
	slog.Info("email (not sent: mail.provider is log)", "to", to, "subject", subject, "text", text)
	return nil
}
