package time

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/dedis/prifi/coco"
)

type Client struct {
	name    string
	servers map[string]*coco.GoConn // servers I "work" with
	dir     *coco.GoDirectory       // directory of connection with servers

	// client history maps request numbers to replies from server
	// maybe at later phases we will want pair(reqno, server) as key
	history map[SeqNo]TimeStampMessage
	reqno   SeqNo // next request number in communications with server

	// maps response request numbers to channels confirming
	// where response confirmations are sent
	doneChan map[SeqNo]chan bool

	nRounds     int        // # of last round messages received in, as perceived by client
	curRoundSig []byte     // merkle tree root of last round
	roundChan   chan int   // round numberd are sent in as rounds change
	Mux         sync.Mutex // potentially coarse grained mutex
}

func (cli *Client) handleRequest(tsm *TimeStampMessage) {
	switch tsm.Type {
	default:
		fmt.Println("Message of unknown type")
	case StampReplyType:
		cli.ProcessStampReply(tsm)

	}
}

func (cli *Client) Listen() {
	for _, c := range cli.servers {
		go func(c *coco.GoConn) {
			for {
				tsm := &TimeStampMessage{}
				c.Get(tsm)
				cli.handleRequest(tsm)
			}
		}(c)
	}
}

func NewClient(name string, dir *coco.GoDirectory) (c *Client) {
	c = &Client{name: name, dir: dir}
	c.servers = make(map[string]*coco.GoConn)
	c.history = make(map[SeqNo]TimeStampMessage)
	c.doneChan = make(map[SeqNo]chan bool)
	c.roundChan = make(chan int)
	return
}

func (c *Client) Name() string {
	return c.name
}

func (c *Client) Put(name string, data coco.BinaryMarshaler) {
	myConn := c.servers[name]
	myConn.Put(data)
}

// When client asks for val to be timestamped by a server
// It blocks until it get a stamp reply back
func (c *Client) TimeStamp(val []byte, serverName string) {
	// new request requires new done channel
	c.Mux.Lock()
	c.reqno++
	myReqno := c.reqno
	c.doneChan[c.reqno] = make(chan bool, 1)
	c.Mux.Unlock()

	// send request to server
	c.Put(serverName,
		&TimeStampMessage{
			Type:  StampRequestType,
			ReqNo: myReqno,
			sreq:  &StampRequest{Val: val}})

	// get channel associated with request
	c.Mux.Lock()
	myChan := c.doneChan[myReqno]
	c.Mux.Unlock()

	// wait for response to request
	<-myChan

	// delete channel as it is of no longer meaningful
	c.Mux.Lock()
	delete(c.doneChan, myReqno)
	c.Mux.Unlock()
}

func (c *Client) ProcessStampReply(tsm *TimeStampMessage) {
	// update client history
	c.Mux.Lock()
	c.history[tsm.ReqNo] = *tsm
	done := c.doneChan[tsm.ReqNo]

	// can keep track of rounds by looking at changes in the signature
	// sent back in a messages
	if bytes.Compare(tsm.srep.Sig, c.curRoundSig) != 0 {
		c.curRoundSig = tsm.srep.Sig
		c.nRounds++

		c.Mux.Unlock()
		c.roundChan <- c.nRounds
	} else {
		c.Mux.Unlock()
	}

	done <- true
}

func (c *Client) showHistory() {
	for {
		select {
		case nRound := <-c.roundChan:
			if nRound != 1 {
				// If not all replies received by client it will block infinitely
				// fmt.Println("All round", nRound-1, "responses received by", c.Name())
			}
			// c.historyMux.Lock()
			// for _, msg := range c.history {
			// 	fmt.Println("ReqNo =", msg.reqno, "Signature =", msg.Sig)
			// }
			// c.historyMux.Unlock()
		}

	}
}
