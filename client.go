package prisclient

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/priscillachat/prislog"
	"io"
	"net"
	"time"
)

type Client struct {
	raw        net.Conn
	decoder    *json.Decoder
	encoder    *json.Encoder
	sourceId   string
	clientType string
	logger     *prislog.PrisLog
	autoRetry  bool
	host       string
	port       string
	secret     string
}

type CommandBlock struct {
	Id      string            `json:"id,omitempty"`
	Action  string            `json:"action,omitempty"`
	Type    string            `json:"type,omitempty"`
	Time    int64             `json:"time,omitempty"`
	Data    string            `json:"data,omitempty"`
	Error   string            `json:"error,omitempty"`
	Array   []string          `json:"array,omitempty"`
	Options []string          `json:"options,omitempty"`
	Map     map[string]string `json:"map,omitempty"`
}

type MessageBlock struct {
	Message       string    `json:"message,omitempty"`
	From          string    `json:"from,omitempty"`
	Room          string    `json:"room,omitempty"`
	Mentioned     bool      `json:"mentioned,omitempty"`
	Stripped      string    `json:"stripped,omitempty"`
	MentionNotify []string  `json:"mentionnotify,omitempty"`
	User          *UserInfo `json:"user,omitempty"`
	DisplayName   string    `json:"username,omitempty"`
}

type Query struct {
	Type    string        `json:"type,omitempty"`
	Source  string        `json:"source,omitempty"`
	To      string        `json:"to,omitempty"`
	Command *CommandBlock `json:"command,omitempty"`
	Message *MessageBlock `json:"message,omitempty"`
}

type UserInfo struct {
	Id      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Mention string `string:"mention,omitempty"`
	Email   string `string:"email,omitempty"`
}

type ResponderCommand struct {
	Name        string
	Type        string
	Regex       string
	Help        string
	HelpCmd     string
	Fallthrough bool
}

func RandomId() string {
	b := make([]byte, 8)
	io.ReadFull(rand.Reader, b)
	return fmt.Sprintf("%x", b)
}

func NewClient(host, port, clientType, sourceId, secret string, autoretry bool,
	logger *prislog.PrisLog) (*Client, error) {

	if clientType != "adapter" && clientType != "responder" {
		return nil, errors.New("client type has to be adapter or responder")
	}

	pris := new(Client)

	pris.logger = logger
	pris.sourceId = sourceId
	pris.clientType = clientType
	pris.autoRetry = autoretry
	pris.host = host
	pris.port = port
	pris.secret = secret

	return pris, nil
}

func (pris *Client) connect() error {
	conn, err := net.Dial("tcp", pris.host+":"+pris.port)

	if err != nil {
		return err
	}

	// we can't assign it to pris until the engagement is complete, because as
	// soon as the encoder/decoder is assigned, communication starts
	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)

	timestamp := time.Now().UTC().Unix()
	authMsg := fmt.Sprintf("%d%s%s", timestamp, pris.sourceId, pris.secret)

	mac := hmac.New(sha256.New, []byte(pris.secret))
	mac.Write([]byte(authMsg))

	err = encoder.Encode(&Query{
		Type:   "command",
		Source: pris.sourceId,
		To:     "server",
		Command: &CommandBlock{
			Action: "engage",
			Type:   pris.clientType,
			Time:   timestamp,
			Data:   base64.StdEncoding.EncodeToString(mac.Sum(nil)),
		},
	})

	if err != nil {
		return err
	}

	ack := Query{}

	err = decoder.Decode(&ack)

	if err != nil {
		return err
	}

	if ack.Type != "command" || ack.Command.Action != "proceed" {
		pris.logger.Error.Fatal("Unexpected response from server:",
			ack.Command)
		return errors.New("Unexpected response from server")
	}

	pris.sourceId = ack.Command.Data

	pris.encoder = encoder
	pris.decoder = decoder
	pris.raw = conn

	if err != nil {
		pris.logger.Error.Println("Failed to engage:", err)
		return err
	}

	pris.logger.Info.Println("Priscilla engaged")

	return nil
}

