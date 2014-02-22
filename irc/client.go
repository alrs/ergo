package irc

import (
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

func IsNickname(nick string) bool {
	return NicknameExpr.MatchString(nick)
}

type HostnameLookup struct {
	client   *Client
	hostname string
}

func NewHostnameLookup(client *Client, ipAddr string) *HostnameLookup {
	return &HostnameLookup{
		client:   client,
		hostname: LookupHostname(ipAddr),
	}
}

type Client struct {
	atime       time.Time
	awayMessage string
	channels    ChannelSet
	ctime       time.Time
	flags       map[UserMode]bool
	hasQuit     bool
	hops        uint
	hostname    string
	idleTimer   *time.Timer
	loginTimer  *time.Timer
	lookups     chan string
	nick        string
	phase       Phase
	quitTimer   *time.Timer
	realname    string
	replies     chan string
	server      *Server
	socket      *Socket
	username    string
}

func NewClient(server *Server, conn net.Conn) *Client {
	now := time.Now()
	client := &Client{
		atime:    now,
		channels: make(ChannelSet),
		ctime:    now,
		flags:    make(map[UserMode]bool),
		lookups:  make(chan string),
		phase:    server.InitPhase(),
		server:   server,
		socket:   NewSocket(conn),
		replies:  make(chan string, 16),
	}

	client.loginTimer = time.AfterFunc(LOGIN_TIMEOUT, client.connectionTimeout)
	go client.LookupHostname(IPString(conn.RemoteAddr()))
	go client.readCommands()
	go client.writeReplies()

	return client
}

//
// socket read gorountine
//

func (client *Client) readCommands() {
	done := false
	for !done {
		select {
		case ipAddr := <-client.lookups:
			client.server.hostnames <- NewHostnameLookup(client, ipAddr)

		case line := <-client.socket.Read():
			if line == EOF {
				done = true
				break
			}
			msg, err := ParseCommand(line)
			if err != nil {
				switch err {
				case NotEnoughArgsError:
					parts := strings.SplitN(line, " ", 2)
					client.ErrNeedMoreParams(parts[0])
				}
				continue
			}

			msg.SetClient(client)
			client.server.commands <- msg
		}
	}

	client.connectionClosed()
}

func (client *Client) LookupHostname(ipAddr string) {
	client.lookups <- ipAddr
}

func (client *Client) connectionClosed() {
	msg := &QuitCommand{
		message: "connection closed",
	}
	msg.SetClient(client)
	client.server.commands <- msg
}

//
// reply writing goroutine
//

func (client *Client) writeReplies() {
	for reply := range client.replies {
		if reply == EOF {
			break
		}
		if client.socket.Write(reply) != nil {
			break
		}
	}
	client.socket.Close()
}

//
// idle timer goroutine
//

func (client *Client) connectionIdle() {
	client.server.idle <- client
}

//
// quit timer goroutine
//

func (client *Client) connectionTimeout() {
	msg := &QuitCommand{
		message: "connection timeout",
	}
	msg.SetClient(client)
	client.server.commands <- msg
}

//
// server goroutine
//

func (client *Client) Active() {
	client.atime = time.Now()
}

func (client *Client) Touch() {
	if client.quitTimer != nil {
		client.quitTimer.Stop()
	}

	if client.idleTimer == nil {
		client.idleTimer = time.AfterFunc(IDLE_TIMEOUT, client.connectionIdle)
	} else {
		client.idleTimer.Reset(IDLE_TIMEOUT)
	}
}

func (client *Client) Idle() {
	client.Reply(RplPing(client))

	if client.quitTimer == nil {
		client.quitTimer = time.AfterFunc(QUIT_TIMEOUT, client.connectionTimeout)
	} else {
		client.quitTimer.Reset(QUIT_TIMEOUT)
	}
}

func (client *Client) Register() {
	client.phase = Normal
	client.loginTimer.Stop()
	client.Touch()
}

func (client *Client) destroy() {
	// clean up self

	client.loginTimer.Stop()

	if client.idleTimer != nil {
		client.idleTimer.Stop()
	}
	if client.quitTimer != nil {
		client.quitTimer.Stop()
	}

	// clean up channels

	for channel := range client.channels {
		channel.Quit(client)
	}

	// clean up server

	client.server.clients.Remove(client)

	if DEBUG_CLIENT {
		log.Printf("%s: destroyed", client)
	}
}

func (client *Client) IdleTime() time.Duration {
	return time.Since(client.atime)
}

func (client *Client) SignonTime() int64 {
	return client.ctime.Unix()
}

func (client *Client) IdleSeconds() uint64 {
	return uint64(client.IdleTime().Seconds())
}

func (client *Client) HasNick() bool {
	return client.nick != ""
}

func (client *Client) HasUsername() bool {
	return client.username != ""
}

// <mode>
func (c *Client) ModeString() (str string) {
	for flag := range c.flags {
		str += flag.String()
	}

	if len(str) > 0 {
		str = "+" + str
	}
	return
}

func (c *Client) UserHost() string {
	username := "*"
	if c.HasUsername() {
		username = c.username
	}
	return fmt.Sprintf("%s!%s@%s", c.Nick(), username, c.hostname)
}

func (c *Client) Nick() string {
	if c.HasNick() {
		return c.nick
	}
	return "*"
}

func (c *Client) Id() string {
	return c.UserHost()
}

func (c *Client) String() string {
	return c.Id()
}

func (client *Client) Friends() ClientSet {
	friends := make(ClientSet)
	friends.Add(client)
	for channel := range client.channels {
		for member := range channel.members {
			friends.Add(member)
		}
	}
	return friends
}

func (client *Client) SetNickname(nickname string) {
	client.nick = nickname
	client.server.clients.Add(client)
}

func (client *Client) ChangeNickname(nickname string) {
	// Make reply before changing nick to capture original source id.
	reply := RplNick(client, nickname)
	client.server.clients.Remove(client)
	client.nick = nickname
	client.server.clients.Add(client)
	for friend := range client.Friends() {
		friend.Reply(reply)
	}
}

func (client *Client) Reply(reply string) {
	if client.hasQuit {
		return
	}
	client.replies <- reply
}

func (client *Client) Quit(message string) {
	if client.hasQuit {
		return
	}

	client.Reply(RplError("connection closed"))
	client.Reply(EOF)

	client.hasQuit = true
	friends := client.Friends()
	friends.Remove(client)
	client.destroy()

	if len(friends) > 0 {
		reply := RplQuit(client, message)
		for friend := range friends {
			friend.Reply(reply)
		}
	}
}
