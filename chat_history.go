package whatsapp

import (
	"log"
	"strconv"
	"time"

	"github.com/adarosci/go-whatsapp/binary"
	"github.com/adarosci/go-whatsapp/binary/proto"
	"github.com/pkg/errors"
)

type MessageOffsetInfo struct {
	FirstMessageId    string
	FirstMessageOwner bool
}

func decodeMessages(n *binary.Node) []*proto.WebMessageInfo {

	var messages = make([]*proto.WebMessageInfo, 0)

	if n == nil || n.Attributes == nil || n.Content == nil {
		return messages
	}

	for _, msg := range n.Content.([]interface{}) {
		switch msg.(type) {
		case *proto.WebMessageInfo:
			messages = append(messages, msg.(*proto.WebMessageInfo))
		default:
			log.Println("decodeMessages: Non WebMessage encountered")
		}
	}

	return messages
}

// ContextInfo load first message messageID
func (wac *Conn) LoadMessageByID(jid, messageID string) *proto.WebMessageInfo {

	node, err := wac.query("message", jid, messageID, "", "false", "", 1, 0)
	if err != nil {
		return nil
	}

	for _, msg := range decodeMessages(node) {
		node, err = wac.query("message", jid, *msg.Key.Id, "after", "true", "", 1, 0)
		if err != nil {
			node, err = wac.query("message", jid, *msg.Key.Id, "after", "false", "", 1, 0)
			if err != nil {
				return nil
			}
		}
		for _, msg1 := range decodeMessages(node) {
			return msg1
		}
	}

	return nil
}

// DownloadMediaMessage load first message messageID
func (wac *Conn) DownloadMediaMessage(jid, messageID string) ([]byte, error) {

	node, err := wac.query("message", jid, messageID, "", "false", "", 1, 0)
	if err != nil {
		return nil, err
	}

	for _, msg := range decodeMessages(node) {
		node, err = wac.query("message", jid, *msg.Key.Id, "after", "true", "", 1, 0)
		if err != nil {
			node, err = wac.query("message", jid, *msg.Key.Id, "after", "false", "", 1, 0)
			if err != nil {
				return nil, err
			}
		}
		for _, msg1 := range decodeMessages(node) {
			message := ParseProtoMessage(msg1)
			switch m := message.(type) {
			case error:
				return nil, m
			case ImageMessage:
				image := message.(ImageMessage)
				return image.Download()
			case VideoMessage:
				video := message.(VideoMessage)
				return video.Download()
			case AudioMessage:
				audio := message.(AudioMessage)
				return audio.Download()
			case DocumentMessage:
				document := message.(DocumentMessage)
				return document.Download()
			case StickerMessage:
				sticker := message.(StickerMessage)
				return sticker.Download()
			default:
				return nil, errors.New("not message download")
			}
		}
	}

	return nil, errors.New("not message download")
}

// LoadChatMessages is useful to "scroll" messages, loading by count at a time
// if handlers == nil the func will use default handlers
// if after == true LoadChatMessages will load messages after the specified messageId, otherwise it will return
// message before the messageId
func (wac *Conn) LoadChatMessages(jid string, count int, messageId string, owner bool, after bool, handlers ...Handler) error {
	if count <= 0 {
		return nil
	}

	if handlers == nil {
		handlers = wac.handler
	}

	kind := "before"
	if after {
		kind = "after"
	}

	node, err := wac.query("message", jid, messageId, kind,
		strconv.FormatBool(owner), "", count, 0)

	if err != nil {
		wac.handleWithCustomHandlers(err, handlers)
		return err
	}

	for _, msg := range decodeMessages(node) {
		wac.handleWithCustomHandlers(ParseProtoMessage(msg), handlers)
		wac.handleWithCustomHandlers(msg, handlers)
	}
	return nil

}

// LoadFullChatHistory loads full chat history for the given jid
// chunkSize = how many messages to load with one query; if handlers == nil the func will use default handlers;
// pauseBetweenQueries = how much time to sleep between queries
func (wac *Conn) LoadFullChatHistory(jid string, chunkSize int,
	pauseBetweenQueries time.Duration, handlers ...Handler) error {
	if chunkSize <= 0 {
		return nil
	}

	if handlers == nil {
		handlers = wac.handler
	}

	beforeMsg := ""
	beforeMsgIsOwner := true

	c := make(chan bool)
	quit := false

	go func() {
		for {
			if wac == nil || quit {
				return
			}

			node, err := wac.query("message", jid, beforeMsg, "before",
				strconv.FormatBool(beforeMsgIsOwner), "", chunkSize, 0)

			if err != nil {
				wac.handleWithCustomHandlers(err, handlers)
			} else {

				msgs := decodeMessages(node)
				for _, msg := range msgs {
					wac.handleWithCustomHandlers(ParseProtoMessage(msg), handlers)
					wac.handleWithCustomHandlers(msg, handlers)
				}

				c <- true
				if len(msgs) == 0 {
					break
				}

				beforeMsg = *msgs[0].Key.Id
				beforeMsgIsOwner = msgs[0].Key.FromMe != nil && *msgs[0].Key.FromMe

			}

			<-time.After(pauseBetweenQueries)
		}
	}()

	var err error

	select {
	case <-c:
		{
			err = nil
		}
	case <-time.After(time.Second * 30):
		{
			err = errors.New("timeout chat full")
		}
	}

	quit = true

	close(c)

	return err
}

// LoadFullChatHistoryAfter loads all messages after the specified messageId
// useful to "catch up" with the message history after some specified message
func (wac *Conn) LoadFullChatHistoryAfter(jid string, messageId string, chunkSize int,
	pauseBetweenQueries time.Duration, handlers ...Handler) {

	if chunkSize <= 0 {
		return
	}

	if handlers == nil {
		handlers = wac.handler
	}

	msgOwner := true
	prevNotFound := false

	for {
		node, err := wac.query("message", jid, messageId, "after",
			strconv.FormatBool(msgOwner), "", chunkSize, 0)

		if err != nil {

			// Whatsapp will return 404 status when there is wrong owner flag on the requested message id
			if err == ErrServerRespondedWith404 {

				// this will detect two consecutive "not found" errors.
				// this is done to prevent infinite loop when wrong message id supplied
				if prevNotFound {
					log.Println("LoadFullChatHistoryAfter: could not retrieve any messages, wrong message id?")
					return
				}
				prevNotFound = true

				// try to reverse the owner flag and retry
				if msgOwner {
					// reverse initial msgOwner value and retry
					msgOwner = false

					<-time.After(time.Second)
					continue
				}

			}

			// if the error isn't a 404 error, pass it to the error handler
			wac.handleWithCustomHandlers(err, handlers)
		} else {

			msgs := decodeMessages(node)
			for _, msg := range msgs {
				wac.handleWithCustomHandlers(ParseProtoMessage(msg), handlers)
				wac.handleWithCustomHandlers(msg, handlers)
			}

			if len(msgs) != chunkSize {
				break
			}

			messageId = *msgs[0].Key.Id
			msgOwner = msgs[0].Key.FromMe != nil && *msgs[0].Key.FromMe
		}

		// message was found
		prevNotFound = false

		<-time.After(pauseBetweenQueries)

	}

}