func (pris *Client) disconnect() {
	if pris.raw != nil {
		pris.raw.(*net.TCPConn).Close()
	}
}

func (pris *Client) listen(out chan<- *Query) {
	for err := pris.connect(); err != nil; err = pris.connect() {
		if err == nil {
			break
		}

		pris.logger.Error.Println("Error connecting to priscilla server")

		if pris.autoRetry {
			pris.disconnect()
			pris.logger.Error.Println("Auto retry in 5 seconds...")
			time.Sleep(5 * time.Second)
		} else {
			pris.logger.Error.Fatal(
				"Error connecting to priscilla server, and autoRetry is not set")
		}
	}

	var q *Query
	for {
		q = new(Query)
		err := pris.decoder.Decode(q)

		if err != nil {
			fmt.Println(err)
			if err.Error() == "EOF" {
				pris.logger.Error.Println("Priscilla disconnected")
			} else {
				pris.logger.Error.Println("Priscilla connection error:", err)
				pris.disconnect()
			}

			if pris.autoRetry {
				pris.logger.Error.Println("Auto reconnect in 5 seconds...")
				time.Sleep(5 * time.Second)
				err := pris.connect()
				if err != nil {
					pris.logger.Error.Println("Connect error:", err)
				}
			} else {
				out <- &Query{
					Type:   "command",
					Source: "pris",
					Command: &CommandBlock{
						Action: "disengage",
					},
				}
				break
			}
			// }
		}

		if pris.ValidateQuery(q) {
			pris.logger.Debug.Println("Query received:", *q)
			if q.Type == "message" {
				pris.logger.Debug.Println("Message:", q.Message.Message)
				pris.logger.Debug.Println("From:", q.Message.From)
				pris.logger.Debug.Println("Room:", q.Message.Room)
			}
			out <- q
		} else {
			pris.logger.Error.Println("Invalid query from server(?!!):", q)
		}
	}
}

func (pris *Client) Run(toPris <-chan *Query, fromPris chan<- *Query) {

	go pris.listen(fromPris)
	var q *Query
	for {
		q = <-toPris
		if pris.ValidateQuery(q) {
			if pris.encoder != nil {
				pris.encoder.Encode(q)
			}
		} else {
			pris.logger.Error.Println("Invalid query:", q)
		}
	}
}

func (pris *Client) ValidateQuery(q *Query) bool {
	switch {
	case q.Type == "command":
		if q.Command == nil {
			return false
		}
		cmd := q.Command

		if cmd.Id == "" {
			pris.logger.Warn.Println("Missing user request id, assigning ...")
			cmd.Id = RandomId()
		}

		switch cmd.Action {
		case "user_request":
			if cmd.Type != "user" && cmd.Type != "mention" &&
				cmd.Type != "email" && cmd.Type != "id" {
				pris.logger.Error.Println("Invalid user action type:", cmd.Type)
				return false
			}
			if cmd.Data == "" {
				pris.logger.Error.Println("Missing data field")
				return false
			}
			return true
		case "room_request":
			if cmd.Type != "name" && cmd.Type != "id" {
				pris.logger.Error.Println("Invalid room action type:", cmd.Type)
				return false
			}
			if cmd.Data == "" {
				pris.logger.Error.Println("Missing data field")
				return false
			}
			return true
		case "info":
			if cmd.Type != "user" && cmd.Type != "room" {
				pris.logger.Error.Println("Invalid info action type:", cmd.Type)
				return false
			}
			return true
		case "disengage":
			return true
		}
		pris.logger.Error.Println("Unsupported command:", cmd.Action)
		pris.logger.Info.Println("Client type:", pris.clientType)
		return false
	case q.Type == "message" && q.Message != nil && q.Message.Room != "":
		return true
	}
	return false
}
