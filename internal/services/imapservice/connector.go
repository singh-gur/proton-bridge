// Copyright (c) 2023 Proton AG
//
// This file is part of Proton Mail Bridge.
//
// Proton Mail Bridge is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Proton Mail Bridge is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with Proton Mail Bridge.  If not, see <https://www.gnu.org/licenses/>.

package imapservice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/mail"
	"sync/atomic"
	"time"

	"github.com/ProtonMail/gluon/async"
	"github.com/ProtonMail/gluon/connector"
	"github.com/ProtonMail/gluon/imap"
	"github.com/ProtonMail/gluon/rfc822"
	"github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"
	"github.com/ProtonMail/proton-bridge/v3/internal/services/sendrecorder"
	"github.com/ProtonMail/proton-bridge/v3/internal/usertypes"
	"github.com/ProtonMail/proton-bridge/v3/pkg/message"
	"github.com/ProtonMail/proton-bridge/v3/pkg/message/parser"
	"github.com/bradenaw/juniper/stream"
	"github.com/sirupsen/logrus"
	"golang.org/x/exp/slices"
)

// Connector contains all IMAP state required to satisfy sync and or imap queries.
type Connector struct {
	addrID      string
	showAllMail uint32

	flags     imap.FlagSet
	permFlags imap.FlagSet
	attrs     imap.FlagSet

	identityState sharedIdentity
	client        APIClient
	telemetry     Telemetry
	panicHandler  async.PanicHandler
	sendRecorder  *sendrecorder.SendRecorder

	addressMode usertypes.AddressMode
	labels      sharedLabels
	updateCh    *async.QueuedChannel[imap.Update]
	log         *logrus.Entry
}

func NewConnector(
	addrID string,
	apiClient APIClient,
	labels sharedLabels,
	identityState sharedIdentity,
	addressMode usertypes.AddressMode,
	sendRecorder *sendrecorder.SendRecorder,
	panicHandler async.PanicHandler,
	telemetry Telemetry,
	showAllMail bool,
) *Connector {
	userID := identityState.UserID()

	return &Connector{
		identityState: identityState,
		addrID:        addrID,
		showAllMail:   b32(showAllMail),
		flags:         defaultFlags,
		permFlags:     defaultPermanentFlags,
		attrs:         defaultAttributes,

		client:       apiClient,
		telemetry:    telemetry,
		panicHandler: panicHandler,
		sendRecorder: sendRecorder,

		updateCh: async.NewQueuedChannel[imap.Update](
			0,
			0,
			panicHandler,
			fmt.Sprintf("connector-update-%v-%v", userID, addrID),
		),
		labels:      labels,
		addressMode: addressMode,
		log: logrus.WithFields(logrus.Fields{
			"gluon-connector": addressMode,
			"addr-id":         addrID,
			"user-id":         userID,
		}),
	}
}

func (s *Connector) StateClose() {
	s.log.Debug("Closing state")
	s.updateCh.CloseAndDiscardQueued()
}

func (s *Connector) Authorize(ctx context.Context, username string, password []byte) bool {
	addrID, err := s.identityState.CheckAuth(username, password, s.telemetry)
	if err != nil {
		return false
	}

	if s.addressMode == usertypes.AddressModeSplit && addrID != s.addrID {
		return false
	}

	s.telemetry.SendConfigStatusSuccess(ctx)

	return true
}

func (s *Connector) CreateMailbox(ctx context.Context, name []string) (imap.Mailbox, error) {
	if len(name) < 2 {
		return imap.Mailbox{}, fmt.Errorf("invalid mailbox name %q: %w", name, connector.ErrOperationNotAllowed)
	}

	switch name[0] {
	case folderPrefix:
		return s.createFolder(ctx, name[1:])

	case labelPrefix:
		return s.createLabel(ctx, name[1:])

	default:
		return imap.Mailbox{}, fmt.Errorf("invalid mailbox name %q: %w", name, connector.ErrOperationNotAllowed)
	}
}

func (s *Connector) GetMessageLiteral(ctx context.Context, id imap.MessageID) ([]byte, error) {
	msg, err := s.client.GetFullMessage(ctx, string(id), usertypes.NewProtonAPIScheduler(s.panicHandler), proton.NewDefaultAttachmentAllocator())
	if err != nil {
		return nil, err
	}

	var literal []byte
	err = s.identityState.WithAddrKR(msg.AddressID, func(_, addrKR *crypto.KeyRing) error {
		l, buildErr := message.BuildRFC822(addrKR, msg.Message, msg.AttData, defaultMessageJobOpts())
		if buildErr != nil {
			return buildErr
		}

		literal = l

		return nil
	})

	return literal, err
}

