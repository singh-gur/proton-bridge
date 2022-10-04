package bridge

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/Masterminds/semver/v3"
	"github.com/ProtonMail/gluon"
	imapEvents "github.com/ProtonMail/gluon/events"
	"github.com/ProtonMail/proton-bridge/v2/internal/constants"
	"github.com/ProtonMail/proton-bridge/v2/internal/vault"
	"github.com/sirupsen/logrus"
)

const (
	defaultClientName    = "UnknownClient"
	defaultClientVersion = "0.0.1"
)

func (bridge *Bridge) serveIMAP() error {
	imapListener, err := newListener(bridge.vault.GetIMAPPort(), bridge.vault.GetIMAPSSL(), bridge.tlsConfig)
	if err != nil {
		return fmt.Errorf("failed to create IMAP listener: %w", err)
	}

	bridge.imapListener = imapListener

	return bridge.imapServer.Serve(context.Background(), bridge.imapListener)
}

func (bridge *Bridge) restartIMAP(ctx context.Context) error {
	if err := bridge.imapListener.Close(); err != nil {
		logrus.WithError(err).Warn("Failed to close IMAP listener")
	}

	return bridge.serveIMAP()
}

func (bridge *Bridge) closeIMAP(ctx context.Context) error {
	if err := bridge.imapServer.Close(ctx); err != nil {
		logrus.WithError(err).Warn("Failed to close IMAP server")
	}

	if err := bridge.imapListener.Close(); err != nil {
		logrus.WithError(err).Warn("Failed to close IMAP listener")
	}

	return nil
}

func (bridge *Bridge) handleIMAPEvent(event imapEvents.Event) {
	switch event := event.(type) {
	case imapEvents.SessionAdded:
		if bridge.identifier.HasClient() {
			return
		}

		bridge.identifier.SetClient(defaultClientName, defaultClientVersion)

	case imapEvents.IMAPID:
		bridge.identifier.SetClient(event.IMAPID.Name, event.IMAPID.Version)
	}
}

func getGluonDir(encVault *vault.Vault) (string, error) {
	empty, err := isEmpty(encVault.GetGluonDir())
	if err != nil {
		return "", fmt.Errorf("failed to check if gluon dir is empty: %w", err)
	}

	if empty {
		if err := encVault.ForUser(func(user *vault.User) error {
			return user.SetSync(false)
		}); err != nil {
			return "", fmt.Errorf("failed to reset user sync status: %w", err)
		}
	}

	return encVault.GetGluonDir(), nil
}

func newIMAPServer(gluonDir string, version *semver.Version, tlsConfig *tls.Config) (*gluon.Server, error) {
	imapServer, err := gluon.New(
		gluon.WithTLS(tlsConfig),
		gluon.WithDataDir(gluonDir),
		gluon.WithVersionInfo(
			int(version.Major()),
			int(version.Minor()),
			int(version.Patch()),
			constants.FullAppName,
			"TODO",
			"TODO",
		),
		gluon.WithLogger(
			logrus.StandardLogger().WriterLevel(logrus.InfoLevel),
			logrus.StandardLogger().WriterLevel(logrus.InfoLevel),
		),
	)
	if err != nil {
		return nil, err
	}

	return imapServer, nil
}

func isEmpty(dir string) (bool, error) {
	if _, err := os.Stat(dir); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("failed to stat %s: %w", dir, err)
		}

		return true, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("failed to read dir %s: %w", dir, err)
	}

	return len(entries) == 0, nil
}