package whatsapp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// forwardToWebhook is a helper function to forward event to webhook url
func forwardToWebhook(evt any) error {
	logrus.Info("Forwarding event to webhook:", config.WhatsappWebhook)

	var payload map[string]interface{}
	var err error

	switch e := evt.(type) {
	case *events.Message:
		payload, err = createPayload(e)
	case *events.Receipt:
		payload, err = createReceiptPayload(e)
	case *events.Presence:
		payload, err = createPresencePayload(e)
	default:
		return fmt.Errorf("unsupported event type: %T", evt)
	}

	if err != nil {
		return err
	}

	for _, url := range config.WhatsappWebhook {
		if err = submitWebhook(payload, url); err != nil {
			return err
		}
	}

	logrus.Info("Event forwarded to webhook")
	return nil
}

func createPayload(evt *events.Message) (map[string]interface{}, error) {
	message := buildEventMessage(evt)
	waReaction := buildEventReaction(evt)
	forwarded := buildForwarded(evt)

	body := make(map[string]interface{})
	body["event_type"] = "message"

	if from := evt.Info.SourceString(); from != "" {
		body["from"] = from
	}
	if message.Text != "" {
		body["message"] = message
	}
	if pushname := evt.Info.PushName; pushname != "" {
		body["pushname"] = pushname
	}
	if waReaction.Message != "" {
		body["reaction"] = waReaction
	}
	if evt.IsViewOnce {
		body["view_once"] = evt.IsViewOnce
	}
	if forwarded {
		body["forwarded"] = forwarded
	}
	if timestamp := evt.Info.Timestamp.Format(time.RFC3339); timestamp != "" {
		body["timestamp"] = timestamp
	}

	if audioMedia := evt.Message.GetAudioMessage(); audioMedia != nil {
		path, err := ExtractMedia(config.PathMedia, audioMedia)
		if err != nil {
			logrus.Errorf("Failed to download audio from %s: %v", evt.Info.SourceString(), err)
			return nil, pkgError.WebhookError(fmt.Sprintf("Failed to download audio: %v", err))
		}
		body["audio"] = path
	}

	if contactMessage := evt.Message.GetContactMessage(); contactMessage != nil {
		body["contact"] = contactMessage
	}

	if documentMedia := evt.Message.GetDocumentMessage(); documentMedia != nil {
		path, err := ExtractMedia(config.PathMedia, documentMedia)
		if err != nil {
			logrus.Errorf("Failed to download document from %s: %v", evt.Info.SourceString(), err)
			return nil, pkgError.WebhookError(fmt.Sprintf("Failed to download document: %v", err))
		}
		body["document"] = path
	}

	if imageMedia := evt.Message.GetImageMessage(); imageMedia != nil {
		path, err := ExtractMedia(config.PathMedia, imageMedia)
		if err != nil {
			logrus.Errorf("Failed to download image from %s: %v", evt.Info.SourceString(), err)
			return nil, pkgError.WebhookError(fmt.Sprintf("Failed to download image: %v", err))
		}
		body["image"] = path
	}

	if listMessage := evt.Message.GetListMessage(); listMessage != nil {
		body["list"] = listMessage
	}

	if liveLocationMessage := evt.Message.GetLiveLocationMessage(); liveLocationMessage != nil {
		body["live_location"] = liveLocationMessage
	}

	if locationMessage := evt.Message.GetLocationMessage(); locationMessage != nil {
		body["location"] = locationMessage
	}

	if orderMessage := evt.Message.GetOrderMessage(); orderMessage != nil {
		body["order"] = orderMessage
	}

	if stickerMedia := evt.Message.GetStickerMessage(); stickerMedia != nil {
		path, err := ExtractMedia(config.PathMedia, stickerMedia)
		if err != nil {
			logrus.Errorf("Failed to download sticker from %s: %v", evt.Info.SourceString(), err)
			return nil, pkgError.WebhookError(fmt.Sprintf("Failed to download sticker: %v", err))
		}
		body["sticker"] = path
	}

	if videoMedia := evt.Message.GetVideoMessage(); videoMedia != nil {
		path, err := ExtractMedia(config.PathMedia, videoMedia)
		if err != nil {
			logrus.Errorf("Failed to download video from %s: %v", evt.Info.SourceString(), err)
			return nil, pkgError.WebhookError(fmt.Sprintf("Failed to download video: %v", err))
		}
		body["video"] = path
	}

	return body, nil
}

func createReceiptPayload(evt *events.Receipt) (map[string]any, error) {
	body := make(map[string]any)
	body["event_type"] = "receipt"
	body["from"] = evt.SourceString()
	body["timestamp"] = evt.Timestamp.Format(time.RFC3339)
	body["message_ids"] = evt.MessageIDs

	switch evt.Type {
	case types.ReceiptTypeDelivered:
		body["receipt_type"] = "delivered"
	default:
		body["receipt_type"] = evt.Type
	}
	return body, nil
}

func createPresencePayload(evt *events.Presence) (map[string]any, error) {
	body := make(map[string]any)
	body["event_type"] = "presence"
	body["from"] = evt.From.String()
	body["timestamp"] = time.Now().Format(time.RFC3339)
	body["status"] = "online"

	if evt.Unavailable {
		body["status"] = "offline"
		if !evt.LastSeen.IsZero() {
			body["last_seen"] = evt.LastSeen.Format(time.RFC3339)
		}
	}

	return body, nil
}

func submitWebhook(payload map[string]interface{}, url string) error {
	client := &http.Client{Timeout: 10 * time.Second}

	postBody, err := json.Marshal(payload)
	if err != nil {
		return pkgError.WebhookError(fmt.Sprintf("Failed to marshal body: %v", err))
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(postBody))
	if err != nil {
		return pkgError.WebhookError(fmt.Sprintf("error when create http object %v", err))
	}

	secretKey := []byte(config.WhatsappWebhookSecret)
	signature, err := getMessageDigestOrSignature(postBody, secretKey)
	if err != nil {
		return pkgError.WebhookError(fmt.Sprintf("error when create signature %v", err))
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", fmt.Sprintf("sha256=%s", signature))

	var attempt int
	var maxAttempts = 5
	var sleepDuration = 1 * time.Second

	for attempt = 0; attempt < maxAttempts; attempt++ {
		if _, err = client.Do(req); err == nil {
			logrus.Infof("Successfully submitted webhook on attempt %d", attempt+1)
			return nil
		}
		logrus.Warnf("Attempt %d to submit webhook failed: %v", attempt+1, err)
		time.Sleep(sleepDuration)
		sleepDuration *= 2
	}

	return pkgError.WebhookError(fmt.Sprintf("error when submit webhook after %d attempts: %v", attempt, err))
}