func (s *Connector) GetMailboxVisibility(_ context.Context, mboxID imap.MailboxID) imap.MailboxVisibility {
	switch mboxID {
	case proton.AllMailLabel:
		if atomic.LoadUint32(&s.showAllMail) != 0 {
			return imap.Visible
		}
		return imap.Hidden

	case proton.AllScheduledLabel:
		return imap.HiddenIfEmpty
	default:
		return imap.Visible
	}
}

func (s *Connector) UpdateMailboxName(ctx context.Context, mboxID imap.MailboxID, name []string) error {
	if len(name) < 2 {
		return fmt.Errorf("invalid mailbox name %q: %w", name, connector.ErrOperationNotAllowed)
	}

	switch name[0] {
	case folderPrefix:
		return s.updateFolder(ctx, mboxID, name[1:])

	case labelPrefix:
		return s.updateLabel(ctx, mboxID, name[1:])

	default:
		return fmt.Errorf("invalid mailbox name %q: %w", name, connector.ErrOperationNotAllowed)
	}
}

func (s *Connector) DeleteMailbox(ctx context.Context, mboxID imap.MailboxID) error {
	if err := s.client.DeleteLabel(ctx, string(mboxID)); err != nil {
		return err
	}

	wLabels := s.labels.Write()
	defer wLabels.Close()

	wLabels.Delete(string(mboxID))

	return nil
}

func (s *Connector) CreateMessage(ctx context.Context, mailboxID imap.MailboxID, literal []byte, flags imap.FlagSet, _ time.Time) (imap.Message, []byte, error) {
	if mailboxID == proton.AllMailLabel {
		return imap.Message{}, nil, connector.ErrOperationNotAllowed
	}

	toList, err := getLiteralToList(literal)
	if err != nil {
		return imap.Message{}, nil, fmt.Errorf("failed to retrieve addresses from literal:%w", err)
	}

	// Compute the hash of the message (to match it against SMTP messages).
	hash, err := sendrecorder.GetMessageHash(literal)
	if err != nil {
		return imap.Message{}, nil, err
	}

	// Check if we already tried to send this message recently.
	if messageID, ok, err := s.sendRecorder.HasEntryWait(ctx, hash, time.Now().Add(90*time.Second), toList); err != nil {
		return imap.Message{}, nil, fmt.Errorf("failed to check send hash: %w", err)
	} else if ok {
		s.log.WithField("messageID", messageID).Warn("Message already sent")

		// Query the server-side message.
		full, err := s.client.GetFullMessage(ctx, messageID, usertypes.NewProtonAPIScheduler(s.panicHandler), proton.NewDefaultAttachmentAllocator())
		if err != nil {
			return imap.Message{}, nil, fmt.Errorf("failed to fetch message: %w", err)
		}

		// Build the message as it is on the server.
		if err := s.identityState.WithAddrKR(full.AddressID, func(_, addrKR *crypto.KeyRing) error {
			var err error

			if literal, err = message.BuildRFC822(addrKR, full.Message, full.AttData, defaultMessageJobOpts()); err != nil {
				return err
			}

			return nil
		}); err != nil {
			return imap.Message{}, nil, fmt.Errorf("failed to build message: %w", err)
		}

		return toIMAPMessage(full.MessageMetadata), literal, nil
	}

	wantLabelIDs := []string{string(mailboxID)}

	if flags.Contains(imap.FlagFlagged) {
		wantLabelIDs = append(wantLabelIDs, proton.StarredLabel)
	}

	var wantFlags proton.MessageFlag

	unread := !flags.Contains(imap.FlagSeen)

	if mailboxID != proton.DraftsLabel {
		header, err := rfc822.Parse(literal).ParseHeader()
		if err != nil {
			return imap.Message{}, nil, err
		}

		switch {
		case mailboxID == proton.InboxLabel:
			wantFlags = wantFlags.Add(proton.MessageFlagReceived)

		case mailboxID == proton.SentLabel:
			wantFlags = wantFlags.Add(proton.MessageFlagSent)

		case header.Has("Received"):
			wantFlags = wantFlags.Add(proton.MessageFlagReceived)

		default:
			wantFlags = wantFlags.Add(proton.MessageFlagSent)
		}
	} else {
		unread = false
	}

	if flags.Contains(imap.FlagAnswered) {
		wantFlags = wantFlags.Add(proton.MessageFlagReplied)
	}

	msg, literal, err := s.importMessage(ctx, literal, wantLabelIDs, wantFlags, unread)
	if err != nil {
		if errors.Is(err, proton.ErrImportSizeExceeded) {
			// Remap error so that Gluon does not put this message in the recovery mailbox.
			err = fmt.Errorf("%v: %w", err, connector.ErrMessageSizeExceedsLimits)
		}

		if apiErr := new(proton.APIError); errors.As(err, &apiErr) {
			s.log.WithError(apiErr).WithField("Details", apiErr.DetailsToString()).Error("Failed to import message")
		} else {
			s.log.WithError(err).Error("Failed to import message")
		}
	}

	return msg, literal, err
}

