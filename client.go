package prisclient

import (
	"crypto/rand"
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
	SourceId   string
	clientType string
	logger     *prislog.PrisLog
	autoRetry  bool
	host       string
	port       string
}

type CommandBlock struct {
	Id      string   `json:"id,omitempty"`
	Action  string   `json:"action,omitempty"`
	Type    string   `json:"type,omitempty"`
	Time    int64    `json:"time,omitempty"`
	Data    string   `json:"data,omitempty"`
	Array   []string `json:"array,omitempty"`
	Options []string `json:"options,omitempty"`
}

type MessageBlock struct {
	Message       string   `json:"message,omitempty"`
	From          string   `json:"from,omitempty"`
	Room          string   `json:"room,omitempty"`
	Mentioned     bool     `json:"mentioned,omitempty"`
	Stripped      string   `json:"stripped,omitempty"`
	MentionNotify []string `json:"mentionnotify,omitempty"`
}

type Query struct {
	Type    string        `json:"type,omitempty"`
	Source  string        `json:"source,omitempty"`
	To      string        `json:"to,omitempty"`
	Command *CommandBlock `json:"command,omitempty"`
	Message *MessageBlock `json:"message,omitempty"`
}

func RandomId() string {
	b := make([]byte, 8)
	io.ReadFull(rand.Reader, b)
	return fmt.Sprintf("%x", b)
}

func NewClient(host, port, clientType, sourceid string, autoretry bool,
	logger *prislog.PrisLog) (*Client, error) {

	if clientType != "adapter" && clientType != "responder" {
		return nil, errors.New("client type has to be adapter or responder")
	}

	pris := new(Client)

	pris.logger = logger
	pris.SourceId = sourceid
	pris.clientType = clientType
	pris.autoRetry = autoretry
	pris.host = host
	pris.port = port

	pris.connect()

	return pris, nil
}

func (pris *Client) connect() error {
	conn, err := net.Dial("tcp", pris.host+":"+pris.port)

	if err != nil {
		return err
	}

	pris.raw = conn
	pris.decoder = json.NewDecoder(pris.raw)
	pris.encoder = json.NewEncoder(pris.raw)

	err = pris.Engage()

	if err != nil {
		pris.logger.Error.Println("Failed to engage:", err)
		return err
	}

	pris.logger.Info.Println("Priscilla engaged")

	return nil
}

func (pris *Client) Engage() error {
	q := Query{
		Type:   "command",
		Source: pris.SourceId,
		Command: &CommandBlock{
			Id:     RandomId(),
			Action: "engage",
			Type:   pris.clientType,
			Time:   time.Now().Unix(),
		},
	}

	return pris.encoder.Encode(&q)
}

func (pris *Client) listen(out chan<- *Query) {
	var q *Query
	for {
		q = new(Query)
		err := pris.decoder.Decode(q)

		if err != nil {
			fmt.Println(err)
			if err.Error() == "EOF" {
				pris.logger.Error.Println("Priscilla disconnected")
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
			}
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
			pris.encoder.Encode(q)
		} else {
			pris.logger.Error.Println("Invalid query:", q)
		}
	}
}

func (pris *Client) ValidateQuery(q *Query) bool {
	switch {
	case q.Type == "command" && q.Command != nil:
		return true
	case q.Type == "message" && q.Message != nil && q.Message.Room != "":
		return true
	}
	return false
}
