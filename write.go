package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Rhymen/go-whatsapp/binary"
	"github.com/Rhymen/go-whatsapp/crypto/cbc"
)

type websocketWrapper struct {
	sync.Mutex
	sync.WaitGroup
	conn   *websocket.Conn
	ctx    context.Context
	cancel func()

	pingInKeepalive int
}

func (wsw *websocketWrapper) countTimeout() {
	if wsw.pingInKeepalive < 10 {
		wsw.pingInKeepalive++
	}
}

func newWebsocketWrapper(conn *websocket.Conn) *websocketWrapper {
	ctx, cancel := context.WithCancel(context.Background())
	return &websocketWrapper{
		conn:   conn,
		ctx:    ctx,
		cancel: cancel,
	}
}

func (wsw *websocketWrapper) write(messageType int, data []byte) error {
	wsw.Lock()
	err := wsw.conn.WriteMessage(messageType, data)
	wsw.Unlock()

	if err != nil {
		return fmt.Errorf("error writing to websocket: %w", err)
	}

	return nil
}

func (wac *Conn) writeJson(data []interface{}) (<-chan string, error) {
	ch, _, err := wac.writeJsonRetry(data)
	return ch, err
}

//writeJson enqueues a json message into the writeChan
func (wac *Conn) writeJsonRetry(data []interface{}) (<-chan string, func() error, error) {
	ch := make(chan string, 1)

	wac.writerLock.Lock()
	defer wac.writerLock.Unlock()

	d, err := json.Marshal(data)
	if err != nil {
		close(ch)
		return ch, nil, err
	}

	ts := time.Now().Unix()
	messageTag := fmt.Sprintf("%d.--%d", ts, wac.msgCount)
	bytes := []byte(fmt.Sprintf("%s,%s", messageTag, d))

	if wac.timeTag == "" {
		tss := fmt.Sprintf("%d", ts)
		wac.timeTag = tss[len(tss)-3:]
	}

	wac.listener.add(ch, messageTag)

	err = wac.ws.write(websocket.TextMessage, bytes)
	if err != nil {
		close(ch)
		wac.listener.remove(messageTag)
		return ch, nil, err
	}

	retry := func() error {
		return wac.ws.write(websocket.TextMessage, bytes)
	}

	wac.msgCount++
	return ch, retry, nil
}

func (wac *Conn) writeBinary(node binary.Node, metric metric, flag flag, messageTag string) (<-chan string, error) {
	ch, _, err := wac.writeBinaryRetry(node, metric, flag, messageTag)
	return ch, err
}

func (wac *Conn) writeBinaryRetry(node binary.Node, metric metric, flag flag, messageTag string) (<-chan string, func() error, error) {
	ch := make(chan string, 1)

	if len(messageTag) < 2 {
		close(ch)
		return ch, nil, ErrMissingMessageTag
	}

	wac.writerLock.Lock()
	defer wac.writerLock.Unlock()

	data, err := wac.encryptBinaryMessage(node)
	if err != nil {
		close(ch)
		return ch, nil, fmt.Errorf("encryptBinaryMessage(node) failed: %w", err)
	}

	bytes := []byte(messageTag + ",")
	bytes = append(bytes, byte(metric), byte(flag))
	bytes = append(bytes, data...)

	wac.listener.add(ch, messageTag)

	err = wac.ws.write(websocket.BinaryMessage, bytes)
	if err != nil {
		close(ch)
		wac.listener.remove(messageTag)
		return ch, nil, fmt.Errorf("failed to write message: %w", err)
	}

	retry := func() error {
		return wac.ws.write(websocket.BinaryMessage, bytes)
	}

	wac.msgCount++
	return ch, retry, nil
}

func (wac *Conn) encryptBinaryMessage(node binary.Node) (data []byte, err error) {
	b, err := binary.Marshal(node)
	if err != nil {
		return nil, fmt.Errorf("binary node marshal failed: %w", err)
	}

	cipher, err := cbc.Encrypt(wac.session.EncKey, nil, b)
	if err != nil {
		return nil, fmt.Errorf("encrypt failed: %w", err)
	}

	h := hmac.New(sha256.New, wac.session.MacKey)
	h.Write(cipher)
	hash := h.Sum(nil)

	data = append(data, hash[:32]...)
	data = append(data, cipher...)

	return data, nil
}