func (s *Connector) AddMessagesToMailbox(ctx context.Context, messageIDs []imap.MessageID, mboxID imap.MailboxID) error {
	if isAllMailOrScheduled(mboxID) {
		return connector.ErrOperationNotAllowed
	}

	return s.client.LabelMessages(ctx, usertypes.MapTo[imap.MessageID, string](messageIDs), string(mboxID))
}

func (s *Connector) RemoveMessagesFromMailbox(ctx context.Context, messageIDs []imap.MessageID, mboxID imap.MailboxID) error {
	if isAllMailOrScheduled(mboxID) {
		return connector.ErrOperationNotAllowed
	}

	msgIDs := usertypes.MapTo[imap.MessageID, string](messageIDs)
	if err := s.client.UnlabelMessages(ctx, msgIDs, string(mboxID)); err != nil {
		return err
	}

	if mboxID == proton.TrashLabel || mboxID == proton.DraftsLabel {
		if err := s.client.DeleteMessage(ctx, msgIDs...); err != nil {
			return err
		}
	}

	return nil
}

func (s *Connector) MoveMessages(ctx context.Context, messageIDs []imap.MessageID, mboxFromID, mboxToID imap.MailboxID) (bool, error) {
	if (mboxFromID == proton.InboxLabel && mboxToID == proton.SentLabel) ||
		(mboxFromID == proton.SentLabel && mboxToID == proton.InboxLabel) ||
		isAllMailOrScheduled(mboxFromID) ||
		isAllMailOrScheduled(mboxToID) {
		return false, connector.ErrOperationNotAllowed
	}

	shouldExpungeOldLocation := func() bool {
		rdLabels := s.labels.Read()
		defer rdLabels.Close()

		var result bool

		if v, ok := rdLabels.GetLabel(string(mboxFromID)); ok && v.Type == proton.LabelTypeLabel {
			result = true
		}

		if v, ok := rdLabels.GetLabel(string(mboxToID)); ok && (v.Type == proton.LabelTypeFolder || v.Type == proton.LabelTypeSystem) {
			result = true
		}

		return result
	}()

	if err := s.client.LabelMessages(ctx, usertypes.MapTo[imap.MessageID, string](messageIDs), string(mboxToID)); err != nil {
		return false, fmt.Errorf("labeling messages: %w", err)
	}

	if shouldExpungeOldLocation {
		if err := s.client.UnlabelMessages(ctx, usertypes.MapTo[imap.MessageID, string](messageIDs), string(mboxFromID)); err != nil {
			return false, fmt.Errorf("unlabeling messages: %w", err)
		}
	}

	return shouldExpungeOldLocation, nil
}

func (s *Connector) MarkMessagesSeen(ctx context.Context, messageIDs []imap.MessageID, seen bool) error {
	if seen {
		return s.client.MarkMessagesRead(ctx, usertypes.MapTo[imap.MessageID, string](messageIDs)...)
	}

	return s.client.MarkMessagesUnread(ctx, usertypes.MapTo[imap.MessageID, string](messageIDs)...)
}

func (s *Connector) MarkMessagesFlagged(ctx context.Context, messageIDs []imap.MessageID, flagged bool) error {
	if flagged {
		return s.client.LabelMessages(ctx, usertypes.MapTo[imap.MessageID, string](messageIDs), proton.StarredLabel)
	}

	return s.client.UnlabelMessages(ctx, usertypes.MapTo[imap.MessageID, string](messageIDs), proton.StarredLabel)
}

func (s *Connector) GetUpdates() <-chan imap.Update {
	return s.updateCh.GetChannel()
}

func (s *Connector) Close(_ context.Context) error {
	// Nothing to do
	return nil
}

func (s *Connector) ShowAllMail(v bool) {
	atomic.StoreUint32(&s.showAllMail, b32(v))
}

var (
	defaultFlags          = imap.NewFlagSet(imap.FlagSeen, imap.FlagFlagged, imap.FlagDeleted) // nolint:gochecknoglobals
	defaultPermanentFlags = imap.NewFlagSet(imap.FlagSeen, imap.FlagFlagged, imap.FlagDeleted) // nolint:gochecknoglobals
	defaultAttributes     = imap.NewFlagSet()                                                  // nolint:gochecknoglobals
)

const (
	folderPrefix = "Folders"
	labelPrefix  = "Labels"
)

// b32 returns a uint32 0 or 1 representing b.
func b32(b bool) uint32 {
	if b {
		return 1
	}

	return 0
}

func (s *Connector) createLabel(ctx context.Context, name []string) (imap.Mailbox, error) {
	if len(name) != 1 {
		return imap.Mailbox{}, fmt.Errorf("a label cannot have children: %w", connector.ErrOperationNotAllowed)
	}

	label, err := s.client.CreateLabel(ctx, proton.CreateLabelReq{
		Name:  name[0],
		Color: "#f66",
		Type:  proton.LabelTypeLabel,
	})
	if err != nil {
		return imap.Mailbox{}, err
	}

	wLabels := s.labels.Write()
	defer wLabels.Close()

	wLabels.SetLabel(label.ID, label)

	return toIMAPMailbox(label, s.flags, s.permFlags, s.attrs), nil
}

func (s *Connector) createFolder(ctx context.Context, name []string) (imap.Mailbox, error) {
	var parentID string

	wLabels := s.labels.Write()
	defer wLabels.Close()

	if len(name) > 1 {
		for _, label := range wLabels.GetLabels() {
			if !slices.Equal(label.Path, name[:len(name)-1]) {
				continue
			}

			parentID = label.ID

			break
		}

		if parentID == "" {
			return imap.Mailbox{}, fmt.Errorf("parent folder %q does not exist: %w", name[:len(name)-1], connector.ErrOperationNotAllowed)
		}
	}

	label, err := s.client.CreateLabel(ctx, proton.CreateLabelReq{
		Name:     name[len(name)-1],
		Color:    "#f66",
		Type:     proton.LabelTypeFolder,
		ParentID: parentID,
	})
	if err != nil {
		return imap.Mailbox{}, err
	}

	// Add label to list so subsequent sub folder create requests work correct.
	wLabels.SetLabel(label.ID, label)

	return toIMAPMailbox(label, s.flags, s.permFlags, s.attrs), nil
}

func (s *Connector) updateLabel(ctx context.Context, labelID imap.MailboxID, name []string) error {
	if len(name) != 1 {
		return fmt.Errorf("a label cannot have children: %w", connector.ErrOperationNotAllowed)
	}

	label, err := s.client.GetLabel(ctx, string(labelID), proton.LabelTypeLabel)
	if err != nil {
		return err
	}

	update, err := s.client.UpdateLabel(ctx, label.ID, proton.UpdateLabelReq{
		Name:  name[0],
		Color: label.Color,
	})
	if err != nil {
		return err
	}

	wLabels := s.labels.Write()
	defer wLabels.Close()

	wLabels.SetLabel(label.ID, update)

	return nil
}

func (s *Connector) updateFolder(ctx context.Context, labelID imap.MailboxID, name []string) error {
	var parentID string

	wLabels := s.labels.Write()
	defer wLabels.Close()

	if len(name) > 1 {
		for _, label := range wLabels.GetLabels() {
			if !slices.Equal(label.Path, name[:len(name)-1]) {
				continue
			}

			parentID = label.ID

			break
		}

		if parentID == "" {
			return fmt.Errorf("parent folder %q does not exist: %w", name[:len(name)-1], connector.ErrOperationNotAllowed)
		}
	}

	label, err := s.client.GetLabel(ctx, string(labelID), proton.LabelTypeFolder)
	if err != nil {
		return err
	}

	update, err := s.client.UpdateLabel(ctx, string(labelID), proton.UpdateLabelReq{
		Name:     name[len(name)-1],
		Color:    label.Color,
		ParentID: parentID,
	})
	if err != nil {
		return err
	}

	wLabels.SetLabel(label.ID, update)

	return nil
}

func (s *Connector) importMessage(
	ctx context.Context,
	literal []byte,
	labelIDs []string,
	flags proton.MessageFlag,
	unread bool,
) (imap.Message, []byte, error) {
	var full proton.FullMessage

	addr, ok := s.identityState.GetAddress(s.addrID)
	if !ok {
		return imap.Message{}, nil, fmt.Errorf("could not find address")
	}

	if err := s.identityState.WithAddrKR(s.addrID, func(_, addrKR *crypto.KeyRing) error {
		var messageID string

		if slices.Contains(labelIDs, proton.DraftsLabel) {
			msg, err := s.createDraft(ctx, literal, addrKR, addr)
			if err != nil {
				return fmt.Errorf("failed to create draft: %w", err)
			}

			// apply labels

			messageID = msg.ID
		} else {
			str, err := s.client.ImportMessages(ctx, addrKR, 1, 1, []proton.ImportReq{{
				Metadata: proton.ImportMetadata{
					AddressID: s.addrID,
					LabelIDs:  labelIDs,
					Unread:    proton.Bool(unread),
					Flags:     flags,
				},
				Message: literal,
			}}...)
			if err != nil {
				return fmt.Errorf("failed to prepare message for import: %w", err)
			}

			res, err := stream.Collect(ctx, str)
			if err != nil {
				return fmt.Errorf("failed to import message: %w", err)
			}

			messageID = res[0].MessageID
		}

		var err error

		if full, err = s.client.GetFullMessage(ctx, messageID, usertypes.NewProtonAPIScheduler(s.panicHandler), proton.NewDefaultAttachmentAllocator()); err != nil {
			return fmt.Errorf("failed to fetch message: %w", err)
		}

		if literal, err = message.BuildRFC822(addrKR, full.Message, full.AttData, defaultMessageJobOpts()); err != nil {
			return fmt.Errorf("failed to build message: %w", err)
		}

		return nil
	}); err != nil {
		return imap.Message{}, nil, err
	}

	return toIMAPMessage(full.MessageMetadata), literal, nil
}

func (s *Connector) createDraft(ctx context.Context, literal []byte, addrKR *crypto.KeyRing, sender proton.Address) (proton.Message, error) {
	// Create a new message parser from the reader.
	parser, err := parser.New(bytes.NewReader(literal))
	if err != nil {
		return proton.Message{}, fmt.Errorf("failed to create parser: %w", err)
	}

	message, err := message.ParseWithParser(parser, true)
	if err != nil {
		return proton.Message{}, fmt.Errorf("failed to parse message: %w", err)
	}

	decBody := string(message.PlainBody)
	if message.RichBody != "" {
		decBody = string(message.RichBody)
	}

	draft, err := s.client.CreateDraft(ctx, addrKR, proton.CreateDraftReq{
		Message: proton.DraftTemplate{
			Subject:  message.Subject,
			Body:     decBody,
			MIMEType: message.MIMEType,

			Sender:  &mail.Address{Name: sender.DisplayName, Address: sender.Email},
			ToList:  message.ToList,
			CCList:  message.CCList,
			BCCList: message.BCCList,

			ExternalID: message.ExternalID,
		},
	})

	if err != nil {
		return proton.Message{}, fmt.Errorf("failed to create draft: %w", err)
	}

	for _, att := range message.Attachments {
		disposition := proton.AttachmentDisposition
		if att.Disposition == "inline" && att.ContentID != "" {
			disposition = proton.InlineDisposition
		}

		if _, err := s.client.UploadAttachment(ctx, addrKR, proton.CreateAttachmentReq{
			MessageID:   draft.ID,
			Filename:    att.Name,
			MIMEType:    rfc822.MIMEType(att.MIMEType),
			Disposition: disposition,
			ContentID:   att.ContentID,
			Body:        att.Data,
		}); err != nil {
			return proton.Message{}, fmt.Errorf("failed to add attachment to draft: %w", err)
		}
	}

	return draft, nil
}

func (s *Connector) publishUpdate(_ context.Context, update imap.Update) {
	s.updateCh.Enqueue(update)
}